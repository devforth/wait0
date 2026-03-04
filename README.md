# wait0

- GitHub: https://github.com/devforth/wait0
- Docker Hub: https://hub.docker.com/r/devforth/wait0

Extremely fast cache-first dynamic HTTP proxy cacher with revalidate-under-the-hood strategy (SWR + warm up).

For extreme performance on SSR like next.js/nuxt.js or any other "slow" server side rendering.

## Mantras

- For non-bypass URLs, always serves from instant cache, which revalidates in background
- Only dynamic responses are cached which have one of headers `Cache-Control: no-cache` or `Cache-Control: no-store` or `Cache-Control: max-age=0` (or their combination)
- Adds headers for debug, e.g. x-wait0: hit|miss|bypass|ignore-by-cookie|ignore-by-status header to response to indicate cache status
- Never changes Cache-Control or any existing headers from origin
- Only GET requests are cached
- Only 2xx responses are cached, if non-2xx happens, cache is instantly invalidated
- Simple, fast & lightweight, one file configuration
- Bypass if some cookie exists (you define name) to prevent cookie-specific issues
- No query/fragment in cache key to prevent cache thrashing, only path is used as cache key.
- When wait0 starts - cache is empty (even disk one).
- Optionally wait0 can warm up from sitemaps so first users get hits!



## Usage

Create Dockerfile:

```yaml
FROM devforth/wait0:latest
ADD wait0.yaml /wait0.yaml
EXPOSE 8082
```

In Compose file:

```yaml
services:
  wait0:
    build: .
    ports:
      - "8082:8082"
```

Or simply attach `wait0.yaml` via volume if you have config on server:

```yaml
services:
  wait0:
    image: devforth/wait0:latest
    ports:
      - "8082:8082"
    volumes:
      - ./wait0.yaml:/wait0.yaml:ro
```

Config `wait0.yaml`:

```yaml
storage:
  # request path is cached as RAM->disk->origin, stops at first hit
  ram:
    max: '100m' # buffer for LRU cache, in fact RSS might be higher due to Go overhead
  disk:
    max: '1g'

server:
  # wait0 listens on this port and proxies to origin
  port: 8082
  origin: 'http://localhost:8080'
  invalidation:
    enabled: true
    queue_size: 128
    worker_concurrency: 4
    max_body_bytes: 1048576
    max_paths_per_request: 1024
    max_tags_per_request: 1024
    # if true, reject requests above max_paths/max_tags with 400 status code.
    # if false, process them and emit a warning log.
    hard_limits: false

auth:
  tokens:
    - id: backoffice
      # use one of token or token_env
      token_env: WAIT0_INVALIDATION_TOKEN
      scopes:
        - invalidation:write

rules:
  - match: PathPrefix(/api) | PathPrefix(/admin)
    priority: 1
    bypass: true
  - match: PathPrefix(/)
    priority: 2
    bypassWhenCookies:
      - sessionid
    # serves instantly, but if cache is stale,
    # it sends request to origin and updates cache
    expiration: '1m'

    # automatic scheduller which checks all known URLs in origin
    warmUp:
      runEvery: '10m'
      maxRequestsAtATime: 5

urlsDiscover:
  # optional: pre-discover URLs from sitemap(s) and seed them as inactive cache keys
  # so warmUp can fetch them without any user visiting first.
  # (historical typo "initalDelay" is supported)
  initalDelay: '20s'
  rediscoverEvery: '10m'
  sitemaps:
    - https://example.com/sitemap.xml

logging:
  # use this to analyze cache and RAM stats, e.g:
  # 2026-02-08 13:56:15 2026/02/08 11:56:15.116381 Cached: Paths: 7010, RAM usage: 6.4mb, Disk usage: 6.4mb, RSS: 136.7mb, RSSRollup: 138.1mb, RSSSplit: anon=132.1mb file=n/a shmem=n/a, GoAlloc: 73.1mb, Resp Min/avg/max 0b/0b/0b
  log_stats_every: '1m'
  # log warmup stats for each rule after a warmup batch drains:
  # 2026-02-08 13:56:09 2026/02/08 11:56:09.053192 Revalidated for match "PathPrefix(/)": 7010 URLs (unchanged=0 updated=2000 deleted=0 ignoredStatus=0 ignoredCC=0 errors=5010 updated+errors=7010), Took: 2.081s, RPS: 3367.34, resp time min/avg/max - 27ms/248ms/1.898s
  log_warmup: true
  # log url autodiscovery stats per-sitemap when urlsDiscover is enabled
  # urlsDiscover sitemap="https://.../sitemap.xml" urls=123 fit=120 ignored=3
  log_url_autodiscover: true
```

