# Implementation Plan: file-config read after producer pod reap

## Phase 0: Confirm the root cause (read-only)

- [x] Trace whether get-step output volumes are registered/mirrored with the
      DaemonSet. FINDING: they ARE — get Dir volume is hostPath-backed at
      steps/{handle}/dir, RecordOutputs matches it (Get != Task/Check) and
      registers a daemon alias under ArtifactKey({handle}-dir), and get_step.go:302
      wraps it via ArtifactFromVolume. Pure-H1 refuted. (see cgx.md Phase 0 finding)
- [x] Inspect `ArtifactFromVolume(...).StreamOut`. FINDING: ArtifactFromVolume →
      WrapVolumeForLookup → *DaemonSetVolume, whose StreamOut is HTTP-ONLY and
      NEVER execs — so it cannot reach executor.go:122. BUT WrapVolumeForLookup
      creates the volume WITHOUT a daemonClient (unlike WrapVolumeForArtifact),
      so it has no peer-fallback / daemon-discovery recovery.
- [x] Decide H1 vs H2. DECISION (provisional, needs CI confirm): neither pure form —
      it's a routing-resilience gap. The lookup-wrapped DaemonSetVolume lacks a
      daemonClient, so its read depends entirely on locator sourceNode + NodeIP
      resolution + a best-effort alias POST; any failure hard-fails with no recovery.
      The "exec stream" text is the test's own assertion guess, not a confirmed error.
      BLOCKER: confirm exact failure from CI build #181 diagnostics (fly token invalid).

## Phase 1: Reproduce / confirm the real failure (RED)

- [x] CONFIRMED via CI build #181 diagnostics: the build errors DURING THE GET
      STEP ("pod deleted externally before reaching Running: Pending"), failing in
      ~6.5s (< the 45s `hold`). The premise (artifact-routing exec) was WRONG — it's
      a TEST RACE: `deleteProducerPod` deleted the get pod mid-fetch. (cgx.md "CI
      CONFIRMATION")
- [x] Reproduced the daemonClient-gap (resilience) via a unit test:
      `TestDaemonSetBackend_WrapVolumeForLookup_SetsDaemonClient` fails before the
      fix (lookup-wrapped volume had nil daemonClient → no peer-fallback).

## Phase 2: Fix (GREEN)

- [x] Test race fix (`topgun/k8s/integration/artifact_read_after_reap_test.go`):
      gate producer-pod deletion on the intermediate step (`hold`/`bystander`)
      being Running (proves the producer completed), and target the producer by
      `concourse.ci/step` label (cross-step has 3 type=task pods). Both specs.
- [x] Resilience fix (`atc/worker/jetbridge/storage_daemonset.go` WrapVolumeForLookup):
      wire `SetDaemonClient` on the returned volume(s) so lookup-wrapped reads
      (web-process file-config StreamOut) get peer-fallback / daemon discovery,
      matching WrapVolumeForArtifact.

## Phase 3: Verify

- [x] Local: `go vet ./topgun/k8s/integration/` + `go build ./atc/worker/jetbridge/`
      pass; `go test ./atc/worker/jetbridge/` (storage/volume/worker) green incl. the
      two new WrapVolumeForLookup tests.
- [ ] CI: push + trigger `k8s-e2e/k8s-integration-tests`; spec "loads a file-based
      task config even after the producing get pod has been reaped" must now exit 0
      (and take >45s, proving `hold` ran). CI builds from fresh `repo` (no tag bump).
- [ ] Confirm no regression in the sibling cross-step / get / task specs.
