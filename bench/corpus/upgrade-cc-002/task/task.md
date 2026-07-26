# Renovate `renovate/all` is red — adopt the pending major bumps

**Type:** dependency upgrade
**Source:** Renovate bot, rolling branch `renovate/all`
**PR title:** `fix(deps): update all dependencies`
**Status:** CI red, nothing merges until this is green

## What happened

Our Renovate config groups every dependency into a single rolling
`renovate/all` branch. Its latest run pushed a commit that rewrites `go.mod`
and nothing else — three modules moved to a new major version, three lines
changed, no Go source touched, no `go mod tidy` run:

| module | from | to |
|---|---|---|
| `github.com/caarlos0/env` | `v6 v6.10.1` | `v9 v9.0.0` |
| `github.com/vbauerster/mpb` | `v4 v4.12.2` | `v8 v8.6.2` |
| `gopkg.in/yaml` | `v2 v2.4.0` | `v3 v3.0.1` |

Because a Go major version is part of the import path, the repository in this
state does not build at all. Every `go` command in the module now stops at:

```
go: updates to go.mod needed; to update it:
	go mod tidy
```

## What we need

Get the branch green so the PR can merge, on the versions Renovate is moving
us to.

The point of this PR is that the versions actually move forward. When the work
is finished the superseded major versions must be gone from `go.mod`, not
restored — a tree that is green because it is back on the old majors is not a
fix.

## Done means

- `go build ./...` succeeds.
- `go vet ./...` succeeds.
- The module's unit tests pass.
- `go.mod` and `go.sum` are consistent and `go mod tidy`-clean, with no
  duplicated requirements.

## Constraints

- **Do not edit tests to accommodate a dependency.** If a new version changes
  observable behavior such that an existing spec fails, that is a fact about
  the dependency, not a fault in the spec. Every spec that passes today on the
  previous versions must still pass, unchanged.
- This is a dependency PR. Do not take the opportunity to refactor, rename or
  restructure the code that consumes these libraries. The observable behavior
  of `fly` — what it prints and how — must be identical afterwards.
- Do not vendor, fork, copy or shim a dependency in order to dodge an API
  change, and do not pin to an intermediate version that happens to compile.
- If any of the three proposed moves cannot be adopted under the constraints
  above, back that one out, stop the bot from re-proposing it, and record the
  reason in the change itself — in the repository, where the next person to
  hit this will find it, not only in a reply to this ticket.
