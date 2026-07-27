# wait0

> Ultra-fast cache-first reverse proxy for dynamic SSR workloads.

`wait0` serves cached HTML instantly and revalidates in the background.
It is designed for Next.js/Nuxt.js and other dynamic origins where latency and origin offload matter.

- GitHub: https://github.com/devforth/wait0
- Docker Hub: https://hub.docker.com/r/devforth/wait0

## Why wait0

- Instant cached responses with background revalidation (SWR-like flow)
- Built for dynamic pages, not static asset caching
- RAM + LevelDB cache tiers for low latency and persistence
- Async invalidation API with bearer auth and tag support
- Built-in Basic-Auth dashboard for stats, charts, and invalidation
- Warmup and sitemap discovery to reduce cold-start misses
- Explicit response marker `X-Wait0` (`hit`, `miss`, `bypass`, etc.)
- Diagnostic `X-Wait0-Reason` header when a response is not cacheable

## Quick Start

1. Create `wait0.yaml`:

```yaml
storage:
  ram:
    max: '100m'
  disk:
    max: '1g'

server:
  port: 8082
  origin: 'http://localhost:8080'

rules:
  - match: PathPrefix(/blog)
    priority: 1
    varyByQueryParams: ['page']
    # Stale-after signal: serve cached content, then refresh in background.
    expiration: '1m'

  - match: PathPrefix(/)
    priority: 2
    # Defaults to HTML and XHTML, keeping static assets out of wait0.
    cachableContentType: ['text/html', 'application/xhtml+xml']
    expiration: '1m'
```

2. Run `wait0` via Docker:

```bash
docker run --rm -p 8082:8082 \
  -v "$(pwd)/wait0.yaml:/wait0.yaml:ro" \
  devforth/wait0:latest
```

3. Alternative: build a tiny wrapper image with your config baked in.

`Dockerfile`:

```dockerfile
FROM devforth/wait0:latest
COPY wait0.yaml /wait0.yaml
EXPOSE 8082
```

Build and run:

```bash
docker build -t my-wait0:latest .
docker run --rm -p 8082:8082 my-wait0:latest
```

4. Send requests through wait0:

```bash
curl -i http://localhost:8082/
```

First request is usually `X-Wait0: miss`, subsequent requests are usually `X-Wait0: hit`.

## Invalidation Example

```bash
curl -i \
  -X POST "http://localhost:8082/wait0/invalidate" \
  -H "Authorization: Bearer ${WAIT0_INVALIDATION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"paths":["/products/123"],"tags":["product:123"]}'
```

Returns `202 Accepted` and processes invalidation asynchronously.
Authorization is scope-based (`invalidation:write`) to support least-privilege tokens and future API growth without changing token model.

When you invalidate a path such as `/blog`, wait0 removes and recrawls all cached variants for that path, including query-aware keys like `/blog?page` and `/blog?page=2`.

## Stats API Example

```bash
curl -i \
  "http://localhost:8082/wait0" \
  -H "Authorization: Bearer ${WAIT0_STATS_TOKEN}"
```

Returns `200 OK` with cache/memory/refresh/sitemap metrics snapshot.
Authorization is scope-based (`stats:read`).

## Dashboard Example

Set dashboard credentials in environment:

```bash
export WAIT0_DASHBOARD_USERNAME='ops'
export WAIT0_DASHBOARD_PASSWORD='change-me'
```

Open:

```text
http://localhost:8082/wait0/dashboard
```

Notes:

- The dashboard is Basic-Auth protected and disabled when either env variable is missing.
- The dashboard reads stats and triggers invalidation through server-side bridge handlers.
- Bearer tokens are not exposed to browser JavaScript.
- Dashboard invalidation is CSRF-protected (same-origin + token check) and dashboard routes are rate-limited per IP.

## Documentation

