
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
EXPOSE 8080
```

In Compose file:

```yaml
services:
  wait0:
    build: .
    ports:
      - "8080:8080"
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
    wormUp: '10m' 
```

# For developers

How to run:

```bash