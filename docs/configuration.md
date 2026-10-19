# Configuration Reference

Configuration is split into two files:

- **`infrastructure.yaml`** - Infrastructure settings (requires restart to apply changes)
  - Passed via `INFRA_CONFIG_PATH` environment variable (default: `infrastructure.yaml`)
- **`routes.yaml`** - Routing configuration (hot-reloadable)
  - Passed via `ROUTES_CONFIG_PATH` environment variable (default: `routes.yaml`)

All keys are `kebab-case`. Unknown keys are rejected at load time with a parse
error, so a typo never silently turns into a zero value.

---

## Infrastructure Config (`infrastructure.yaml`)

### Top-level

| Key | Type | Required | Description |
|---|---|---|---|
| `net-timeouts-s` | object | no | Network timeouts in seconds. |
| `connection-pool` | object | no | HTTP transport connection pool settings. |
| `mirroring` | object | no | Mirroring worker and queue configuration. |
| `port` | string | no | Server listen port. The example config sets `"8080"`. |

### `net-timeouts-s`

#### `net-timeouts-s.transport`

| Key | Type | Default | Description |
|---|---|---|---|
| `tcp` | int (s) | 0 (no timeout) | TCP dial timeout. |
| `keep-alive` | int (s) | 0 (Go default) | TCP keep-alive interval. |
| `tls` | int (s) | 0 (no timeout) | TLS handshake timeout. |
| `response-header` | int (s) | 0 (no timeout) | Time to wait for response headers. |
| `idle-conn` | int (s) | 0 (no timeout) | Idle connection keep-alive. |

#### `net-timeouts-s.server`

| Key | Type | Default | Description |
|---|---|---|---|
| `context` | int (s) | - | Per-request context timeout. Must be greater than 0: a zero deadline cancels every request immediately. |
| `read-header` | int (s) | 0 (no timeout) | Time to read request headers. |
| `idle` | int (s) | 0 (Go default) | Idle connection timeout. |

### `connection-pool`

| Key | Type | Default | Description |
|---|---|---|---|
| `max-idle-conns` | int | 0 (no limit) | Maximum idle connections across all hosts. |
| `max-idle-conns-per-host` | int | 0 (Go default) | Maximum idle connections per host. |

### `mirroring`

| Key | Type | Default | Description |
|---|---|---|---|
| `workers` | int | 0 (disabled) | Number of mirroring worker goroutines. |
| `queue-size` | int | 0 (disabled) | Size of the mirroring job queue. |

---

## Routing Config (`routes.yaml`)

### Top-level

| Key | Type | Required | Description |
|---|---|---|---|
| `upstreams` | list | yes | Backend definitions. |
| `routes` | list | yes | Routing rules. |

### `upstreams[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique upstream name. |
| `host` | string | yes | Base URL, must include scheme and port (`http://host:3001`). No path, query, or fragment. |
| `timeout-ms` | int (ms) | yes | Request timeout to this upstream. |

### `routes[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `rules` | object | yes | Matchers for this route. |
| `upstream` | string | yes | Name of the target upstream. Must exist in `upstreams`. |
| `mirrors` | list | no | Mirror targets for this route. |

#### `routes[].rules`

All fields are AND'ed. Multiple values within one field are OR'ed.

| Key | Type | Required | Description |
|---|---|---|---|
| `path` | string | one of | Exact path match. Mutually exclusive with `path-prefix`. Must start with `/`. |
| `path-prefix` | string | one of | Prefix match. Mutually exclusive with `path`. Must start with `/`. |
| `rewrite` | string | no | Outbound path base. Defaults to the matched `path`/`path-prefix`. Must start with `/`, no scheme or host. The unmatched remainder of a prefix match is appended. |
| `method` | string | no | HTTP method, uppercase (GET, POST, …). |
| `headers` | list | no | Header matchers, see below. |
| `hosts` | list | no | Request `Host` values to match. A leading `*.` wildcard (`*.example.com`) matches exactly one or more leading labels. Ports are not allowed. |
| `client-cidrs` | list | no | Source IP ranges in CIDR notation (`172.16.0.0/12`). Matched against the peer address. |

##### `routes[].rules.headers[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Header name. Must be a valid RFC 7230 token. Unique within a route. |
| `op` | string | no | One of `present`, `equals`, `prefix`, `contains`. Inferred when omitted: `present` if `value` is empty, otherwise `equals`. |
| `value` | string | with `op` | Comparison value. Forbidden for `op: present`, required for the other ops. |

A rule matches if **any** value of the header satisfies the op. Two matchers for
the same header name on one route are rejected: headers are AND'ed, so the
author almost certainly meant OR, which needs separate routes.

#### `routes[].mirrors[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `upstream` | string | yes | Mirror upstream name. Must exist in `upstreams`, must differ from the route's own upstream, and must not create a mirror cycle. |
| `ratio` | float | no | Fraction of requests mirrored, `[0, 1]`. `0` mirrors nothing. |

---

## Not yet configurable

Tracked in the roadmap; will be added:

- `max_body_size` - mirror body buffer limit
- hot-reload debounce window
