[← For Developers](for-developers.md) · [Back to README](../README.md)

# API Endpoints

Complete HTTP endpoint reference for `wait0`.

## Overview

`wait0` exposes:

- A reverse-proxy data path for regular client requests.
- A control endpoint for asynchronous cache invalidation.
- A control endpoint for read-only runtime/cache statistics.
- A Basic-Auth dashboard route with stats polling and invalidation form.

Base URL examples:

- Local: `http://localhost:8082`
- Docker: mapped host/port from your compose file.

## 1) Reverse Proxy Data Path

## Route

- `ANY /<path>`

This is the default request path handled by the proxy controller.

## Behavior

| Condition | Result | `X-Wait0` | `X-Wait0-Reason` |
|----------|--------|-----------|------------------|
| Matching rule has `bypass: true` | Skip cache; fetch upstream with bodyless `GET` | `bypass` | `bypass-rule` |
| Matching rule cookie bypass is triggered | Skip cache; fetch upstream with bodyless `GET` | `ignore-by-cookie` | `bypass-cookie` |
| A header listed by `bypassWhenRequestHeaders` is present | Skip cache; fetch upstream with bodyless `GET` | `ignore-by-request-header` | `bypass-request-header` |
| Method is not `GET` | Skip cache; convert to bodyless upstream `GET` | `bypass` | `non-get-method` |
| RAM or disk hit for active entry | Serve cached response instantly | `hit` | absent |
| Variant manifest evaluates to a cached concrete subkey | Serve that variant instantly | `hit` | absent |
| Miss and cacheable origin `2xx` | Store and serve response | `miss` | absent |
| Cacheable origin `2xx` declares valid `Cache-Variant` expressions | Store a root manifest and the selected concrete response | `miss` | absent |
| `Cache-Variant` cannot compile/evaluate or does not return strings | Serve without storing | `bypass` | `cache-variant-expression-error` |
| Origin `2xx` has disallowed `Content-Type` | Serve without storing | `bypass` | `non-cacheable-content-type` |
| Origin `2xx` has a disallowed cache-control directive | Serve without storing | `bypass` | `non-cacheable-cache-control` |
| Origin `2xx` carries `Set-Cookie` | Serve without storing | `bypass` | `non-cacheable-set-cookie` |
| Origin `2xx` has an unsupported or unkeyed `Vary` value | Serve without storing | `bypass` | `non-cacheable-vary` |
| Request has cookies without rule or origin shared-cache opt-in, or has authorization without origin shared-cache opt-in | Serve without storing | `bypass` | `non-cacheable-request-credentials` |
| Origin non-`2xx` | Do not cache, evict existing key | `ignore-by-status` | `non-cacheable-status` |
| Origin fetch/network failure | Gateway error | `bad-gateway` | `origin-error` |

## Cacheability rule

An origin response is cacheable only when:

- status is `2xx`, and
- the media type in `Content-Type` appears in the matching rule's `cachableContentType` list, and
- `Cache-Control` does not include `private`, `no-store`, `no-cache`, a zero/invalid `max-age`, or a zero/invalid `s-maxage`,
- the response does not carry `Set-Cookie`,
- `Vary` is not `*` and each named header is normalized by wait0 (`Host` and `Accept-Encoding`) or represented by `Cache-Variant`,
- a request carrying `Cookie` is allowed by the rule (`allowSharedCacheWithCookies: true`), explicitly shareable according to the origin (`Cache-Control: public` or a positive `s-maxage`), or partitioned by `Cache-Variant`; `Authorization` still requires origin shared-cache permission or a matching `Cache-Variant`, and
- every declared `Cache-Variant` expression compiles, evaluates for the request, and returns a string.

`cachableContentType` defaults to `text/html` and `application/xhtml+xml`. Matching is case-insensitive and ignores media-type parameters such as `charset=utf-8`. A missing or malformed `Content-Type` is not cacheable.

Origin redirects are returned to the client and are never followed by wait0. Redirect responses remain non-cacheable under the `2xx` status rule.

## Diagnostic headers added by wait0

