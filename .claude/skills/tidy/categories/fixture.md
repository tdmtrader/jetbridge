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
| 2026-09-23 | configFile@fly/integration/format_pipeline_test.go | tidied | The temp config file was created in the `format-pipeline` Describe's `BeforeEach` and removed by a separate `AfterEach` sixteen lines below, outliving nothing but the spec that used it, so a `DeferCleanup` registered at the creation site now owns it and the `AfterEach` node is gone. Cleanup semantics are unchanged: the same `os.RemoveAll(configFile.Name())` with the same `Expect(err).NotTo(HaveOccurred())`, and it is the only cleanup in the container so nothing reorders. Assertion count in the file is 27 before and after; `ginkgo -r ./fly/integration/` ran 600 of 600 specs green with 0 pending and 0 skipped |
