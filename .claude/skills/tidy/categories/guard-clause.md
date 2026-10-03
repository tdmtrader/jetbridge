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
