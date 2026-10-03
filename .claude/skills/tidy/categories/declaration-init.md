# declaration-init

Bring one local declaration to its initialization (Beck, *Tidy First?* ch. 7).
When the first statement after `var x T` that names `x` is a plain assignment
of `x` in the same block, the two become `x := e`, or `var x T = e` where `:=`
would infer a different type; when that assignment cannot declare `x` (a field
or an outer variable shares its left side), the `var` line moves to directly
above it. `x`'s zero value is never read, `x` keeps its exact type and scope,
and nothing runs in a different order. The scan's tool edits; never by hand.

Merge: green
Instance key: `<var>[,<var>…]:<scope>@<file>`, the scan's second column
verbatim. `<scope>` is the enclosing `Recv.Method` or `Func`, or, inside a
function literal passed to a call whose first argument is a string literal,
that call (`It("…")`, `t.Run("…")`) with the string literal's whitespace collapsed, `|` written `/`
and a backquote `'`; a package-level closure outside any such call is `_`.

## Scan

`bash .claude/skills/tidy/categories/declaration-init.scan.sh` from the repo
root builds `declinit` in `/tmp/tidy-declinit`, type-checks the tree for the
host, `GOOS=linux -tags live,hangar_live` and `GOOS=windows`, and writes the
queue to `queue.tsv` there, in minutes. A nonzero exit (no tool build, a failed
load, a host or linux package that does not type-check) is a broken scan, never
an empty queue: rerun once, then log `skipped` with `scan failed:` and the last
stderr line as the note. `cannot type-check` lines for `GOOS=windows` alone do not fail it.

Only the scan decides eligibility; never hand-pick. Each line is one instance:
form, key, `decl=` the `var` lines, `at=` the target statement, `type=` each
declared type, `drop=` the imports the edit deletes; a line naming several
declarations shares one target and is taken whole or not at all. Pick in queue
order (form `join`, `join-multi`, `join-typed`, `move`; production before
`_test.go`; file, line). Take line `N`, record the baseline, let the tool edit:

```bash
D=/tmp/tidy-declinit
key=$(sed -n '<N>p' $D/queue.tsv | cut -f2); file=${key##*@}
$D/declinit -locals "$file" > $D/before.txt
$D/declinit -apply "$key"   # rescans, edits <file> in place, prints its line
```

## Predicate

- Each declaration is a one-line `var x T`: one name, a type, no initializer,
  not parenthesized, directly in a statement list (a block, or a `case` or
  `select` clause body) of a function or function literal.
- The target is the first later statement of that list naming `x` anywhere,
  closures included. It is `=` or `:=`, `x` is a bare name on its left, and
  nothing else in it, on either side, names `x`: nothing reads the zero value.
- `join`: every other name on the left is `_`, a name the target declares, or a
  variable of `x`'s block (in a function's outermost block, also a parameter or
  named result), so `:=` shadows nothing; `x := e` gives `x` exactly `type=`,
  identical and spelled the same (parameter names inside a function type aside; an untyped
  constant or comparison takes its default type). The `var` line goes and `=`
  becomes `:=` (`:=` stays). `join-multi`: the same, several names on the left.
- `join-typed`: `x` alone on the left, `:=` would give a different type (an
  interface `T`, an untyped constant, a named type from an unnamed value, an
  alias spelled differently), and the right side is not `nil`. The `var` line
  goes and `x = e` becomes `var x T = e`, `T` spelled as declared.
- `move`: neither join applies (a field, an index expression or an outer-block
  variable shares the left side, or `:=` would mistype one of several names).
  At least one non-declaration statement sits between, and the statement
  directly above the target writes no name the target reads. The `var` line
  moves unchanged to directly above the target, or above the target's own-line
  comment ending on the line above it; several `var` lines keep their order.
- `drop=`: an import whose only use in the file was the deleted type goes with
  it, and another file of the same package still imports that path.
- Every name in `T` resolves to the same declaration at the target as at the
  `var` line.
- No tracked file cites a line of `<file>` at or below the first line the edit
  touches, since those lines shift: `<path>.go:<N>`, `:<N>-<M>`, `#L<N>` or
  `#L<N>-L<M>` (the last line counts), where `<path>`, leading `./` and `../`
  dropped, is `<file>` or any suffix of it down to the bare basename.
