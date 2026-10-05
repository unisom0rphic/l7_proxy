# ADR-0001: Use Go for the proxy

**Status**: Accepted
**Date**: 2026-10-03

## Context
The project is a high-throughput HTTP proxy with hot-reload configuration, traffic mirroring, and observability. Requirements: high throughput with low memory footprint, predictable latency, single-binary deployment, and first-class concurrency.

I have prior experience in Rust (a KV store and an ONNX batcher) and Python (my main language before Go). Both were seriously considered.

## Decision
Use Go.

Three reasons, in order of weight:

1. **Development speed and maintainability.** Go's simplicity and fast compile times, and make iteration faster and long-term maintenance cheaper. This project is a learning vehicle as much as a tool, and Go lets me spend time on routing/mirroring/hot-reload logic rather than fighting the borrow checker or tokio.
2. **The proxy is I/O-bound.** This was a hypothesis, confirmed by flamegraphs: the hot path is dominated by syscall/IO, not CPU. Optimizing CPU work beyond what the runtime already gives us has low ROI. Rust's zero-cost abstractions don't make up for the time spent.
3. **`net/http/httputil.ReverseProxy` exists.** The standard library gives us a battle-tested reverse proxy implementation out of the box. A custom implementation would cost days, bring its own bugs, and add nothing for MVP. (See ADR-0006.)

## Alternatives Considered

### Rust
- Pros: top-tier performance, no GC pauses, strong type system, async/await without a runtime.
- Cons: steeper learning curve; longer time-to-MVP; manual memory management and lifetimes add friction to what should be a fast-iterating project; no `ReverseProxy` equivalent in stdlib - would have to build it or pull a heavier dependency; I don't know the syntax well enough to write idiomatic code without constant reference or relying on LLMs too much. 

### Python (asyncio)
- Pros: my primary language; fast to prototype; familiar ecosystem.
- Cons: GIL and per-connection overhead make it a poor fit for a high-RPS proxy; async plumbing in Python is fragile under load; deployment is heavier (interpreter, venv, dependencies).

### C / C++
- Pros: full control over memory and performance.
- Cons: manual memory management and language complexity; development and debugging speed significantly lower than Go; no standard package manager - dependency management is ad-hoc and inconsistent across projects, with no cargo-equivalent.

## Consequences
**Positive:**
- Simple deployment: one static binary, no runtime dependencies.
- Built-in concurrency (goroutines, channels) with no external library.
- `net/http` and `net/http/httputil` cover most of what is needed out of the box.
- Strong runtime and tooling (pprof, trace, escape analysis) support profiling.
- Aligns with my goal of learning Go internals (GMP scheduler, GC, defer).

**Negative:**
- GC pauses are acceptable for a proxy but require attention to hot-path allocations.
- No zero-cost abstractions like Rust: interfaces and reflection cost.
- Ecosystem for some tasks (e.g. zero-copy networking) is thinner than C/Rust.

**Technical debt:**
- If the CPU profile ever shows the proxy is CPU-bound rather than I/O-bound, revisit this decision. Trigger: flamegraph shows >~30% of time in our own code rather than syscalls/runtime.