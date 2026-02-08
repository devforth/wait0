# wait0

Extreamly fast cache-first dynamic HTTP proxy cacher with revalidate-under-the-hood strategy. For extream performance on SSR like next.js/nuxt.js or any other "slow" server side rendering.

## Mantras

- For non-bypass URLs, always serves from instant cache, which revalidates in background
- Only dynamic responses are cached like which have headers `Cache-Control: no-cache` or `Cache-Control: no-store` 
- Adds x-wait0: hit|miss|bypass|ignore-by-cookie|ignore-by-status header to response to indicate cache status
- Never changes Cache-Control headers.
- Only GET requests are cached
- Only 2xx responses are cached, if non-2xx happens, cache is instantly invalidated
- Simple, fast & lightweight, one file configuration
- Bypass if some cookie exists (you define name) to prevent cookie-specific issues
- No query/fragment in cache key to prevent cache thrashing, only path is used as cache key.


## Under the hood

- First it checks RAM-cache for the URL, if exists, serves it instantly and revalidates in background if revalidate condition is met. 
- Revalidated content calculates quick hash and check hash in storage, if hash is different, updates cache with new content and hash (read-safe, write-atomic)
- If RAM storage is full, it moves 10% of least recently used items to disk storage (based on leveldb) and removes them from RAM, if disk storage is full, it evicts 10% of least recently used items - deletes them from disk storage. Then it puts new item to RAM storage (by prechecking if it can fit in RAM, if not, it directly puts to disk storage if it can fit)
- if some storage is overflowin it drops log warning, not ofter then once per minute.

## Ussage

Create Dockerfile:

```yaml
FROM wait0:latest
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

Config `wait0.yaml`:

```yaml
storage:
  # cache is received as RAM->disk->origin, cached first in RAM
  ram:
    max: '100m'
  disk:
    max: '1g'

server:
  port: 8082
  origin: 'http://localhost:8080'
  

  

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

logging:
  # use this to analyze cache and RAM stats, e.g:
  # 2026-02-08 13:56:15 2026/02/08 11:56:15.116381 Cached: Paths: 7010, RAM usage: 6.4mb, Disk usage: 6.4mb, RSS: 136.7mb, RSSRollup: 138.1mb, RSSSplit: anon=132.1mb file=n/a shmem=n/a, SmapsRollup: 00400000-7fffd0061000 ---p 00000000 00=0b AnonHugePages=0b Anonymous=132.1mb FilePmdMapped=0b KSM=0b LazyFree=0b Locked=0b Private_Clean=6.1mb Private_Dirty=132.1mb Private_Hugetlb=0b Pss=138.1mb Pss_Anon=132.1mb Pss_Dirty=132.1mb Pss_File=6.1mb Pss_Shmem=0b Referenced=138.1mb Rss=138.1mb Shared_Clean=4kb Shared_Dirty=0b Shared_Hugetlb=0b ShmemPmdMapped=0b Swap=0b SwapPss=0b, GoAlloc: 73.1mb, Resp Min/avg/max 0b/0b/0b
  log_stats_every: '1m'

  # use this to see revalidation stats for each rule:
  # 2026-02-08 13:56:09 2026/02/08 11:56:09.053192 Revalidated for match "PathPrefix(/)": 7010 URLs (unchanged=0 updated=2000 deleted=0 ignoredStatus=0 ignoredCC=0 errors=5010 updated+errors=7010), Took: 2.081s, RPS: 3367.34, resp time min/avg/max - 27ms/248ms/1.898s
  log_revalidation_every: '1m'
```

## Redeployment caveats

In Nuxt/Next and similar SSR setups, HTML pages often reference versioned static assets (usually hashed filenames). After a redeploy those filenames can change, and you typically **should not** keep old static files around.

If old HTML is still cached in wait0, it may reference static files that are no longer available (or not yet present in a given CDN/geo). This can cause broken pages after redeploy.

To avoid this, invalidate all wait0 caches, by enforcing a docker service restart:

e.g. in compose:

```yaml
docker compose restart wait0
```

Both RAM and disk caches are cleared on restart, so all stale HTML is removed and new HTML with correct static asset references is cached.

If you need to pre-warm cache after redeploy, it is recommended to use a sitemap.



# For developers

How to run:

```bash
go test ./...
go run ./cmd/wait0 -config ./wait0.yaml
```

Debug stack (origin + wait0):

```bash
./debug-compose
curl -i http://localhost:8082/
curl -i http://localhost:8082/

# cleanup
docker compose -f debug-compose.yml down -v
```
