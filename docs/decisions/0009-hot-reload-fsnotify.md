# ADR-0009: fsnotify + debounce for hot-reload

**Status**: Accepted
**Date**: 2026-10-03

## Context
The proxy must pick up config changes without restart, so that routing rules, mirroring targets, and observability settings can be updated in place. Restarting the process would drop connections and cause downtime.

Options include: signal-driven reload (SIGHUP), filesystem watching, an admin HTTP endpoint, or a polling loop.

## Decision
Use `fsnotify` to watch the config file for `write` events. Debounce incoming events with a 2-second window before triggering a reload.

Rationale for debounce: editors and deployment tools often produce multiple write events for a single logical change (atomic rename, temp file + move, backup + write). Without debounce, the proxy would parse a partially written file or reload several times in rapid succession. A 2-second window is long enough to coalesce bursts and short enough to feel responsive.

Reload itself follows ADR-0002 (RWLock for map swap) and ADR-0010 (panic on invalid config).

## Alternatives Considered

### SIGHUP only
- Pros: no dependency, explicit operator control, no surprise reloads.
- Cons: requires the operator to remember to send the signal after every change; breaks the "save file, see effect" workflow; not composable with config-management tools that just write files.

### Polling (stat file every N seconds)
- Pros: no dependency, works across all filesystems (including some where fsnotify doesn't).
- Cons: latency equals poll interval; wasted syscalls; less responsive feel. fsnotify is strictly better on Linux/macOS for the development workflow.

### Admin HTTP endpoint (POST /reload)
- Pros: explicit, observable, easy to trigger from CI.
- Cons: requires extra endpoint and auth consideration; doesn't solve the "editor saves file" case; would still need a file watcher or SIGHUP for the common path.

### fsnotify without debounce
- Pros: simplest.
- Cons: rapid-fire reloads on multi-write events; risk of reading a partially written file; noisy logs.

## Consequences
**Positive:**
- Editing the config file and saving triggers reload automatically - no extra commands.
- Debounce smooths out editor/atomic-rename noise.
- Composes with config-management tools that simply write the file.
- `fsnotify` is a small, well-maintained dependency.

**Negative:**
- Adds `fsnotify` as a dependency.
- 2-second debounce means the reload is not instantaneous; acceptable trade-off.
- fsnotify behavior differs slightly across platforms (inotify on Linux, FSEvents/kqueue on macOS). Documented; not a concern for MVP.

**Technical debt:**
- None intended. If SIGHUP or an admin endpoint becomes necessary (e.g. for CI integration), add them alongside fsnotify rather than replacing it.

## Revisit Triggers
- fsnotify fails on a target filesystem or platform we need to support.
- Need for explicit reload triggers from CI/deploy tooling.
- Need for reload observability beyond logs (e.g. metrics on reload events).