| Guide | Description |
|-------|-------------|
| [Usage and configuration](#usage-and-configuration) | [How wait0 works](#how-wait0-works), [query parameters](#query-parameter-caching), [sitemap warmup](#sitemap-warmup), and the [config reference](#config-file-reference) |
| [For Developers](docs/for-developers.md) | Build/test commands, config reference, runtime options |
| [API Endpoints](docs/api-endpoints.md) | Proxy behavior, invalidation API, schemas, status codes |

## Usage and configuration

### How wait0 works

wait0 checks RAM, then disk, and waits for the origin only on a cache miss. A cached response is returned immediately. **Even when it is older than `expiration`, wait0 serves it first and refreshes it asynchronously.** Therefore, `expiration: '1m'` is a stale-after signal, not a hard expiry or eviction.

wait0 caches only `GET` responses with a `2xx` status, an allowed `Content-Type`, and no `Cache-Control: no-cache` or `no-store` directive. `cachableContentType` defaults to `text/html` and `application/xhtml+xml`, including values with parameters such as `text/html; charset=utf-8`. This keeps wait0 focused on controllable dynamic SWR; static assets should normally be cached by a CDN, Nginx, and browser caching.

An origin response that fails these checks is served as `X-Wait0: bypass` without being stored, with `X-Wait0-Reason` explaining why. If background revalidation receives a disallowed content type, either cache-control directive, or a non-`2xx` status, the existing entry is deleted; a network error leaves it available. A stale response is reported as `X-Wait0: hit`.

### Query parameter caching

Cache keys use only the URL path by default, so `/blog`, `/blog?page=1`, and `/blog?utm_source=email` share the same cached response. The first cold request reaches the origin with its complete query and fills the shared entry; later background refreshes omit query parameters that are not in `varyByQueryParams`.

Use `varyByQueryParams` to explicitly choose the parameters that must create different entries:

```yaml
varyByQueryParams: ['page', 'lang']
```

With this setting, `/blog?page=1` and `/blog?page=2` are different cache entries, while an unlisted parameter such as `utm_source` is ignored for cache identity and background refreshes. Allowed parameter names and values are normalized into a stable order.

### Sitemap warmup

`urlsDiscover` reads sitemap files and registers their paths in the disk cache. It supports sitemap indexes, gzip sitemaps, absolute URLs, and origin-relative paths. Discovery does not fetch each page body by itself: add `warmUp` to a matching rule to fetch and periodically refresh the discovered paths. Paths with no matching rule, or a rule with `bypass: true`, are ignored.

Each warmup rule starts its first loop immediately and processes one complete URL snapshot at up to `maxRequestsAtATime` concurrency. After the full loop finishes, wait0 pauses for `pauseBetweenRuns`, then takes a fresh snapshot and runs again. A value such as `10s` is recommended when you want warmup to restart quickly at full capacity without overlapping loops.

Dashboard statistics retain only the latest completed warmup loop per rule. Fastest, slowest, largest, and smallest URL rankings are capped at 10 entries while they are collected, so wait0 does not accumulate warmup history or discarded ranking candidates.

### Config file reference

This example contains every current configuration option. Durations use Go syntax such as `10s`, `1m`, or `2h`; sizes accept bytes or `k`, `m`, and `g` suffixes.

```yaml
storage:
  ram:
    # Maximum RAM cache size.
    max: '100m'
  disk:
    # Maximum LevelDB cache size.
    max: '1g'

server:
  # HTTP port; defaults to 8080 when omitted or set to 0.
  port: 8082
  # Required origin base URL.
  origin: 'http://host.docker.internal:8080'

  invalidation:
    # Enables POST /wait0/invalidate.
    enabled: true
    # Maximum number of waiting asynchronous invalidation jobs; default 128.
    queue_size: 128
    # Number of invalidation workers; default 4.
    worker_concurrency: 4
    # Maximum invalidation request body in bytes; default 1 MiB.
    max_body_bytes: 1048576
    # Recommended maximum normalized paths per request; default 1024.
    max_paths_per_request: 1024
    # Recommended maximum normalized tags per request; default 1024.
    max_tags_per_request: 1024
    # true rejects requests over path/tag limits; false accepts and logs them.
    hard_limits: false

auth:
  tokens:
    # Unique name used for authentication and audit logs.
    - id: 'backoffice'
      # Optional inline secret/fallback; prefer token_env in production.
      token: 'replace-me'
      # Environment variable whose non-empty value overrides token.
      token_env: 'WAIT0_API_AUTH_TOKEN'
      # invalidation:write protects invalidation; stats:read protects stats.
      scopes: ['invalidation:write', 'stats:read']

urlsDiscover:
  # Delay before the first sitemap scan; 0s scans immediately.
  initialDelay: '20s'
  # Rescan interval; omit to scan only once.
  rediscoverEvery: '10m'
  # Sitemap or sitemap-index URLs; origin-relative paths are also accepted.
  sitemaps:
    - 'https://example.com/sitemap.xml'

rules:
  # Matches path prefixes; combine alternatives with |.
  - match: PathPrefix(/api) | PathPrefix(/admin)
    # Lower priority is evaluated first; the first matching rule wins.
    priority: 1
    # Skips cache lookup and storage for matching requests.
    bypass: true

  - match: PathPrefix(/)
    priority: 2
    # Bypasses cache when any named cookie is present.
    bypassWhenCookies: ['sessionid']
    # Exact media types eligible for wait0 caching. Parameters such as charset
    # are ignored. Defaults to the two values shown here.
    cachableContentType: ['text/html', 'application/xhtml+xml']
    # Only these query parameters become part of the cache key.
    varyByQueryParams: ['page', 'lang']
    # Stale-after age: serve cached data immediately, then refresh async;
    # omit to disable request-triggered age refresh.
    expiration: '1m'
    warmUp:
      # After a full loop completes, pause before loading the next URL snapshot.
      pauseBetweenRuns: '10s'
      # Maximum concurrent requests for this warmup rule.
      maxRequestsAtATime: 20

logging:
  # Logs a stats snapshot at this interval; omit to disable.
  log_stats_every: '1m'
  # Logs a summary after each warmup batch.
  log_warmup: true
  # Logs details for each scanned sitemap.
  log_url_autodiscover: true
```

Compatibility-only options are still accepted but should not be used in new files: `urlsDiscover.initalDelay` is the old spelling of `initialDelay`, `logging.log_revalidation_every` is replaced by `log_warmup`, `server.invalidation.tokens` is replaced by top-level `auth.tokens`, and `warmUp.runEvery` is replaced by `warmUp.pauseBetweenRuns`. Legacy `runEvery` logs a warning and now uses pause-after-completion semantics.

## Redeploy Note

SSR frameworks like Next.js/Nuxt/Sveltekit usually output versioned static asset names (for example, `app.abc123.js`).
After a redeploy, HTML references switch to new filenames (for example, `app.def456.js`), and old files are commonly removed.

If stale HTML is still cached, clients can receive pages that point to missing assets. Typical symptoms are broken UI, hydration failures, and partial renders.

To reduce this risk, `wait0` clears disk cache on startup by default (`WAIT0_INVALIDATE_DISK_CACHE_ON_START=true`).
That behavior makes rollout safer because old HTML is not reused across deploy generations.

However, during redeploy you still need to restart wait0 by explicitly orchestrating it (for example, `docker restart wait0`).

If you do not restart between deploys, proactively refresh cache using invalidation and warmup for critical routes.

## Under the Hood

- Request pipeline: RAM cache -> disk cache -> origin.
- Cache key is path-only by default.
- Rule field `varyByQueryParams[]` opts specific query parameters into cache identity for matching paths.
- Query parameters not listed in `varyByQueryParams[]` and all fragments are ignored for cache identity.
- Only `GET` requests are cache candidates.
- Only origin `2xx` responses matching the rule's `cachableContentType` allowlist are stored.
- `cachableContentType` defaults to HTML and XHTML; use CDN, reverse-proxy, and browser caches for static assets.
- Rule `expiration` marks entries stale but does not evict them; stale responses are served immediately and revalidation is scheduled on a best-effort basis.
- On a cache-path miss or revalidation, an origin non-`2xx` is not cached and any existing key is evicted.
- Invalidation is asynchronous: accept request -> resolve keys by `paths` and `tags` -> delete keys -> recrawl in background. Path invalidation clears all cached query-aware variants for that path.
