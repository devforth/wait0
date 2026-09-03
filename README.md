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
    # Replace sessionid with every cookie that authenticates your users.
    bypassWhenCookies: ['sessionid']
    bypassWhenRequestHeaders: ['Authorization']
    # Stale-after signal: serve cached content, then refresh in background.
    expiration: '1m'

  - match: PathPrefix(/)
    priority: 2
    # Defaults to HTML and XHTML, keeping static assets out of wait0.
    cachableContentType: ['text/html', 'application/xhtml+xml']
    bypassWhenCookies: ['sessionid']
    bypassWhenRequestHeaders: ['Authorization']
    expiration: '1m'
```

2. Run `wait0` via Docker:

```bash
docker run --rm -p 8082:8082 \
  -v "$(pwd)/wait0.yaml:/wait0.yaml:ro" \
  -v wait0-data:/data \
  devforth/wait0:latest
```

The image declares `/data` as a volume and wait0 writes everything durable there, including the LevelDB cache and the [URL persister](#url-persister) file. Mount a named volume as shown; with `--rm` and no named volume that data goes to an anonymous volume and is lost on every stop.

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
| [Usage and configuration](#usage-and-configuration) | [How wait0 works](#how-wait0-works), [cache bypass](#cache-bypass), [cache variants](#cache-variants), [query parameters](#query-parameter-caching), [sitemap warmup](#sitemap-warmup), [URL persister](#url-persister), and the [config reference](#config-file-reference) |
| [For Developers](docs/for-developers.md) | Build/test commands, config reference, runtime options |
| [API Endpoints](docs/api-endpoints.md) | Proxy behavior, invalidation API, schemas, status codes |
| [Cache variant complexity](docs/cache-variant-complexity.md) | Root/subkey storage model and operation complexity |

## Usage and configuration

### How wait0 works

wait0 checks RAM, then disk, and waits for the origin only on a cache miss. A cached response is returned immediately. **Even when it is older than `expiration`, wait0 serves it first and refreshes it asynchronously.** Therefore, `expiration: '1m'` is a stale-after signal, not a hard expiry or eviction.

wait0 caches only `GET` responses that are safe for its shared cache. In addition to requiring a `2xx` status and an allowed `Content-Type`, it rejects private or immediately stale cache-control directives, `Set-Cookie`, and `Vary` headers that are not covered by the cache identity. Requests carrying `Cookie` or `Authorization` do not populate an unpartitioned cache unless the origin explicitly permits shared caching or partitions the response with `Cache-Variant`. `cachableContentType` defaults to `text/html` and `application/xhtml+xml`, including values with parameters such as `text/html; charset=utf-8`. This keeps wait0 focused on controllable dynamic SWR; static assets should normally be cached by a CDN, Nginx, and browser caching.

`logging.debug_headers` controls wait0 diagnostic response headers and the markers sent to the origin during revalidation. Omit it to enable every supported header, use a subset to select individual headers, or set `debug_headers: []` to disable all diagnostics. Functional headers such as `X-Wait0-CSRF` and origin-provided `X-Wait0-Tag` are not controlled by this option.

### Cache bypass

Request-side bypass rules are useful when a matching response must never be shared. They skip cache lookup, Cache-Variant evaluation, and storage:

```yaml
rules:
  - match: PathPrefix(/admin)
    bypass: true

  - match: PathPrefix(/)
    bypassWhenCookies: ['sessionid']
    bypassWhenRequestHeaders: ['Authorization']
```

`bypass: true` applies to every request matching the rule. `bypassWhenCookies` applies when any named cookie is present. `bypassWhenRequestHeaders` applies when any named request header is present; header names are matched case-insensitively, and an explicitly present empty header still triggers the bypass. Bypassed and non-`GET` requests are sent upstream as bodyless `GET` requests.

When diagnostic headers are enabled, request-side decisions are reported as follows:

| Condition | `X-Wait0` | `X-Wait0-Reason` |
|-----------|-----------|------------------|
| `bypass: true` | `bypass` | `bypass-rule` |
| Cookie listed by `bypassWhenCookies` is present | `ignore-by-cookie` | `bypass-cookie` |
| Header listed by `bypassWhenRequestHeaders` is present | `ignore-by-request-header` | `bypass-request-header` |
| Request method is not `GET` | `bypass` | `non-get-method` |

wait0 can also fetch an origin response but decline to store it:

| Origin result | `X-Wait0` | `X-Wait0-Reason` |
|---------------|-----------|------------------|
| `Cache-Control` contains `private`, `no-cache`, `no-store`, or a zero/invalid `max-age` or `s-maxage` | `bypass` | `non-cacheable-cache-control` |
| Response carries `Set-Cookie` | `bypass` | `non-cacheable-set-cookie` |
| `Vary` is `*` or names a header not normalized by wait0 or covered by `Cache-Variant` | `bypass` | `non-cacheable-vary` |
| Request carries `Cookie` or `Authorization` without shared-cache opt-in or a matching `Cache-Variant` | `bypass` | `non-cacheable-request-credentials` |
| `Content-Type` is missing, malformed, or not allowed by `cachableContentType` | `bypass` | `non-cacheable-content-type` |
| A `Cache-Variant` declaration cannot compile or evaluate | `bypass` | `cache-variant-expression-error` |
| Status is not `2xx` | `ignore-by-status` | `non-cacheable-status` |
| Origin request fails | `bad-gateway` | `origin-error` |

A non-cacheable miss response is returned to the client without being stored. If a legacy cache entry violates these response rules, wait0 evicts it instead of serving it. If background revalidation receives a disallowed response or a non-`2xx` status, the existing entry is deleted; a network error leaves it available. A stale cached response is still reported as `X-Wait0: hit`.

### Cache variants

Cache variants are useful when an SSR origin renders the same URL differently by country, region, device, cookie, or another request header. The origin declares one or more `Cache-Variant` response headers containing [Expr](https://github.com/expr-lang/expr) expressions:

```
response.append_header(
  'Cache-Variant',
  `"header('User-Agent') matches '(?i)(Android.*Mobile|iPhone|iPod|IEMobile|Windows Phone|Opera Mini)' ? 'mobile' : 'desktop'"`
)

