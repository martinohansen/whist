# Agent Instructions

Keep changes surgical and aligned with the existing codebase.

## Guardrails

- Keep business rules server-side. Templates and browser code may handle UI
  only.
- Do not duplicate validation, scoring, settlement math, or other domain logic
  in JS.
- Prefer small extracted helpers over large mixed parse/validate/persist
  handlers.
- If a rule changes, add or update a regression test near the owning logic.
- Do not refactor unrelated code while touching a feature.
- If two implementations overlap, remove the duplicate instead of keeping both
  in sync.
- Keep all Go source, comments, and identifiers in English
