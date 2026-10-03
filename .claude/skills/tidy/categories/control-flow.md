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
| 2026-09-29 | else-drop:ClearResourceCache@go-concourse/concourse/resource.go | rewritten | the `if err != nil` branch ends in an unconditional `return`, and `crcResponse` (the variable scoped inside the `else`) is never used after it, so the `else` was dropped in favor of a direct trailing return |
| 2026-10-02 | direct-return:CacheWarmUp@atc/db/cache_warmup.go | rewritten | `err := warmUpBaseResourceTypesCache(runner)` was followed only by `if err != nil { return err }; return nil`; the callee returns a plain `error`, `CacheWarmUp` has no named result and no defer, so the body became a direct return; gates: go build+vet, ginkgo ./atc/db (1419 passed) |
| 2026-10-02 | else-drop:ClearTaskCache@go-concourse/concourse/jobs.go | rewritten | the `if err != nil` branch ends in an unconditional `return 0, err`, the if has no init statement, the `else` declared nothing and `ctcResponse` is declared before the if, so the `else` was dropped for a trailing direct return; gates: go build+vet, ginkgo ./go-concourse/concourse (211 passed) |
| 2026-10-02 | direct-return:scanComponent@atc/db/component.go | rewritten | `row.Scan` on `scannable` returns a plain `error`, `scanComponent` has no named result and no defer, and nothing ran between the assignment and the `if err != nil { return err }; return nil`, so it returns the scan directly; `Reload`'s `err == sql.ErrNoRows` check still sees the same error value; gates: go build+vet, ginkgo ./atc/db (1419 passed) |
| 2026-10-02 | else-drop:ClearResourceVersions@go-concourse/concourse/resourceversions.go | rewritten | the `if err != nil` branch ends in an unconditional `return 0, err`, the if has no init statement, the `else` declared nothing and `crvResponse` is declared before the request, so the `else` was dropped for a trailing direct return; ClearResourceTypeVersions in the same file is its own instance; gates: go build+vet, ginkgo ./go-concourse/concourse (211 passed) |
| 2026-10-02 | direct-return:ClearWall@go-concourse/concourse/wall.go | rewritten | `Connection.Send` returns a plain `error`, `ClearWall` has no named result and no defer, and only a blank line sat between `err := client.connection.Send(...)` and `return err`, so it returns the send directly; SetWall in the same file was left as its own instance; gates: go build+vet, ginkgo ./go-concourse/concourse (211 passed) |
| 2026-10-02 | else-drop:ClearResourceTypeVersions@go-concourse/concourse/resourceversions.go | rewritten | the `if err != nil` branch ends in an unconditional `return 0, err`, the if has no init statement, the `else` declared nothing and `crvResponse` is declared before the request, so the `else` was dropped for a trailing direct return, matching the ClearResourceVersions row above; gates: go build+vet, ginkgo ./go-concourse/concourse (211 passed) |
| 2026-10-02 | direct-return:NextEventRaw@go-concourse/concourse/eventstream/stream.go | rewritten | `(*sse.EventSource).Next()` (go-sse v1.1.3) returns `(Event, error)`, the same types as the unnamed `(sse.Event, error)` result, with no defer and nothing between `se, err := ...` and `return se, err`, so it returns the call directly; gates: go build+vet; the eventstream package has no suite, ginkgo ./go-concourse/concourse (which declares the interface) 211 passed |
