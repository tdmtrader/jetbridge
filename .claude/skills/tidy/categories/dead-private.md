# dead-private

Delete one unexported Go declaration that staticcheck's U1000 reports unused
and that no other line in the repository names: a function, a method, a
one-line top-level `var` or `const`, or a type. A type and the methods declared
on it in its file are one instance: a method cannot outlive its type, and the
type cannot go while a method names it. The private sibling of `surface`, which
owns exported declarations. The deletion takes the doc comments, the imports it
leaves unused, and the file when nothing else is left in it; it adds no line.

Merge: green
Instance key: `<name>@<file>`, where a method's `<name>` is `<receiver type>.<method>` with no `*` or type parameters (`Container.volumeForPath@atc/worker/jetbridge/container.go`); a type's methods ride on the type's key

## Scan

`bash .claude/skills/tidy/categories/dead-private.scan.sh` installs the pinned
staticcheck v0.8.1 if needed, runs one U1000 pass and applies every exclude
below but the last. It prints `<tier> <file>:<line> <key> <span ranges>
part|whole [<commit>]`, ordered production files (tier 1), `_test.go` files
outside `topgun/` (tier 2), then `topgun/` (tier 3: CI-only suites, so
compiling is the only local evidence), each by file and line.

## Predicate

- On `origin/core`, `"$SC" -checks U1000 ./<dir>/` reports it unused, and the
  scan printed it with the key the log row carries.
- The diff deletes exactly the printed span ranges (the declaration with its
  doc comment, the `//` lines directly above it with no blank line between;
  for a type, every method declared on it in the file, each with its doc
  comment), plus:
  - one blank line per range, judged on the file as `origin/core` has it, only
    when the range has a blank line or the end of the file on both sides: the
    blank line after it, or, when the range ends the file, the one before it;
  - the import specs `go vet ./<dir>/` then reports as `"<path>" imported and
    not used` or `"<path>" imported as <name> and not used`, and the `import (`
    and `)` lines and the blank line after them when no spec is left;
  - a floating comment directly above the span (one blank line between), with
    one blank line beside it, only when after the deletion the next non-blank
    line below it is the end of the file or another comment block followed by
    a blank line. Any other floating comment stays even if it now introduces
    nothing; say so in the log note.
  No two blank lines meet afterwards and the file does not end in one.
- With `whole`, the file is deleted instead; that is still one instance.
- The diff adds no line besides the log row: `git diff --numstat origin/core
  -- . ':!.claude/skills/tidy'` shows 0 added. Delete by hand; never run
  `gofmt -w` or `goimports -w`.
