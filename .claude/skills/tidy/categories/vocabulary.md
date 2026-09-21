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
