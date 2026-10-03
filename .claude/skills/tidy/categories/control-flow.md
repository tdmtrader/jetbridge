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
| 2026-10-02 | else-drop:Execute@fly/commands/clear_resource_cache.go | rewritten | in `ClearResourceCacheCommand.Execute` the `if err != nil` branch ends in an unconditional `return err`, the if has no init statement and the `else` declared nothing, so the Printf and `return nil` moved to function level; the only `else` in the function; gates: go build+vet, ginkgo ./fly/commands (42 passed), make test-fly-integration (601 passed) |
| 2026-10-02 | direct-return:health@atc/creds/vault/api_client.go | rewritten | vault api v1.22.0 `(*Sys).Health()` returns `(*HealthResponse, error)`, the same types as the unnamed `(*vaultapi.HealthResponse, error)` result; `err` was already declared by the `baseClient` call, there is no defer and nothing ran between `healthResponse, err := ...` and its return, so it returns the call directly; gates: go build+vet, ginkgo ./atc/creds/vault (45 passed) |
| 2026-10-02 | else-drop:Execute@fly/commands/clear_task_cache.go | rewritten | in `ClearTaskCacheCommand.Execute` the `if err != nil` branch prints and ends in an unconditional `return err`, the if has no init statement and the `else` declared nothing, so the Printf and `return nil` moved to function level; the only `else` in the function; gates: go build+vet, ginkgo ./fly/commands (42 passed), make test-fly-integration (601 passed) |
| 2026-10-02 | errors.New:validateHangarOptions@cmd/artifact-daemon/hangar.go | rewritten | all 8 literal-only `fmt.Errorf` calls in the function have no `%` and no arguments (the `%s` warrant-TTL and `%w` warrant-key-read calls stay); `errors` was already imported and `fmt` stays needed elsewhere in the file; gated by build, vet and `go test ./cmd/artifact-daemon/` |
| 2026-10-02 | slices.Contains:inArray@atc/policy/checker.go | rewritten | the found/break loop over a `[]string` is exactly `slices.Contains` (same `==`, false for a nil or empty filter list, no copy); only the `slices` import was added and the package has no `slices` identifier; gated by build, vet and the `atc/policy` suite |
| 2026-10-02 | slices.Contains:contains@atc/db/resource.go | rewritten | the helper's range/return-true/return-false loop over `[]int` is exactly `slices.Contains` (same `==`, false for nil or empty); `slices` was the only import added and the `db` package already imports it elsewhere, so no identifier conflicts; gated by build, vet and the `atc/db` suite (1419 passed) |
| 2026-10-02 | errors.New:decodeHangarControl@cmd/artifact-daemon/hangar_handlers.go | rewritten | the two literal-only `fmt.Errorf` calls the 2026-09-23 row left behind have no `%` and no arguments; `errors` was already imported, and they were the file's only `fmt` uses, so the `fmt` import was dropped; gated by build, vet and `go test ./cmd/artifact-daemon/` |
| 2026-10-02 | slices.Contains:RequestBodyIsSensitive@atc/sensitive_body.go | rewritten | the range/return-true/return-false loop over the package `[]string` of sensitive-body action names is exactly `slices.Contains` (same `==`, no copy); only the `slices` import was added and the `atc` package declares no `slices` identifier; gated by build, vet and the `atc` suite |
| 2026-10-02 | errors.New:New@hangar/treestore/store.go | rewritten | all 6 literal-only `fmt.Errorf` configuration checks in the tree store constructor have no `%` and no arguments (the three `%w` calls stay); `errors` was already imported and `fmt` stays needed elsewhere in the file; `errors` is stdlib, so Hangar's no-first-party-import rule is untouched; gated by build, vet and `go test ./hangar/treestore/` |
| 2026-10-02 | slices.Sorted:names@vars/template.go | rewritten | the append-from-nil key loop plus `sort.Strings` is exactly `slices.Sorted(maps.Keys(...))`: both return nil for an empty map (checked with a scratch run) and fresh strings in the same order; `sort` had no other use in the file, so its import gave way to `maps` and `slices`; gated by build, vet and the `vars` suite |
| 2026-10-02 | maps.Copy:MergeResourceTypeImages@atc/worker/jetbridge/config.go | rewritten | the `for k, v := range DefaultResourceTypeImages { merged[k] = v }` loop is `maps.Copy`'s own body, the same shallow copy between two `map[string]string`, and the preceding `make` stays, so the result is still a fresh non-nil map; only the `maps` import was added and the package declares no `maps` identifier; gated by build, vet, the `atc/worker/jetbridge` suite, and build+vet of the brine module whose steps call it |
| 2026-10-02 | errors.New:RunTaskDeclarations@atc/run_result.go | rewritten | all 5 literal-only `fmt.Errorf` limit checks in the function (four inside its walk closure, one on the marshalled mapping size) have no `%` and no arguments; `errors` was already imported and unshadowed, and `fmt` stays needed for the formatted calls; gated by build, vet and the `atc` suite |
| 2026-10-02 | %w:handleRegister@cmd/artifact-daemon/server.go | rejected | codex rejected under rule 2: Both `%v`→`%w` changes match the card’s explicit exclusion of “wrapping where the original did not wrap.” Unmerged on `tidy-batch/w1/lane-4` at d09dd367a8, authored in today's batch run (each commit reviewed by both codex and Claude; either rejection drops it). |
| 2026-10-02 | direct-return:Jobs@atc/db/pipeline.go | rewritten | `jobs, err := scanJobs(...)` was immediately followed by `return jobs, err`; `scanJobs` returns `(Jobs, error)`, matching `Jobs`'s unnamed results, and the function has no defer, so it returns the call directly; gates: `go build ./... && go vet ./...`, `ginkgo -p --procs=4 ./atc/db/` (1419/1419) |
| 2026-10-02 | else-drop:determineMigrationStrategy@atc/db/migration/parser.go | rewritten | the `if strings.HasSuffix(...)` branch ends in an unconditional `return GoMigration`, it has no init statement and the `else` declares nothing, so `return SQLMigration` moves out of the `else`; the file is outside the excluded `atc/db/migration/migrations/`; gates: `go build ./... && go vet ./...`, `ginkgo -p --procs=4 ./atc/db/migration/` (280/280) |
| 2026-10-02 | direct-return:DeleteBuildEventsByBuildIDs@atc/db/pipeline.go | rewritten | `err = tx.Commit()` was immediately followed by `return err`; `Tx.Commit` returns `error`, the result is unnamed, and the only defer, `defer Rollback(tx)`, is a plain call that does not observe `err`, so the commit is returned directly; gates: `go build ./... && go vet ./...`, `ginkgo -p --procs=4 ./atc/db/` (1419/1419) |
| 2026-10-02 | else-drop:createVolume@atc/db/dbtest/builder.go | rewritten | the `if handle == ""` branch ends in an unconditional `return`, it has no init statement and the `else` declares nothing, so the `CreateVolumeWithHandle` return moves out of the `else`; `dbtest` is test support, not a fakes directory; gates: `go build ./... && go vet ./...` (dbtest has no tests of its own, and its only callers, `WithCreatingVolume`/`WithCreatedVolume`, have no callers anywhere in the repo including the brine module) |
| 2026-10-02 | else-drop:NewVersionSourceFromPlan@atc/exec/version_source.go | rewritten | the `Version` and `VersionFrom` branches each end in an unconditional `return`, there are no init statements and neither `else` declares anything, so both elses in the function were dropped and `&EmptyVersionSource{}` became the trailing return; gates: `go build ./... && go vet ./...`, `ginkgo -p --procs=4 ./atc/exec/` (625/625) |
| 2026-10-02 | direct-return:UnmarshalFlag@atc/creds/dummy/flags.go | rewritten | `err := yaml.Unmarshal(...)` was followed only by `if err != nil { return err }; return nil`; `sigs.k8s.io/yaml.Unmarshal` returns a plain `error`, the result is unnamed and there is no defer, so the call is returned directly; gates: `go build ./... && go vet ./...` (`atc/creds/dummy` has no tests of its own) |
| 2026-10-03 | direct-return:WriteTar@go-archive/archivetest/archiver.go | rewritten | `err := w.Close()` was followed only by `if err != nil { return err }; return nil`; `(*tar.Writer).Close` returns a plain `error`, the result is unnamed, there is no defer, and the loop's own `err` is scoped to the loop, so the close is returned directly; gates: `go build ./... && go vet ./...` (`archivetest` has no tests of its own), plus its consumers `ginkgo -p --procs=4 ./go-archive/tarfs/ ./go-archive/tgzfs/` (31/31, 10/10) |
| 2026-10-03 | else-drop:findSecret@atc/creds/kubernetes/secrets.go | rewritten | the not-found and error branches each end in an unconditional `return`, there are no init statements and neither `else` declares anything (`secret` and `err` are declared above the chain), so both elses in the function were dropped and `return secret, true, err` became the trailing return unchanged; gates: `go build ./... && go vet ./...`, `ginkgo -p --procs=4 ./atc/creds/kubernetes/` (14/14) |
