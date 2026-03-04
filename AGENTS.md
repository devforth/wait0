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
│   └── wait0/                     # Core service modules (config, cache, routing, warmup, stats)
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
| internal/wait0/service_core.go | Service composition root: lifecycle, initialization, worker startup |
| internal/wait0/handler.go | Main request handling path and cache hit/miss/bypass flow |
| internal/wait0/revalidate.go | Background and on-demand revalidation logic |
| internal/wait0/cache_ram.go / cache_disk.go | RAM + LevelDB cache implementations |
| internal/wait0/config.go | YAML configuration loading and rule parsing |
| debug/debug-compose.yml | Spins up reproducible local debug environment |
| Dockerfile | Defines production image for running wait0 |

## Documentation
| Document | Path | Description |
|----------|------|-------------|
| README | README.md | Project landing page and operational guide |
| Docker Hub notes | DOCKERHUB.md | Alias to README for Docker Hub presentation |

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
