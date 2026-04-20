# Local development

Runbook for working on `rfc-api` on a developer machine. The short version:
install toolchain, copy env, bring up deps, run binary.

## One-command getting started

```sh
mise install              # pin Go / golangci-lint / goimports / goreleaser...
cp .env.example .env      # local config; gitignored, edit freely
make compose-up           # starts postgres + meilisearch
go run ./cmd/rfc-api serve
```

After `go run`:

- Main HTTP listens on `http://localhost:8080` (user traffic, `/api/v1/*`).
- Admin HTTP listens on `http://127.0.0.1:8081` (`/healthz`, `/readyz`,
  `/metrics`, optional `/debug/pprof/*`).

Quick verify:

```sh
curl -s http://localhost:8081/healthz  # -> {"status":"ok"}
curl -s http://localhost:8081/readyz   # 200 once Postgres probe passes
```

## Compose profiles

`compose.yaml` splits services into profiles so you only bring up what you
need. Default (no flag) is Postgres + Meilisearch only.

| Target                | Services                                         |
|-----------------------|--------------------------------------------------|
| `make compose-up`     | postgres, meilisearch                            |
| `make compose-up-auth`| + keycloak                                       |
| `make compose-up-obs` | + otel-collector, jaeger, prometheus, grafana, loki, alloy |
| `make compose-up-full`| everything                                       |
| `make compose-down`   | stop all; keep volume data                       |
| `make compose-nuke`   | stop all; drop volumes (prompts; `CONFIRM=1` skips) |
| `make compose-logs SERVICE=postgres` | tail one service's logs           |

## Port map

| Service          | Host port | Purpose                                   |
|------------------|-----------|-------------------------------------------|
| postgres         | 5432      | primary datastore                         |
| meilisearch      | 7700      | search index                              |
| rfc-api (main)   | 8080      | `/api/v1/*` (host-run binary)             |
| rfc-api (admin)  | 8081      | ops endpoints (host-run binary)           |
| keycloak         | 8180      | dev OIDC provider (profile `auth`)        |
| otel-collector   | 4317/4318 | OTLP gRPC/HTTP (profile `tracing`)        |
| jaeger UI        | 16686     | trace viewer (profile `tracing`)          |
| prometheus       | 9090      | metrics UI (profile `metrics`)            |
| grafana          | 3000      | dashboards (profile `metrics`)            |
| loki             | 3100      | log store (profile `logs`)                |
| alloy            | 12345     | log shipper UI (profile `logs`)           |

## Observability workflow

With `make compose-up-obs` running and `RFC_API_PPROF_ENABLED=true` in
`.env`, the binary emits:

- **Traces** via OTLP to `otel-collector` → Jaeger
  (`http://localhost:16686`).
- **Metrics** scraped by Prometheus (`http://localhost:9090`) from
  `host.docker.internal:8081/metrics`. Visualized via the
  pre-provisioned **rfc-api overview** dashboard in Grafana
  (`http://localhost:3000`, anonymous Admin in dev).
- **Logs** via `slog` JSON to stdout → Alloy tails the docker socket →
  Loki. Query `{service="rfc-api"} | json` in Grafana Explore.

`trace_id` surfaces as a Loki label so clicking a log line in Grafana
jumps straight to the corresponding Jaeger trace.

## Profiling

pprof is opt-in via `RFC_API_PPROF_ENABLED=true` (on in `.env.example`).
When enabled, `/debug/pprof/*` is registered on the **admin port** only
(never on the main port — that port never exposes pprof regardless of
the flag).

Convenience Makefile targets:

```sh
make pprof-cpu        # 30s CPU profile -> interactive go tool pprof
make pprof-heap       # heap snapshot
make pprof-goroutine  # goroutine dump
make pprof-allocs     # allocation profile
make pprof-trace      # 5s runtime trace -> go tool trace
```

If the targets 404, either the binary isn't running or
`RFC_API_PPROF_ENABLED` is false.

## Troubleshooting

### `host.docker.internal` unreachable on Linux

Mac/Windows Docker Desktop resolves `host.docker.internal` automatically.
On Linux, compose services already declare:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

for prometheus and otel-collector. If you add a new service that needs
to reach the host-run `rfc-api`, add the same line to that service.

### Postgres won't come up clean after schema changes

```sh
make compose-nuke    # drops all volumes
make compose-up      # fresh start
```

This is a dev-only escape hatch — volumes persist data across
`compose down / up` cycles, so drop them only when you actually want
a clean slate.

### Alloy can't read docker container logs

Alloy needs read access to `/var/run/docker.sock`. The compose file
mounts it `:ro`. If Alloy logs show permission errors, check the
socket's group ownership matches what the alloy container can access
(on Linux, usually `docker` group); on Mac/Windows via Docker Desktop
this works out of the box.

### Tracing not reaching Jaeger

Confirm `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317` in `.env`
and that the `tracing` profile is up (`docker compose ps otel-collector
jaeger`). The collector forwards OTLP to `jaeger:4317` internally; the
host-run binary only needs to reach the collector on `localhost:4317`.

## References

- [IMPL-0001 #Prerequisites](./impl/0001-rfc-api-http-server-phase-1-implementation.md)
  — the source of truth for the stack shape.
- [DESIGN-0001 #Configuration](./design/0001-rfc-api-http-server-go-net-http-structure.md#configuration)
  — env-var naming rule and full config surface.
- [DESIGN-0001 #Observability hooks](./design/0001-rfc-api-http-server-go-net-http-structure.md#observability-hooks)
  — signal story (logs → stdout → Alloy, metrics → Prometheus,
  traces → OTel → Jaeger).
