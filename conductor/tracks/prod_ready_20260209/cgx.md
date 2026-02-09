# CGX: Production Readiness Track

## Conductor Growth Experience Notes

_Record insights, friction points, and workflow improvements discovered during this track._

---

### Session Log

- [2026-02-09] **Good Pattern**: The existing test patterns in worker_test.go (counterfeiter fakes, Ginkgo Describe/Context/It) made it straightforward to add new tests. The FakeVolumeRepository, FakeCreatingVolume, FakeCreatedVolume, and FakeWorkerArtifact fakes were already generated and ready to use.
- [2026-02-09] **Good Pattern**: The dual-return-type pattern (ArtifactStoreVolume vs DeferredVolume based on config) mirrors exactly what LookupVolume already does, keeping the codebase consistent.
- [2026-02-09] **Observation**: The `fly execute` flow goes through `atc/api/artifactserver/create.go` -> `Pool.CreateVolumeForArtifact` -> `Worker.CreateVolumeForArtifact`, then immediately calls `StreamIn` on the returned volume. When ArtifactStoreClaim is configured, ArtifactStoreVolume's StreamIn returns an error ("use artifact-helper"). This means `fly execute -i` with artifact store PVC will need additional work to handle the upload path differently — potentially worth a follow-up task.