| Header | When present | Meaning |
|--------|--------------|---------|
| `X-Wait0` | handled responses when enabled | Cache/proxy decision marker |
| `X-Wait0-Reason` | response bypassed or failed | Machine-readable reason the response was not cached |
| `X-Wait0-Revalidated-At` | cache `hit` with revalidation metadata | Last revalidation timestamp (RFC3339Nano) |
| `X-Wait0-Revalidated-By` | with `X-Wait0-Revalidated-At` | Revalidation source (`user`, `warmup`, `invalidate`, etc.) |
| `X-Wait0-Discovered-By` | if entry was discovery seeded | Discovery source marker |
| `X-Wait0-Cache-Variant-Key` | response selected/stored through `Cache-Variant` | Ordered expression results joined by `|` |
| `Access-Control-Expose-Headers` | when wait0 headers exist | Exposes wait0 headers to browser clients |

During background revalidation, wait0 can also send `X-Wait0-Revalidate-At` and `X-Wait0-Revalidate-Entropy` to the origin. `logging.debug_headers` controls all eight diagnostic headers. Omission enables all; an explicit `debug_headers: []` disables all. A configured subset enables only the named headers. `WAIT0_SEND_REVALIDATE_MARKERS=false` remains a master disable for the two origin markers.

## Example

```bash
curl -i "http://localhost:8082/"
```

Typical first request: `X-Wait0: miss`.
Subsequent request: `X-Wait0: hit`.

## 2) Stats API

## Route

- `GET /wait0`
- `GET /wait0/`

## Auth

- `Authorization: Bearer <token>` required.
- Token must map to scope: `stats:read`.

## Behavior

- Returns a cached metrics snapshot (recomputed at most once every 5 seconds).
- Designed for dashboard/backoffice polling with bounded server overhead.
- `refresh_duration_ms` is calculated from observed revalidation execution durations (min/avg/max), not cache entry age.
- Duration aggregates are process-lifetime metrics (since current process start).

## Successful response

Status: `200 OK`

```json
{
  "generated_at": "2026-03-05T10:00:00Z",
  "snapshot_ttl_seconds": 5,
  "cache": {
    "urls_total": 123,
    "responses_size_bytes_total": 456789,
    "response_size_bytes": {
      "min": 128,
      "avg": 1024,
      "max": 4096
    }
  },
  "memory": {
    "rss_bytes": 12345678,
    "go_alloc_bytes": 2345678
  },
  "refresh_duration_ms": {
    "min": 19,
    "avg": 66,
    "max": 119
  },
  "sitemap": {
    "discovered_urls": 80,
    "crawled_urls": 60,
    "crawl_percentage": 75
  },
  "url_persister": {
    "records": 1240,
    "restored": 1180,
    "last_flush_unix": 1755252664
  },
  "rules": [
    {
      "id": 0,
      "match": "PathPrefix(/)",
      "priority": 2,
      "urls": 123,
      "responses": 120,
      "ram_size_bytes": 345678,
      "disk_size_bytes": 567890,
      "warmup": {
        "configured": true,
        "pause_between_runs_ms": 10000,
        "last_loop_duration_ms": 1532,
        "last_finished_at": "2026-03-05T09:59:56Z",
        "last_finished_seconds_ago": 4,
        "urls": 123,
        "top_slowest_urls": [{"url": "/reports", "duration_ms": 119.4}],
        "top_fastest_urls": [{"url": "/", "duration_ms": 19.2}]
      },
      "top_largest_responses": [{"url": "/reports", "size_bytes": 4096}],
      "top_smallest_responses": [{"url": "/", "size_bytes": 128}]
    }
  ]
}
```

## Response field reference

The table below explains each field in the stats payload, including what it means and how it is computed.

