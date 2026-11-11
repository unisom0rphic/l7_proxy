# Rate Limiting

Rate limiting is opt-in and configured in `infrastructure.yaml`. The proxy
applies it per upstream name, immediately before each proxied attempt; a
rejected request reaches the client as `429 Too Many Requests`.

The implementation lives in [`internal/ratelimit`](../internal/ratelimit).

## Interfaces

The proxy consumes a small interface:

```go
type Limiter interface {
	Allow(upstream string) bool
}
```

`internal/ratelimit.RateLimiter` implements it by delegating to a pluggable
algorithm:

```go
type RateLimitAlgorithm interface {
	Allow(key string) bool
}
```

`RateLimiter` owns the keying strategy; algorithms only decide whether a key may
take another token. Today only `token_bucket` is implemented. Adding a new
algorithm means adding its config block, validation, and constructor to the
`ratelimit` package without changing the proxy.

## Configuration

```yaml
rate-limit:
  enabled: true
  algorithm: token_bucket
  token-bucket:
    refill-rate: 10
    bucket-size: 20
```

Parameters are grouped per algorithm because their meaning differs. Leaky
bucket, for example, may only need a capacity, while token bucket needs a
refill rate and a burst size. The `ratelimit` package parses and validates the
selected algorithm's block; `config` only embeds the resulting typed config and
delegates validation to it.

Unknown algorithms fail startup with:

```text
Unknown algorithm. Supported: "token_bucket"
```

`InfrastructureConfig.Validate()` calls `ratelimit.ValidateConfig`, so the
existing startup validation path rejects bad rate-limit settings before the
proxy starts.

## Token bucket

Each upstream gets its own bucket, keyed by upstream name. A bucket:

- starts full at `bucket-size`;
- refills continuously at `refill-rate` tokens per second;
- never holds more than `bucket-size` tokens;
- admits a request only when at least one token is available.

Defaults:

| Setting | Default |
|---|---|
| `enabled` | `false` |
| `algorithm` | `token_bucket` |
| `token-bucket.refill-rate` | `10` tokens/second |
| `token-bucket.bucket-size` | `20` tokens |

Zero means “use the default” for the token-bucket parameters. Negative values
and non-finite refill rates are rejected by validation.

When rate limiting is disabled, the proxy uses an unlimited limiter and never
rejects traffic.
