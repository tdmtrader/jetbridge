# simplify

Apply one staticcheck simplification at one site: every hit of one admitted
S1xxx code inside one function (or one such hit outside any function), or one
ST1019 duplicate import folded into its twin. Each admitted code is taken only in
a form the Predicate names and rewritten exactly as named, so the new code runs
the same calls in the same order and computes the same values; every other S1
code is on `Exclude` with the way its rewrite can change behavior.

Merge: green (S1000, S1017, S1028, S1037, ST1019, and S1008 when its `if` returns `false`: review)
Instance key: `<code>:<site>@<file>`, the scan's first column verbatim

The site is `Recv.Method` or `Func` for a hit inside a top-level function,
`L<line>` (its line at the scanned commit) for one outside, such as inside
`var _ = Describe(...)`, and the import path for ST1019. An `L<line>` key drifts
as the file changes, so it counts as logged when a row has the same code and file
and its note quotes the same flagged line; every note quotes it in backticks.

## Scan

`bash .claude/skills/tidy/categories/simplify.scan.sh` from the repo root
installs staticcheck 2026.2.1 into `/tmp/tidy-simplify` (never `go run` it), runs
the admitted codes over the four builds that reach every constrained file,
merged so S1002, S1019, S1031 and S1039 count only when every build compiling the
file reports them, writes the gate helper `/tmp/tidy-simplify/g.sh`, and prints
`key<TAB>file:line<TAB>message` per hit: green tier, then review, by file:line.

- A scan that cannot install staticcheck or load a build exits non-zero with
  nothing on stdout: never log `queue empty` from it.
- The instance is every line carrying the first key neither logged nor excluded.
- Before editing, confirm in the file that the key's function encloses the hit's
  line (a raw string holding a column-0 `func ` or `}` misleads the scan); if it
  does not, re-key by hand and say so in the note.

## Predicate

The hit is one of these forms and the diff is exactly its rewrite. `c`, `x`,
`s` are source text kept verbatim. `not(c)` is `!c` for an identifier, selector,
call or index expression, `d` when `c` is `!d`, else `!(c)`; never staticcheck's
flipped comparison (`!(a < b)` and `a >= b` differ when either side is NaN).