| Field | Type | Meaning | Calculation | Update behavior / notes |
|------|------|---------|-------------|-------------------------|
| `generated_at` | RFC3339Nano string | UTC timestamp when this snapshot was generated. | `time.Now().UTC()` at snapshot build time. | New value only when snapshot is recomputed. |
| `snapshot_ttl_seconds` | integer | Snapshot cache TTL used by `/wait0`. | Fixed constant `5`. | Endpoint may return identical payload for calls within this TTL. |
| `cache.urls_total` | integer | Total number of logical cached URLs currently known to wait0. Includes active + inactive entries. | Unique union of RAM and disk keys after concrete variant subkeys are folded into their base root. | Recomputed per snapshot. |
| `cache.responses_size_bytes_total` | integer (bytes) | Total logical size of cached concrete responses. Variant manifests are excluded. | Sum over unique physical response entries of logical size (`headers + body` bytes). | Recomputed per snapshot. |
| `cache.response_size_bytes.min` | integer (bytes) | Smallest logical response size among concrete response entries. | Min of per-response logical size; variant manifests are excluded. | Recomputed per snapshot; `0` when no responses. |
| `cache.response_size_bytes.avg` | integer (bytes) | Average logical response size among concrete response entries. | `responses_size_bytes_total / concrete response count` (integer division). | Recomputed per snapshot; `0` when no responses. |
| `cache.response_size_bytes.max` | integer (bytes) | Largest logical response size among concrete response entries. | Max of per-response logical size; variant manifests are excluded. | Recomputed per snapshot; `0` when no responses. |
| `memory.rss_bytes` | integer (bytes) | Current process resident memory (RSS) as seen by OS probes. | `ProcessRSSBytes()`; `0` when unavailable on platform/runtime. | Recomputed per snapshot. |
| `memory.go_alloc_bytes` | integer (bytes) | Current heap bytes allocated by Go runtime. | `runtime.ReadMemStats(&ms); ms.Alloc`. | Recomputed per snapshot. |
| `refresh_duration_ms.min` | integer (ms) | Fastest observed revalidation execution time. | Min of observed `revalidation.Once(...)` durations, converted to milliseconds. | Process-lifetime aggregate since current process start. |
| `refresh_duration_ms.avg` | integer (ms) | Average observed revalidation execution time. | Sum of all observed revalidation durations / count, converted to ms (integer division). | Process-lifetime aggregate since current process start. |
| `refresh_duration_ms.max` | integer (ms) | Slowest observed revalidation execution time. | Max of observed `revalidation.Once(...)` durations, converted to milliseconds. | Process-lifetime aggregate since current process start. |
| `sitemap.discovered_urls` | integer | Number of unique cached keys whose discovery source is sitemap. | Count of unique keys where `discovered_by == "sitemap"` (case-insensitive). | Recomputed per snapshot. |
| `sitemap.crawled_urls` | integer | Number of sitemap-discovered keys that are currently active (not inactive seed entries). | Count of sitemap keys where `inactive == false`. | Recomputed per snapshot. |
| `sitemap.crawl_percentage` | float | Share of sitemap-discovered keys currently crawled/active. | `crawled_urls * 100 / discovered_urls`; `0` if `discovered_urls == 0`. | Recomputed per snapshot. |
| `url_persister` | object | Durable URL list state. Omitted entirely when `urlPersister.enabled` is false. | Read from the in-memory registry. | Absent key means the feature is off. |
| `url_persister.records` | integer | URLs currently remembered, including variants as separate records. | Size of the registry, which is capped by `maxUrls`. | Recomputed per snapshot. |
| `url_persister.restored` | integer | Records seeded from the file at the last start. | Excludes records skipped because no rule matched or the rule bypasses. | `0` when `restoreOnStart` is false or the file was missing. |
| `url_persister.last_flush_unix` | integer (unix seconds) | When the file was last written. | Updated only on a successful write; a flush with nothing dirty does not write. | `0` before the first write. |
| `rules[]` | array | One entry for every configured rule, including rules with zero URLs or responses. | Configuration order after priority sorting. Cached keys are assigned to the first matching rule. | Recomputed per snapshot. |
| `rules[].urls` | integer | Unique logical root URLs assigned to this rule, including inactive discovery seeds. | Union of matching RAM and disk keys after variant subkeys are folded into their base root. | `0` when empty. |
| `rules[].responses` | integer | Active concrete responses assigned to this rule. | Matching non-manifest keys where `inactive == false`; each concrete variant is one response. | `0` when the rule has no responses. |
| `rules[].ram_size_bytes` | integer (bytes) | Encoded bytes occupied by this rule in RAM. | Sum of matching RAM entry storage sizes. | A key present in both tiers contributes to both tier sizes. |
| `rules[].disk_size_bytes` | integer (bytes) | Encoded bytes occupied by this rule on disk. | Sum of matching LevelDB entry storage sizes. | A key present in both tiers contributes to both tier sizes. |
| `rules[].warmup.configured` | boolean | Whether this rule has warmup configured. | `pauseBetweenRuns > 0` and `maxRequestsAtATime > 0`. | False rules are displayed as `- not set`. |
| `rules[].warmup.pause_between_runs_ms` | integer (ms) | Configured guaranteed pause between complete warmup loops. | Parsed `pauseBetweenRuns` duration. | `0` when warmup is not configured. |
| `rules[].warmup.last_loop_duration_ms` | integer or null | Wall-clock duration of the last fully completed warmup loop. | Finish time minus batch start time; excludes the pause between runs. | `null` until the first loop finishes or when warmup is not set. |
| `rules[].warmup.last_finished_at` | RFC3339Nano string or null | UTC finish time of the last complete loop. | Recorded only after every URL in that loop finishes. | `null` until first completion. |
| `rules[].warmup.last_finished_seconds_ago` | integer or null | Whole seconds since the last completed loop. | Snapshot time minus `last_finished_at`. | Dashboard also derives the live value from the timestamp. |
| `rules[].warmup.urls` | integer | URL attempts completed in the last full warmup loop. | Count of results in that loop's fixed URL snapshot. | `0` before a non-empty loop completes. |
| `rules[].warmup.top_slowest_urls[]` | array | Up to 10 slowest URL attempts from the last completed loop. | Descending request duration. | Empty when no completed URL attempts exist. |
| `rules[].warmup.top_fastest_urls[]` | array | Up to 10 fastest URL attempts from the last completed loop. | Ascending request duration. | Empty when no completed URL attempts exist. |
| `rules[].top_largest_responses[]` | array | Up to 10 largest active cached responses currently assigned to the rule. | Descending logical response size (`headers + body`). | Empty when the rule has no responses. |
| `rules[].top_smallest_responses[]` | array | Up to 10 smallest active cached responses currently assigned to the rule. | Ascending logical response size (`headers + body`). | Empty when the rule has no responses. |

