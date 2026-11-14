# Load Balancing

Load balancing is opt-in and configured in `infrastructure.yaml`. When enabled,
each attempt against an upstream is sent to one of that upstream's backends,
chosen by the configured algorithm. The backends themselves are declared per
upstream in `routes.yaml` via `hosts`.

The implementation lives in [`internal/load_balancing`](../internal/load_balancing).

## Interfaces

The proxy consumes a small interface:

```go
type Balancer interface {
	Pick(upstream string, targets []*url.URL) *url.URL
}
```

`internal/load_balancing.LoadBalancer` implements it by delegating to a
pluggable algorithm:

```go
type Algorithm interface {
	Pick(key string, targets []*url.URL) *url.URL
}
```

`LoadBalancer` owns the fallback behavior; algorithms only choose among the
targets they are given. Today only `round_robin` is implemented. Adding a new
algorithm means adding its constructor and validation to the `load_balancing`
package without changing the proxy.

## Configuration

```yaml
load-balancing:
  enabled: true
  algorithm: round_robin
```

| Setting | Default |
|---|---|
| `enabled` | `false` |
| `algorithm` | `round_robin` |

The algorithm has no parameters today. Unknown algorithms fail startup with:

```text
Unknown algorithm. Supported: "round_robin"
```

`InfrastructureConfig.Validate()` calls `loadbalancing.ValidateConfig`, so a
bad algorithm is rejected before the proxy starts - even when `enabled` is
`false`.

## Multiple backends

Backends belong to the upstream, not to the balancer. In `routes.yaml`:

```yaml
upstreams:
  - name: users
    hosts:
      - http://users-1:3001
      - http://users-2:3001
```

`hosts` and `host` are mutually exclusive: exactly one must be set. Each entry
follows the same rules as `host` - scheme plus host and port, no path, query,
or fragment - and duplicates are rejected. An upstream with no backends fails
validation.

## Round robin

Each upstream rotates through its own backends independently. An upstream
named `users` with backends `users-1`, `users-2`, `users-3` receives its
requests in that order and wraps around. The counter is mutex-guarded, so
concurrent requests still distribute across backends; which of two concurrent
requests lands first is not defined.

Selection happens per attempt, before the limiter and gate are consulted. The
fallback attempt is not balanced: it goes to the fallback upstream's
configured host. Mirrored copies of a request are also unaffected - the mirror
fan-out targets are resolved independently of the picked backend.

When load balancing is disabled, the proxy keeps the first target, which is
exactly the single-backend behavior of an upstream declared with `host`.
