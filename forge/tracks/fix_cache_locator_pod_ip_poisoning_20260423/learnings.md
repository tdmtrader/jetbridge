# Learnings

### 2026-04-23 [project]

**`make test-unit` has 5 pre-existing migration failures unrelated to this track.** Verified by checking out base commit `3b00cbb7b8` (the jetbridge HEAD before this track started) and rerunning `./atc/db/migration/` — same 5 failures reproduce. The failures are in `Legacy Database Upgrade` specs (`preserves all pipeline data`, `Migration idempotency`, `Migration rollback`, `Pre-flight validation script`) and relate to database migration version assertions, not jetbridge code. This track's changes build and pass cleanly:
- `ginkgo ./atc/worker/jetbridge/` — 319/319 pass
- `go test ./atc/worker/jetbridge/` — pass (covers non-Ginkgo tests including `TestNodeIPResolver_IPShapedInputRejected` and `TestDaemonSetBackend_WrapVolumeForLookup_*`)
- `ginkgo ./atc/exec/` — 543/543 pass (exercises `ArtifactFromVolume` → `WrapVolumeForLookup`, which I touched)
- `go build ./...` — clean