- After the deletion `git grep -nw '<name>' -- . ':!.claude/skills/tidy'`
  prints nothing, where `<name>` is the declared name (a method's method name,
  a type's type name).
- `grep -c 'Expect(\|Eventually(\|Consistently(' <file>` does not fall.
- Production file (tier 1): the scan printed a commit, the newest that changed
  how often `<name>` appears in the directory's non-test `.go` files. It is at
  least 30 days old, its subject does not start `tidy(`, and its diff removes a
  code line naming `<name>` that is not its declaration or a receiver line. The
  log note names it.

## Exclude

The scan applies every line but the last, which only `go vet` after the edit
can see: when it applies, revert and take the next candidate. Stderr names each
finding set aside and why. `born-dead`, `recent:`, `tidy-newest:` and
`no-caller-removed:` production findings are the owner's to wire or delete;
name them in the note when the day logs `skipped`. `named-here:` usually means
a dead caller in the same file goes first; `named:` in a string or a comment is
a human's call.

- `atc/worker/jetbridge/brine/` and `hack/mcp-oauth-probe/` (their own modules;
  `./...` from the root never analyses them), `vendor/`, `*fakes/`,
  `atc/db/migration/migrations/`, generated files (`// Code generated … DO NOT
  EDIT.`).
- Struct fields, embedded ones included (`field … is unused`): removing one
  changes `%v` output, `reflect.DeepEqual`, struct size and comparability.
- Exported names: that is `surface`. An exported method goes only with its
  unexported type.
- A file with a build constraint (`//go:build`, `// +build`) or a
  `_<GOOS>`/`_<GOARCH>` file-name suffix: the one host-configuration pass
  cannot see the configurations that compile it, and no gate here builds them.
- A file `gofmt -l` lists on `origin/core`: the gate needs a clean file.
- A name that any other line in the repository names: another file anywhere
  but the tidy cards, brine and docs included, in code, a string or a comment;
  or its own file outside the span, comments included.
- A span that contains an assertion call (`Expect(`, `Eventually(`,
  `Consistently(`, `Ω(`, any `…WithOffset(`, `Fail(`, `t.Error`, `t.Fatal`,
  `t.Fail`, `assert.`, `require.`) or calls, as `<name>(`, a function or method
  in the same directory whose body does, directly or through further such
  calls. REVIEW rule 4 counts assertions whether or not they ever ran.
- A production declaration born without a production caller (the scan's
  pickaxe finds one commit, or its newest commit removes no caller line), whose
  last caller left less than 30 days ago, or whose newest commit is a `tidy(`
  commit: an uncalled capability is as often an unwired box as cruft, even one
  only tests called.
- A `var`, `const` or `type` spec inside a parenthesized block; a `var` or
  `const` naming more than one identifier, spanning more than one line, or with
  a `(` after its `=` (an initializer call runs at package init).
- A range whose line above or below ends in a `//` comment after code, or a
  one-line range carrying its own trailing comment beside a non-blank line:
  gofmt would realign the surviving comments, an added line. A `/* */` comment
  above the declaration.
- A span containing a `//go:` directive. A type with a method in another file.
  A span over 60 lines.
- A `whole` deletion of the last non-test `.go` file in its directory (tier 1)
  or the last `.go` file (tiers 2 and 3), or of a file carrying a `// Package`
  doc comment, a `//go:` line or a blank import.
- A deletion that leaves an import unused whose path is neither imported by
  another `.go` file in the same directory with no build constraint (for a
  production file, a non-`_test.go` one) nor standard library outside
  `crypto/…`, `embed`, `expvar`, `image/…`, `net/http/httptest`,
  `net/http/pprof`, `runtime/…` and `time/tzdata`: dropping the package's last
  import of it can drop an `init` that registers a driver, hash, codec, handler
  or flag (why `GetPipelineCommand.showConfigWarning` stays).

## Gate

```bash
go build ./... && go vet ./...
GOOS=linux go vet ./<dir>/
git grep -nw '<name>' -- . ':!.claude/skills/tidy'    # prints nothing
gofmt -l <file>                          # prints nothing (skip for a deleted file)
"$SC" -checks U1000 ./<dir>/             # every line ends (U1000); none is this instance
grep -c 'Expect(\|Eventually(\|Consistently(' <file>   # same as on origin/core
```

`$SC` is the scan's staticcheck (`$(go env GOPATH)/bin/staticcheck` unless `SC`
is set). A declaration the rerun newly reports is the next instance, never part
of this diff. A failure with `no such file or directory` under `go-build/`, or
`cache entry not found`, is another session's `go clean -cache`, not this diff:
rerun with `GOCACHE=$(mktemp -d)` and delete that directory afterwards. Under
`atc/worker/jetbridge/`, also `go vet -tags live,hangar_live
./atc/worker/jetbridge/`. Under `fly/`, also `make test-fly-integration`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-10-03 | findJobByName@atc/configvalidate/validate.go | deleted | tier 1: `staticcheck -checks U1000 ./atc/configvalidate/` reported the func unused and the scan printed span 810-817 `part`; it ended the file, so the blank line 809 before it went too and no import was left unused (`go vet` clean); `git grep -nw findJobByName` now prints nothing; its last caller line `nextJob := findJobByName(nextJobName, pipelineConfig.Jobs)` was removed by 00dab50f5c (2026-01-07, "feat: allow glob patterning for get.passed"), 269 days old and not a tidy; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./atc/configvalidate/`, `gofmt -l` clean, U1000 rerun on the package empty, assertion count 0 before and after |
| 2026-10-03 | keepAliveDialer@atc/db/keepalive_dialer.go | deleted | tier 1, `whole`: `staticcheck -checks U1000 ./atc/db/` reported the type unused and the scan printed spans 8-9,11-17,19-26 (the type with its `Dial` and `DialTimeout` methods, both in this file), which leave only the package clause and imports, so the file went; it carries no `// Package` comment, `//go:` line or blank import, and atc/db keeps 129 other non-test files; its imports `net` and `time` are standard library outside the init-carrying list; `git grep -nw keepAliveDialer` now prints nothing; its last caller line `listener := pq.NewDialListener(keepAliveDialer{}, dsn, ...)` was removed by da1f79fc2a (2025-02-08, "switch listener from pq to pgx"), not a tidy; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./atc/db/`, U1000 rerun on the package lists only the three findings it had before (`defaultCheckTimeout`, `listener.cancelFunc`, `start`) |
| 2026-10-03 | Container.volumeForPath@atc/worker/jetbridge/container.go | deleted | tier 1: `staticcheck -checks U1000 ./atc/worker/jetbridge/` reported the method unused and the scan printed span 327-336 `part` (the method with its two-line doc comment); blank lines sat on both sides, so the one after it (337) went too, and no import was left unused (`go vet` clean); `git grep -nw volumeForPath` now prints nothing; its last caller line `vol := p.container.volumeForPath(input.DestinationPath)` was removed by 1b3972e893 (2026-03-27, "refactor(jetbridge): deprecate PVC and SPDY artifact backends"), not a tidy; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet` and `go vet -tags live,hangar_live` on the package, `gofmt -l` clean, U1000 rerun lists only the package's earlier findings, assertion count 0 before and after |
| 2026-10-03 | enginePostgresRunner@atc/engine/engine_suite_test.go | deleted | tier 2: `staticcheck -checks U1000 ./atc/engine/` reported the one-line, one-name `var enginePostgresRunner = &engine.EnginePostgresRunner` unused (no `(` after its `=`, not inside a block) and the scan printed span 51-51 `part`; the line below it is code, so no blank line went, and the `engine` import stays in use; `git grep -nw enginePostgresRunner` now prints nothing (the exported `engine.EnginePostgresRunner` is a different word and is left alone); gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./atc/engine/`, `gofmt -l` clean, U1000 rerun lists only `closedEngineCloneConn` (another instance), assertion count 1 before and after |
| 2026-10-03 | closedEngineCloneConn@atc/engine/engine_suite_test.go | deleted | tier 2: `staticcheck -checks U1000 ./atc/engine/` reported the one-line, one-name `var closedEngineCloneConn = engine.ClosedEngineCloneConn` unused (no `(` after its `=`, not inside a block) and the scan printed span 53-53 `part` (line 52 after the previous row's deletion); code sits on both sides, so no blank line went, and the `engine` import stays in use; `git grep -nw closedEngineCloneConn` now prints nothing (the exported `engine.ClosedEngineCloneConn` is a different word and is left alone); gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./atc/engine/`, `gofmt -l` clean, U1000 rerun on the package empty, assertion count 1 before and after |
| 2026-10-03 | failCreatedTransition@atc/worker/jetbridge/jetbridge_suite_test.go | deleted | tier 2: `staticcheck -checks U1000 ./atc/worker/jetbridge/` reported the type and its `CreateContainer` method (declared only in this file) unused and the scan printed spans 155-155,157-163 `part`; each range had blank lines on both sides, so the blank line after each (156, 164) went too; the span holds no assertion and calls no asserting function; the floating "decorators below" comment above stays because `failStaleCreatedTransition` still follows it; `creatingContainerCreatedFails`, which the method named, is left for its own instance; `git grep -nw failCreatedTransition` now prints nothing; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet` and `go vet -tags live,hangar_live` on the package, `gofmt -l` clean, U1000 rerun lists only the package's earlier findings, assertion count 12 before and after |
| 2026-10-03 | failStaleCreatedTransition@atc/worker/jetbridge/jetbridge_suite_test.go | deleted | tier 2: `staticcheck -checks U1000 ./atc/worker/jetbridge/` reported the type and its `FindContainer` method (declared only in this file) unused and the scan printed spans 165-165,167-173 `part` (155-163 after the previous row's deletion); each range had blank lines on both sides, so the blank line after each went too; the span holds no assertion and calls no asserting function; the floating "decorators below" comment above stays because `creatingContainerCreatedFails` still follows it; that type, which the method named and which no other line now names, is left for its own instance; `git grep -nw failStaleCreatedTransition` now prints nothing; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet` and `go vet -tags live,hangar_live` on the package, `gofmt -l` clean, U1000 rerun lists only the package's earlier findings, assertion count 12 before and after |
| 2026-10-03 | destructiveSite@cmd/artifact-daemon/architecture_destructive_test.go | deleted | tier 2: `staticcheck -checks U1000 ./cmd/artifact-daemon/` reported the struct type unused (the whole type goes, so no struct field is removed from a live type) and the scan printed span 214-217 `part`; it has no methods, blank lines sat on both sides, so the one after it (218) went too, and no import was left unused (`go vet` clean); `git grep -nw destructiveSite` now prints nothing; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./cmd/artifact-daemon/`, `gofmt -l` clean, U1000 rerun lists only the package's earlier `hostFromURL` and `existingOutsideDir` findings, assertion count 0 before and after |
| 2026-10-03 | hasExpressionOver@deploy/chart/tests/alert_metric_drift_test.go | deleted | tier 2: `staticcheck -checks U1000 ./deploy/chart/tests/` reported the func unused and the scan printed span 296-304 `part`; it ended the file, so the blank line 295 before it went too and the file still ends on a `}` line; `strings` stays in use elsewhere in the file (`go vet` clean); the span holds no assertion; `git grep -nw hasExpressionOver` now prints nothing; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./deploy/chart/tests/`, `gofmt -l` clean, U1000 rerun lists only `containersOf` (another instance), assertion count 0 before and after |
| 2026-10-03 | containersOf@deploy/chart/tests/hangar_output_reachability_test.go | deleted | tier 2: `staticcheck -checks U1000 ./deploy/chart/tests/` reported the func unused and the scan printed span 146-149 `part` (the func with its one-line doc comment); blank lines sat on both sides, so the one after it (150) went too; `go vet` then reported `"k8s.io/api/core/v1" imported as corev1 and not used`, so that one import spec went, and nine other unconstrained files in deploy/chart/tests still import the path, so no package init is dropped; the span holds no assertion; `git grep -nw containersOf` now prints nothing; taken in place of `GetPipelineCommand.showConfigWarning`, whose deletion would drop fly/commands' last non-test import of `github.com/mattn/go-isatty`; gates `go build ./...`, `go vet ./...`, `GOOS=linux go vet ./deploy/chart/tests/`, `gofmt -l` clean, U1000 rerun on the package empty, assertion count 0 before and after |
