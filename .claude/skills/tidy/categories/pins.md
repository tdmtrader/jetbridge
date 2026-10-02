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

Pipeline YAML: `fly -t home validate-pipeline -c <file>` and a diff of
the rendered config. Chart: `helm template deploy/chart` diffed, plus the chart
tests. Go: `go build ./... && go vet ./...` in the root and in
`atc/worker/jetbridge/brine`. Yarn: `(cd web && yarn install --immutable && yarn run build) && make test-elm`.

## Log

| date | instance | outcome | note |
|---|---|---|---|
| 2026-09-24 | concourse-test-runner:v11@deploy/concourse-pipeline.yml | gate-failed | `make test-quick` red before the card's own gate, in `./atc/worker/jetbridge`: `exact_execution_test.go:545` ("the unwitnessed command was not cancelled before dispatch", `stat /tmp/concourse-resource-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa-1/cancel: no such file or directory") and, on one run, `exact_execution_test.go:420` (expected 0 to equal 1). Both reproduce on unmodified origin/core in this clone under `ginkgo -p` -- three full-suite runs on the clean tree, two red on those specs, one green -- so the cause is not this diff. The fixed fake UUID in that `/tmp` path is shared by every spec in the file, so parallel specs collide on one directory. The tidy itself was sound and is reverted: an `&test_runner_image` anchor on the first of eleven identical `rootfs_uri` pins with aliases on the other ten, `fly validate-pipeline -o` byte-identical (54617 bytes both ways), `hack/ci-extract-task` resolving the alias, chart tests and `go test .` green. Blocks this card until the collision is fixed |
| 2026-10-02 | concourse-kind-runner:v35@deploy/k8s-e2e-pipeline.yml | tidied | Four byte-identical `rootfs_uri` pins inside one file (`deploy/k8s-e2e-pipeline.yml` lines 189, 369, 535, 645): a `&kind_runner_image` anchor on the first with aliases on the other three. `fly -t home validate-pipeline -c` says "looks good" and its `-o` render is byte-identical, 29627 bytes both ways; `hack/ci-extract-task` resolves the alias for all four jobs (`hangar-generated-pod-contract`, `hangar-disk-store-contract`, `k8s-integration-tests`, `k8s-behavioral-tests`). The same pin also sits in `deploy/review-kubelet-task.yml:4` and `deploy/run-kubelet-task.yml:8`; both were left alone, since cross-file consolidation is on `Exclude`. `make test-quick` green in all 78 suites plus 3111 Elm tests, `go test .` and the hangar architecture tests green. The 2026-09-24 block is lifted: `atc/worker/jetbridge/exact_execution_test.go:88` now derives its execution ID from `os.Getpid()`, so parallel ginkgo processes no longer share one `/tmp` directory |