## Redeployment pitfall

In Nuxt/Next and similar SSR setups, HTML pages often reference versioned static assets (usually hashed filenames). After a redeploy those filenames can change, and you typically **should not** keep old static files around.

If old HTML is still cached in wait0, it may reference static files that are no longer available (or not yet present in a given CDN/geo). This can cause broken pages after redeploy.

To avoid this, invalidate all wait0 caches, by enforcing a docker service restart:

e.g. in compose:

```yaml
docker compose restart wait0
```

Both RAM and disk caches are cleared on restart, so all stale HTML is removed and new HTML with correct static asset references is cached.

If you need to pre-warm cache after redeploy, it is recommended to use a sitemap.

## Invalidation API

`wait0` exposes an authenticated async invalidation endpoint:

- `POST /wait0/invalidate`
- `Authorization: Bearer <token>`
- `Content-Type: application/json`

Request body:

```json
{
  "paths": ["/products/123", "/"],
  "tags": ["product:123", "homepage"]
}
```

Notes:
- `paths`, `tags`, or both can be provided.
- Path inputs are normalized to wait0 cache keys (path-only; query/fragment are ignored).
- Tag invalidation matches cached entries where origin response headers contain `X-Wait0-Tag` (supports repeated and comma-separated values).
- Invalidation runs in the background and returns `202 Accepted` immediately.
- After invalidation, wait0 automatically re-crawls affected paths to refill cache with fresh origin responses.

Example:

```bash
curl -i \
  -X POST "http://localhost:8082/wait0/invalidate" \
  -H "Authorization: Bearer ${WAIT0_INVALIDATION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"paths":["/products/123"],"tags":["product:123"]}'
```


## Under the hood

- First it checks RAM-cache for the URL, if exists, serves it instantly and revalidates in background if revalidate condition is met.
- Revalidated content calculates quick hash and check hash in storage, if hash is different, updates cache with new content and hash (read-safe, write-atomic)
- If RAM storage is full, it moves 10% of least recently used items to disk storage (based on leveldb) and removes them from RAM, if disk storage is full, it evicts 10% of least recently used items - deletes them from disk storage. Then it puts new item to RAM storage (by prechecking if it can fit in RAM, if not, it directly puts to disk storage if it can fit)
- if some storage is overflowin it drops log warning, not ofter then once per minute.

# For developers

How to run:

```bash
make test
go run ./cmd/wait0 -config ./debug/wait0.yaml
```

Testing and coverage gates:

```bash
# show all available targets
make help

# full local quality gate (lint + tests + race + coverage threshold + build)
make ci-check

# common commands
make build
make test
make test-race
make lint
make fmt
make coverage
make dev
```

Coverage policy:
- `internal/wait0` minimum coverage threshold: `80%`
- Explicit exclusions from threshold calculation:
  - `internal/wait0/stats/proc_linux.go`
  - `internal/wait0/stats/proc_other.go`

Docker quick commands:

```bash
make docker-build
make docker-run
make docker-logs
make docker-stop
```

Debug stack (origin + wait0):

```bash
bash debug/debug.sh
# get origin and wait0 logs:
curl -i http://localhost:8080/xx
curl -i http://localhost:8082/xx
```
