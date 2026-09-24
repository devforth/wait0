[Back to README](../README.md) · [API Endpoints →](api-endpoints.md)

# For Developers

Developer guide for building, running, testing, and operating `wait0`.

## Requirements

- Go `1.27.1+`
- GNU Make
- `govulncheck` `v1.7.0+` for `make vulncheck` / `make ci-check`
- Optional: Docker (for containerized local runs)

Install the pinned vulnerability scanner with:

```bash
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
```

## Local Development

### Run with debug config

```bash
make dev
```

The development setup uses fixed local-only credentials:

- API bearer token: `demotoken`
- Dashboard Basic Auth: `admin` / `admin`

Never use the debug configuration in production.

Equivalent direct command:

```bash
go run ./cmd/wait0 -config ./debug/wait0.yaml
```

The direct command uses the same `demotoken` API credential. The dashboard is enabled automatically by `make dev`; when using the direct command, set both dashboard credential environment variables to `admin`.

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
| `make vulncheck` | Scans source and `bin/wait0` with `govulncheck` | No reachable known vulnerabilities |
| `make coverage` | Runs coverage gate for `internal/wait0` | Coverage >= configured threshold |
| `make ci-check` | Full local quality gate (`lint + test + test-race + coverage + build + vulncheck`) | Exit `0` when release-ready |
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
| `WAIT0_DASHBOARD_USERNAME` | unset | Basic Auth username for `GET /wait0/dashboard` and dashboard API routes |
| `WAIT0_DASHBOARD_PASSWORD` | unset | Basic Auth password for `GET /wait0/dashboard` and dashboard API routes |
| `WAIT0_DASHBOARD_RATE_LIMIT_RPM` | `120` | Per-IP fixed-window rate limit for `/wait0/dashboard*` routes |
| `WAIT0_DASHBOARD_TRUST_PROXY_HEADERS` | `false` | If `true`, dashboard rate limiter client IP extraction trusts `X-Forwarded-For` (first hop) |
| `WAIT0_DASHBOARD_TRUSTED_PROXY_CIDRS` | unset | Comma-separated CIDRs of trusted proxy source IPs allowed to supply `X-Forwarded-For` |

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

For dashboard:

- `stats:read` token is required to enable dashboard routes.
- `invalidation:write` token is optional; without it, dashboard is stats-only and invalidate action is disabled.
- Built-in dashboard rate limiting exists (`WAIT0_DASHBOARD_RATE_LIMIT_RPM`), but for internet-exposed deployments reverse-proxy/WAF rate limiting is still mandatory.
- Pre-auth rate-limit key is `client IP` only.
- Post-auth rate-limit key is `(client IP, verified auth principal)`.
- With trusted proxies enabled, `client IP` uses `X-Forwarded-For` first hop only when `RemoteAddr` belongs to `WAIT0_DASHBOARD_TRUSTED_PROXY_CIDRS`.

## `rules[]`

| Field | Required | Notes |
|-------|----------|------|
| `match` | yes | Supports `PathPrefix(...)` with optional `|` combinations |
| `priority` | no | Rules are sorted ascending by priority |
| `bypass` | no | For matching paths, bypass cache completely |
| `bypassWhenCookies[]` | no | If any listed cookie exists, bypass cache even when `allowSharedCacheWithCookies` is `false` and the origin sends `Cache-Control: public` |
| `allowSharedCacheWithCookies` | no | Default `false`; set `true` on public pages whose HTML is identical across cookies so cookie requests can share the anonymous cache entry |
| `bypassWhenRequestHeaders[]` | no | If any listed request header is present, bypass cache; matching is case-insensitive and empty values count |
| `cachableContentType[]` | no | Exact cache-eligible media types; defaults to `text/html` and `application/xhtml+xml`; parameters are ignored |
| `varyByQueryParams[]` | no | Query params that should participate in cache identity for matching paths |
| `expiration` | no | Duration for stale check and async revalidation |
| `warmUp.pauseBetweenRuns` | with `warmUp` | Pause after a complete loop before loading the next URL snapshot; must be `> 0`; `10s` is recommended for fast full-capacity reruns |
| `warmUp.runEvery` | deprecated | Legacy alias for `pauseBetweenRuns`; logs a replacement warning and uses the new pause-after-completion behavior |
| `warmUp.maxRequestsAtATime` | with `warmUp` | Must be `> 0` |
| `warmupRequestHeaderPresets` | no | Map of header names to non-empty value arrays; requires `warmUp` and expands their Cartesian product on every loop |

