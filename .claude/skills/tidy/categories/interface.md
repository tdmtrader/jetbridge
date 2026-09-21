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