| code | form | rewrite |
|---|---|---|
| S1000 | a `select` with one `case`, a send or receive, and no `default` | that send or receive, then the case body |
| S1002 | `c == true`, `c != false`; `c == false`, `c != true`, with the literal `true`/`false` on either side | `c`; `not(c)` |
| S1003 | `strings`/`bytes` `.Index`/`.IndexAny`/`.IndexRune` compared with `-1` or `0` | `.Contains`/`.ContainsAny`/`.ContainsRune` on the same arguments, or its `!`, as the message names |
| S1004 | `bytes.Compare(a, b) == 0`; `!= 0` | `bytes.Equal(a, b)`; `!bytes.Equal(a, b)` |
| S1005 | a `_` in a range clause (`for k, _ :=`; `for _ = range`, `for _, _ = range`) or a receive (`v, _ = <-ch`; `_ = <-ch`) | the `_` dropped with its comma, `:=` or `=` kept: `for k :=`; `for range`; `v = <-ch`; `<-ch` |
| S1006, S1010 | `for true {`; `s[a:len(s)]` | `for {`; `s[a:]` |
| S1008 | `if c { return true }` then `return false`; `if c { return false }` then `return true` | `return c`; `return not(c)` |
| S1009 | the `x != nil &&`, or the `x == nil \|\|`, before `len(x)` | deleted; the `len` comparison kept verbatim |
| S1012, S1024 | `time.Now().Sub(x)`; `x.Sub(time.Now())` | `time.Since(x)`; `time.Until(x)` |
| S1017 | `if strings.HasPrefix(s, p) { s = … }`, likewise `HasSuffix`, `Contains`, `bytes.HasPrefix`/`HasSuffix`; `s` a local variable of the enclosing function that no func literal in it names | the body's assignment alone, unconditional: `s = strings.TrimPrefix(s, p)`; `TrimSuffix`; the body's `strings.Replace` with its arguments verbatim; `bytes.TrimPrefix`/`TrimSuffix` |
| S1019 | `make(chan T, 0)`; `make(T, n, n)` | `make(chan T)`; `make(T, n)` |
| S1020 | `ok && x != nil` after `_, ok := x.(T)`; the `if x != nil {` around `if _, ok := x.(T); ok {…}` | `x != nil` deleted; that `if` deleted and its body dedented |
| S1023 | a bare `return` ending a function with no results; an unlabeled `break` ending a `case` body | deleted |
| S1028 | `errors.New(fmt.Sprintf(f, …))`, `f` a string literal with no `%w` | `fmt.Errorf(f, …)` |
| S1029 | `for _, r := range []rune(s)` | `for _, r := range s` |
| S1031 | the `if x != nil {` around a lone `range x` loop | deleted and the loop dedented |
| S1032 | `sort.Sort(sort.IntSlice(x))`; `sort.Sort(sort.StringSlice(x))` | `sort.Ints(x)`; `sort.Strings(x)` |
| S1033 | `if _, ok := m[k]; ok { delete(m, k) }` | `delete(m, k)` |
| S1035 | `http.CanonicalHeaderKey(k)` as the key of an `http.Header` `Add`/`Del`/`Get`/`Set` call | `k` |
| S1037 | `select { case <-time.After(d): … }` | `time.Sleep(d)`, then the case body |
| S1038 | `fmt.Print(fmt.Sprintf(f, …))`; `fmt.Fprint(w, fmt.Sprintf(f, …))`; `fmt.Sprint(fmt.Sprintf(f, …))` | `fmt.Printf(f, …)`; `fmt.Fprintf(w, f, …)`; `fmt.Sprintf(f, …)` |
| S1039 | `fmt.Sprint("lit")`, or `fmt.Sprintf("lit")` with no `%` in it | `"lit"` |
| ST1019 | the imports of one path in one file | one. A dot import survives, else the one with the most `name.` references in the file (tie: the unaliased one, else the first); every other is deleted and its references change only their qualifier, to the survivor's name or none under a dot import |

- S1000 and S1037: the case body has no unlabeled `break`, and no name the case
  declares (on its `case` line, or by `:=`, `var`, `const` or `type` at the top
  of its body) appears elsewhere in the enclosing top-level function: lifted out
  of the clause, a `:=` would assign an outer variable it used to shadow.
- ST1019: every rewritten reference resolves to the import. Named survivor `q`:
  `grep -nwE 'q' <file> | grep -vE '(^|[^.[:alnum:]_])q\.[A-Za-z_]'` prints only
  import lines. Dot survivor: for each name `X` losing its qualifier,
  `grep -nE '(^|[^.[:alnum:]_])X([^[:alnum:]_]|$)' <file>` prints nothing before
  the change (nothing in the file binds `X` as a parameter, result, `:=` or
  range variable, so a bare `X` can only reach the import).
- No call, conversion or channel receive runs a different number of times or in
  a different order, except the one the form replaces (the `Index*`, `Compare`,
  `HasPrefix`/`HasSuffix`/`Contains` guard, `time.Now()`, `time.After`, `len`,
  `[]rune`, `sort.*Slice`, `CanonicalHeaderKey`, or `fmt`/`errors` call). Every
  other expression whose evaluation count or order changes (`s`, `p` and the
  `Replace` arguments in S1017, `x` in S1009 and S1012, `s` in S1010, `m` and `k`
  in S1033, the size in S1019, a deleted nil check's operand) is an identifier,
  constant or literal, or a selector or index expression built only from those.
- The diff adds no import; it may delete an import of `bytes`, `errors`, `fmt`,
  `net/http`, `sort`, `strings` or `time` the rewrite leaves unused, and ST1019
  deletes its duplicates. No exported declaration's name or type changes (a
  qualifier swap inside one names the same type), so the brine module, which
  imports the root through `replace`, sees nothing new.
- Every comment in the rewritten span survives verbatim apart from indentation.
- The diff touches only the hit's file, holds every scan line of the key and
  nothing else, and `git diff --numstat` shows at most 40 deleted lines.

## Exclude

