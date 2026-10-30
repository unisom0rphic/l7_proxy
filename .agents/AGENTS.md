# AGENTS.md

Rules for every agent working in this repository. A skill may add rules, never relax these.

## Commits

- One logical change per commit. Atomic: every commit must build and pass tests on its own.
- Hard limit: **500 changed lines per commit**. Excluded: lockfiles,
  codegen, snapshots, vendored deps — call them out in the commit body.
- If the change exceeds the limit, split it. Split order:
  refactor (no behavior change) → behavior change → tests → docs.
- Never mix formatting- or rename-only changes with logic changes.
- Message: Conventional Commits — `<type>(<scope>): <subject>`
- Subject: imperative, ≤ 72 chars, no trailing period, keep as short as possible,
  no more than one or two sentences unless neccessary.

## Branches

- Name: `<type>/<short-slug>`.
- Never merge, rebase onto, or push to the default branch without user's agreement.
- Never force-push. Never amend a commit that was already pushed.
- Never open a PR unless explicitly asked.

## When in doubt

Split the commit. A rejected small commit costs less than an unsplittable large one.