- The diff is exactly what `-apply` writes, all in `<file>`: the `var` lines
  deleted (and, for `move`, reinserted), the target's `=` or `x =` rewritten,
  the `drop=` import lines deleted, and a blank line directly after deleted
  lines when the line before them is blank or ends in `{`, `(` or `:`. Then
  gofmt, which changes nothing else in a gofmt-clean file.

## Exclude

- `atc/worker/jetbridge/brine/` (own module), `atc/db/migration/migrations/`,
  `*fakes/`, `vendor/`, `node_modules/`, generated files.
- `topgun/`, `testflight/`, `integration/`, `testhelpers/otel/`: no local tier
  runs them.
- A file the host build does not compile (`//go:build live`, `hangar_live`,
  linux- or windows-only): `go vet ./...` would never compile the edit. A
  declaration listed differently by a linux or windows build of its file.
- A file that is not gofmt-clean: the edit's gofmt would rewrite other lines.
- A function or function literal containing `goto`: a backward jump across a
  `:=` makes a fresh variable on every pass.
- `var (...)` groups, `var a, b T`, initializers, multi-line declarations.
- A comment on any line of the run of consecutive `var` lines holding the
  declaration, or an own-line comment ending directly above that run: it
  documents the run and has nowhere to go.
- A target that is not itself an assignment (an `if`, `for`, `switch`,
  `select`, block or call, or a first mention inside a function literal such as
  `BeforeEach(func() { x = … })` or `defer func() { … }()`): it assigns `x` on
  some paths and reads the zero value on the others.
- A target that reads `x` (`x = append(x, v)`, `m[x], x = …`), and `x = nil`,
  which reassigns the zero value.
- A `move` that would land between a statement and the assignment reading what
  it wrote (`s := load()` / `h.v, x = len(s), 2`), a `move` past nothing but
  other declarations, and any move into a nested block, loop body, `case` or
  closure: a declaration only moves within its own statement list.
- A deleted type whose import no other file of the package keeps (that
  package's `init` could leave the binary), whose import line carries a
  comment, or that is dot-imported.
- A file cited by line number at or below the edit.
- One target whose declarations take different forms, and a key two instances
  would share (the scan lists neither).

## Gate

```bash
D=/tmp/tidy-declinit
go build ./... && go vet ./...
test "$(git diff --name-only)" = "<file>"           # the edit touched one file
$D/declinit -locals <file> | diff $D/before.txt -   # prints nothing
gofmt -l <file>                                     # prints nothing
```

All before the Log line goes in, plus `make test-fly-integration` when `<file>` is under `fly/`.
`-locals` lists every variable `<file>` declares (top-level declaration, name,
type, enclosing scopes; blank identifiers left out) and must be byte-identical:
an interface made concrete by `:=` (`go vet` passes it), a shadowed outer `err`,
or a declaration slid into a loop body each changes a line.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-10-03 | idle:Server.hijack@atc/api/containerserver/hijack.go | joined | form `join` (queue line 1 of 85 at this branch's tip): `var idle InterceptTimeout` is a one-line, uncommented declaration in the outermost block of `Server.hijack`, and the first later statement naming `idle`, including inside the three goroutine closures between them, is the plain `idle = s.interceptTimeoutFactory.NewInterceptTimeout()`, whose right side does not read `idle` and whose result type is the declared `InterceptTimeout` interface itself, so `idle := ...` keeps the exact type and scope; the function has no `goto`, the file is gofmt-clean, has no build tag and no tracked file cites its lines; the edit is exactly what `declinit -apply` wrote (the `var` line went, `=` became `:=`); gates `go build ./...`, `go vet ./...`, one file in `git diff --name-only`, `declinit -locals` byte-identical before and after, `gofmt -l` clean |
| 2026-10-03 | migrationContents:Parser.ParseFileToMigration@atc/db/migration/parser.go | joined | form `join` (first queue line after the previous pick, rescanned at this branch's tip): `var migrationContents string` opens the outermost block of `Parser.ParseFileToMigration` with no comment on or above it, nothing names it until the plain `migrationContents = string(migrationBytes)`, whose right side does not read it and whose conversion yields exactly `string`, so `migrationContents := ...` keeps the type and scope and its zero value was never read; the `var` line and the blank line after it went (the line above ends in `{`), as `declinit -apply` wrote; no `goto`, gofmt-clean, no build tag, no tracked file cites its lines; gates `go build ./...`, `go vet ./...`, one file in `git diff --name-only`, `declinit -locals` byte-identical before and after, `gofmt -l` clean |
