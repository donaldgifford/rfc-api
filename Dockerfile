# syntax=docker/dockerfile:1.7
#
# rfc-api container image.
#
# Multi-stage: cross-compiled Go build on the build platform, distroless
# nonroot runtime for the final stage. Referenced by docker-bake.hcl's
# `ci` target (CI) and available as a standalone
# `docker buildx build -f Dockerfile .` for local shaping.

# -- build stage -------------------------------------------------------

FROM --platform=$BUILDPLATFORM golang:1.26.1-alpine AS build

WORKDIR /src

# Go module cache layer: copy manifests only so `go mod download`
# caches independently of source changes.
COPY go.mod ./
COPY go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Full source for the build.
COPY . .

# Cross-compile to TARGETPLATFORM.
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/rfc-api \
      ./cmd/rfc-api

# -- runtime stage -----------------------------------------------------

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=build /out/rfc-api /rfc-api

# 8080 = main (user traffic); 8081 = admin (ops + optional pprof).
EXPOSE 8080 8081

USER nonroot:nonroot
ENTRYPOINT ["/rfc-api"]
CMD ["serve"]
