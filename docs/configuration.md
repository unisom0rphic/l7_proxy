# Configuration Reference

Configuration is split into two files:

- **`infrastructure.yaml`** - Infrastructure settings (requires restart to apply changes)
  - Passed via `INFRA_CONFIG_PATH` environment variable (default: `infrastructure.yaml`)
- **`routes.yaml`** - Routing configuration (hot-reloadable)
  - Passed via `ROUTES_CONFIG_PATH` environment variable (default: `routes.yaml`)

---

## Infrastructure Config (`infrastructure.yaml`)

### Top-level

| Key | Type | Required | Description |
|---|---|---|---|
| `net_timeouts_s` | object | no | Network timeouts in seconds. |
| `connection_pool` | object | no | HTTP transport connection pool settings. |
| `mirroring` | object | no | Mirroring worker and queue configuration. |
| `port` | string | no | Server port. Default: `8080`. |

### `net_timeouts_s`

#### `net_timeouts_s.transport`

| Key | Type | Default | Description |
|---|---|---|---|
| `tcp` | int (s) | - | TCP dial timeout. |
| `keep_alive` | int (s) | - | TCP keep-alive interval. |
| `tls` | int (s) | - | TLS handshake timeout. |
| `response_header` | int (s) | - | Time to wait for response headers. |
| `idle_conn` | int (s) | - | Idle connection keep-alive. |

#### `net_timeouts_s.server`

| Key | Type | Default | Description |
|---|---|---|---|
| `context` | int (s) | - | Per-request context timeout. |
| `read_header` | int (s) | - | Time to read request headers. |
| `idle` | int (s) | - | Idle connection timeout. |

### `connection_pool`

| Key | Type | Default | Description |
|---|---|---|---|
| `max_idle_conns` | int | 1000 | Maximum idle connections across all hosts. |
| `max_idle_conns_per_host` | int | 1000 | Maximum idle connections per host. |

### `mirroring`

| Key | Type | Default | Description |
|---|---|---|---|
| `workers` | int | 10 | Number of mirroring worker goroutines. |
| `queue_size` | int | 100 | Size of the mirroring job queue. |

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
| `host` | string | yes | Base URL, must include scheme and port (`http://host:3001`). No query or fragment. |
| `timeout_ms` | int (ms) | yes | Request timeout to this upstream. |

### `routes[]`

| Key | Type | Required | Description |
|---|---|---|---|
| `rules` | object | yes | Matchers for this route. |
| `upstream` | string | yes | Name of the target upstream. Must exist in `upstreams`. |
| `mirror` | object | no | Mirror configuration. |

#### `routes[].rules`

All fields are AND'ed. Multiple values within one field are OR'ed.

| Key | Type | Required | Description |
|---|---|---|---|
| `path` | string | one of | Exact path match. Mutually exclusive with `path-prefix`. Must start with `/`. |
| `path-prefix` | string | one of | Prefix match. Mutually exclusive with `path`. Must start with `/`. |
| `rewrite` | string | will use `path`/`path-prefix` value if not set | Rewrite path. Must start with `/`, no scheme or host. |
| `method` | string | no | HTTP method (GET, POST, …). |
| `headers` | map | no | Header existence check. Keys must be valid header names. |

#### `routes[].mirror`

| Key | Type | Required | Description |
|---|---|---|---|
| `upstream` | string | yes | Mirror upstream name. Must exist in `upstreams`; no cycles allowed. |
| `ratio` | float | no | Fraction of requests mirrored, `[0, 1]`. Default `0`. |

---

## Not yet configurable

Tracked in the roadmap; will be added:

- `max_body_size` - mirror body buffer limit
- hot-reload debounce window
