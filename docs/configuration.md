# Configuration Reference

All options are defined in the YAML config passed via `CONFIG_PATH`.

## Top-level

| Key | Type | Required | Description |
|---|---|---|---|
| `net_timeouts_s` | object | no | Network timeouts in seconds. |
| `upstreams` | list | yes | Backend definitions. |
| `routes` | list | yes | Routing rules. |

## `net_timeouts_s`

### `net_timeouts_s.transport`

| Key | Type | Default | Description |
|---|---|---|---|
| `tcp` | int (s) | - | TCP dial timeout. |
| `keep_alive` | int (s) | - | TCP keep-alive interval. |
| `tls` | int (s) | - | TLS handshake timeout. |
| `response_header` | int (s) | - | Time to wait for response headers. |
| `idle_conn` | int (s) | - | Idle connection keep-alive. |

### `net_timeouts_s.server`

| Key | Type | Default | Description |
|---|---|---|---|
| `context` | int (s) | - | Per-request context timeout. |
| `read_header` | int (s) | - | Time to read request headers. |
| `idle` | int (s) | - | Idle connection timeout. |

## `upstreams[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique upstream name. |
| `host` | string | yes | Base URL, must include scheme and port (`http://host:3001`). No query or fragment. |
| `timeout_ms` | int (ms) | yes | Request timeout to this upstream. |

## `routes[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `rules` | object | yes | Matchers for this route. |
| `upstream` | string | yes | Name of the target upstream. Must exist in `upstreams`. |
| `mirror` | object | no | Mirror configuration. |

### `routes[].rules`

All fields are AND'ed. Multiple values within one field are OR'ed.

| Key | Type | Required | Description |
|---|---|---|---|
| `path` | string | one of | Exact path match. Mutually exclusive with `path-prefix`. Must start with `/`. |
| `path-prefix` | string | one of | Prefix match. Mutually exclusive with `path`. Must start with `/`. |
| `rewrite` | string | will use `path`/`path-prefix` value if not set | Rewrite path. Must start with `/`, no scheme or host. |
| `method` | string | no | HTTP method (GET, POST, …). |
| `headers` | map | no | Header existence check. Keys must be valid header names. |

### `routes[].mirror`

| Key | Type | Required | Description |
|---|---|---|---|
| `upstream` | string | yes | Mirror upstream name. Must exist in `upstreams`; no cycles allowed. |
| `ratio` | float | no | Fraction of requests mirrored, `[0, 1]`. Default `0`. |

## Not yet configurable

Tracked in the roadmap; will be added:

- `max_body_size` - mirror body buffer limit
- transport connection pool (`MaxIdleConns`, `MaxIdleConnsPerHost`)
- hot-reload debounce window