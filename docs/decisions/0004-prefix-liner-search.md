# ADR-0004: Linear scan for prefix routing (temporary)

**Status**: Accepted
**Date**: 2026-10-03

## Context
Routing resolves a request path to a backend by longest-prefix match against a set of registered prefixes. For now the routing layer is a placeholder: the priority is to smoke-test the rest of the proxy (mirroring, hot-reload, observability) against a minimal, obviously-correct routing implementation.

The target implementation is a trie. This ADR documents the temporary choice and the intent to replace it.

## Decision
Linear scan over all registered prefixes using `strings.HasPrefix`, tracking the longest match.

The public routing interface is designed to stay stable across the replacement: tests are written against this interface, not against the linear-scan internals, so the trie can be validated against the same test suite without modification.

## Alternatives Considered

### Trie (target implementation, deferred)
- Pros: O(len(path)) lookup independent of prefix count; natural fit for longest-prefix matching; the eventual production implementation.
- Cons: more code and more surface for bugs. Introducing it now would slow down smoke-testing of the rest of the proxy.
- Deferred to the next routing milestone.

### Sorted slice + binary search
- Cons: still requires a scan among prefixes sharing a common start; complexity of maintaining sort order on hot-reload. Not simpler than a trie by enough to justify an intermediate step.

## Consequences
**Positive:**
- Trivial to implement and test.
- The rest of the proxy (mirroring, hot-reload, observability) can be smoke-tested immediately.
- Routing tests double as the correctness oracle for the future trie.

**Negative:**
- O(N) per request where N is the prefix count. Fine for smoke-testing, not for production-scale configs.
- This is explicitly temporary. Treat the linear scan as a stub, not as the design.

**Revisit when**: the smoke-test phase is over and routing is the next milestone. Trie is the target.