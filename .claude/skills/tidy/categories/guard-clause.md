# guard-clause

Turn one trailing `if` that wraps the rest of a loop body, or the rest of a
function with no results, into a guard clause (Beck, _Tidy First?_ ch. 1):
negate its condition by a fixed table, leave early with `continue` or `return`,
and un-indent the old body one level. Nothing is reordered (REVIEW.md rule 3):
the `if` ends its block, so the exit lands where the body used to fall through,
every path runs the same statements in the same order, and the condition's
operands keep their order and short-circuiting. No import, signature or
identifier changes, so the tidy cannot cross a bounded context or break the
brine module's view of the root.

Merge: review
Instance key: `<form>:<function>@<file>:<condition>`: form `continue` or
`return`; function `Func` or `Recv.Method` of the enclosing top-level
declaration (also for a loop inside a closure); condition verbatim as it reads
before the tidy. It is exactly the scan's first column.

## Scan

```bash
export T=$(mktemp -d)   # keeps $T/guardscan.go for -apply and the Gate
bash .claude/skills/tidy/categories/guard-clause.scan.sh
```

Run from the repo root: a stdlib-only go/ast program (Go 1.21+, nothing to
install) that enforces every Predicate and Exclude line. It writes the whole
queue to `$T/queue.tsv` (production files, then `_test.go`, each by key) and
prints the first row whose key is not in this card's log. Columns: key,
`<file>:<if line>-<closing brace line>`, body lines, `if <guard> { <exit> }`.
A file it cannot read or parse fails the scan (`SCAN FAILED`), never skipped.

Apply with `go run "$T/guardscan.go" -apply '<key>'`, which rewrites that one
`if` and nothing else and exits non-zero when the key is not in the queue.
Touch the file by hand afterwards only to revert.

## Predicate

- Shape: the `if` is the last statement of a `for`/`range` body (`continue`),
  or of the body of a top-level `func` declaration with no result list
  (`return`). It has no `else` and no init statement.
- Layout: the `if` line is exactly `<indent>if <condition> {`, the whole
  condition on that line, and its closing line is exactly `<indent>}`.
- Body: 4 to 20 lines between the braces (blank and comment lines count), at
  least two top-level statements, and its last statement does not already
  leave: `return`, `continue`, `break`, `goto`, or a call to `panic`, to a
  function or method named `Exit` or `Goexit`, or to one whose name begins with
  `Fatal`, `Panic`, `Fail` or `Skip` (`os.Exit`, `runtime.Goexit`,
  `log.Fatalf`, `log.Panicf`, `t.FailNow`, `t.Skip`, Ginkgo `Fail` and
  `Skip`). An `if` that ends by leaving is a guard already.
- Negation by this table only: `!x` → `x` when `x` is not parenthesized; an
  identifier, selector, call or index `x` → `!x`; `a == b` ↔ `a != b`; `<` ↔
  `>=` and `>` ↔ `<=` only when an operand is a `len(…)` or `cap(…)` call, with
  `len(x) > 0` → `len(x) == 0`; a left-nested `a && b && …` chain of table
  operands → each operand negated, joined by `||` in the same order, so
  short-circuit evaluation is unchanged. An operand that is itself a
  comparison, `&&` or `||` is outside the table.
- Scope: no name declared at the top level of the body (`:=`, `var`, `const`,
  `type`) is already declared at the top level of the block it moves into
  before the `if`, nor, for `return`, as a receiver, parameter or type
  parameter: moving `v, err := …` next to an existing `v` would turn a
  declaration into an assignment.
- The diff: the guard line, the exit line and a `}` replace the `if` line, the
  old closing brace goes, and every body line loses exactly one leading tab.
- One instance is one `if`. When the un-indented body itself ends in an
  eligible `if`, that is another day's instance under its own key.

## Exclude

- Failure paths, whose body is not the main path; the guard would turn the
  idiom inside out (`if err == nil || errors.Is(err, ErrX) { continue }`):
  - a condition naming, anywhere in it, an identifier that begins or ends with
    `err` or ends with `error`, case-insensitively (`err`, `getErr`,
    `ErrNotFound`, `errors.Is`, `x.Err`, `stderr`);
  - `!ok` as the whole condition or as an operand of its `&&` chain;
  - a body with a top-level call to a function or method named `Error`,
    `Errorf`, `Errorw`, `Errorln` or `Fail` (`t.Errorf`, `logger.Error`).
- A comment between the `if` and the statement before it (or its block's
  opening brace), including a trailing comment on that statement's last line:
  it may describe the condition the tidy inverts, and a hand fix is forbidden.
  Also a comment after the `if`'s closing brace in the same block.
