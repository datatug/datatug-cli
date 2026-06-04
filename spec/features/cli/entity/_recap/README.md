# Recap Reports — cli/entity

Per-run recap reports produced by `specstudio:recap`. Each report is named `<sha>.md` where `<sha>` is the abbreviated git SHA of `HEAD` at run time. Each row records which verify report the recap was compared against (`Verify revision`) so reviewers can trace the drift gate end-to-end.

## Contents

| Report | Run revision | Verify revision | Drift summary |
|---|---|---|---|
| [b8ff1fa.md](b8ff1fa.md) | b8ff1fa | 1b0448e | 19 no-drift, 0 spec-tighter, 0 code-tighter, 0 contradiction, 0 unmapped, 0 errored |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/index-specification*
