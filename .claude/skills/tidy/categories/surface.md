# surface

Shrink the exported surface by one declaration: delete a dead exported Go or
Elm declaration, or unexport one that nothing outside its directory names.
Deletion before unexport.

Merge: green
Instance key: `<identifier>@<file>`

## Scan

Dead Go exports (zero references anywhere, all file types):

```bash
grep -rhoE --include='*.go' '^func (\([^)]*\) )?[A-Z][A-Za-z0-9_]*|^(type|var|const) [A-Z][A-Za-z0-9_]*' . \
  | grep -v -e '_test.go' -e fakes | awk '{print $NF}' | sort -u | while read -r id; do
  n=$(grep -rIw --exclude-dir=node_modules --exclude-dir=.git "$id" . | grep -v "func $id\|type $id\|var $id\|const $id" | wc -l | tr -d ' ')
  [ "$n" -eq 0 ] && echo "dead	$id"
done
```

Go exports never named outside their own directory: for each exported
identifier, `grep -rlw "$id" --include='*.go' .` returns only files in its own
directory. Elm: for each name in a module's `exposing (...)`, `grep -rw name
web/elm/src web/elm/tests web/elm/benchmarks` hits only the defining module.
Also Elm imports whose module or exposed names are unused in the file.

Order: dead Go declaration, dead Elm definition, unused Elm import, unexport
(only after the dead queues are empty). A dead declaration's whole file counts
as one instance when nothing else remains in it.

## Predicate

- Zero references outside the declaring file (delete) or outside the declaring
  directory (unexport), counting YAML, SQL, Elm, JS, and shell, not only Go.
- Unexport keeps the identifier's body untouched; only the case of the first
  letter and its references change.

## Exclude

- `atc/worker/jetbridge/brine/steps/` (step types are resolved by name).
- `fly/commands/` (go-flags reads exported fields by reflection).
- Any `package main` with a `package main_test` sibling file.
- `*fakes/`, `vendor/`, `atc/db/migration/migrations/`.
- Elm ports and any `Msg` constructor (emitted via effects, not by name).
- Interfaces with one method used as a seam by a test in another package.

## Gate

Go: `go build ./... && go vet ./... && make test-fly-integration`.
Elm: `make test-elm && (cd web && yarn run build)`; the tracked
`web/public/elm.min.js` must be in the commit.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-22 | AlgorithmOutput@atc/db/input_mapping.go | deleted | zero references anywhere in the tree besides its own declaration; not in an excluded path and not an interface |
| 2026-09-26 | AssetDir@atc/db/migration/bindata.go | deleted | zero references anywhere in the tree besides its own declaration; a go-bindata stub file, not under the excluded `atc/db/migration/migrations/` path; `Asset`/`MustAsset`/`AssetInfo`/`AssetNames`/`RestoreAsset`/`RestoreAssets` in the same file are also unreferenced (or, for bare `Asset`, blocked from the scan by an unrelated Elm `Asset` type collision) but stay untouched since the file doesn't collapse to empty — a future day's instances |
