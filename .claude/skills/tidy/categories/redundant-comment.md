# redundant-comment

Delete one own-line `//` comment inside a function body that says only what
the code line directly below it already says (Beck, _Tidy First?_ ch. 15):
every word of the comment, articles and the like aside, is a word of that
line's identifiers, and the comment addresses one statement, not a chunk. The
diff deletes the comment line and nothing else, so there is no behavior to
preserve: no code line, string, import or signature changes, gofmt realigns
nothing (trailing-comment neighbours are excluded), and the file compiles in
every configuration it compiled in before. The same comment text in the same
function of the same file is one instance, every site of it.

Merge: review
Instance key: `<function>@<file>:<comment>`: function `Func` or `Recv.Method`
of the enclosing top-level declaration (also for a comment inside a closure in
it), or `_` for a function literal in a package-level `var` (a Ginkgo
`Describe`/`It` body); comment verbatim as it reads after `// `. It is exactly
the scan's first column.

## Scan

```bash
export T=$(mktemp -d)   # keeps $T/rcscan.go for the Gate
bash .claude/skills/tidy/categories/redundant-comment.scan.sh
```

A stdlib-only go/ast program (Go 1.21+, nothing to install, under three
seconds) that enforces every Predicate and Exclude line but the gofmt one,
which it applies by `go/format`. It writes the whole queue to `$T/queue.tsv`
(production files, then `_test.go`, then `topgun/`, each by file and line) and
prints the first row whose key is not in this card's log. Columns: key,
`<file>:<line>[,<line>…]` (every site of the key, so an instance may list
several lines), the comment, the code line below the first site. A file it
cannot read or parse fails the scan (`SCAN FAILED`), never skipped.

Apply by hand: delete each listed line whole. Nothing else moves.

## Predicate

- Place: the comment is a one-line `//` comment on a line of its own, the line
  above it is not a `//` comment (`go/parser` would otherwise make them one
  group), it begins `// ` with the space, and it sits inside the body of a
  function or function literal directly above a statement of a statement list:
  the statement's first line is the next line. The statement is not a `case`,
  `default` or `select` clause and not a label.
- Words: a word is a maximal run of letters or of digits, lowercased, after
  splitting camel case (`ErrNotFound` → err, not, found; `HTTPServer` → http,
  server; `sha256` → sha, 256). The comment's words, with `a an the and or of
  to in on for with by from as at is are be it its this that we our now then
  here into` removed, are at least one, and each is a word of the statement's
  first line with its string and rune literals blanked. Exact equality: no
  stems, no plurals (`validate` does not match `Validating`, `conn` does not
  match `connection`).
- Shape: the comment is letters, digits, spaces, commas, apostrophes and
  hyphens, starts with a letter, ends with a letter or digit and optionally a
  period; its first word is none of `nolint`, `lint`, `go`, `line`, `export`,
  `extern`, `todo`, `fixme`, `note`, `xxx`, `hack`, `bug`, `deprecated`.
- One statement: the line after the statement's last line is blank, a
  comment, a line beginning `}`, a `case` or `default` line, or the end of the
  file. When the statement assigns `err` and the next statement starts on the
  line after it as `if err != nil {` or `Expect(err)`, that check belongs to
  the statement and the rule reads the line after the check instead.
- One instance is one key: every site in the file whose function, file and
  comment text match. The scan lists them in one row; the diff takes them all.
- The diff: each site's comment line deleted whole and nothing else; `git diff
  --numstat origin/core -- <file>` shows 0 added and as many deleted as the
  key has sites.

## Exclude

- `atc/worker/jetbridge/brine/` and `hack/mcp-oauth-probe/` (their own
  modules), `atc/db/migration/migrations/`, `*fakes/`, `vendor/`,
  `node_modules/`, the directories `go build` ignores (`testdata`, names
  starting with `.` or `_`, which keeps nested worktrees out), and generated
  files (`// Code generated … DO NOT EDIT.`).
- A file `gofmt -l` lists on `origin/core`: the gate needs a clean file.
- A word found on the line only inside a string, rune or raw-string literal:
  the comment may be describing the data, not restating the code (`// minor
  version` above `Describe("… differ by a minor version", …)` stays; so does
  `// A valid handle with output dir.` above `os.MkdirAll(filepath.Join(stepsDir,
  "valid-handle", "output"), 0755)`).
- A comment heading more than one statement: a chunk label, not a restatement
  (`// Teams` above a run of `mustExec` inserts, `// PUT` above the lines that
  build a request, `// Generate CA.` above a key, a template and a
  certificate). The One-statement rule.
- A trailing `//` comment on the line above the comment or on the statement's
  first line: gofmt would realign the survivors, a changed line.
- Doc comments of declarations, field comments in a struct type, comments in a
  composite literal, and comments above a `case`, `default` or `select`
  clause: outside statement lists or above a label, so the scan never prints
  them and the card never takes them, however much they restate.
- A comment containing `|` or a backtick (the key lives in a Markdown table
  cell), a `:` (a label introducing what follows), a URL, or decoration
  (`---- hold ----`): the Shape rule refuses them all.
- A multi-line comment (two or more `//` lines without a blank between): it
  may explain, and the predicate reads one line.

## Gate

Each line exits 0 when it passes, under `set -e` or chained with `&&` alike.
`<file>` is the path in the scan's second column, `<n>` its number of sites,
`<dir>` its directory; `$T` is the Scan's directory (in a new shell, run the
Scan block again first).

