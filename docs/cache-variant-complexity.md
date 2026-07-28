# Cache variant complexity

[← Back to README](../README.md) · [For Developers →](for-developers.md)

wait0 keeps the ordinary path/query cache key as a constant-size lookup root. A URL that declares `Cache-Variant` stores a root manifest containing the ordered expressions and referenced header names. Concrete responses live under hashed subkeys selected by the evaluated value tuple. The root never contains a list of its children, so adding variants does not make the request-path root lookup grow with the family.

## Complexity table

| Operation | RAM complexity | Disk complexity | Notes |
|-----------|----------------|-----------------|-------|
| Unvaried root lookup | Average `O(1)` | Approximately `O(log N)` | Existing cache behavior. |
| Variant root-manifest lookup | Average `O(1)` | Approximately `O(log N)` | Manifest size is `O(E + H)`, independent of `V`. |
| Evaluate a manifest | `O(E + B + X)` | None | Compiled programs are cached by expression fingerprint; `X` is expression-specific work such as regular-expression matching. |
| Concrete subkey construction | `O(VK)` | None | Values are length-framed and hashed; `VK` is the total byte length of the value tuple. |
| Concrete subcache lookup | Average `O(1)` | Approximately `O(log N)` | Direct lookup; no sibling scan or sort. |
| Add/update a value tuple in an unchanged family | Average `O(1)` family-index update | Approximately `O(log N)` write | The root manifest is not rewritten. Response encoding costs remain proportional to response size. |
| Enumerate one family index | `O(V)` | None | Used for family maintenance, not the normal request path. |
| Delete or replace one family | `O(V)` | Approximately `O(V log N)` | A changed ordered expression list replaces the whole family. |
| Build warmup targets for one root after the global snapshot | `O(V + P)` | None beyond entry reads | Discovered variants plus preset combinations, with known subkeys deduplicated. |
| Build a complete warmup snapshot | `O(N + Σ(V + P))` | `O(N)` key/access snapshot plus entry reads | Per warmup rule; requests then run with configured concurrency. |
| Rebuild the in-memory family index at startup | `O(N)` | `O(N)` key scan | Concrete subkeys are registered without loading sibling lists into manifests. |
| Build cache statistics | `O(N)` | `O(N)` key/metadata scan | Physical variant children are folded into logical root URL counts. |

Definitions:

- `N`: total physical cache entries across roots and concrete variant responses.
- `V`: discovered concrete value tuples for one root.
- `E`: total bytes/nodes in the root's ordered Expr declarations.
- `H`: number of distinct request headers referenced by those declarations.
- `B`: total bytes of request-header input read during evaluation.
- `VK`: total bytes in the evaluated value tuple.
- `P`: Cartesian preset count for the root, `∏ len(warmupRequestHeaderPresets[header])`.

## Practical guarantees

- A normal unvaried request still performs one root lookup.
- A variant hit performs one root-manifest lookup, one expression evaluation, and one direct child lookup. It never iterates over `V`.
- Root metadata is `O(E + H)`, not `O(V)`. The separate in-memory family index is `O(V)` and is used for replacement/deletion bookkeeping.
- Expr programs compile once per declaration fingerprint in a process and are reused. Evaluation still depends on expression complexity, so operators should keep origin-provided expressions bounded and understandable.
- Only referenced request-header values are persisted on a concrete child, costing `O(H)` fields plus their value bytes.
- RAM or disk capacity eviction can leave a stale child name in the in-memory family index until the family is replaced or deleted. This does not affect direct request lookup complexity.
- Presets can dominate work. Three country values and two user-agent values produce `P = 3 × 2 = 6` warmup requests per root before distinct discovered variants are added. `maxRequestsAtATime` bounds concurrency, not the total request count.

See [Cache variants in the README](../README.md#cache-variants) for configuration and Expr examples.
