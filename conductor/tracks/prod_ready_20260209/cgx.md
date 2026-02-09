# CGX: Production Readiness Track

## Conductor Growth Experience Notes

_Record insights, friction points, and workflow improvements discovered during this track._

---

### Session Log

- [2026-02-09] **Good Pattern**: The existing test patterns in worker_test.go (counterfeiter fakes, Ginkgo Describe/Context/It) made it straightforward to add new tests. The FakeVolumeRepository, FakeCreatingVolume, FakeCreatedVolume, and FakeWorkerArtifact fakes were already generated and ready to use.
- [2026-02-09] **Good Pattern**: The dual-return-type pattern (ArtifactStoreVolume vs DeferredVolume based on config) mirrors exactly what LookupVolume already does, keeping the codebase consistent.
- [2026-02-09] **Observation**: The `fly execute` flow goes through `atc/api/artifactserver/create.go` -> `Pool.CreateVolumeForArtifact` -> `Worker.CreateVolumeForArtifact`, then immediately calls `StreamIn` on the returned volume. When ArtifactStoreClaim is configured, ArtifactStoreVolume's StreamIn returns an error ("use artifact-helper"). This means `fly execute -i` with artifact store PVC will need additional work to handle the upload path differently — potentially worth a follow-up task.
- [2026-02-09] **Frustration**: The existing `Dockerfile.local` relies on `concourse/dev` base image (not publicly described) and `CONCOURSE_WEB_PUBLIC_DIR` volume mount pattern that doesn't work in K8s without a volume mount. The root cause of the broken deployment was that Go's `//go:embed public` embeds whatever is in `web/public/` at build time — if assets aren't built before `go build`, the binary serves empty pages.
- [2026-02-09] **Good Pattern**: Multi-stage Dockerfile with node -> go -> runtime stages keeps the build self-contained. No external CI pipeline needed — `docker build -f Dockerfile.build .` does everything.
- [2026-02-09] **Good Pattern**: Basing the Helm chart on the official concourse-chart structure (values.yaml sections, helper templates, resource naming) but removing the worker StatefulSet makes it familiar to existing Concourse users while reflecting JetBridge's architecture.
- [2026-02-09] **Good Pattern**: The integration_test.go pattern (fakeExecExecutor + fake K8s clientset + DB fakes) is highly reusable for testing new workflows. Adding artifact integration tests was mostly wiring up existing fakes in new combinations.
- [2026-02-09] **Observation**: When ArtifactStoreClaim is configured, container.Run triggers artifact-helper sidecar exec calls (tar commands) in addition to the main command exec. Tests that assert on exec call counts need to account for this — the first exec is always the main command, subsequent ones are artifact-helper tar operations.
- [2026-02-09] **Good Pattern**: The artifact store is storage-agnostic — it works with any PVC (hostPath, NFS, GCS FUSE). The mechanism is purely init containers (tar xf from PVC → emptyDir) and sidecar exec (tar cf from emptyDir → PVC). This means E2E validation doesn't require GCS FUSE specifically.
- [2026-02-09] **Bug Found**: `Volume.StreamOut` had a data race — `spanErr` was shared between the calling goroutine's deferred `tracing.End` and the background goroutine that writes to it after exec completes. Fix: move `tracing.End` into the goroutine that owns the span. Discovered by running `-race` on the full suite during Phase 3 gap audit.
- [2026-02-09] **Good Pattern**: The `failingArtifact` test double (returns errors from `StreamOut`) complements the existing `fakeArtifact` (always succeeds). Together they cover both happy and error paths of `streamInputs` in non-artifact-store mode.
- [2026-02-09] **Observation**: Phase 3 scenarios (load, failure, soak testing) are largely covered by unit tests — 8/10 scenarios already tested. The two gaps (streaming input failures, concurrent operations) were straightforward to add. The `-race` detector run was the most valuable — it caught a real pre-existing bug.
