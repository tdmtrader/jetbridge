# interface

Collapse one Go interface that has exactly one implementation and no consumer
outside its package: callers take the concrete type, the interface goes.

Merge: review
Instance key: `<interface>@<package>`

## Scan

```bash
grep -rnE --include='*.go' --exclude-dir=vendor '^type [A-Z][A-Za-z0-9]* interface' . | grep -v _test.go | while IFS=: read -r f l decl; do
  name=$(echo "$decl" | awk '{print $2}'); dir=$(dirname "$f")
  methods=$(awk -v s="$l" 'NR>s && /^}/{exit} NR>s && /\(/{n++} END{print n+0}' "$f")
  outside=$(grep -rlw --include='*.go' "$name" . | grep -v "^$dir/" | wc -l | tr -d ' ')
  [ "$outside" -eq 0 ] && [ "$methods" -le 3 ] && echo "$methods	$name	$f:$l"
done | sort -n
```

For each survivor confirm one implementation: grep for a type with every
method of the interface, and for assignments `var _ Name = ...`. Pick the
smallest method count first.

## Predicate

- Exactly one type satisfies the interface in the whole tree, tests included.
- No file outside the declaring package names the interface.
- At most three methods.
- The implementation lives in the same package.

## Exclude

- Interfaces whose single implementation is in production and whose test
  substitute lives in `*fakes/` or is a hand-written double in a `_test.go`
  file: that is a seam, and removing it is fake removal, a different job.
- Interfaces embedded in another interface.
- Interfaces named in `CONTEXT.md` or `docs/adr/` as a boundary (search the
  name before collapsing).
- `atc/worker/jetbridge/brine/`.

## Gate

`go build ./... && go vet ./...`

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-21 | BuildLogRetentionCalculator@atc/gc | gate-failed | `make test-quick` red before the card's own gate, entirely in the nested brine module: TestEngineReleaseDrainsRealDaemon, TestDocumentOwnsSelection, TestDocumentCheckEchoesRoster, TestDocumentRefusals and TestHoldPreservesThenDrainsRealResources want a `brine` binary on PATH; TestBusyboxScriptOutcomeBoundary wants `.build/busybox`; TestTraceCaptureExportsAndDisposesRealCollector wants `otelcol`. All seven reproduce identically on unmodified origin/core in this clone (`ginkgo -r` descends into the nested module despite its own go.mod), so the collapse was reverted unmerged and nothing is left on the branch but this row. Blocks every category here until the prerequisites exist in /Users/tdmtrader/concourse/tidy |
| 2026-09-21 | BuildLogRetentionCalculator@atc/gc | tidied | One method, one implementation, no namer outside atc/gc and no fake: the interface went and the struct took its name, so the exported constructor still returns an exported type. Supersedes the gate-failed row above, whose cause was the root ginkgo run descending into the nested brine module, fixed separately |
| 2026-09-26 | ClaimsParser@skymarshal/token | rejected | codex rejected under rule 2, the predicate fails: `skymarshal/token/access_token_test.go` is package `token_test`, a separate package from the declaring `token`, and it names `token.ClaimsParser` at line 52, so "no file outside the declaring package names the interface" is not satisfied. The card's scan cannot see this -- its `outside` check drops every path under the declaring directory, so an external test package sitting in the same directory reads as zero namers. Everything else was sound: one method, one implementation (`claimsParserNoVerify`), no fake or hand-written double, no nil check at any call site, and `make test-quick`, `go test .`, the hangar architecture tests, `go build ./...`, `go vet` and `go test ./skymarshal/...` all green. Branch `tidy/interface/2026-09-26` left unmerged for the weekly digest. Any future interface pick must check `_test` packages in the declaring directory by hand before trusting the scan |
