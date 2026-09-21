# pins

Single-source one repeated build pin inside one file, or advance one
allow-listed dependency by a patch release.

Merge: review
Instance key: `<pin or module>@<file>`

## Scan

```bash
# repeated image/runner pins
grep -rhoE 'image: *[^ ]+|repository: *[^ ]+|tag: *[^ ]+|FROM [^ ]+|[a-z0-9./-]+:v[0-9]+' deploy/*.yml deploy/Dockerfile.* Dockerfile* docker-compose.yml | sort | uniq -c | sort -rn | awk '$1>1'
# root vs nested module disagreements
join -j1 <(grep -E '^\s' go.mod | awk '{print $1, $2}' | sort) <(grep -E '^\s' atc/worker/jetbridge/brine/go.mod | awk '{print $1, $2}' | sort) | awk '$2!=$3'
# yarn dual resolutions under compatible ranges
grep -E '^"?[a-z@][^"]*@' yarn.lock | sed -E 's/@[^@]*$//; s/^"//' | sort | uniq -d
# patch updates for the allow-list
go list -m -u $(cat .claude/skills/tidy/categories/pins.allowlist) 2>/dev/null | awk '$3{print}'
```

Order: within-file repeated pin (YAML anchor or single `ARG`), then root/brine
version alignment, then yarn dedupe, then one patch bump.

## Predicate

- Within-file pin: the rendered configuration is byte-identical before and
  after (`fly validate-pipeline` output or `helm template` output diffed).
- Alignment: both modules select the same version after `go mod tidy` in each,
  and the higher of the two is a patch step from the lower.
- Bump: the module is on `pins.allowlist`, the step is patch-only, and
  `go.sum` changes only for that module.

## Exclude

- Kubernetes, cloud SDK, auth, OTel, and browser-automation modules; the
  Go toolchain line; Elm packages; anything Renovate groups as major.
- `latest`/floating tags (making them reproducible needs an image build).
- Cross-file consolidation of a pin (needs a build-arg design, not a tidy).
- The brine adapter `replace` directive.

## Gate

Pipeline YAML: `fly -t loupe-local validate-pipeline -c <file>` and a diff of
the rendered config. Chart: `helm template deploy/chart` diffed, plus the chart
tests. Go: `go build ./... && go vet ./...` in the root and in
`atc/worker/jetbridge/brine`. Yarn: `(cd web && yarn install --immutable && yarn run build) && make test-elm`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
