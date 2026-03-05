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
- Warmup and sitemap discovery to reduce cold-start misses
- Explicit response marker `X-Wait0` (`hit`, `miss`, `bypass`, etc.)

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
  - match: PathPrefix(/)
    priority: 1
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

## Stats API Example

```bash
curl -i \
  "http://localhost:8082/wait0" \
  -H "Authorization: Bearer ${WAIT0_STATS_TOKEN}"
```

Returns `200 OK` with cache/memory/refresh/sitemap metrics snapshot.
Authorization is scope-based (`stats:read`).

## Documentation

| Guide | Description |
|-------|-------------|
| [For Developers](docs/for-developers.md) | Build/test commands, config reference, runtime options |
| [API Endpoints](docs/api-endpoints.md) | Proxy behavior, invalidation API, schemas, status codes |

## Redeploy Note

SSR frameworks like Next.js/Nuxt usually output versioned static asset names (for example, `app.abc123.js`).
After a redeploy, HTML references switch to new filenames (for example, `app.def456.js`), and old files are commonly removed.

If stale HTML is still cached, clients can receive pages that point to missing assets. Typical symptoms are broken UI, hydration failures, and partial renders.

To reduce this risk, `wait0` clears disk cache on startup by default (`WAIT0_INVALIDATE_DISK_CACHE_ON_START=true`).
That behavior makes rollout safer because old HTML is not reused across deploy generations.

If you do not restart between deploys, proactively refresh cache using invalidation and warmup for critical routes.

## Under the Hood

- Request pipeline: RAM cache -> disk cache -> origin.
- Cache key is path-only (query and fragment are ignored for cache identity).
- Only `GET` requests are cache candidates.
- Only origin `2xx` responses are stored.
- For stale entries (rule `expiration`), wait0 serves cached response immediately and revalidates asynchronously.
- For origin non-`2xx`, wait0 skips caching and evicts any existing key for that path.
- Invalidation is asynchronous: accept request -> resolve keys by `paths` and `tags` -> delete keys -> recrawl in background.