Per-rule rankings are maintained incrementally with a hard 10-entry bound: when a better candidate arrives, the previous last-place entry is replaced immediately. Warmup history is also bounded to exactly one completed loop per rule; completing a new loop removes the prior loop before publishing the new summary.

### Additional interpretation notes

- Snapshot caching: `/wait0` returns cached stats for up to `snapshot_ttl_seconds`; polling faster than TTL will often return unchanged values.
- Lifetime vs point-in-time:
  - `refresh_duration_ms.*` is lifetime cumulative for this process (does not reset per warmup batch).
  - `cache.*`, `memory.*`, `sitemap.*` are point-in-time values at snapshot generation.
- Duplicate keys across RAM and disk are deduplicated, and concrete variant subkeys are folded into one logical root URL for URL and sitemap counts. Response counts and sizes still include each concrete variant response.
- Size units:
  - `*_bytes` fields are raw bytes.
  - `refresh_duration_ms` is milliseconds.

## Error responses

| HTTP | Body `error` | Cause |
|------|--------------|-------|
| `401` | `unauthorized` | Missing/invalid bearer token |
| `403` | `forbidden` | Token exists but lacks `stats:read` scope |
| `405` | `method not allowed` | Non-GET request |

## 3) Dashboard

## Routes

- `GET /wait0/dashboard`
- `GET /wait0/dashboard/`
- `GET /wait0/dashboard/stats`
- `POST /wait0/dashboard/invalidate`

## Auth

- HTTP Basic Auth required on all dashboard routes.
- Credentials are loaded from:
  - `WAIT0_DASHBOARD_USERNAME`
  - `WAIT0_DASHBOARD_PASSWORD`

If either credential env variable is missing, dashboard routes are not registered and return `404`.

## Behavior

- `GET /wait0/dashboard` serves a lightweight HTML page with:
  - parsed stats cards,
  - simple charts over time (client-side in-memory history),
  - one panel per configured rule with URL count, RAM/disk occupancy, last-loop warmup timing, completion age, and top latency/response-size rankings,
  - invalidation form.
- `GET /wait0/dashboard/stats` bridges to `GET /wait0` server-side.
- `POST /wait0/dashboard/invalidate` bridges to `POST /wait0/invalidate` server-side.
- `POST /wait0/dashboard/invalidate` applies CSRF checks:
  - same-origin via `Origin` (or `Referer` fallback),
  - CSRF token header (`X-Wait0-CSRF`) must match dashboard CSRF cookie.
- Bridge calls use server-side bearer tokens from `auth.tokens[]`, scoped by:
  - stats: `stats:read`
  - invalidation: `invalidation:write`
- Bearer tokens are never sent to browser code.
- Dashboard routes are rate-limited per IP (default `120` requests/minute).
- Dashboard responses disable caching (`Cache-Control: no-store, max-age=0`, `Pragma: no-cache`, `Expires: 0`).

### Token availability behavior