- The `return` form in a function that contains `defer` anywhere, and every
  function with a result list, named or not: a value-returning
  `if c { …; return x }; return y` is not this card (such sites were error
  exits already in guard position, and inverting one would undo a guard).
- Any function containing a label or `goto`.
- A trailing `if` in a function literal's own body (its key would not be
  addressable). A loop inside a closure is eligible.
- A body holding a multi-line raw string: removing a tab changes the string's
  value, and `git diff -w` hides it.
- A condition containing `|`, `'` or a backtick (the key lives in a Markdown
  table cell and a single-quoted shell argument), and any key the scan would
  print twice (two sites, one key: the scan drops both).
- `atc/worker/jetbridge/brine/` and `hack/mcp-oauth-probe/` (their own
  modules), `atc/db/migration/migrations/`, `*fakes/`, `vendor/`,
  `node_modules/`, `topgun/`, the directories `go build` ignores (`testdata`,
  names starting with `.` or `_`, which keeps nested worktrees out), generated
  files, and any file outside the default build on linux/amd64 or darwin/arm64
  (a `//go:build` line or a GOOS/GOARCH file suffix): the local gate builds
  darwin files, CI linux ones, and neither `live`-tagged ones.

## Gate

Each line exits 0 when it passes, under `set -e` or chained with `&&` alike.
`<file>` is the path in the scan's second column; `$T` is the Scan's directory
(in a new shell, run the Scan block again first).

```bash
test -z "$(gofmt -l <file>)"
test "$(git diff -w --numstat origin/core -- <file> | cut -f1,2)" = "$(printf '3\t2')"
test "$(git diff --name-only origin/core | grep -vxF .claude/skills/tidy/categories/guard-clause.md)" = '<file>'
go run "$T/guardscan.go" > "$T/after.tsv"
test "$(cut -f1 "$T/after.tsv" | grep -cxF '<key>')" = 0
go build ./... && go vet ./...
```

Plus `make test-fly-integration` when the file is under `fly/`. For a
`_test.go` instance, the count from
`grep -c 'Expect(\|Eventually(\|Consistently(\|t\.Error\|t\.Fatal' <file>` is
the same before and after.