response.append_header(
  'Cache-Variant',
  `"let c = header('CF-IPCountry', 'XX'); c == 'CA' && header('CF-Region-Code') == 'ON' ? 'CA-ON' : c"`
)
```

Each expression must return a string. Header names and fallbacks must be string literals. `header('Name')` requires the request header to exist; `header('Name', 'fallback')` supplies a value when it does not. The optional outer double quotes shown above are accepted. Multiple declarations are evaluated in response-header order and form a Cartesian variant family: the examples can produce keys such as `mobile|CA-ON` and `desktop|US`. Combinations are created lazily as requests discover them.

`X-Wait0-Cache-Variant-Key` reports the ordered values joined by `|`. Origin `Cache-Variant` headers remain visible in the client response for diagnosis. Invalid declarations use the expression-error behavior listed under [Cache bypass](#cache-bypass).

Discovered variants are monitored and warmed at their original URL using the request-header values that selected them. For example, discovering `mobile|CA` on `/a` adds a warmup target for `/a`; internal subcache keys are never requested from the origin.

You can seed additional request-header combinations on every warmup loop:

```yaml
rules:
  - match: PathPrefix(/)
    warmUp:
      pauseBetweenRuns: '10s'
      maxRequestsAtATime: 20
    warmupRequestHeaderPresets:
      CF-IPCountry: ['CA', 'US', 'UA']
      User-Agent: ['iPad', 'Mozilla']
```

Preset values form a Cartesian product, so this example can make six requests per URL in addition to any distinct discovered variants. Known variant keys are deduplicated. This option can produce substantial origin traffic and cache growth; `warmUp.maxRequestsAtATime` still limits concurrency.

All request headers are allowed, including `Cookie` and authorization headers. wait0 persists the values of headers referenced by `header(...)` so a discovered variant can be refreshed later. Choosing sensitive headers is therefore the operator's responsibility: restrict cache/disk access and avoid expressions that retain secrets unless that storage is acceptable.

See [Cache variant complexity](docs/cache-variant-complexity.md) for the root-manifest, subkey, family, and warmup complexity guarantees.

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

A warmup rule covers only the URLs that the rule itself governs, using the same priority resolution the request path uses. A broad rule such as `PathPrefix(/)` therefore never warms a path that a higher-priority rule already owns, and warmup schedules are not inherited: a rule with no `warmUp` block leaves its own paths cold even when a lower-priority rule warms everything else. Give every rule that needs warming its own `warmUp` block; wait0 logs a `warmup gap` line at startup for each rule that would otherwise silently lose warmup.

Dashboard statistics retain only the latest completed warmup loop per rule. Fastest, slowest, largest, and smallest URL rankings are capped at 10 entries while they are collected, so wait0 does not accumulate warmup history or discarded ranking candidates.

### URL persister

`urlPersister` remembers every URL that was successfully stored in the cache and writes that list to a YAML file. On the next start the file is read back and each remembered URL is registered as an inactive cache entry, so the existing warmup machinery refetches it. This is what carries a working set across the start-up disk cache wipe described in [Redeploy Note](#redeploy-note), which otherwise leaves every restart cold.

```yaml
urlPersister:
  enabled: true
  file: './data/urls.yaml'
  flushEvery: '30s'
  restoreOnStart: true
  maxUrls: 50000
  forgetAfterFailures: 3
