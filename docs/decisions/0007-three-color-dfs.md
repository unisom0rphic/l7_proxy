# ADR-0007: Three-color DFS for mirror cycle detection

**Status**: Accepted
**Date**: 2026-10-03

## Context
Mirroring can chain: a request to backend A is mirrored to backend B, which may itself be mirrored to backend C, and so on. A misconfiguration can create a cycle (A -> B -> A), which would cause infinite mirror traffic and resource exhaustion.

Config validation must detect cycles in the mirror graph before the config is accepted.

## Decision
Use three-color DFS (white/gray/black) for cycle detection.

When a cycle is found, DFS reconstructs the cycle path from the recursion stack and includes it in the error message. Example: `mirror cycle detected: a -> b -> c -> a`.

## Alternatives Considered

### Kahn's algorithm (topological sort)
- Pros: iterative, no recursion depth limit.
- Cons: detects *that* a cycle exists but not *which* nodes form it. To reconstruct the path we would need additional bookkeeping.
- Rejected because the cycle path is directly useful in logs during config debugging.

### Plain DFS without colors (visited set only)
- Pros: simpler.
- Cons: a visited set alone cannot distinguish a back-edge (cycle) from a cross-edge (already-visited node reached via a different path). This produces false positives on DAGs with shared nodes.

## Consequences
**Positive:**
- Correct detection of directed cycles.
- Cycle path is available in the error message, which makes debugging misconfigured mirror chains much faster.
- Recursion depth is bounded by the number of mirror targets, which is small (tens, not thousands).

**Negative:**
- Recursive implementation - if the mirror graph were ever to grow extremely large (thousands of nodes), stack depth could become a concern. Not realistic for this project.

**Technical debt:**
- Practically none.

## Revisit Triggers
- Mirror graph size grows beyond what recursion can safely handle (unrealistic, but noted).
- Need for additional graph analysis (e.g. shortest mirror path, mirror fan-out metrics).