Review with `git diff -w origin/core...BRANCH` and read the whole enclosing
function in `git show BRANCH:<file>`: the Scope, `defer`, label and comment
lines depend on code outside the diff. Under `-w` the un-indented body shows as
unchanged context and every `}` compares equal, so the deleted closing brace
can show as a later `}`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-10-03 | continue:CheckForInputType@fly/commands/internal/executehelpers/inputs.go:i.Path != "" | rewritten | the `if` is the last statement of the `range inputMaps` body, has no `else` or init, and wraps 8 lines (the `os.Stat` call, its error return and the regular-file `switch`), the last of which does not leave; `i.Path != ""` negates by the table to `i.Path == ""` and names no `err`; the moved `fi, err :=` meet no earlier declaration in the loop body; no comment near the `if`, no label or `goto`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`, `make test-fly-integration` (601 passed) |
| 2026-10-03 | continue:HangarBootstrapCommand.databaseStep@cmd/concourse/hangar_bootstrap.go:entry.Kind == bootstrap.KindDatabaseCredential | rewritten | the `if` is the last statement of the `range inventory.Entries` body, has no `else` or init, and wraps 4 lines holding two statements (the more-than-one-credential refusal and `secret = entry.Name`), the last an assignment that does not leave; `==` flips to `!=` by the table and the condition names no `err` or `ok`; the body declares nothing; no comment near the `if`, no label or `goto` in `databaseStep`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./cmd/concourse` runs at the chain gate |
| 2026-10-03 | continue:Monitor.Initialize@atc/metric/emit.go:factory.IsConfigured() | rewritten | the `if` is the last statement of the second `range m.emitterFactories` body, has no `else` or init, and wraps 4 lines holding two statements (the `NewEmitter` assignment and its error check), the last an `if` that does not itself leave; the call negates by the table to `!factory.IsConfigured()` and names no `err`; the body only assigns the outer `emitter, err`, declaring nothing; no comment near the `if`, no label or `goto`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/metric` runs at the chain gate |
| 2026-10-03 | continue:Server.CreateJobBuild@atc/api/jobserver/create_build.go:found | rewritten | the `if` is the last statement of the `range inputs` body (a loop inside the handler closure, which the card allows), has no `else` or init, and wraps 13 lines holding three statements (the pinned version, the `TryCreateCheck` call and its logged error), the last an `if` that does not leave; `found` negates by the table to `!found`, and no `!ok` or `err` is in the condition; the moved `version` and `_, _, err :=` meet only `resource, found` earlier in the loop body, so `err` stays a fresh declaration as it was in the `if` block; the `logger.Error` call is nested, not top-level; no comment near the `if`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/api` runs at the chain gate |
| 2026-10-03 | continue:TaskStep.registerOutputs@atc/exec/task_step.go:filepath.Clean(mount.MountPath) == filepath.Clean(outputPath) | rewritten | the `if` is the last statement of the inner `range volumeMounts` body, has no `else` or init, and wraps 7 lines (a 5-line comment, then `ArtifactFromVolume` and `RegisterArtifact`), the last a call that does not leave; `==` flips to `!=` with both `filepath.Clean` operands in their order, and the condition names no `err`; the moved `artifact :=` meets no earlier declaration in the loop body; the comment is inside the body and moves with it, none sits before the `if` or after its brace; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/exec` runs at the chain gate |
| 2026-10-03 | continue:TaskStep.run@atc/exec/task_step.go:sc.ImageArtifact != "" | rewritten | the `if` is the last statement of the `range containerSpec.Sidecars` body, has no `else` or init, and wraps 6 lines holding four statements (the `ImageRefFor` lookup, its not-found refusal and the two sidecar field writes), the last an assignment that does not leave; `!=` flips to `==` by the table and the condition names no `err` or `ok`; the moved `imageRef, found :=` meet no earlier declaration in the loop body; the comment above the `for` and the trailing `// resolved` inside the body are not between the `if` and its block's brace nor after its closing brace; no label or `goto` in `run`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/exec` runs at the chain gate |
| 2026-10-03 | continue:insertJobInput@atc/db/team.go:matched | rewritten | the `if` is the last statement of the inner `range jobNameToID` body, has no `else` or init, and wraps 18 lines holding four statements (`var version`, the version-JSON block, the `job_inputs` insert and its error check), the last an `if` that does not leave; `matched` negates by the table to `!matched` and names no `err` or `ok`; the moved `version` and `_, err :=` meet only `matched` earlier in the loop body; the line before the `if` is blank, not a comment, and nothing follows its brace; no label or `goto` in `insertJobInput`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/db` runs at the chain gate |
| 2026-10-03 | continue:newLocalUsers@skymarshal/dexserver/dexserver.go:username != "" && password != "" | rewritten | the `if` is the last statement of the `range config.Users` body, has no `else` or init, and wraps 17 lines holding three statements (`var hashed`, the bcrypt cost/hash `if`-`else`, `users[username] = hashed`), the last an assignment that does not leave; the two-operand `&&` chain negates operand by operand into an OR of `username == ""` then `password == ""`, same order and short-circuit, naming no `err`; the moved `var hashed` meets no earlier declaration in the loop body; the nested `continue` still targets the same loop, and `config.Logger.Error` is nested, not top-level; no comment near the `if`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./skymarshal/dexserver` runs at the chain gate |
| 2026-10-03 | continue:pipelineRunFactory.ClaimRunCancellationOperation@atc/db/pipeline_run_cancel_queue.go:cycle == 0 | rewritten | the `if` is the last statement of the two-cycle `for cycle` body, has no `else` or init, and wraps 7 lines holding three statements (`after = 0`, the high-water read and the cursor reset), the last an `if` that does not leave; `==` flips to `!=` by the table and `cycle` names no `err`; the body declares nothing at its top level (each `err` is in an `if` init); its raw-string SQL is single-line, so the un-indent changes no string; no comment before the `if` or after its brace; no label or `goto`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/db` runs at the chain gate |
| 2026-10-03 | continue:pipelineRunFactory.PendingRunCancellations@atc/db/pipeline_run_cancel_queue.go:cycle == 0 | rewritten | the `if` is the last statement of the two-cycle `for cycle` body, has no `else` or init, and wraps 7 lines holding three statements (`after = 0`, the Run high-water read and the worker-row reset), the last an `if` that does not leave; `==` flips to `!=` by the table and `cycle` names no `err`; the body declares nothing at its top level (each `err` is in an `if` init), so the earlier `rows, err` and `ids` in the loop body are untouched; its raw-string SQL is single-line (the multi-line `runNeedsCancellationWork` is a package const outside the body); no comment before the `if` or after its brace; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `ginkgo ./atc/db` runs at the chain gate |
| 2026-10-03 | continue:interpolator.Interpolate@vars/template.go:found | rewritten | the `if` is the last statement of the `range i.extractVarNames(typedNode)` body, has no `else` or init, and wraps 15 lines holding two statements (the anchored-regex whole-field return and the `foundVal` type `switch`), the last a `switch` that does not leave; `found` negates by the table to `!found` and names no `err` or `ok`; the body declares nothing at its top level (`foundValStr` is inside a `case`), so the earlier `reference`, `foundVal, found, err` in the loop body are untouched; the line before the `if` is blank and nothing follows its brace; no label or `goto` in `Interpolate`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; `go test ./vars` runs at the chain gate |
| 2026-10-03 | continue:podEventTracker.emitPodLifecycleEvents@atc/worker/jetbridge/process.go:cs.State.Running != nil && !t.startedSidecars[cs.Name] | rewritten | the `if` is the last statement of the sidecar `range pod.Status.ContainerStatuses` body, has no `else` or init, and wraps 4 lines holding two statements (`t.startedSidecars[cs.Name] = true` and the `sidecar.started` `span.AddEvent`), the last a call that does not leave; the two-operand `&&` chain negates operand by operand into an OR of `cs.State.Running == nil` then `t.startedSidecars[cs.Name]`, same order and short-circuit, naming no `err` or `ok`; the body declares nothing; the statement before the `if` is the main-container `continue` guard with no comment between, and nothing follows its brace; the `defer`s in process.go are in other functions and only bar the `return` form; no label or `goto`; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; the `atc/worker/jetbridge` suite runs at the chain gate |
| 2026-10-03 | continue:podEventTracker.emitPodLifecycleEvents@atc/worker/jetbridge/process.go:cs.State.Terminated != nil && !t.completedInits[cs.Name] | rewritten | the `if` is the last (and only) statement of the `range pod.Status.InitContainerStatuses` body, has no `else` or init, and wraps 18 lines holding two statements (`t.completedInits[cs.Name] = true` and the exit-code `if`-`else` choosing `init.container.completed` or `init.container.failed`), the last an `if` that does not leave; the two-operand `&&` chain negates operand by operand into an OR of `cs.State.Terminated == nil` then `t.completedInits[cs.Name]`, same order and short-circuit, naming no `err` or `ok`; the body declares nothing and makes no top-level `Error` call; nothing sits between the loop's brace and the `if` or after its brace; no `defer`, label or `goto` in the function; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; the `atc/worker/jetbridge` suite runs at the chain gate |
| 2026-10-03 | continue:podEventTracker.emitPodLifecycleEvents@atc/worker/jetbridge/process.go:cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" && !t.pullingImages[cs.Name] | rewritten | the `if` is the last (and only) statement of the image-pulling `range pod.Status.ContainerStatuses` body, has no `else` or init, and wraps 7 lines holding two statements (`t.pullingImages[cs.Name] = true` and the `image.pulling` `span.AddEvent`), the last a call that does not leave; the left-nested three-operand `&&` chain negates operand by operand into an OR of `cs.State.Waiting == nil`, `cs.State.Waiting.Reason != "ContainerCreating"`, `t.pullingImages[cs.Name]`, same order, so the nil check still short-circuits before the `.Reason` read; it names no `err` or `ok`; the body declares nothing; nothing sits between the loop's brace and the `if` or after its brace; no `defer`, label or `goto` in the function; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; the `atc/worker/jetbridge` suite runs at the chain gate |
| 2026-10-03 | return:ATCConfig.showPipelineUpdateResult@fly/commands/internal/setpipelinehelpers/atc_config.go:pipeline.Paused | rewritten | the `if` is the last statement of `showPipelineUpdateResult`, which has no result list, no `defer`, label or `goto`; it has no `else` or init and wraps 5 lines holding five `fmt.Println` statements (the paused notice and unpause hints), the last a call that does not leave; `pipeline.Paused` negates by the table to `!pipeline.Paused` and names no `err` or `ok`; the body declares nothing, so no receiver or parameter is shadowed; the line before the `if` is blank after the updated/created `if`-`else` chain, and nothing follows its brace; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`, `make test-fly-integration` (601 passed) |
| 2026-10-03 | return:Worker.bindStartCheck@atc/worker/jetbridge/execution_preparer.go:w.executionPreparer != nil | rewritten | the `if` is the last (and only) statement of `Worker.bindStartCheck`, which has no result list, no `defer`, label or `goto`; it has no `else` or init and wraps 4 lines holding two statements (the `c.checkStart` and `c.recordWitness` closure assignments), the last an assignment that does not leave (the `return`s are inside the closures and stay there); `!=` flips to `==` by the table and `w.executionPreparer` names no `err` or `ok`; the body declares nothing, so receiver `w` and parameters `c`, `owner`, `spec` are untouched; nothing sits between the function's brace and the `if` or after its brace; gates: gofmt, `git diff -w` 3/2, guardscan re-scan drops the key, `go build ./... && go vet ./...`; the `atc/worker/jetbridge` suite runs at the chain gate |
