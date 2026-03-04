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
| internal/wait0/service.go | Main request handling path, cache lookup/store flow, and background revalidation |
| internal/wait0/config.go | YAML configuration loading and rule parsing |
| debug/debug-compose.yml | Spins up reproducible local debug environment |
| Dockerfile | Defines production image for running wait0 |

## Documentation
| Document | Path | Description |
|----------|------|-------------|
| README | README.md | Project landing page and operational guide |
| Docker Hub notes | DOCKERHUB.md | Alias to README for Docker Hub presentation |
