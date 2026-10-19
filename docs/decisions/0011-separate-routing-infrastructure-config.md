# ADR-0011: Separate routing and infrastructure configuration

**Status**: Accepted
**Date**: 2026-10-19

## Context

The proxy originally had a single configuration file containing both routing rules (upstreams, routes, mirrors) and infrastructure settings (timeouts, connection pools, worker counts). The routing configuration is hot-reloadable, but infrastructure settings like network timeouts require rebuilding the `http.Transport` and `http.Server`, which has the same operational impact as restarting the proxy.

This mixing of concerns created confusion about what could be changed without restart and led to unnecessary complexity when attempting to make infrastructure settings hot-reloadable.

## Decision

Split configuration into two distinct files:

1. **`routes.yaml`** - Hot-reloadable routing configuration
   - upstreams
   - routes
   - mirrors

2. **`infrastructure.yaml`** - Infrastructure configuration (requires restart)
   - network timeouts (transport & server)
   - connection pool settings (max idle conns)
   - mirroring settings (workers, queue size)
   - server port

This makes the boundary explicit: routing changes are safe to hot-reload, infrastructure changes require restart.

## Alternatives Considered

### Callback mechanism for infrastructure reload
- Pros: Would allow infrastructure changes without restart
- Cons: Adds complexity (callbacks, mutexes, atomic swaps), no clear benefit since rebuilding transport/server has same operational impact as restart
- Rejected: Over-engineering for minimal benefit

### Single config with section markers
- Pros: One file to manage
- Cons: Still unclear what requires restart, validation harder
- Rejected: Doesn't solve the core confusion problem

### Environment variables for infrastructure
- Pros: Clear separation
- Cons: Harder to manage complex config via env vars, less human-readable
- Rejected: YAML is better for complex configuration

## Consequences

**Positive:**
- Clear separation of concerns: routing vs infrastructure
- Explicit restart requirement for infrastructure changes
- Simpler code: no callbacks, mutexes, or atomic swaps for infrastructure
- Better validation: each config validated independently
- Clear documentation: operators know exactly what can be hot-reloaded

**Negative:**
- Breaking change: existing single-file configs must be split
- Two files instead of one (mitigated by clear naming)
- Infrastructure changes require restart (intentional and correct behavior)

**Technical debt:**
- None. This is the correct end state.

## Migration Guide

For existing deployments using a single `config.yaml`:

1. Split the file into two parts:
   - Move `net-timeouts-s` section to `infrastructure.yaml`
   - Move `upstreams` and `routes` sections to `routes.yaml`
   - Add `connection-pool`, `mirroring`, and `port` to `infrastructure.yaml` (use defaults from example)

2. Update environment variables:
   - `CONFIG_PATH` → `INFRA_CONFIG_PATH` and `ROUTES_CONFIG_PATH`

3. Restart the proxy

## Revisit Triggers

- If infrastructure hot-reload becomes a strong requirement with clear operational benefit
- If the two-file approach proves confusing in practice
