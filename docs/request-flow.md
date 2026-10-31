# Request Flow

How a request travels through the proxy, where timeouts apply, and when it is
retried against a fallback upstream.

## Lifecycle

1. **Admit** - `ServeHTTP` rejects a body larger than `max-body-size` with 413
   before doing any routing work.
2. **Route** - `DecideRoute` picks a route (exact path, then longest prefix,
   then the first matching regex) and resolves it into a `Decision`: the
   upstream name, the outbound target URL, the per-attempt timeout, the mirror
   targets, and the optional fallback.
3. **Bound** - `ServeHTTP` puts the request in a context carrying the server
   context timeout (`net-timeouts-s.server.context`). This deadline is the
   request's overall ceiling and is never extended.
4. **Rewrite** - the body is buffered so it can be replayed, `X-Forwarded-*` is
   set, and the outbound URL is swapped to the target while the path and query
   are preserved.
5. **Attempt** - the attempting transport sends the request to the primary
   upstream under that upstream's `timeout-ms`.
6. **Failover** - if the attempt fails and the request is eligible, the same
   request is replayed once against the fallback upstream.

## Timeouts

| Timeout | Scope | Source |
|---|---|---|
| server context | the whole request, every attempt included | `net-timeouts-s.server.context` |
| per-attempt | one attempt: dial, headers, and body | `upstreams[].timeout-ms` |
| transport | dial / TLS / response headers / idle connection | `net-timeouts-s.transport.*` |

A `timeout-ms` of `0` leaves the attempt bounded only by the server context.
Because a per-attempt deadline is derived from the server context, a slow
primary cannot push the request past the ceiling: whatever the primary leaves
unused is what remains for the fallback.

## Failover rules

A fallback is tried only when all of the following hold:

- the primary cannot be reached - a dial, DNS, or TLS failure, a refused
  connection, or the per-attempt timeout firing. A reachable upstream replying
  5xx is a normal response, not a failure;
- the primary declares `fallback`, and the named upstream exists;
- the method is idempotent: `GET`, `HEAD`, `PUT`, `DELETE`, `OPTIONS`, or
  `TRACE`. `POST` and `PATCH` are never replayed;
- the request body was buffered, so it can be sent again.

The fallback is followed for exactly one hop: if it also fails, the request
fails. The request is not mirrored to the fallback, and a fallback that names
another upstream does not chain further.

## Policy seams

Two interfaces in `internal/proxy` mark where the future circuit breaker and
rate limiter plug in:

- `Gate` - `Allow(upstream)` before an attempt and `Record(upstream, err)`
  after it. A closed gate sends the request to the fallback, or answers 503 when
  nothing else can serve it.
- `Limiter` - `Allow(upstream)` before an attempt.

The defaults admit every request, so the proxy behaves today as if no policy
layer exists. Real implementations belong in `internal/policy`.

## Status mapping

| Outcome | Status |
|---|---|
| no route matched | 404 |
| body larger than `max-body-size` | 413 |
| every upstream unavailable (gate closed, no fallback) | 503 |
| the request exceeded the server context timeout | 504 |
| the client went away | no response written |
| any other transport error | 502 |
