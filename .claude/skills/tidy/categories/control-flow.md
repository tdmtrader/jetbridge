# control-flow

One local, behavior-preserving rewrite of a function body: a direct return, an
`else` dropped after a return, `errors.New` for a literal-only `fmt.Errorf`, a
hand loop replaced by the `slices`/`maps` call it reimplements, or one `%v`
error format restored to `%w`.

Merge: green (`%w` sites: review)
Instance key: `<form>:<function>@<file>`

## Scan

```bash
# direct return: err assigned then immediately returned
grep -rnE -A2 --include='*.go' '^\s*(err :?= |[a-z]+, err :?= )' . | grep -B1 -A1 'if err != nil' | grep -A1 'return err$' | grep 'return nil$' | head
# else after return
grep -rnE -B1 --include='*.go' '^\s*} else \{' . | grep -B1 'return' | head
# literal-only Errorf
grep -rnE --include='*.go' 'fmt\.Errorf\("[^"%]*"\)' . | grep -v _test.go | head
# hand loops the stdlib already has
grep -rnE --include='*.go' -e 'sort\.Slice\(.*func\(i, j int\) bool \{ return [a-z]+\[i\] < [a-z]+\[j\]' -e 'for _, [a-z]+ := range .*\{$' . | head
# broken error chains
grep -rnE --include='*.go' 'fmt\.Errorf\(".*%[vs]", .*err\)' . | grep -v _test.go | head
```

Rotate forms day to day so one form's queue does not starve the others. One
function per day; every occurrence of the chosen form inside that function.

## Predicate

- Direct return: nothing runs between the assignment and the return, no named
  result, no deferred closure observing the variable.
- Else drop: the `if` branch ends in an unconditional `return`, `continue`,
  `break`, or `panic`; no variable scoped to the `else` is used after it.
- `errors.New`: the format string has no `%` and no arguments.
- Stdlib call: identical semantics including nil-vs-empty and shallow copy.
- `%w`: the wrapped error is not compared by string anywhere, and no
  `errors.Is`/`errors.As` on the call path changes its answer (grep the
  sentinel).

## Exclude

- `atc/db/migration/migrations/`, `*fakes/`, `vendor/`,
  `atc/worker/jetbridge/brine/`.
- Introducing a shared sentinel error, changing an error message, or wrapping
  where the original did not wrap.
- Any rewrite that needs a new helper or a new import beyond `errors`,
  `slices`, `maps`.

## Gate

`go build ./... && go vet ./...`; `make test-fly-integration` when the file is
under `fly/`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-23 | errors.New:validateHangarControlSchema@cmd/artifact-daemon/hangar_handlers.go | rewritten | all 8 literal-only `fmt.Errorf` calls in the function have no `%` and no arguments; `errors` was already imported, `fmt` stays needed for the two literal-only calls remaining in `decodeHangarControl` in the same file |
