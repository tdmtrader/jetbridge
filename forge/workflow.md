# Development Workflow

## Methodology: Test-Driven Development (TDD)

All feature work follows the Red-Green-Refactor cycle:

1. **Red** — Write a failing test that specifies the desired behavior.
2. **Green** — Write the minimum code to make the test pass.
3. **Refactor** — Clean up the implementation while keeping tests green.

### Task Structure

Each feature task in a track plan includes two sub-tasks:
1. `[ ] Write tests for <feature>` — Create test cases covering happy path, edge cases, and error scenarios.
2. `[ ] Implement <feature>` — Write production code to satisfy the tests.

### Test Pyramid

- **Unit tests** — Test individual functions and types in isolation. Use `counterfeiter` for mocking interfaces. Run with `ginkgo`.
- **Integration tests** — Test component interactions (e.g., K8s runtime against a real or fake API server). Located in `testflight/` and `integration/`.
- **E2E tests** — Test full pipeline execution. Located in `topgun/`.

### Test Conventions

- Use Ginkgo v2 `Describe`/`Context`/`It` blocks.
- Use Gomega matchers for assertions.
- Name test files `*_test.go` adjacent to the code under test.
- Use `counterfeiter` to generate fakes: `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -o fakes/fake_<interface>.go . <Interface>`

## Version Control

### Branch Strategy
- `jetbridge` — Main development branch for the K8s runtime fork.
- `main` — Mirrors `jetbridge` after CI passes (pipeline auto-merges).
- Feature branches: `jetbridge/<feature-name>` — Branched from `jetbridge`.
- PRs target `jetbridge` branch.

### Keeping main in sync
The CI pipeline merges `jetbridge` → `main` after all tests pass. Local `main` can fall behind. **Always run `git fetch origin main:main` (without checking out) at the start of Forge commands** (`/forge:status`, `/forge:newTrack`, `/forge:complete`) to ensure the local `main` ref is current. This prevents the Claude Code UI from showing false diff between the branches.

### Commit Strategy
- Conventional commits: `type(scope): description`
- Atomic commits — Each commit should build and pass tests.
- Squash merges for feature branches into `jetbridge`.

### Pre-Commit Checks
```bash
go vet ./...
go test ./worker/kubernetes/...
```

## Code Review Checklist

- [ ] Tests pass locally
- [ ] New code has test coverage
- [ ] No regressions in existing tests
- [ ] GoDoc comments on exported symbols
- [ ] Error handling follows project conventions (wrap with context)
- [ ] Kubernetes resources have proper labels and annotations
- [ ] Observability: logging, tracing spans, metrics where appropriate

## Definition of Done

A task is complete when:
1. All acceptance criteria from the spec are met.
2. Tests are written and passing (unit + integration as appropriate).
3. Code is committed with a conventional commit message.
4. No linting errors or vet warnings.
