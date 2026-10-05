# ADR-0006: Use net/http/httputil.ReverseProxy

**Status**: Accepted
**Date**: 2026-10-03

## Context
The proxy needs to forward HTTP requests to upstream backends. The forwarding logic has many edge cases: hop-by-hop headers, `X-Forwarded-*`, connection reuse, chunked encoding, trailers, streaming bodies, error handling on upstream failure, context cancellation, and more. Getting these right is nontrivial, and bugs tend to be subtle and security-relevant.

## Decision
Use `net/http/httputil.ReverseProxy` from the standard library as the forwarding engine.

## Alternatives Considered

### Custom forwarding implementation on top of net/http.Client
- Pros: full control over the forwarding path; can tailor every behavior.
- Cons: must reimplement everything `ReverseProxy` already does - header filtering, `X-Forwarded-For`/`X-Forwarded-Proto` handling, connection pooling via `http.Transport`, streaming, cancellation, error responses. This is weeks of work to reach parity, with no gain for MVP. The standard library version is battle-tested and maintained.
- Rejected for MVP. Revisit only if a specific behavior must be customized in a way `ReverseProxy` cannot express (e.g. custom connection pooling per backend, request hedging).

### Third-party reverse proxy library (e.g. oxy, fasthttp-based proxies)
- Pros: additional features (load balancing, circuit breaking) out of the box.
- Cons: extra dependency, different design opinions, more to learn and maintain. Not justified at MVP, doesn't fit the "lightweight" requirement.

### Forwarding via a generic HTTP client only
- Pros: simplest possible.
- Cons: loses all reverse-proxy-specific behaviors (X-Forwarded headers, hop-by-hop filtering). Would have to be re-added manually.

## Consequences
**Positive:**
- Forwarding works on day one and behaves like every other Go proxy.
- Header handling, connection reuse, and error semantics are correct by default.
- The team can spend time on routing, mirroring, and hot-reload logic - the actual project goals.

**Negative:**
- Some behaviors are opaque unless you read the stdlib source. Debugging requires familiarity with `ReverseProxy` internals.
- Customization (e.g. per-request retry policy, custom `Rewrite` logic) must go through `ReverseProxy`'s hooks (`Rewrite`, `ModifyResponse`, `ErrorHandler`), which are less flexible than a from-scratch implementation.
- Tied to the standard library's release cycle for fixes.

**Technical debt:**
- None at MVP. If a requirement emerges that `ReverseProxy` cannot express, consider a custom `RoundTripper` before replacing `ReverseProxy` entirely.

## Revisit Triggers
- A required behavior that `ReverseProxy` cannot express through its hooks.
- Significant performance regression attributable to the stdlib implementation (unlikely, but verifiable via flamegraph).