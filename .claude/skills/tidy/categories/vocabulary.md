# vocabulary

Rename one Go identifier that uses a term a glossary lists under _Avoid_ to the
entry's canonical term.

Merge: green
Instance key: `<old identifier>@<package>`

## Scan

Build the term list from the four glossaries, derive identifier forms, count
non-test production hits:

```bash
for ctx in atc atc/agent atc/worker/jetbridge hangar; do
  grep -h '^_Avoid_:' $ctx/CONTEXT.md | sed 's/^_Avoid_: *//; s/ (.*//' | tr ',' '\n' | sed 's/^ *//'
done | grep -E '[ -]' | sort -u | while read -r term; do
  camel=$(echo "$term" | perl -pe 's/[ -](\w)/\u$1/g'); pascal="$(echo ${camel:0:1} | tr a-z A-Z)${camel:1}"
  n=$(grep -rIlw --include='*.go' -e "$camel" -e "$pascal" . 2>/dev/null | grep -v _test.go | wc -l | tr -d ' ')
  [ "$n" -gt 0 ] && echo "$n	$term	$camel|$pascal"
done | sort -rn
```

Pick the term with the most files, then within it the identifier with the
fewest files (finish a term before starting the next). One identifier per day,
every reference to it renamed, compile-verified. Local variables count.

## Predicate

- The term appears under `_Avoid_` in the CONTEXT.md of the context the file
  belongs to, and the entry names the canonical replacement.
- The identifier is Go source: a type, function, method, field, variable, or
  package-level name.

## Exclude

- DB column names, migration SQL, JSON/YAML/wire struct tags, metric and label
  names, flag names, environment variable names. Rename the Go identifier and
  leave the tag string as it was.
- `atc/db/migration/migrations/` (immutable history).
- `atc/worker/jetbridge/brine/` (own module, own vocabulary guard).
- Single-word _Avoid_ terms (`run`, `node`, `cache`, `hold`, `receipt`, and
  every entry marked "alone"): the same word is legitimate in a neighbouring
  context, so the scan keeps only multi-word terms. Renaming a single-word
  term is a reviewed decision, not a daily pick.
- A term whose glossary entry is ambiguous about which canonical term applies
  at this site (e.g. `durableKey` may be a content key or an artifact key; read
  the entry and the code before choosing, and skip if unsure).

## Gate

```bash
go build ./... && go vet ./...
```

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-21 | instancePipeline@atc/api/present | renamed | renamed to `payload`; `durable key` skipped as ambiguous (content key vs. artifact key) across all its sites; first ci-check attempt hit a postgres testdb teardown race (build 882228), unrelated to this diff and since addressed by "test: make postgres test-port selection atomic and self-healing" — rebased onto core and rerun |
| 2026-09-24 | (none) | skipped | all candidates excluded: `durable key` still ambiguous at every atc/worker/jetbridge site (storage.go's long-term-storage sense vs. resource_cache_key.go's content-key sense); `instance pipeline`'s only non-brine identifier (InstancePipeline/InstancePipelines@atc/db, called from atc/api/pipelinerunserver) is also called by atc/worker/jetbridge/brine/steps via the `replace github.com/concourse/concourse => ../../../..` module boundary, so renaming it would silently break brine's separate build with no way to fix the call sites under the brine exclude, and `go build ./... && go vet ./...` would not catch it; `durable restore` and `capability key`'s only hits (cmd/artifact-daemon, artifactwire, atc/atccmd) sit outside any bounded context whose CONTEXT.md avoids the term, or inside excluded brine |
