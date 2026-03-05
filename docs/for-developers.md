[Back to README](../README.md) · [API Endpoints →](api-endpoints.md)

# For Developers

Developer guide for building, running, testing, and operating `wait0`.

## Requirements

- Go `1.22+`
- GNU Make
- Optional: Docker (for containerized local runs)

## Local Development

### Run with debug config

```bash
make dev
```

Equivalent direct command:

```bash
go run ./cmd/wait0 -config ./debug/wait0.yaml
```

### Build binary

```bash
make build
```

Output binary: `bin/wait0`.

## Quality Commands

| Command | What it does | Expected result |
|---------|---------------|-----------------|
| `make test` | Runs unit tests | Exit `0` on success |
| `make test-race` | Runs tests with race detector | Exit `0` and no race reports |
| `make lint` | Runs `go vet ./...` | No vet issues |
| `make coverage` | Runs coverage gate for `internal/wait0` | Coverage >= configured threshold |
| `make ci-check` | Full local quality gate (`lint + test + test-race + coverage + build`) | Exit `0` when release-ready |
| `make fmt` | Runs `go fmt ./...` | Source files formatted |
| `make help` | Lists all targets | Human-readable command list |

Coverage threshold default: `80%` (`COVERAGE_THRESHOLD` in `Makefile`).

## Runtime Interface

### CLI flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-config` | string | `/wait0.yaml` (or `WAIT0_CONFIG`) | Path to YAML config file |

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WAIT0_CONFIG` | `/wait0.yaml` | Default value for `-config` |
| `WAIT0_INVALIDATE_DISK_CACHE_ON_START` | `true` | If `true`, LevelDB cache directory is cleared on process start |
| `WAIT0_SEND_REVALIDATE_MARKERS` | `true` | Controls sending revalidation marker headers during background revalidation |

## Configuration Reference (`wait0.yaml`)

## `storage`

| Field | Type | Required | Notes |
|-------|------|----------|------|
| `storage.ram.max` | size string | yes | RAM budget (example: `100m`) |
| `storage.disk.max` | size string | yes | Disk budget (example: `1g`) |

## `server`

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|------|
| `server.port` | int | no | `8080` | Listener port |
| `server.origin` | URL string | yes | - | Origin base URL (trailing slash trimmed) |

### `server.invalidation`

| Field | Type | Default | Notes |
|-------|------|---------|------|
| `enabled` | bool | `false` | Enables `POST /wait0/invalidate` |
| `queue_size` | int | `128` | Buffered async queue size |
| `worker_concurrency` | int | `4` | Number of invalidation workers |
| `max_body_bytes` | int | `1048576` | Max request body size |
| `max_paths_per_request` | int | `1024` | Paths soft/hard cap |
| `max_tags_per_request` | int | `1024` | Tags soft/hard cap |
| `hard_limits` | bool | `false` | When `true`, oversized path/tag arrays return `400` |

Validation rules: all numeric values above must be `> 0`.

## `auth`

### `auth.tokens[]`

| Field | Required | Notes |
|-------|----------|------|
| `id` | yes | Unique token ID |
| `token` or `token_env` | yes | `token_env` is resolved at startup |
| `scopes[]` | yes | At least one scope, deduplicated |

For invalidation API, at least one token must have scope `invalidation:write` when invalidation is enabled.

For stats API (`GET /wait0`), tokens need scope `stats:read`.

## `rules[]`

| Field | Required | Notes |
|-------|----------|------|
| `match` | yes | Supports `PathPrefix(...)` with optional `|` combinations |
| `priority` | no | Rules are sorted ascending by priority |
| `bypass` | no | For matching paths, bypass cache completely |
| `bypassWhenCookies[]` | no | If any listed cookie exists, bypass cache |
| `expiration` | no | Duration for stale check and async revalidation |
| `warmUp.runEvery` | with `warmUp` | Duration, must be `> 0` |
| `warmUp.maxRequestsAtATime` | with `warmUp` | Must be `> 0` |

## `urlsDiscover`

| Field | Type | Notes |
|-------|------|------|
| `sitemaps[]` | URL list | Enables sitemap discovery loop |
| `initialDelay` | duration | Initial wait before first discovery |
| `initalDelay` | duration | Legacy typo still supported |
| `rediscoverEvery` | duration | Periodic rediscovery interval (`> 0`) |

## `logging`

| Field | Type | Notes |
|-------|------|------|
| `log_stats_every` | duration | Enables periodic stats logging (`> 0`) |
| `log_warmup` | bool | Emits warmup batch summaries |
| `log_url_autodiscover` | bool | Emits per-sitemap discovery logs |
| `log_revalidation_every` | duration | Deprecated alias; enables warmup logging |

## Operational Notes

- Cache key is path-only (`/a/b`); query and fragment are ignored for cache identity.
- Only `GET` requests are cache-eligible.
- Non-2xx origin responses are not cached and existing cached key is removed.
- Dynamic pages are expected to send `Cache-Control: no-cache` or `no-store` so wait0 treats them as passthrough and revalidation-managed.
- `X-Wait0` response header identifies behavior (`hit`, `miss`, `bypass`, `ignore-by-cookie`, `ignore-by-status`, `bad-gateway`).

## See Also

- [API Endpoints](api-endpoints.md) — auth, schemas, status codes, and examples.
- [README](../README.md) — product overview and quick start.
