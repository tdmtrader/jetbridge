# fixture

Tidy one test fixture: give a temporary resource a local owner, extract one
repeated setup or assertion block into a package-local helper, or mark one
synchronous assertion helper with `GinkgoHelper()` / `t.Helper()`.

Merge: review
Instance key: `<helper or resource>@<file>`

## Scan

```bash
# temp resources cleaned up far from creation
grep -rn --include='*_test.go' -e 'os.MkdirTemp' -e 'os.CreateTemp' -e 'os.Setenv' . | grep -v topgun
# identical multi-line blocks inside one package (>=3 copies)
for d in $(find . -name '*_test.go' -not -path './topgun/*' -exec dirname {} \; | sort -u); do
  cat $d/*_test.go | grep -v '^\s*$' | sed 's/^\s*//' | awk '{a[NR]=$0} END{for(i=1;i<=NR-3;i++) print a[i]"⏎"a[i+1]"⏎"a[i+2]"⏎"a[i+3]}' | sort | uniq -c | awk -v d=$d '$1>=3{print $1"\t"d"\t"$0}'
done | sort -rn | head -40
# assertion helpers without GinkgoHelper
grep -rlE --include='*_test.go' 'func [a-z][A-Za-z]*\(.*\) .*\{$' . | xargs grep -L 'GinkgoHelper\|t.Helper' | head
```

Order: temp resource ownership, then `GinkgoHelper()` marking, then
extraction. Prefer files under `atc/db`, `atc/api`, `fly/integration`.

## Predicate

- Ownership: the resource is created and used in one spec's lifetime;
  `DeferCleanup` / `t.TempDir()` / `t.Setenv` replaces a distant `AfterEach`
  line with identical cleanup semantics, including unset-vs-empty for env vars.
- Extraction: the helper's body is the copied lines verbatim; every original
  assertion survives with the same expected value; spec count is unchanged.
- Helper marking: the function asserts synchronously in the calling
  goroutine and returns; callbacks and background handlers are not eligible.

## Exclude

- Suite-wide resources (`BeforeSuite`, `SynchronizedBeforeSuite`), Postgres
  runners, the template database.
- `atc/db/worker_cache_test.go` timing values and any `Eventually` timeout.
- `time.Sleep` sites: readiness-vs-sleep is behavioral work, not fixture work.
- Skipped or pending specs.
- `topgun/`, `*fakes/`.
- Any helper that would compute the expected value with production code.

## Gate

The affected tier: `make test-fly-integration` for `fly/`, `ginkgo -r <package>`
for the touched package, `make test-brine-guards` for brine. Assertion count in
the touched files before and after, from `grep -c 'Expect(\|Eventually(\|Consistently('`,
must not fall.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-23 | configFile@fly/integration/format_pipeline_test.go | rejected | codex rejected under rule 3, side effect moved across a boundary: a `DeferCleanup` registered in a `BeforeEach` runs *after* the ordinary `AfterEach` nodes, so moving the config file's `os.RemoveAll` there puts it after the suite-level `AfterEach` in `fly/integration/suite_test.go:156` (`atcServer.Close()`, `os.RemoveAll(homeDir)`) instead of before it. The note on the branch claimed "nothing reorders", which was wrong: it accounted only for the Describe's own cleanup and missed the suite node. Everything else checked out (assertions 27 to 27, `ginkgo -r ./fly/integration/` 600 of 600 green, `make test-quick` green), so the ordering is the whole of it. Branch `tidy/fixture/2026-09-23` left unmerged for the weekly digest. Any future fixture pick that moves cleanup out of an `AfterEach` needs the suite-level cleanup order checked first |
| 2026-09-28 | numBuildEventsForCheck@atc/db/check_lifecycle_test.go | tidied | Helper marking, the safest of the card's three shapes and the one that cannot reorder a side effect: `numBuildEventsForCheck` runs one `COUNT(*)` synchronously in the calling goroutine, asserts on the error and returns, so `GinkgoHelper()` only moves the reported failure location to the caller. Eight call sites across `check_lifecycle_test.go`, `resource_test.go` and `resource_type_test.go` now point at themselves. No import added (ginkgo is already dot-imported) and the placement matches the house pattern at `atc/syslog/drainer_test.go:17`. Assertions in the touched file 57 to 57. Ownership was considered first per the card's order and none survived: every `fly/integration` temp resource repeats the 2026-09-23 rejection exactly (a `DeferCleanup` in a `BeforeEach` would run after the suite `AfterEach` at `fly/integration/suite_test.go:156`), and `atc/db/migration/cli/command/command_test.go` has its `AfterEach` directly under the `BeforeEach`, so no distant cleanup to replace. `make test-quick` (114 suites, elm 3111, brine guards), `go test .`, the hangar architecture tests, `go build ./atc/...` and `go vet ./atc/db/` green. The card's own gate was red on its first run with one failure at `atc/db/build_event_page_test.go:203` (`EventPage` returned an error mid-pagination), a file this diff does not touch and whose specs never call this helper; it did not recur in a full `ginkgo -r -p ./atc/db/` on unmodified origin/core, in a second full run with the change (5 of 5 suites green, 1418 of 1418 specs), or in three focused runs, so it is a flake in that spec, not this diff -- worth its own look |
