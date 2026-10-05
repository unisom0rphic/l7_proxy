# ADR-0002: RWLock for the name → host map

**Status**: Accepted
**Date**: 2026-10-03

## Context
The map is read on every request and written on every hot-reload, in different goroutines. The original plain map had a race that `-race` reproduced intermittently.

## Decision
`sync.RWMutex`. RLock on read, Lock on write.

Temporary: will be replaced with `atomic.Pointer` to an immutable snapshot once the config API stabilizes.

## Alternatives Considered
- **Plain map**: race, unacceptable.
- **sync.Map**: designed for write-once/read-many, higher overhead than RWMutex on read-heavy reload workloads.
- **atomic.Pointer + copy-on-write**: correct end state, but more code and more room for bugs at MVP.

## Consequences
- Race eliminated, `-race` clean.
- Small per-request cost on `RLock`; may show in flamegraph at high RPS.

**Revisit when**: `RWMutex` appears in flamegraph top, or reload becomes slow.