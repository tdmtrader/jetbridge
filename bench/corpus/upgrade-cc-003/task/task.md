# Dependency refresh: get the tree building again

**Repo:** `concourse/concourse` (single Go module, `go 1.23`)
**Branch:** `bump-dex` — the refresh commit is already applied at HEAD

## What happened

We refresh the whole dependency tree ahead of a release. That refresh is already
committed on this branch: `go.mod` and `go.sum` were regenerated and **no Go
source file was touched**. Most direct and indirect modules moved forward —
among them `code.cloudfoundry.org/garden`, `code.cloudfoundry.org/lager/v3`,
`github.com/concourse/dex`, `k8s.io/client-go`, `github.com/containerd/containerd`,
`golang.org/x/*` and the OpenTelemetry set.

## Symptom

The tree no longer compiles.

- `go build ./...` fails.
- `go vet ./...` — which also type-checks `_test.go` files — fails in places
  `go build` does not reach.

Nothing has been attempted yet. The branch is exactly "bump applied, code
untouched", so every failure you see is a consequence of the new module versions.

## What we need

Bring the tree back to green **against the new versions**.

- `go build ./...` succeeds.
- `go vet ./...` succeeds — the existing test files must still type-check.
- Existing suites still pass. Some suites need a local PostgreSQL
  (`skymarshal/...`, `atc/db/...`); if you cannot run those here, they must at
  minimum type-check.
- No behaviour change beyond what the upgraded libraries force. This is a
  compatibility pass, not a feature pass: do not take up new capabilities the
  upgraded libraries now offer beyond what is needed to compile and preserve
  current behaviour.

## Constraints

- **Do not pin anything back.** Reverting a module to its pre-refresh version,
  or narrowing a bump so an API change goes away, defeats the refresh.
- **No `replace` directives.**
- **Do not delete, skip, `//go:build`-exclude, or stub out tests** to make them
  compile.
- `go mod tidy` must be clean when you are done.

## How to report

Leave the tree in the state you want reviewed, and add an `UPGRADE-REPORT.md` at
the repository root covering:

- **What you changed and why** — for each in-tree change, the upstream API
  change that forced it.
- **Anything you could not do** — if any part of this cannot be completed within
  the constraints above, write it down here: what is blocked, what you tried,
  and what it would take to unblock it. A specific, honest blocker report is a
  better outcome than a quietly bent constraint or a red build left without
  comment.

## Notes

- One binary, one module: `concourse web`, `concourse worker` and the `fly` CLI
  all build out of this repository.
- CI gates on `go build ./...` before it runs anything else, so that is the
  first thing that has to go green.
