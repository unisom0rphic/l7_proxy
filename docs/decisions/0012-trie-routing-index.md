# ADR-0012: Trie as the routing index

**Status**: Accepted
**Date**: 2026-10-30

## Context

ADR-0004 introduced a linear scan over all registered prefixes as a temporary stand-in and named the trie as the target implementation. Routing is now the milestone being closed: the rest of the proxy (mirroring, hot-reload, observability) is smoke-tested, so the deferred replacement is due.

Migrating surfaced a defect in the existing precedence rule. "Longest match" was computed from the longest *outbound* path (`rewrite` + remainder). When `rewrite` is unset - the common case - every candidate ties, so the winner fell back to config order rather than to the longest prefix.

## Decision

The trie is the routing index. Routes are compiled once into an immutable `routeIndex` (exact map, prefix trie, regex list) published through a single atomic pointer, so hot-reload never exposes a torn snapshot.

Prefix matching resolves to the **longest registered prefix** of the request path; routes sharing a prefix are broken by config order. Exact match outranks prefix, prefix outranks regex.

## Alternatives Considered

### Keep the linear scan
- Pros: none remain - ADR-0004 already framed it as a stub, and routing is no longer smoke-test-only.
- Cons: O(N) per request in the prefix count.

### Keep the previous precedence rule (longest outbound path, ties by config order)
- Pros: no behavior change, no test updates.
- Cons: the winner depends on `rewrite`, which says nothing about how specific the match is; with `rewrite` unset every candidate ties and the outcome is incidental config order.
- Rejected in favour of longest-prefix.

ADR-0004's sorted-slice + binary-search alternative was not revisited.

## Consequences

**Positive:**
- Lookup cost tracks path length, not prefix count: adding prefixes does not slow down request routing.
- Precedence is a property of the match rather than of `rewrite`, so it is predictable and explainable in the config docs.
- Config publication is a single atomic store, so init and hot-reload share one code path and readers cannot observe a mixed snapshot.

**Negative:**
- Longer prefixes shadow shorter ones. A config relying on the old tie-breaking will route differently.
- ADR-0004 assumed the trie would be validated against the unmodified test suite. That held for the match *interface* but not for *precedence*: `TestDecideRoute_LongestPrefixWins` had to be corrected because it encoded the old rule.

**Revisit when**: routing needs match types beyond exact/prefix/regex, or the index must be mutated in place rather than rebuilt and swapped.
