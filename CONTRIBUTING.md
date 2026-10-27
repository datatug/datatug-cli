# Contributing to DataTug

Thank you for your interest in contributing to DataTug! We welcome contributions from developers, database engineers, data architects, and open-source enthusiasts.

---

## Code of Conduct

All contributors and maintainers are expected to adhere to our [Code of Conduct](CODE_OF_CONDUCT.md). Please report any unacceptable behavior to [conduct@datatug.app](mailto:conduct@datatug.app).

---

## Open-Source Architecture & Licensing Scopes

DataTug is developed under an open-core licensing model (**Model A**):
- **Community Core (Apache License 2.0)**: All contributions to `apps/`, `pkg/`, `internal/`, root CLI (`main.go`), `testdata/`, `scripts/`, and `docs/` are licensed under the permissive Apache License 2.0. By submitting a pull request to these paths, you agree that your contribution is licensed under Apache 2.0.
- **Commercial Extensions (`commercial/`)**: Features located in `commercial/` are proprietary and govern centralized organizational coordination, enterprise audit logging, and team services under the DataTug Commercial Terms.
- For complete licensing details and capability mapping, see [LICENSING.md](LICENSING.md).

---

## Developer Certificate of Origin (DCO)

To ensure clear provenance of open-source contributions, all commits must include a `Signed-off-by` line certifying compliance with the Developer Certificate of Origin:

```text
Signed-off-by: Random Developer <random@example.com>
```

You can automatically add this line to your commits by using `git commit -s`.

---

## Development Environment Setup

### Prerequisites
- **Go**: 1.23+ recommended.
- **Git**: 2.30+.
- **SQLite / CGo**: CGo is required for SQLite and DuckDB drivers.

### Getting the Code
```bash
git clone https://github.com/datatug/datatug-cli.git
cd datatug-cli
```

### Running Tests
Targeted fast tests:
```bash
# Test commercial licensing primitives
go test -v ./commercial/licensing/...

# Test watch daemon and coordination
go test -v ./pkg/watchdaemon/...

# Test server endpoints
go test -v ./pkg/server/endpoints/...
```

Full repository test suite:
```bash
go test ./...
```

### Building Binaries
Build the local `datatug` CLI binary:
```bash
go build -o bin/datatug .
```

Run local self-check:
```bash
./bin/datatug --version
./bin/datatug --help
```

---

## Submitting Pull Requests

1. **Create an Issue**: Before undertaking substantial refactoring or new feature development, please open an issue to discuss design and scope with the maintainers.
2. **Work in a Branch or Worktree**: Keep changes focused and branch from `main`.
3. **Write Unit Tests**: Ensure new logic includes corresponding unit tests with good test coverage.
4. **Follow Go Conventions**: Run `gofmt -s -w .` and `go vet ./...` before submitting.
5. **Open Pull Request**: Reference related issues in your PR description.
