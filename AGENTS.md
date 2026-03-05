# AGENTS.md

> Project map for AI agents. Keep this file up-to-date as the project evolves.

## Project Overview
wait0 is an ultra-fast cache-first HTTP reverse proxy written in Go that serves from cache instantly and revalidates responses in the background. It targets SSR and other dynamic origin workloads where latency and origin offload are critical.

## Tech Stack
- **Language:** Go 1.22
- **Framework:** Standard library `net/http`
- **Database:** LevelDB (embedded disk cache)
- **ORM:** N/A

## Project Structure
```text
.
├── cmd/
│   └── wait0/
│       └── main.go                # Program entrypoint and HTTP server lifecycle
├── internal/
│   └── wait0/
│       ├── service_core.go        # Service composition root and lifecycle wiring
│       ├── config.go              # YAML schema parsing + validation
│       ├── cache_ram.go           # Root cache facade (wraps cache module)
│       ├── cache_disk.go          # Root cache facade (wraps cache module)
│       ├── *_runtime_adapter.go   # Root adapters that inject Service deps into modules
│       ├── auth/                  # Shared bearer authentication
│       ├── invalidation/          # /wait0/invalidate API + async workers
│       ├── dashboard/             # /wait0/dashboard HTML + stats/invalidation bridge handlers
│       ├── proxy/                 # Request handling/origin fetch/response headers
│       ├── revalidation/          # Revalidate and warmup orchestration
│       ├── discovery/             # Sitemap discovery and URL normalization
│       ├── stats/                 # Metrics collector, periodic stats loop, proc probes
│       └── cache/                 # Cache internals (RAM + LevelDB + codec)
├── debug/
│   ├── debug-compose.yml          # Local debug stack (origin + wait0)
│   ├── wait0.yaml                 # Debug configuration example
│   ├── debug.sh                   # Helper script for local debugging
│   └── stress.sh                  # Stress/load helper script
├── Dockerfile                     # Container image build definition
├── README.md                      # Usage, config, behavior, and developer notes
├── go.mod                         # Go module definition and dependencies
├── go.sum                         # Dependency checksums
└── publish.sh                     # Publishing helper script
```

## Key Entry Points
| File | Purpose |
|------|---------|
| cmd/wait0/main.go | Bootstraps config/service, starts HTTP server, handles graceful shutdown |
| internal/wait0/service_core.go | Root composition: constructs module controllers and adapters |
| internal/wait0/dashboard/controller.go | Dashboard HTTP controller (Basic Auth + bridge endpoints + HTML shell) |
| internal/wait0/proxy/controller.go | Main request handling path and cache hit/miss/bypass flow |
| internal/wait0/revalidation/controller.go | Async revalidation and warmup orchestration |
| internal/wait0/cache_ram.go / cache_disk.go | Root cache facades wrapping `internal/wait0/cache` |
| internal/wait0/config.go | YAML configuration loading and rule parsing |
| debug/debug-compose.yml | Spins up reproducible local debug environment |
| Dockerfile | Defines production image for running wait0 |

## Documentation
| Document | Path | Description |
|----------|------|-------------|
| README | README.md | Project landing page and quick start |
| For Developers | docs/for-developers.md | Build, config, and operations guide |
| API Endpoints | docs/api-endpoints.md | Endpoint and response reference |
| Docker Hub notes | DOCKERHUB.md | Alias to README for Docker Hub presentation |

## AI Context Files
| File | Description |
|------|-------------|
| .ai-factory/ARCHITECTURE.md | Architecture decisions and guidelines |

## Build & Development Commands
This project uses a `Makefile` for build automation.

Common commands:
- `make help` — list all available targets
- `make test` — run unit tests
- `make test-race` — run race-enabled tests
- `make coverage` — run coverage gate for `internal/wait0`
- `make lint` — run static checks (`go vet`)
- `make build` — build the `wait0` binary
- `make ci-check` — run full local quality gate
- `make docker-build` / `make docker-run` — build and run the container locally