```bash
test -z "$(gofmt -l <file>)"
test "$(git diff --numstat origin/core -- <file> | cut -f1,2)" = "$(printf '0\t<n>')"
test "$(git diff --name-only origin/core | grep -vxF .claude/skills/tidy/categories/redundant-comment.md)" = '<file>'
go run "$T/rcscan.go" > "$T/after.tsv"
test "$(cut -f1 "$T/after.tsv" | grep -cxF '<key>')" = 0
go build ./... && go vet ./...
go test ./<dir>/
```

Plus `make test-fly-integration` when the file is under `fly/`. For a
`_test.go` instance, the count from
`grep -c 'Expect(\|Eventually(\|Consistently(\|t\.Error\|t\.Fatal' <file>` is
the same before and after (a comment line never holds one, so a changed count
means something else moved).

Review with `git diff origin/core...BRANCH`: every hunk is one deleted `//`
line with the restated statement as its trailing context; read the words
against the Words rule. The One-statement and trailing-comment rules depend on
the lines around the hunk, which the diff's three lines of context show.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-10-17 | TaskStep.run@atc/exec/task_step.go:apply resource type defaults | deleted | tier 1, one site (line 211): words apply, resource, type, defaults are all words of `taskConfigSource = BaseResourceTypeDefaultsApplySource{`; the statement ends at its closing brace with a blank line after it, and no trailing comment sits above or on it; the sibling `// interpolate template vars` stays (vars is not on its first line); gates: gofmt clean, numstat 0/1, only this file, rescan drops the key (5 rows left), `go build ./... && go vet ./...`, `ginkgo ./atc/exec/` 625/625 passed |
| 2026-10-17 | TaskStep.run@atc/exec/task_step.go:override limits and requests | deleted | tier 1, one site (line 217 on origin/core, 216 after the previous row): words override, limits, requests are all words of `taskConfigSource = &OverrideContainerLimitsSource{ConfigSource: taskConfigSource, Limits: step.plan.Limits, Requests: step.plan.Requests}` (and is a stop word); the one-line statement has a blank line after it, and no trailing comment sits above or on it; gates: gofmt clean, numstat 0/1, only this file, rescan drops the key (4 rows left), `go build ./... && go vet ./...`, `ginkgo ./atc/exec/` 625/625 passed |
| 2026-10-17 | TaskStep.run@atc/exec/task_step.go:override params | deleted | tier 1, one site (line 220 on origin/core, 218 after the previous rows): words override, params are both words of `taskConfigSource = &OverrideParamsConfigSource{ConfigSource: taskConfigSource, Params: step.plan.Params}`; the one-line statement has a blank line after it, and no trailing comment sits above or on it; gates: gofmt clean, numstat 0/1, only this file, rescan drops the key (3 rows left), `go build ./... && go vet ./...`, `ginkgo ./atc/exec/` 625/625 passed |
| 2026-10-17 | registryResolver.Resolve@atc/imageresolver/resolver.go:Parse the reference. | deleted | tier 1, one site (line 66): words parse, reference are both words of `imageRef, err := name.ParseReference(ref)` (the is a stop word); the statement assigns `err` and its `if err != nil {` check follows on the next line, and the line after the check is blank; no trailing comment sits above or on it; gates: gofmt clean, numstat 0/1, only this file, rescan drops the key (2 rows left), `go build ./... && go vet ./...`, `go test ./atc/imageresolver/` ok |
| 2026-10-17 | scanner.resolveResource@atc/lidar/scanner.go:Find or create resource config. | deleted | tier 1, one site (line 386): words find, create, resource, config are all words of `resourceConfig, err := s.resourceConfigFactory.FindOrCreateResourceConfig(` (or is a stop word); the call spans four lines, assigns `err`, and its `if err != nil {` check follows on the next line, with a blank line after the check; the trailing `// no parent resource cache` comment sits inside the call, not above it or on its first line, so nothing realigns; gates: gofmt clean, numstat 0/1, only this file, rescan drops the key (1 row left), `go build ./... && go vet ./...`, `go test ./atc/lidar/` ok |
