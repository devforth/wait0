[← For Developers](for-developers.md) · [Back to README](../README.md)

# API Endpoints

Complete HTTP endpoint reference for `wait0`.

## Overview

`wait0` exposes:

- A reverse-proxy data path for regular client requests.
- A control endpoint for asynchronous cache invalidation.

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

## 2) Invalidation API

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