- Missing `stats:read` token: dashboard is disabled (`404`).
- Missing `invalidation:write` token: dashboard works in stats-only mode; invalidate action returns `503`.

## Example

```bash
curl -i \
  -u "${WAIT0_DASHBOARD_USERNAME}:${WAIT0_DASHBOARD_PASSWORD}" \
  "http://localhost:8082/wait0/dashboard/stats"
```

## Error responses

| HTTP | Body `error` | Cause |
|------|--------------|-------|
| `401` | `unauthorized` | Missing/invalid basic auth credentials |
| `403` | `csrf origin check failed` | Cross-origin state-changing request |
| `403` | `csrf token check failed` | Missing/invalid CSRF token for invalidate action |
| `405` | `method not allowed` | Unsupported method for route |
| `429` | `rate limit exceeded` | Dashboard per-IP rate limit exceeded |
| `503` | `dashboard stats are unavailable` | Internal stats bridge unavailable |
| `503` | `dashboard invalidation is unavailable` | No `invalidation:write` scoped token configured |

## 4) Invalidation API

## Route

- `POST /wait0/invalidate`

## Auth

- `Authorization: Bearer <token>` required.
- Token must map to scope: `invalidation:write`.

If invalidation is disabled (`server.invalidation.enabled: false`), endpoint returns `404`.

### Why scopes for invalidation

- Least privilege: token can be limited to `invalidation:write` without broad admin rights.
- Future-proofing: additional control APIs can reuse the same auth model with new scopes.
- Safer rotation and delegation: different systems can receive different scoped tokens.

## Request headers

| Header | Required | Value |
|--------|----------|-------|
| `Content-Type` | yes | `application/json` |
| `Authorization` | yes | `Bearer <token>` |

## Request body

```json
{
  "paths": ["/products/123", "/"],
  "tags": ["product:123", "homepage"]
}
```

Rules:

- At least one non-empty value from `paths` or `tags` is required.
- Unknown JSON fields are rejected.
- Payload must be a single JSON object.
- `paths` are normalized to path-only values:
  - full URLs are accepted and converted to their path,
  - query/fragment are removed from the invalidation path selector,
  - query-only or fragment-only inputs are rejected.
- invalidating a path clears every cached key for that path, including query-aware variants produced by rule `varyByQueryParams[]`.
- `tags` are trimmed, deduplicated, and cannot contain CR/LF characters.

## Successful response

Status: `202 Accepted`

```json
{
  "status": "accepted",
  "request_id": "inv_xxxxxxxxxxxxxxxx",
  "received": {
    "paths": 1,
    "tags": 1
  }
}
```

The job runs asynchronously:

1. Resolve affected cache keys from input `paths` and `tags`.
2. Delete matching keys from RAM and disk caches.
3. Recrawl affected keys in background to refresh cache.

## Error responses

| HTTP | Body `error` | Cause |
|------|--------------|-------|
| `400` | `invalid JSON body` | Invalid JSON, unknown fields, or malformed body |
| `400` | `JSON body must contain a single object` | Multiple JSON objects in payload |
| `400` | `at least one non-empty path or tag is required` | Empty/blank input lists |
| `400` | `paths limit exceeded` | Over `max_paths_per_request` and `hard_limits=true` |
| `400` | `tags limit exceeded` | Over `max_tags_per_request` and `hard_limits=true` |
| `401` | `unauthorized` | Missing/invalid bearer token |
| `403` | `forbidden` | Token exists but lacks scope |
| `404` | standard not found | Invalidation API disabled |
| `405` | `method not allowed` | Non-POST request |
| `415` | `content-type must be application/json` | Missing or wrong content type |
| `503` | `invalidation queue is unavailable` | Endpoint enabled but queue not initialized |
| `503` | `invalidation queue is full, retry later` | Queue saturated |

## Example: invalidate by path + tag

```bash
curl -i \
  -X POST "http://localhost:8082/wait0/invalidate" \
  -H "Authorization: Bearer ${WAIT0_INVALIDATION_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"paths":["/products/123?utm=x"],"tags":["product:123"]}'
```

## Example: path normalization behavior

Input `"https://shop.example.com/catalog/item?id=42#frag"` becomes key `"/catalog/item"`.

## See Also

- [For Developers](for-developers.md) — configuration fields, commands, and runtime flags.
- [Cache variant complexity](cache-variant-complexity.md) — root manifests, concrete subkeys, and operation costs.
- [README](../README.md) — quick start and product overview.
