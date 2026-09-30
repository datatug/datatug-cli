# Project Guidelines for Junie AI

This file contains specific rules and conventions for the DataTug project.

## 1. General Principles

- **Minimalism**: Favor simple, readable solutions over complex abstractions.
- **Consistency**: Follow the existing patterns in the codebase. If you see a specific way of handling errors or naming
  variables, stick to it.
- **Documentation**: Add KDoc-style comments for all public functions and structures, but only if they are not
  self-explanatory.

## 2. Go Coding Standards

- **Functions calling**
    - Do not pass a result of a function directly to another function, instead create a var and pass it.
- **Error Handling**:
    - Always check errors immediately.
    - Use `fmt.Errorf("context: %w", err)` to wrap errors with meaningful context.
- **Naming**:
    - Use camelCase for internal variables and PascalCase for exported symbols.
    - Receivers should be short (usually 1-3 letters), e.g., `func (srv *Server) ...`.
- **Imports**: Group imports into three sections: standard library, external dependencies, and internal packages.

## 3. UI Development (dtviewers, dtproject)

- The terminal UI is built with Bubble Tea v2 / Lip Gloss on the shared `strongo-tui` toolkit (shell, widgets, grid, navtest). See `docs/tui-screens.md` before adding or changing a screen.
- Use the theme from `strongo-tui` `pkg/theme` (and `pkg/color`) to maintain visual consistency.

## 4. Testing

- Place tests in the same package as the code being tested (e.g., `logic.go` and `logic_test.go`).
- If a test is in wrong file move the test to a proper file.
- Use table-driven tests for complex logic.
- Ensure that `main_test.go` remains updated if global setup changes.

## 5. Commit Messages

- each commit should be prefixed with `fix|feat|chore` followed by :.
  Examples: `feat: add new feature`, `fix: corrected typo`, `chore: documentation improvemnt`
- Use imperative mood (e.g., "Add feature" instead of "Added feature").
- Reference issue numbers if available.