## `urlsDiscover`

| Field | Type | Notes |
|-------|------|------|
| `sitemaps[]` | URL list | Enables sitemap discovery loop |
| `initialDelay` | duration | Initial wait before first discovery |
| `initalDelay` | duration | Legacy typo still supported |
| `rediscoverEvery` | duration | Periodic rediscovery interval (`> 0`) |

## `urlPersister`

| Field | Type | Default | Notes |
|-------|------|---------|------|
| `enabled` | bool | `false` | Remembers successfully cached URLs in a YAML file; everything below applies only when `true` |
| `file` | path | `./data/urls.yaml` | Destination file; a `.bak` copy of the previous generation is kept beside it |
| `flushEvery` | duration | `30s` | File write interval (`> 0`); tracking is immediate and an unchanged list is not rewritten |
| `restoreOnStart` | bool | `true` | Reads the file on start and registers each URL as an inactive cache entry |
| `maxUrls` | int | `50000` | Cap on remembered URLs (`> 0`); the least recently used are dropped |
| `forgetAfterFailures` | int | `3` | Failures a URL survives before it is forgotten (`>= 0`); `0` forgets it on the first failure, and a successful store resets the counter. Warmup contributes at most one failure per start because a deleted entry is never retried; client requests contribute one each |

Validation rules: `file` must not resolve inside `./data/leveldb`, `flushEvery` must be `> 0`, `maxUrls` must be `> 0`, `forgetAfterFailures` must be `>= 0`.

## `logging`

| Field | Type | Notes |
|-------|------|------|
| `debug_headers` | string array | Diagnostic headers to emit; omitted enables all eight supported headers, while an explicit `[]` disables all |
| `log_stats_every` | duration | Enables periodic stats logging (`> 0`) |
| `log_warmup` | bool | Emits warmup batch summaries |
| `log_url_autodiscover` | bool | Emits per-sitemap discovery logs |
| `log_revalidation_every` | duration | Deprecated alias; enables warmup logging |

## Operational Notes

