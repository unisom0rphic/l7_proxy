# ADR-0003: ReadAll instead of TeeReader for mirroring

**Status**: Accepted
**Date**: 2026-10-03

## Context
Mirroring must send a copy of the request to a mirror host without affecting the primary path. The mirror needs the full request body.

Two viable strategies exist:

1. **Buffer the body in memory**, then hand a copy to the mirror.
2. **Stream the body with backpressure**, so the mirror consumes bytes as the transport reads them.

Given typical request bodies (~4 KB) and the goal of keeping the primary path fully decoupled from mirror speed, we buffer. Given buffering, `io.ReadAll` is the simplest implementation. `io.TeeReader` only makes sense with a streaming sink, which we are not using.

## Decision
In `Rewrite`: read the body with `io.ReadAll` (bounded by `max_body_size`), replace `req.Body` with `io.NopCloser(bytes.NewReader(buf))`, and enqueue the mirror into the worker pool with a non-blocking send.

Requests whose body was read are mirrored regardless of whether the transport later fails. Transport-stage failures before the body is sent are rare in practice and the added complexity of detecting them is not justified.

## Alternatives Considered

### io.TeeReader + io.Pipe
- `Pipe.Write` blocks until `Pipe.Read` consumes, so without a separate goroutine per request a slow mirror or saturated worker pool blocks the transport.
- Adding the goroutine (one per request) makes the primary non-blocking, but for ~4 KB bodies goroutine creation and scheduling can cost more than reading the body directly.
- Added complexity of async
- Justified only for large bodies at high concurrency, where buffering becomes a memory problem. 
- Under consideration.

### Instrumented `io.ReadCloser` wrapper (fires callback on EOF)
- Functionally identical to ReadAll in memory profile and number of passes.
- Only material difference: does not mirror requests whose transport fails before the body is fully read. This case does not occur in practice on our workloads.
- Adds an `io.ReadCloser` wrapper with EOF corner cases (empty body, `n>0` with `io.EOF`, truncated reads, `GetBody` interaction). More surface for bugs, no practical gain.
- Rejected.

### Mirror from Rewrite before reading the body
- The body has not been consumed at this point; the mirror would receive an empty body.
- Rejected.

## Consequences
**Positive:**
- Simple, predictable control flow: read -> enqueue -> forward.
- Single pass over the body; no wrapper to maintain.
- Error handling on the mirror side (timeouts, retries) is isolated.

**Negative:**
- Full body held in memory; bounded by `max_body_size`, but a memory/throughput trade-off at high RPS.
- Bodies above the limit are not mirrored (documented limitation).
- Requests that fail at the transport stage may still be mirrored. Accepted as a rare, non-critical case.

**Revisit when**: OOM or notable RSS growth under mirroring, requirement to mirror bodies above the current limit, or evidence that transport-stage failures become common enough to matter. The streaming alternative (TeeReader + io.Pipe + goroutine) is the fallback if body sizes or concurrency grow beyond what buffering can sustain.