| code | how its rewrite can change behavior |
|---|---|
| S1000 "should use for range instead of for { select {} }" (the scan drops it) | a closed channel ends the range but feeds the select loop zero values, and an unlabeled `break` in the case leaves the select but would leave the loop |
| S1001, S1018 (`copy`) | an index past the destination's length panics in the loop but truncates silently in `copy`, and overlapping slices copy differently |
| S1007 (raw-string regexp) | value identity rests on hand-translating escapes, which nothing compiles or checks |
| S1011 (one `append`) | the result's capacity differs, and with it which later appends alias |
| S1016 (struct conversion) | ties two types' field lists together and ignores tags; one side is usually a wire or DB type |
| S1017 with `bytes.Contains` | `bytes.Replace` returns a copy even when nothing matches, where the guarded form kept the caller's slice |
| S1017 onto a map index, a field, a dereference, or a local a func literal names | the map gains a key the guarded form never inserted; the others are written where the guarded form only read, and a concurrent reader now races |
| S1021 (`var x T` then `x = e`) | the declaration-init card owns that join (its `join-typed` form), and one site must not carry two keys in two cards |
| S1025 (`Sprintf("%s", x)`) | fmt recovers a nil receiver's `String` panic and prefers `Error()` over `String()`; a direct call does neither |
| S1030 (`buf.Bytes()`/`buf.String()`) | `Bytes()` aliases the buffer where `[]byte(buf.String())` copied, and `String()` on a nil `*Buffer` returns `"<nil>"` |
| S1032 `sort.Float64s` | it is `slices.Sort` now, which may order `-0`/`+0` and NaNs differently from `sort.Sort` |
| S1034 (type-switch binding) | the new switch variable shadows an outer name in every clause |
| S1036 (map guard) | changes which branch creates the entry; key existence and nil-vs-empty can differ |
| S1038 with `Println`, `Fprintln`, `Sprintln`, a `log.*` or a `testing` outer call | they format through `Sprintln` or need the format literal hand-edited, and a format already ending in `\n` gains or loses a newline |
| S1040 (assertion to the current type) | removing it drops the nil-interface panic or `false` |

- An admitted code in a form the Predicate does not name (S1002 against a named
  constant, S1028 with a non-literal format or a `%w`, a `select` whose one
  clause is `default`).
- A pick that fails a Predicate line when checked, or whose diff would delete
  more than 40 lines (`git diff --numstat`, whitespace included): revert and log
  `skipped` naming the line or the count, so the pick discards it from then on.
- A package the scan lists under `UNANALYSED` on stderr (a build cannot compile
  it; its hits are dropped): the day goes on with the rest, and its log row
  names those packages.
- `atc/worker/jetbridge/brine/` and `hack/mcp-oauth-probe/` (own modules, outside
  `./...`); `topgun/`, `testflight/`, `integration/`, `testhelpers/otel/` (no
  local tier runs them); `atc/db/migration/migrations/`, `*fakes/`, `vendor/`,
  and any file carrying `// Code generated … DO NOT EDIT.` (staticcheck skips
  those itself). None of these reaches the scan's output.

## Gate

```bash
bash /tmp/tidy-simplify/g.sh ./<pkg> > /tmp/tidy-simplify/gate-before.txt  # before editing
gofmt -l <file>                                                           # prints nothing
go build ./... && go vet -tags live,hangar_live ./<pkg>
bash /tmp/tidy-simplify/g.sh ./<pkg> | diff /tmp/tidy-simplify/gate-before.txt -
# when `go list -tags live,hangar_live -f '{{.IgnoredGoFiles}}' ./<pkg>` lists any file:
go vet ./<pkg> && GOOS=linux go vet -tags live,hangar_live ./<pkg>
GOOS=windows go build -o /dev/null ./<pkg>   # without -o a package main leaves a .exe in the tree
```

The `diff` prints only `<` lines, each one of the instance's hits: once per build
that compiles the file, and for ST1019 with its `other import` line. A `>` line
is a finding the edit introduced, a `(compile)` line a build it broke; findings
the edit only moved do not count (`g.sh` strips line and column). For a
`_test.go` file the `grep -c 'Expect(\|Eventually(\|Consistently('` count must
not fall. `make test-fly-integration` when the file is under `fly/`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
