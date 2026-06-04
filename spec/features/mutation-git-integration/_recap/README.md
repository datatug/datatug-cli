# Recap Reports — mutation-git-integration

Per-run recap reports produced by `specstudio:recap`. Each report is named `<sha>.md` where `<sha>` is the abbreviated git SHA of `HEAD` at run time. Each row records which verify report the recap was compared against (`Verify revision`) so reviewers can trace the drift gate end-to-end.

## Contents

| Report | Run revision | Verify revision | Drift summary |
|---|---|---|---|
| [24c8659.md](24c8659.md) | 24c8659 | fc3deaf | 6 no-drift, 0 spec-tighter, 0 code-tighter, 0 contradiction, 0 unmapped, 0 errored |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/index-specification*