- Cache key is path-only by default (`/a/b`).
- Rule field `varyByQueryParams[]` opt-ins selected query params so `/a/b?page`, `/a/b?page=1`, and `/a/b` can be distinct cache entries.
- Query params not listed in `varyByQueryParams[]` and all fragments are ignored for cache identity.
- Only `GET` requests are cache-eligible.
- Bypassed and non-`GET` requests are sent upstream as `GET` without the original body.
- `bypassWhenRequestHeaders` skips lookup and storage when any configured header is present; use it for headers such as `Authorization` that make a response unsafe to share.
- Non-2xx origin responses are not cached and existing cached key is removed.
- Origin responses whose media type is not in `cachableContentType` are served but not stored; background revalidation deletes an existing entry if its new media type is disallowed.
- Keep static assets in CDN, Nginx, and browser caches; wait0 defaults to caching only dynamic HTML/XHTML SWR responses.
- Origin `2xx` responses are not stored when cache control is private/non-storable/immediately stale, the response sets a cookie, `Vary` is not covered by wait0's cache identity, or credential-bearing requests lack explicit shared-cache handling. The same admission policy evicts unsafe legacy entries and applies during revalidation.
- The shared origin client does not follow redirects. A `3xx` response is returned to the caller under the normal non-cacheable status behavior, preventing origin and sitemap redirects from becoming arbitrary outbound fetches.
- Warmup loops never overlap: wait0 completes the current rule snapshot, waits `pauseBetweenRuns`, then loads a fresh snapshot.
- A warmup rule covers only the paths it owns under the same priority resolution `pickRule` applies to requests, not everything its matcher accepts. Warmup schedules are never inherited from a lower-priority rule, so a rule without a `warmUp` block leaves its own paths cold; wait0 logs a `warmup gap` line at startup for each such rule that a lower-priority warming rule would otherwise have covered.
- The disk tier fingerprints each stored entry (body CRC32, body length, and a canonical hash of status, `Inactive`, `DiscoveredBy`, response headers, and variant identity). A store whose fingerprint matches the record already on disk rewrites only the `m:` metadata record and leaves the `e:` blob in place, which is what keeps a warmup loop over unchanged content from rewriting the whole cache to LevelDB every pass.
- `Date` and `Age` are excluded from that fingerprint because they differ on every origin response. The cost is that a blob whose body stops changing keeps the `Date` it was stored with; the RAM tier always holds the current headers, and the disk tier is only read after a RAM eviction or a restart.
- Freshness stamps live in the metadata record as well as the blob, and `Disk.Peek` overlays the metadata copies on the way out. Without that overlay a skipped body rewrite would leave the blob's original `StoredAt` in place, `proxy.IsStale` would treat the entry as permanently stale, and every request that reached the disk tier would queue a background revalidation.
- A metadata record whose `e:` blob is missing is dropped at open rather than indexed. Stores reuse the blob a metadata record describes, so an indexed key without a blob would answer every read with a miss instead of being repaired by the next store.
- Metadata written before the fingerprint fields existed reads back as zeroes, which never matches, so the first store after an upgrade rewrites the blob and populates them.
- Variant warmup replays the original URL with the referenced request headers saved on each discovered concrete response. `warmupRequestHeaderPresets` adds its full Cartesian product and can substantially increase cache entries and origin traffic.
- `Cache-Variant` expressions may reference every request header, including cookies and authorization. Referenced values are persisted for later refresh, so protecting sensitive values is the operator's responsibility. With `urlPersister` enabled those values also leave the process and land in the persisted YAML file in plain text.
- `urlPersister.file` must live outside `./data/leveldb`: that directory is deleted on every start when `WAIT0_INVALIDATE_DISK_CACHE_ON_START` is `true`, and surviving exactly that wipe is the point of the feature. A path inside it is rejected by config validation, so wait0 refuses to start.
- Mount the directory containing `urlPersister.file` as a volume. The image declares `/data`, and the default `./data/urls.yaml` resolves inside it.
- A missing, truncated, or otherwise corrupt persisted file never blocks startup: wait0 logs the problem, retries the `.bak` copy, and then continues with an empty list.
- Restoring writes inactive placeholder entries only; it fetches nothing. Restored URLs are refetched by the normal warmup loop, so they need a matching rule with a `warmUp` block, and paths matching a `bypass: true` rule are skipped.
- One record is stored per cache key, so a variant family contributes one record per distinct tuple of evaluated variant values rather than one per request. Records carrying a superseded `Cache-Variant` fingerprint are pruned when a new generation is stored.
- `GET /wait0` includes a `url_persister` object (`records`, `restored`, `last_flush_unix`) while the feature is enabled and omits it otherwise.
- `logging.debug_headers` may select any of `X-Wait0`, `X-Wait0-Reason`, `X-Wait0-Revalidated-At`, `X-Wait0-Revalidated-By`, `X-Wait0-Discovered-By`, `X-Wait0-Revalidate-At`, `X-Wait0-Revalidate-Entropy`, and `X-Wait0-Cache-Variant-Key`.
- Omit `logging.debug_headers` to enable all diagnostics; set `debug_headers: []` to disable all of them.
- `X-Wait0` response header identifies behavior (`hit`, `miss`, `bypass`, `ignore-by-cookie`, `ignore-by-request-header`, `ignore-by-status`, `bad-gateway`).
- `X-Wait0-Reason` identifies why a response was bypassed or failed (for example, `bypass-rule` or `non-cacheable-content-type`).

## See Also

- [API Endpoints](api-endpoints.md) — auth, schemas, status codes, and examples.
- [Cache variant complexity](cache-variant-complexity.md) — root manifests, subkeys, warmup, and family operations.
- [README](../README.md) — product overview and quick start.
