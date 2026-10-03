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
| 2026-09-30 | copiedFlyDir@fly/integration/sync_test.go | rejected | codex rejected under rule 2, the ownership predicate, and rule 3, behavior can differ: the card's ownership line is about replacing a distant `AfterEach` with `DeferCleanup` / `t.TempDir()` / `t.Setenv` at identical cleanup semantics, and this diff instead repaired a shadowing bug and added a `chmod`, which changes cleanup from removing nothing to removing the directory. The reading is fair -- the card is a tidying contract, not a bug-fix one -- and the finding underneath it is real and still unfixed on `core`: line 30 declares `copiedFlyDir, err := os.MkdirTemp` with `:=`, shadowing the outer `copiedFlyDir` from line 25, so the `AfterEach` runs `os.RemoveAll("")`, which returns nil and passes its assertion while the directory survives. Every spec in that Describe leaks a ~93MB copy of the fly binary. 49 leaked `fly_sync*` dirs totalling 3.0GB had taken this machine to 99% full, and `make test-quick` was red in 13 suites on unmodified origin/core with Postgres `No space left on device (SQLSTATE 53100)` at `atc/postgresrunner/postgresrunner.go:320`; the leaked dirs were deleted outside the repo to unblock the gate, which is why the run's own `make test-quick` then passed in all 114 suites. Branch `tidy/fixture/2026-09-30` left unmerged for the weekly digest, which should take the shadow fix as ordinary work rather than a tidy. Two things for future fixture picks: a `DeferCleanup` registered in an `It` also runs after the `AfterEach` nodes, not before, so the 2026-09-23 ordering trap is not confined to `BeforeEach`; and a cleanup line whose assertion passes is not evidence the cleanup ran |
| 2026-09-23 | configFile@fly/integration/format_pipeline_test.go | rejected | codex rejected under rule 3, side effect moved across a boundary: a `DeferCleanup` registered in a `BeforeEach` runs *after* the ordinary `AfterEach` nodes, so moving the config file's `os.RemoveAll` there puts it after the suite-level `AfterEach` in `fly/integration/suite_test.go:156` (`atcServer.Close()`, `os.RemoveAll(homeDir)`) instead of before it. The note on the branch claimed "nothing reorders", which was wrong: it accounted only for the Describe's own cleanup and missed the suite node. Everything else checked out (assertions 27 to 27, `ginkgo -r ./fly/integration/` 600 of 600 green, `make test-quick` green), so the ordering is the whole of it. Branch `tidy/fixture/2026-09-23` left unmerged for the weekly digest. Any future fixture pick that moves cleanup out of an `AfterEach` needs the suite-level cleanup order checked first |
| 2026-10-02 | migrationDir@atc/db/migration/cli/command/command_test.go | tidied | the generate-migration Context's temp dir now owns its cleanup: `DeferCleanup(os.RemoveAll)` registered right after `os.MkdirTemp` in the BeforeEach replaces the Context's `AfterEach`. The dir is made per spec and used only by the two Its; that `AfterEach` was the only cleanup node in the package (no suite-level node, no other `DeferCleanup`; the suite file only calls `RunSpecs`), so nothing reorders. Registered before the `err` check, so it runs on the same paths the `AfterEach` did; error still ignored. No Skip, Sleep or BeforeSuite. Assertions 16 to 16; `go build ./...`, `go vet ./...`, `ginkgo ./atc/db/migration/cli/command` 2 of 2 green |
| 2026-10-02 | tmpdir@fly/commands/internal/templatehelpers/yaml_template_test.go | tidied | the resolve Describe's temp dir now owns its cleanup: `DeferCleanup(os.RemoveAll)` registered right after `os.MkdirTemp` in the BeforeEach replaces the Describe's `AfterEach`. The dir is made per spec and used by its 3 Its (the nested `When("strict")` has no cleanup node); that `AfterEach` was the only cleanup node in the package and the suite file only calls `RunSpecs`, so nothing reorders. Registered before the `err` check, so it still runs when the `sample.yml` write fails, as the `AfterEach` did. No Skip or Sleep. Assertions 9 to 9; `go build ./...`, `go vet ./...`, `ginkgo ./fly/commands/internal/templatehelpers` 3 of 3, `make test-fly-integration` 601 of 601 green (a first run lost `hijack when multiple step containers are found` to a menu-selection timing flake in a package this diff does not touch; the rerun was green) |
| 2026-10-02 | extractionDest@go-archive/tarfs/extract_test.go | tidied | the Extract Describe's destination dir now owns its cleanup: `DeferCleanup(os.RemoveAll)` registered right after `os.MkdirTemp` in the BeforeEach replaces the Describe's `AfterEach`. The Describe has one It and no nested containers; the package's other cleanup (`containment_test.go`'s `AfterEach` and its `DeferCleanup(restoreWritable)` calls) lives under the separate top-level `Describe("Extract containment")`, so it is not on this spec's chain, and the suite file only calls `RunSpecs`, so nothing reorders. Registered before the `err` check, so it still runs when `TarStream` fails, as the `AfterEach` did. No Skip or Sleep in this Describe. Assertions 19 to 19; `go build ./...`, `go vet ./...`, `ginkgo ./go-archive/tarfs` 31 of 31 green |
| 2026-10-02 | extractionDest@go-archive/tgzfs/extract_test.go | tidied | the tgzfs Extract Describe's destination dir now owns its cleanup: `DeferCleanup(os.RemoveAll)` registered right after `os.MkdirTemp` in the BeforeEach replaces the Describe's `AfterEach`. The Describe has one It and no nested containers; the package's other `AfterEach` nodes (`compress_test.go`, `containment_test.go`) sit under separate top-level Describes, the package has no other `DeferCleanup`, and the suite file only calls `RunSpecs`, so nothing reorders. Registered before the `err` check, so it still runs when `TarGZStream` fails, as the `AfterEach` did. No Skip or Sleep in this Describe. Assertions 17 to 17; `go build ./...`, `go vet ./...`, `ginkgo ./go-archive/tgzfs` 10 of 10 green |
| 2026-10-02 | base@go-archive/tgzfs/containment_test.go | tidied | the tgzfs Extract containment Describe's base dir now owns its cleanup: `DeferCleanup(os.RemoveAll)` registered right after `os.MkdirTemp` in the BeforeEach replaces the Describe's `AfterEach`. The closure reads `base` at cleanup time, so it removes the same `EvalSymlinks`-resolved path the `AfterEach` did (and the same `""` no-op if that call fails). `base` is made per spec and used by the 3 Its and `expectOutsideUntouched`; the Describe has no nested containers, the package has no other `DeferCleanup` (unlike tarfs, nothing registers `restoreWritable` here), the other `AfterEach` nodes sit under separate top-level Describes, and the suite file only calls `RunSpecs`, so nothing reorders. No Skip or Sleep in this Describe. Assertions 11 to 11; `go build ./...`, `go vet ./...`, `ginkgo ./go-archive/tgzfs` 10 of 10 green |
| 2026-10-02 | expectRowCount@atc/db/migration/legacy_upgrade_test.go | tidied | `expectRowCount` now opens with `GinkgoHelper()`, so a row-count miss is reported at the calling line instead of inside the helper. It is a top-level func that runs one query and two Expects synchronously and returns; it takes no Gomega and uses no `WithOffset`. All 45 calls (44 in this file, 1 in `hangar_output_test.go`) are direct, in It/BeforeEach bodies, a plain `for` loop, or `verifyFixtureDataPresent`; none is inside a goroutine, a callback or an `Eventually`. The file already dot-imports ginkgo and uses `GinkgoHelper()` in `preflightTargetVersion`. Assertions 82 to 82; `go build ./...`, `go vet ./...`, `ginkgo ./atc/db/migration` 280 of 280 green |
| 2026-10-02 | verifyBuildStatuses@atc/db/migration/legacy_upgrade_test.go | tidied | `verifyBuildStatuses` now opens with `GinkgoHelper()`, so a status-count miss is reported at the calling line. It is a top-level func that runs one GROUP BY query, asserts with four direct Expects (plus a deferred `rows.Close`) and returns; it takes no Gomega and uses no `WithOffset`. Its 3 calls are direct, two in It bodies and one in `verifyFixtureDataPresent`; none is inside a goroutine, a callback or an `Eventually`. Assertions 82 to 82; `go build ./...`, `go vet ./...`, `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration` 15 of 15 green (the whole package runs on the last `GinkgoHelper` commit of this chain) |
| 2026-10-02 | verifyFixtureDataPresent@atc/db/migration/legacy_upgrade_test.go | tidied | `verifyFixtureDataPresent` now opens with `GinkgoHelper()`, so a fixture miss is reported at the calling line. It is a top-level func that asserts with seven direct Expects plus synchronous calls to `expectRowCount` and `verifyBuildStatuses` (both already helpers on this chain) and returns; it takes no Gomega and uses no `WithOffset`. Its 2 calls are direct in one It body; neither is inside a goroutine, a callback or an `Eventually`. Assertions 82 to 82; `go build ./...`, `go vet ./...`, `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration` 15 of 15 green (the whole package runs on the last `GinkgoHelper` commit of this chain) |
| 2026-10-02 | verifyJetBridgeSchemaChanges@atc/db/migration/legacy_upgrade_test.go | tidied | `verifyJetBridgeSchemaChanges` now opens with `GinkgoHelper()`, so a schema miss is reported at the calling line. It is a top-level func that runs information_schema queries, asserts with eight direct Expects and returns; it takes no Gomega and uses no `WithOffset`. Its 3 calls are direct in It bodies; none is inside a goroutine, a callback or an `Eventually`. Assertions 82 to 82; `go build ./...`, `go vet ./...`, `ginkgo -r ./atc/db/migration` 280 of 280 plus the CLI command suite's 2 of 2 green, covering this file's four `GinkgoHelper` commits together |
