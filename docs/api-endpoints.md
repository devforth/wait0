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

| Condition | Result | `X-Wait0` |
|----------|--------|-----------|
| Matching rule has `bypass: true` | Forward to origin, no cache write | `bypass` |
| Matching rule cookie bypass is triggered | Forward to origin, no cache write | `ignore-by-cookie` |
| Method is not `GET` | Forward to origin, no cache write | `bypass` |
| RAM or disk hit for active entry | Serve cached response instantly | `hit` |
| Miss and cacheable origin `2xx` | Store and serve response | `miss` |
| Origin non-`2xx` | Do not cache, evict existing key | `ignore-by-status` |
| Origin fetch/network failure | Gateway error | `bad-gateway` |

## Cacheability rule

An origin response is cacheable only when:

- status is `2xx`, and
- `Cache-Control` does not include `no-store` or `no-cache`.

## Response headers added by wait0

| Header | When present | Meaning |
|--------|--------------|---------|
| `X-Wait0` | always on handled responses | Cache/proxy decision marker |
| `X-Wait0-Revalidated-At` | cache `hit` with revalidation metadata | Last revalidation timestamp (RFC3339Nano) |
| `X-Wait0-Revalidated-By` | with `X-Wait0-Revalidated-At` | Revalidation source (`user`, `warmup`, `invalidate`, etc.) |
| `X-Wait0-Discovered-By` | if entry was discovery seeded | Discovery source marker |
| `Access-Control-Expose-Headers` | when wait0 headers exist | Exposes wait0 headers to browser clients |

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
  }
}
```

## Response field reference

The table below explains each field in the stats payload, including what it means and how it is computed.

| Field | Type | Meaning | Calculation | Update behavior / notes |
|------|------|---------|-------------|-------------------------|
| `generated_at` | RFC3339Nano string | UTC timestamp when this snapshot was generated. | `time.Now().UTC()` at snapshot build time. | New value only when snapshot is recomputed. |
| `snapshot_ttl_seconds` | integer | Snapshot cache TTL used by `/wait0`. | Fixed constant `5`. | Endpoint may return identical payload for calls within this TTL. |
| `cache.urls_total` | integer | Total number of unique cached keys currently known to wait0. Includes active + inactive entries. | Unique union of RAM keys and disk keys. | Recomputed per snapshot. |
| `cache.responses_size_bytes_total` | integer (bytes) | Total logical size of cached responses for all unique keys. | Sum over unique keys of per-entry logical size (`headers + body` bytes). | Recomputed per snapshot. |
| `cache.response_size_bytes.min` | integer (bytes) | Smallest logical response size among unique cached keys. | Min of per-key logical response size. | Recomputed per snapshot; `0` when no keys. |
| `cache.response_size_bytes.avg` | integer (bytes) | Average logical response size among unique cached keys. | `responses_size_bytes_total / urls_total` (integer division). | Recomputed per snapshot; `0` when no keys. |
| `cache.response_size_bytes.max` | integer (bytes) | Largest logical response size among unique cached keys. | Max of per-key logical response size. | Recomputed per snapshot; `0` when no keys. |
| `memory.rss_bytes` | integer (bytes) | Current process resident memory (RSS) as seen by OS probes. | `ProcessRSSBytes()`; `0` when unavailable on platform/runtime. | Recomputed per snapshot. |
| `memory.go_alloc_bytes` | integer (bytes) | Current heap bytes allocated by Go runtime. | `runtime.ReadMemStats(&ms); ms.Alloc`. | Recomputed per snapshot. |
| `refresh_duration_ms.min` | integer (ms) | Fastest observed revalidation execution time. | Min of observed `revalidation.Once(...)` durations, converted to milliseconds. | Process-lifetime aggregate since current process start. |
| `refresh_duration_ms.avg` | integer (ms) | Average observed revalidation execution time. | Sum of all observed revalidation durations / count, converted to ms (integer division). | Process-lifetime aggregate since current process start. |
| `refresh_duration_ms.max` | integer (ms) | Slowest observed revalidation execution time. | Max of observed `revalidation.Once(...)` durations, converted to milliseconds. | Process-lifetime aggregate since current process start. |
| `sitemap.discovered_urls` | integer | Number of unique cached keys whose discovery source is sitemap. | Count of unique keys where `discovered_by == "sitemap"` (case-insensitive). | Recomputed per snapshot. |
| `sitemap.crawled_urls` | integer | Number of sitemap-discovered keys that are currently active (not inactive seed entries). | Count of sitemap keys where `inactive == false`. | Recomputed per snapshot. |
| `sitemap.crawl_percentage` | float | Share of sitemap-discovered keys currently crawled/active. | `crawled_urls * 100 / discovered_urls`; `0` if `discovered_urls == 0`. | Recomputed per snapshot. |

### Additional interpretation notes

- Snapshot caching: `/wait0` returns cached stats for up to `snapshot_ttl_seconds`; polling faster than TTL will often return unchanged values.
- Lifetime vs point-in-time:
  - `refresh_duration_ms.*` is lifetime cumulative for this process (does not reset per warmup batch).
  - `cache.*`, `memory.*`, `sitemap.*` are point-in-time values at snapshot generation.
- Duplicate keys across RAM and disk are deduplicated as one logical cached URL in all `cache.*` and `sitemap.*` counts.
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
- `paths` are normalized to path-only keys:
  - full URLs are accepted and converted to their path,
  - query/fragment are removed from the cache key,
  - query-only or fragment-only inputs are rejected.
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
- [README](../README.md) — quick start and product overview.
