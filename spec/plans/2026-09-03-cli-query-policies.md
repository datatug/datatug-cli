---
format: https://specscore.md/plan-specification
status: Implemented
---
# Plan: Query command over access policies (cli/query)

**Status:** Implemented
**Source Feature:** cli/query
**Date:** 2026-09-03
**Owner:** alex
**Supersedes:** —

## Summary

Implement `datatug query run`: DTQL or `--from` input with query-parameter substitution, database opening by URL, policies discovered from the per-user directory with fail-closed defaults, principal and variables from flags, secured execution through `access.SecureReadSession` with the hidden-field reference check, rows to stdout in five formats, and the limitation report to stderr. All acceptance criteria are covered; none deferred.

## Approach

The reusable core lives in `pkg/accesspolicies` (discovery, decoding, variable parsing, `Run`, `Line`), so the TUI and `serve` can reuse it later; the cobra command only parses flags, maps exit codes and formats rows. Tests use a temporary inGitDB database so the whole feature is verified without cgo, and the documented global policy file is the test fixture, so the example in `docs/policies/global.yaml` is proven on every run.

## Tasks

### Task 1: Policy discovery and decoding

**Id:** task-1
**Verifies:** cli/query#ac:policies-discovered-in-order, cli/query#ac:policy-flag-appended, cli/query#ac:env-dir-used, cli/query#ac:explicit-dir-missing-exit-2, cli/query#ac:no-policies-required-when-none, cli/query#ac:no-policies-conflicts-with-policy, cli/query#ac:invalid-policy-exit-2
**Depends-On:** —
**Status:** complete

`accesspolicies.ResolveDir`, `Load`, `LoadFile` (`.yaml`/`.yml`/`.json`, sorted, source = path), the explicit-directory and zero-policies refusals, errors naming the file.

### Task 2: Principal, variables, secured run and report

**Id:** task-2
**Verifies:** cli/query#ac:var-invalid-exit-2, cli/query#ac:var-typed, cli/query#ac:hidden-field-filter-denied, cli/query#ac:hidden-field-select-denied, cli/query#ac:alias-refused-under-field-lists, cli/query#ac:param-outside-where-exit-2, cli/query#ac:report-names-limitations, cli/query#ac:report-no-limitations
**Depends-On:** 1
**Status:** complete

`ParseVariables` (name grammar, reserved names, YAML typing), `Run` (context, query-parameter substitution, hidden-field check, `SecureReadSession`), `Explain`/`Line.String()` from `access.Decision` per policy.

### Task 3: Query command

**Id:** task-3
**Verifies:** cli/query#ac:query-from-collection, cli/query#ac:query-file-stdin, cli/query#ac:input-mutually-exclusive, cli/query#ac:query-param-substituted, cli/query#ac:db-url-required, cli/query#ac:db-scheme-unsupported, cli/query#ac:rows-filtered-by-principal, cli/query#ac:fields-hidden, cli/query#ac:no-policies-warns, cli/query#ac:quiet-suppresses-report, cli/query#ac:denied-exit-5, cli/query#ac:formats, cli/query#ac:header-follows-request-order, cli/query#ac:numbers-plain, cli/query#ac:db-unopenable-exit-4
**Depends-On:** 2
**Status:** complete

`cmd_query.go`: the `query` group and `run` verb, flags, DTQL/`--from`, `dbcopy` URL backend, exit codes 2/4/5/1, row normalisation and the five formats.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
