# l7_proxy

An L7 HTTP reverse proxy in Go: routing, hot-reloadable config, and traffic mirroring.

## Features

- Exact and prefix routing on path, method, and headers
- Hot reload without restart; invalid configs are rejected, last known-good stays live
- Forwarding via `net/http/httputil.ReverseProxy` - hop-by-hop headers stripped, `X-Forwarded-*` set
- Configurable transport and server timeouts
- Graceful shutdown on `SIGTERM`
- Non-blocking traffic mirroring (**in progress**, [Issue #1](https://github.com/unisom0rphic/l7_proxy/issues/1))
- Prometheus metrics: requests, latency, failed config reloads

## Requirements

- Go 1.22+
- Docker + Docker Compose
- [Task](https://taskfile.dev/) (optional)

## Quick Start

```bash
task up                 # everything in Docker
```

Or locally:

```bash
cd ./cmd/proxy
go build
cp ./config_example.yaml ./config.yaml
CONFIG_PATH=config.yaml ./proxy
```

Config example: [`cmd/proxy/config_example.yaml`](./cmd/proxy/config_example.yaml).

## Configuration

```yaml
upstreams:
  - name: users-prod
    host: http://users-prod:3001
    timeout_ms: 2000
  - name: users-test
    host: http://users-test:3002
    timeout_ms: 2000
  - name: orders
    host: http://orders:3003
    timeout_ms: 2000

routes:
  - rules:
      path: /api/users
    upstream: users-prod
    mirror:
      upstream: users-test
      ratio: 0.2

  - rules:
      path-prefix: /orders
      rewrite: /api/orders
      method: GET
    upstream: orders
```

Config is validated on load and on every reload. Invalid configs are rejected - the previous config stays live.

The list of possible configuration options is listed in [`docs/configuration`](docs/configuration.md).

## Hot Reload

`fsnotify` or `SIGHUP` + debounce. Config is swapped via `atomic.Pointer[Config]`. Missing file at startup panics; missing file at runtime keeps the last known-good config.

## Testing

```bash
task test
# or
go test ./... && go test -race ./...
```

## Taskfile

| Task | Description |
|---|---|
| `task up` / `task down` | Start / stop containers |
| `task up:local` / `task down:local` | Local proxy + Docker backends |
| `task run:proxy` | Run only proxy locally |
| `task test` | Run tests |

## Docs

Architecture decisions live in [`docs/decisions`](/docs/decisions).

## License

MIT.