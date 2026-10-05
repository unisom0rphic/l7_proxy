# ADR-0005: Dummy backends in Docker Compose

**Status**: Accepted
**Date**: 2026-10-03

## Context
The proxy needs targets to route to during development and integration testing. Real backends are out of scope. We need lightweight servers that exercise routing, mirroring, timeouts, and error propagation without adding a dependency.

## Decision
Ship three dummy backends under `cmd/backend`, each a minimal Go HTTP server on the default `http.ServeMux`. Each backend exposes three endpoints:

- `GET /api/SERVICE_NAME` - responds `200 OK` or `500 Internal Server Error` depending on the `ERROR_RATE` environment variable, `SERVICE_NAME` is an environment variable.
- `GET /hang` - hangs for one minute without sending any response; used to test request timeouts.
- `GET /hang-body` - sends `200 OK` headers and then hangs; used to test body/stream timeouts.

Compose runs three instances:

- Two healthy backends (`ERROR_RATE=0`) - primary targets.
- One mirror backend (`ERROR_RATE=0.15`) - mirror target.

The mirror backend's nonzero error rate is deliberate: if 5xx responses originating from the mirror ever reach the client, the mirroring logic is leaking mirror responses into the primary path. This is the fastest way to catch that class of bug end-to-end.

## Alternatives Considered

### Single backend instance
- Cannot distinguish primary from mirror traffic by inspecting responses; cannot test routing across multiple hosts.

### Real backend framework (Gin/chi)
- Adds a dependency and distracts from the proxy; no additional testing value for routing/mirroring/timeout behavior.

### Mock server (httpbin, wiremock)
- Extra container; harder to attach a debugger; harder to add project-specific endpoints like `/hang-body` with the exact semantics we need.

### Real backends on the host
- Not reproducible across machines; breaks the clone-and-run promise.

## Consequences
**Positive:**
- `docker compose up` yields a working end-to-end setup in seconds.
- The three endpoints cover routing, error propagation, request timeout, and body timeout scenarios.
- The mirror backend with `ERROR_RATE=0.15` catches mirror-response leakage end-to-end.
- Backends are small enough to read in one sitting.

**Negative:**
- Not representative of a real service: no realistic latency distribution, no protocol features beyond HTTP/1.1.
- Anyone reading `cmd/backend` might mistake it for production code. Mitigated by package comment and this ADR.

**Revisit when**: need to test HTTP/2, streaming, or chunked encoding; or need richer error semantics than `ERROR_RATE`.