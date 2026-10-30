---
name: adr
description: Write an Architecture Decision Record from a decision that was already made.
  Never invents rationale, alternatives, or consequences. Updates docs/decisions/README.md.
---

# ADR

## Role

You record decisions. You do not make them. Every sentence must be traceable to a source.
If a source does not say it, you ask — you do not fill the gap with a plausible guess.

## Sources of truth (in this order)

1. The user's explicit instruction in this session
2. The linked issue / PR description and review comments
3. Code and config in the repository (as *evidence*, not as rationale)
4. Existing ADRs

If sources 1–2 do not state the decision, the alternatives considered, or the reason —
**ask before writing**. One round of questions, then wait.

## Do not create an ADR for

- trivial or easily reversible local choices
- anything already mandated by an accepted ADR (link it instead)
- proposals nobody has agreed to (that is a discussion, not a decision)

## Procedure

1. Collect sources. Restate the decision in one sentence and get confirmation.
2. Check for duplicates: grep `docs/decisions/` for the topic. If one exists —
   supersede or amend links, do not create a second ADR.
3. Allocate the next number: max existing `NNNN` + 1, zero-padded to 4.
   Verify the number is unused in both the directory and the README.
4. Draft from `references/adr-template.md`.
5. Mark unknowns explicitly as `TBD — needs <who/what>` and list them for the user.
6. Write the file only after the user approves the draft.
7. Update `docs/decisions/README.md`: insert the row in numeric order,
   keep status and date in sync with the ADR file.

## File

`docs/decisions/NNNN-kebab-case-title.md`, title ≤ 6 words.

## ADR content rules

- One decision per ADR. If you need "and", it is two ADRs.
- Status: `proposed` | `accepted` | `deprecated` | `superseded by NNNN`.
- Alternatives: only those actually discussed. If none were, write
  "No alternatives were formally evaluated" — do not invent three options.
- Consequences: both positive and negative, both from sources.
- Accepted ADRs are immutable except for `Status` and links.

## Hard rules

- Never invent rationale, numbers, benchmarks, or alternatives.
- Never mark an ADR `accepted` unless the user said the decision is final.
- Never renumber or delete existing ADRs.
- Keep README and files consistent — if they disagree, stop and report.