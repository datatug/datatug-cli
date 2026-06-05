# Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 73.4% |
| Post-run coverage | 99.6% |
| Uncovered statements remaining | 1 |

## Seams added

None. All branches were reachable via direct test inputs without production-code changes.

## Documented gaps

### unreachable

**Function:** `CompileWhereForTable` — coercion-error branch (compile.go line 37–39)

**Reason:** `CompileWhereForTable` calls `ValidateWhereAgainstSchema` (line 26) before calling
`CoerceValue` itself (line 36). `ValidateWhereAgainstSchema` already calls `CoerceValue`
internally (validate.go line 37) and returns an error on coercion failure. Therefore the
compile-level coerce error (line 38) can never fire: any input that would cause `CoerceValue`
to fail will be rejected by the prior `ValidateWhereAgainstSchema` call first.

**Refactoring required:** Remove the redundant `CoerceValue` call from `CompileWhereForTable`
(the compile step should trust that `ValidateWhereAgainstSchema` already checked coercibility),
or remove the coercion from `ValidateWhereAgainstSchema` and let `CompileWhereForTable` own it.
Either restructuring would eliminate the dead branch.
