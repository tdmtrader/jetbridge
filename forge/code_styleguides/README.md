# Code Style Guides

## Go

Follow the existing Concourse codebase conventions, which align with:
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- `gofmt` / `goimports` for formatting (enforced)

### Project-Specific Conventions

- **Error wrapping:** Use `fmt.Errorf("context: %w", err)` for error wrapping with context.
- **Interfaces:** Define interfaces where they are consumed, not where they are implemented.
- **Fakes:** Generate with `counterfeiter`. Place in `fakes/` subdirectory.
- **Logging:** Use `lager.Logger` for structured logging. Include relevant context fields.
- **Kubernetes types:** Use typed clients from `k8s.io/client-go`. Prefer informers/listers for read operations.
- **Context propagation:** Pass `context.Context` as the first parameter. Use it for cancellation and tracing span propagation.
- **Resource cleanup:** Use `defer` for cleanup. For Kubernetes resources, ensure proper finalizer/owner-reference patterns.

### Naming
- Package names: short, lowercase, no underscores (e.g., `kubernetes`, `volume`, `exec`).
- Interface names: `-er` suffix when representing a single behavior (e.g., `Worker`, `Registrar`).
- Test helpers: prefix with `test` or place in `_test.go` files.

## Elm

Follow Elm community conventions:
- `elm-format` for formatting (enforced).
- Module-per-feature organization.
- Avoid ports unless absolutely necessary.

## SQL

- Use `squirrel` query builder for dynamic queries.
- Raw SQL for migrations (in `atc/db/migration/`).
- Use parameterized queries — never string interpolation.
