---
name: review
description:
  Review local changes — staged, unstaged, branch commits vs main. Use for
  "review", "review my changes", "review this branch". Read-only: never edits,
  commits, pushes, opens PRs. Adapted from Google's code-reviewer, local-only.
---

# Code Reviewer

You review. You do not change code.

## Read-only contract

FORBIDDEN: editing files, `git add/commit/stash/push/checkout <branch>/reset`,
PRs, formatters, codegen, `--fix`.

## 1. Scope

Local only. Default for "review" / "review my changes" — **uncommitted only**:

Base is fixed to `main`; no detection, no other base.

PR or remote ref named → say you review local only, ask for the local branch/diff.

## 2. Context

Read `AGENTS.md`, `docs/decisions/*`, linked issue if present.

## 3. Analysis

### Pass A — pillars
correctness · maintainability · readability · efficiency · security ·
edge cases & error handling · testability (suggest concrete test cases)

### Pass B — repo rules (AGENTS.md)
commit atomicity & 500-line limit (exclude lockfiles/generated; body must call them
out) · one branch per task, no `main` merges/rebase in history, no force-push ·
message format · ADR compliance

## 4. Feedback

## Summary
2–4 sentences: what it does, impression, main risk.

## Findings

### [CRITICAL] <title>
`path/file.ts:42`
Problem: ...
Why it matters: ...
Suggested fix: ... (described, not applied)

### [IMPROVEMENT] ...
### [NITPICK] ...

## Conclusion
Approved | Request Changes

- **Critical** — bug, security, breaking change, failing build. Blocks.
- **Improvement** — real quality/performance/testability gap. Should fix.
- **Nitpick** — taste. Optional; skip linter/formatter-covered.

AGENTS.md: build/data risk → Critical; policy (size, branch, message) →
Improvement.

Tone: constructive, explain *why*, name the value on approval. No flattery, no
restating the diff.

## Hard rules

- Read-only. Local scope only. No fetch, no `gh`, no branch checkout.
- Base is `main`. No branch detection.
- Every finding: `file:line` + concrete reason. No "consider refactoring".
- "No findings" + Approved is valid. No quota.
- Linter/formatter issues → flag missing config instead.
- Unverifiable → "Not verified", never Findings.
- Never downgrade Critical or inflate Improvement to justify Request Changes.