```

Mount the directory that holds `file` as a volume, otherwise the list disappears with the container and the feature does nothing. The default path `./data/urls.yaml` resolves inside `/data`, which the official image already declares as a volume. The file must not be placed inside `./data/leveldb`; that directory is wiped on start, and such a path is rejected when the config is loaded.

The URL persister works with a sitemap or without one, and can replace one: its list is built from URLs traffic actually requested and wait0 actually cached, so it also covers pages no sitemap advertises. Sitemap-discovered URLs join the same list once they have been cached.

Notes:

- Tracking is immediate. `flushEvery` only controls how often the in-memory list is written to the file, and a write is skipped entirely when nothing changed.
- Restoring a URL only registers it; nothing is fetched by the restore itself. A restored URL is refetched only when a matching rule has a `warmUp` block. Paths with no matching rule, or a rule with `bypass: true`, are skipped.
- Cache variants are remembered by the request-header values their `Cache-Variant` expressions reference, which is what makes a variant replayable after restart. There is one record per distinct tuple of evaluated variant values, not one per request: a thousand `User-Agent` strings that all evaluate to `mobile` collapse into a single record.
- Query-aware entries created by `varyByQueryParams` are remembered with their query and stay separate records.
- `maxUrls` caps the file. When it is exceeded, the least recently used URLs are dropped.
- When a remembered URL stops being cacheable its cache entry is deleted and the record counts one failure; the record is forgotten once it reaches `forgetAfterFailures`, or on the first failure when the value is `0`. A successful store resets the counter. Warmup cannot retry a URL after its entry is deleted, so a URL that only warmup touches costs one failed request per start; a URL that clients still request counts a failure per request. Either way a dead URL is never hammered, and a brief origin outage does not erase the file.
- The file contains URL identities only: paths, query strings, variant values, and the referenced request headers. No response bodies and no response headers are written.
- A missing or corrupt file never blocks startup. wait0 falls back to the `.bak` copy written beside it, then to an empty list.
- While enabled, the stats API adds a `url_persister` object reporting `records`, `restored`, and `last_flush_unix`. It is omitted when the feature is off.

> **Warning:** when `urlPersister` is enabled, do not reference per-user request headers such as `Cookie` or `Authorization` in `Cache-Variant` expressions. Their values are written into this file in plain text so the variant can be replayed later, and an expression that returns such a value verbatim also echoes it in the `X-Wait0-Cache-Variant-Key` response header. Use expressions that collapse requests into a small shared set of values such as `mobile`/`desktop` or a country code.

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

urlPersister:
  # Remembers successfully cached URLs across restarts; default false.
  enabled: true
  # File the list is written to; must be outside ./data/leveldb, which is
  # wiped on start. Default './data/urls.yaml'.
  file: './data/urls.yaml'
  # How often the list is written to the file; default 30s. Tracking itself is
  # immediate, and an unchanged list is not rewritten.
  flushEvery: '30s'
  # Reads the file on start and registers each URL for warmup; default true.
  restoreOnStart: true
  # Maximum remembered URLs; least recently used are dropped. Default 50000.
  maxUrls: 50000
  # Failing starts a URL survives before it is forgotten; 0 forgets it on the
  # first failure. Default 3.
  forgetAfterFailures: 3

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
    # Bypasses cache when any named request header is present.
    bypassWhenRequestHeaders: ['Authorization']
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
    # Optional Cartesian header presets used on every warmup loop.
    warmupRequestHeaderPresets:
      CF-IPCountry: ['CA', 'US', 'UA']
      User-Agent: ['iPad', 'Mozilla']

logging:
  # Diagnostic response headers and origin revalidation markers. Omit this
  # option to enable all supported headers by default.
  debug_headers:
    - X-Wait0
    - X-Wait0-Reason
    - X-Wait0-Revalidated-At
    - X-Wait0-Revalidated-By
    - X-Wait0-Discovered-By
    - X-Wait0-Revalidate-At
    - X-Wait0-Revalidate-Entropy
    - X-Wait0-Cache-Variant-Key
  # To disable every diagnostic header, replace the list above with:
  # debug_headers: []
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
- A variant URL stores a constant-size root manifest; evaluated values select response subkeys without scanning sibling variants.
- Rule field `varyByQueryParams[]` opts specific query parameters into cache identity for matching paths.
- Query parameters not listed in `varyByQueryParams[]` and all fragments are ignored for cache identity.
- Only `GET` requests are cache candidates.
- Only origin `2xx` responses matching the rule's `cachableContentType` allowlist are stored.
- `cachableContentType` defaults to HTML and XHTML; use CDN, reverse-proxy, and browser caches for static assets.
- Rule `expiration` marks entries stale but does not evict them; stale responses are served immediately and revalidation is scheduled on a best-effort basis.
- On a cache-path miss or revalidation, an origin non-`2xx` is not cached and any existing key is evicted.
- Invalidation is asynchronous: accept request -> resolve keys by `paths` and `tags` -> delete keys -> recrawl in background. Path invalidation clears all cached query-aware variants for that path.
- Warmup monitors discovered cache variants and may expand `warmupRequestHeaderPresets` into additional Cartesian request-header combinations.
- A warmup rule warms only the paths it governs under normal priority resolution, so overlapping rules never warm the same path twice.
- Storing a response that is byte-identical to the one already in the disk cache refreshes only its metadata record and leaves the stored body untouched, so a warmup loop over unchanged content costs almost no disk writes. `Date` and `Age` are excluded from that comparison, which means a body that never changes keeps the `Date` it was stored with in the disk tier.
- `urlPersister` keeps successfully cached URL identities in a YAML file outside LevelDB and, on start, re-registers them as inactive entries so warmup repopulates the wiped disk cache.
