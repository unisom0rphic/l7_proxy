# ADR-0010: Keep last known-good config when the file is deleted

**Status**: Accepted
**Date**: 2026-10-03

## Context

While the proxy is running with a valid, loaded config, the config file can be deleted out from under it - by an operator, a deploy tool, a bad cleanup script, or an atomic-rename pattern that briefly removes the old file before placing the new one.

The proxy already handles reload-time failures generally (see the sibling policy on invalid configs). This ADR is specifically about **file deletion**: the path the proxy was told to watch no longer exists.

## Decision

On deletion: log an error, emit a `config_reload_failed` counter, and keep serving the last known-good config. Do not swap. Do not exit.

The running config is treated as the source of truth until a valid replacement arrives.

Startup is a separate case: if the config file is missing at first load, the process has no coherent state and should panic.

## Alternatives Considered

### Panic on deletion
- Loud, unambiguous, no stale-config window.
- Rejected because the blast radius is disproportionate. A brief file absence during an atomic rename, or a mistaken `rm`, should not take down a running proxy that already has a valid config in memory.

### Exit gracefully
- Indistinguishable from a normal restart under a supervisor; same outage as panic with less signal.
- Rejected.

### Keep last-known-good, log only
- Same runtime behavior as the chosen decision, minus the metric.
- Rejected. The concern behind the panic option - silent divergence between the file on disk and the config in memory - is real. A log line in a container nobody tails does not address it. `config_reload_failed` is a required part of this decision.

## Consequences

**Positive:**
- Deletion does not take the proxy down.
- The process keeps serving with a config that is known to have worked.
- `config_reload_failed` makes the divergence alertable.

**Negative:**
- The running config silently diverges from disk until an operator intervenes.
- With the current fsnotify implementation, the watcher exits after the delete event and does not recover if the file reappears. A restart is required to resume automatic reloads. This is an implementation limitation of the current watcher, not a policy decision - SIGHUP removes it, see ADR-0011.
- Long-running divergence is possible and surfaces only via the metric.

**Technical debt:**
- The watcher dying on delete is a real wart. Under fsnotify it should be fixed (re-establish the watch, or treat delete as "wait for recreate"). Under SIGHUP the problem does not exist. The ADR should be re-read after the reload mechanism is finalized.

## Revisit Triggers

- Reload mechanism changes (fsnotify -> SIGHUP), which removes the "watcher dies" consequence and makes the negative list shorter.
- `config_reload_failed` fires and is routinely ignored. The mitigation isn't working; reconsider the panic position.
- A requirement to enforce config freshness, i.e. "the running config must match the file within X seconds".