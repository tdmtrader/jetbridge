# Upgrade request: go-jose v3 → v4

**Component:** `github.com/go-jose/go-jose`
**Currently on:** `v3` (`github.com/go-jose/go-jose/v3 v3.0.4`)
**Move to:** `v4` (`github.com/go-jose/go-jose/v4`, already pinned at `v4.1.2` in
`go.mod`)
**Scope:** the whole `github.com/concourse/concourse` module.

## Why

The v3 line is in maintenance: upstream only ships security fixes for it, and a
v5 is already on their roadmap. We do not want to be two major versions behind
when that lands. Nothing in the tree uses v4 yet even though the requirement is
already in `go.mod`, so this is the moment to move the whole codebase over in
one step rather than ending up with both majors linked into the same binary
indefinitely.

## What "done" looks like

- No non-vendored Go file in the repository imports `github.com/go-jose/go-jose/v3`
  (or any package under it) any more.
- `github.com/go-jose/go-jose/v3` is gone from `go.mod`.
- The module builds and the existing test suites still pass. v4 is a major
  version bump, so expect the compiler to reject some call sites that were fine
  under v3; work through them.

## Constraints

- **No behaviour change.** This is a dependency migration, not a redesign of how
  Concourse issues or accepts tokens. Tokens that verified before must still
  verify; tokens that were rejected before must still be rejected.
- **Keep the public surface stable.** Exported identifiers of
  `atc/creds/idtoken`, `atc/db`, `skymarshal/token` and `atc/api/accessor` should
  keep their current names and semantics. If a signature genuinely has to change
  because a go-jose type appears in it, keep the change to the type's major
  version and nothing else.
- **Counterfeiter fakes.** `atc/db/dbfakes` is generated
  (`//go:generate ... counterfeiter`). Whatever you do there, the fakes must
  still satisfy the interfaces they stand in for. Do not hand-edit a fake into
  something the generator would not produce.
- **Do not change unrelated code.** No opportunistic refactors, no reformatting
  passes, no dependency bumps other than the ones this migration forces. (The
  write-up asked for below is part of the deliverable, not unrelated churn.)

## What to hand back

1. The change itself, in the working tree.
2. A short write-up at `UPGRADE-REPORT.md` in the repository root — a page is
   plenty — covering:
   - what you changed and any judgement calls you had to make;
   - which build and test commands you actually ran, and what they reported;
   - anything you could **not** verify in this environment, and what you relied
     on instead to convince yourself that part is right.

## Environment notes

- Go 1.25 toolchain. `go build ./...` does not complete on macOS
  (`worker/runtime` is Linux-only, via containerd); use
  `go build ./atc/... ./skymarshal/... ./fly/...` locally.
- `atc/db` and `atc/gc` suites need a local PostgreSQL to actually run; they
  compile without one.
- `testflight/` needs a running Concourse and is not runnable here — it must
  still compile (`go test -run '^$' ./testflight/`).
