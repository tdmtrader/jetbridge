# CGX — K8s Behavioral Integration Test Suite

## Frustrations & Friction

- [2026-02-15] Plan was 321 tasks all marked [ ] despite 316 It blocks already written — massive plan drift makes status unreliable
- [2026-02-15] Cluster "Too many pods" failures on single-node clusters mask real test issues — need better pod cleanup or multi-node KinD config
- [2026-02-15] Fresh PVCs don't have artifact subdirectories (`/artifacts/artifacts/`, `/artifacts/caches/`) — caused all builds to fail on new clusters until sidecar init was fixed
- [2026-02-15] Dockerfile.build fails on ARM64 due to Elm dependency — had to cross-compile Go binary and use Dockerfile.local instead
- [2026-02-15] AI-generated tests had 15 quality issues across 4 categories (YAML bugs, no-op assertions, fragile patterns, flaky timeouts) — systematic review via FAILURES.md was essential
- [2026-02-15] Task-level `vars:` only works with `file:`-based task configs, not inline — common misunderstanding led to test 5.15 failure
- [2026-02-15] Mock resource `create_files` with nested paths (`ci/task.yml`) may fail — simplify to root-level paths for reliability

## Good Patterns

- [2026-02-15] TestMain for cluster lifecycle is cleaner than SynchronizedBeforeSuite — stdlib error handling, no Ginkgo dependency, env vars as the config interface between TestMain and the suite
- [2026-02-15] Pre-pulling all images (mock-resource, busybox, postgres) in loadImagesIntoKind avoids rate limits and network issues during test runs
- [2026-02-15] FAILURES.md as a structured test quality document — category-based triage (A: definite, B: no-op, C: fragile, D: flaky) prioritizes fixes effectively
- [2026-02-15] `crictl inspecti -q` to check image presence before pulling avoids DNS failures on reuse
- [2026-02-15] Pipeline-scoped pod label filtering (`concourse.ci/pipeline=<name>`) prevents cross-test interference
- [2026-02-15] `SKIP_TEARDOWN=1` + KinD cluster reuse enables fast iterative development
- [2026-02-15] Cross-compile workflow: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build` + `Dockerfile.local` + `kind load` is much faster than full Dockerfile.build

## Anti-Patterns

- [2026-02-15] Tests with no assertions (`_ = sess`) — always add at least an exit code check
- [2026-02-15] Using `PIt` as a catch-all for untested scenarios — pending tests should still have correct test logic so they can be enabled with confidence
- [2026-02-15] Relying on `dd if=/dev/zero of=/dev/null` for OOM testing — overcommit-enabled kernels won't trigger OOM without physical page consumption
- [2026-02-15] Single-shot `nc -l -p` for sidecar connectivity tests — use a loop or a proper HTTP server

## Missing Capabilities

- [2026-02-15] No way to automatically validate that PIt tests would pass if enabled — need a CI mode that runs pending tests and reports
- [2026-02-15] No configurable per-test timeout — `EVENTUALLY_TIMEOUT` is global; some tests need longer timeouts than others

## Improvement Candidates

- [2026-02-15] Create a `conductor:test-quality-audit` skill that scans test files for common anti-patterns (no assertions, PIt with `_ =`, incorrect YAML schemas)
- [2026-02-15] Add a `conductor:rebuild-image` skill that handles the cross-compile + kind load + pod restart cycle
