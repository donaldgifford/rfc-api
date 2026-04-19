// Docker Bake definition for rfc-api.
//
// Referenced by .github/workflows/ci.yml's `docker-build` job. Builds the
// production container image multi-arch (linux/amd64, linux/arm64) so
// ghcr.io artifacts work on both Intel and ARM kubelets.
//
// Usage:
//   docker buildx bake ci          # build the ci target
//   docker buildx bake --print ci  # inspect the resolved target
//   docker buildx bake ci --set ci.platforms=linux/amd64   # narrow arch

variable "REGISTRY" {
  default = "ghcr.io/donaldgifford"
}

variable "IMAGE_NAME" {
  default = "rfc-api"
}

// VERSION / COMMIT are set by CI from the workflow env; locally they
// default to dev placeholders.
variable "VERSION" {
  default = "dev"
}

variable "COMMIT" {
  default = "unknown"
}

group "default" {
  targets = ["ci"]
}

target "_common" {
  context    = "."
  dockerfile = "Dockerfile"
  args = {
    VERSION = "${VERSION}"
    COMMIT  = "${COMMIT}"
  }
  labels = {
    "org.opencontainers.image.source"      = "https://github.com/donaldgifford/rfc-api"
    "org.opencontainers.image.description" = "rfc-api -- Markdown Portal backend service"
    "org.opencontainers.image.licenses"    = "Apache-2.0"
    "org.opencontainers.image.version"     = "${VERSION}"
    "org.opencontainers.image.revision"    = "${COMMIT}"
  }
}

target "ci" {
  inherits  = ["_common"]
  platforms = ["linux/amd64", "linux/arm64"]
  tags = [
    "${REGISTRY}/${IMAGE_NAME}:${VERSION}",
    "${REGISTRY}/${IMAGE_NAME}:latest-ci",
  ]
}

// Local single-arch build (no registry push). Handy for quick iteration:
//   docker buildx bake local --load
target "local" {
  inherits  = ["_common"]
  platforms = ["linux/amd64"]
  tags      = ["${IMAGE_NAME}:local"]
}
