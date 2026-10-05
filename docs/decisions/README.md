# Architecture Decision Records

This directory contains Architecture Decision Records (ADRs) for the project. An ADR captures a single architectural decision: what was decided, why, what alternatives were considered, and what consequences follow.

ADRs are immutable. Once accepted, an ADR is not edited to reflect a new decision - a new ADR is written that supersedes it, and the old one is marked `Superseded by ADR-XXXX`. This preserves the reasoning behind past choices, which is often more valuable than the choices themselves.

## Statuses

- **Proposed** - under discussion, not yet in effect.
- **Accepted** - currently in effect.
- **Deprecated** - no longer relevant but kept for history.
- **Superseded by ADR-XXXX** - replaced by a later decision.

## Format

Each ADR follows a minimal Nygard-style structure:

- **Context** - the situation and constraints that forced a decision.
- **Decision** - what was decided, in one or two sentences.
- **Alternatives Considered** - 2–3 real alternatives with pros and cons.
- **Consequences** - non-obvious positive and negative outcomes, plus any technical debt taken on.
- **Revisit when** - conditions under which the decision should be re-examined.

An ADR should fit on one page. 

## Index

| ADR | Status | Title |
|-----|--------|-------|
| [0001](0001-why-go.md) | Accepted | Use Go for the proxy |
| [0002](0002-rwlock-for-host-map.md) | Accepted | RWLock for the name → host map |
| [0003](0003-readall-vs-teereader.md) | Accepted | ReadAll instead of TeeReader for mirroring |
| [0004](0004-linear-scan-prefix-routing.md) | Accepted | Linear scan for prefix routing (temporary) |
| [0005](0005-dummy-backends-in-compose.md) | Accepted | Dummy backends in Docker Compose |
| [0006](0006-stdlib-reverseproxy.md) | Accepted | Use net/http/httputil.ReverseProxy |
| [0007](0007-three-color-dfs.md) | Accepted | Three-color DFS for mirror cycle detection |
| [0008](0008-yaml-config.md) | Accepted | YAML as the config format |
| [0009](0009-hot-reload-mechanism.md) | Accepted | fsnotify + debounce for hot-reload |
| [0010](0010-panic-on-missing-config.md) | Accepted | Panic on missing config file |
