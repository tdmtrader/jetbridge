# duplicate

Give one function body that exists identically in two or more files a single
home, and make the copies call it.

Merge: review
Instance key: `<function name>@<first file>+<n copies>`

## Scan

Hash normalized function bodies and group:

```bash
grep -rlE --include='*.go' --exclude-dir=vendor --exclude-dir=node_modules '^func ' . \
  | xargs -I{} awk -v f={} '
    /^func /{name=$0; sub(/\(.*/,"",name); sub(/^func /,"",name); body=""; in1=1}
    in1{gsub(/[ \t]+/," "); body=body $0}
    in1 && /^}/{print f "\t" name "\t" length(body) "\t" body; in1=0}' {} \
  | awk -F'\t' '$3>200{print}' | sort -t$'\t' -k4 | awk -F'\t' '{c[$4]=c[$4] " " $1":"$2; n[$4]++} END{for(b in c) if(n[b]>1) print n[b] "\t" c[b]}' | sort -rn
```

Elm: `grep -rn '^[a-z][A-Za-z]* :' web/elm/src | awk -F: '{print $3}' | sort |
uniq -d` and compare bodies by hand. Prefer production duplicates over test
duplicates, and duplicates within one bounded context over pairs that straddle
two.

## Predicate

- Bodies are identical after whitespace normalization, or differ only in an
  identifier the shared version takes as a parameter.
- The shared home is an existing package both copies may already import, or the
  lowest common package inside the same bounded context.
- Every caller's behavior is unchanged: same arguments, same error returned,
  same ordering.

## Exclude

- Copies that straddle two bounded contexts (`CONTEXT-MAP.md`): a copy is the
  boundary. Hangar and runtime `tempdir*_test.go` helpers stay separate.
- `topgun/` (not covered by any local gate).
- `atc/db/migration/migrations/`, `*fakes/`, generated files.
- `…Definitions() []brine.StepDefinition` tables and anything in
  `atc/worker/jetbridge/brine/`.
- A new helper that would need a boolean or a mode parameter to cover both
  copies. That is two functions.

## Gate

`make test-brine-guards`; plus `make test-fly-integration` when any copy is
under `fly/`; plus `make test-elm && (cd web && yarn run build)` for Elm.

## Log

| date | instance | outcome | note |
|---|---|---|---|
