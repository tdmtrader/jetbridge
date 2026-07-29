# Task 11 report — Hangar-backed artifact daemon mirror

## Status

**ACCEPTED** — implementation and the single blocking correction are
committed. Blocking-only review round 2 found the round-1 High issue addressed
with no new blocking breakage.

## Behavior

- The artifact daemon commits a verified canonical snapshot archive to Hangar
  before returning a successful PUT response. Local `snapshots/` remains an
  on-use cache, not a durable replica.
- A local cache miss inspects and opens the exact generation-pinned Hangar
  object, verifies the canonical SHA-256 again, and atomically restores the
  cache before serving GET/HEAD.
- Pre-Hangar local snapshots are adopted on use. New ATC uploads receive one
  canonical `hangar-v1` location and stop before contacting peer daemons.
  Existing `jetbridge-daemon-v1` rows remain readable during migration.
- Lifecycle repair treats a Hangar location as one durable authority and
  verifies one cache endpoint; it does not recreate daemon peer replicas.
  Legacy cache cleanup carries an explicit cache-only header, preventing a
  corrupt legacy cache replacement from deleting the Hangar object.
- The daemon supports production GCS through application-default/workload
  identity credentials and an explicit endpoint only for a long-lived
  GCS-compatible emulator. Helm requires the bucket when snapshots are
  enabled, supports daemon ServiceAccount annotations, constrains optional
  Hangar egress to selected TCP/443 peers, exposes existing daemon metrics,
  and adds a Hangar failure alert.

## Files

- Daemon/runtime: `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/server.go`
- ATC snapshot composition/client: `atc/atccmd/command.go`,
  `atc/worker/jetbridge/snapshot_content_store.go`
- Helm: artifact-daemon DaemonSet/RBAC/NetworkPolicy/PrometheusRule, values,
  and chart tests
- Operations: `docs/operations/hangar.md`

## Test evidence

### RED/GREEN retained gap test

- RED: `go test ./cmd/artifact-daemon -run
  'TestSnapshotArtifactPUTCommitsHangarBeforeSuccessAndGETRestoresLostLocalCache'
  -count=1` initially failed at compile time because `Server.SetHangarStore`
  did not exist.
- GREEN: the same command passed after the daemon Hangar integration.

The later semantic-rebase guidance was followed: existing daemon and
Jetbridge behavioral coverage was adapted where it expressed the retained
contract, and new tests were added only for load-bearing gaps:

- New daemon cache-loss/durable-PUT regression.
- New canonical Hangar-location/no-peer-mirror regression.
- New single-authority lifecycle-repair regression.
- Existing strict location deletion coverage extended to prove legacy cleanup
  is cache-only.
- Current-style Helm tests added/adapted for required bucket, emulator args,
  workload identity annotation, narrow egress, and alert metric wiring.

Focused verification passed:

```text
go test ./cmd/artifact-daemon -run TestSnapshotArtifactPUTCommitsHangarBeforeSuccessAndGETRestoresLostLocalCache -count=1 -v
PASS
go test ./atc/worker/jetbridge -run TestSnapshotContentStore(PutUsesOneHangarLocationAndDoesNotMirrorToPeers|RepairTreatsHangarAsOneDurableLocation|ExistsCryptographicallyVerifiesAndDeletesUseStrictStableLocation) -count=1 -v
PASS
go test ./deploy/chart/tests -run TestAgentSnapshotsRequireHangarBucketAndWireDaemonGCSArguments -count=1 -v
PASS
```

Checkpoint verification passed for the daemon, ATC composition, chart, and
focused Task 11 Jetbridge behavior:

```text
go test ./cmd/artifact-daemon -count=1
go test ./atc/atccmd -count=1
go test ./atc/worker/jetbridge -run 'TestSnapshotContentStore(PutUsesOneHangarLocationAndDoesNotMirrorToPeers|RepairTreatsHangarAsOneDurableLocation|ExistsCryptographicallyVerifiesAndDeletesUseStrictStableLocation)' -count=1
go test ./deploy/chart/tests -count=1
helm lint deploy/chart
```

Helm lint passed with the pre-existing informational missing-helper-image
validation messages and the informational chart icon recommendation.

The full Jetbridge package checkpoint was also run after the accepted fix. It
failed 179 of 380 specs for the already cataloged Task 6 zero-private-mount
cause, `cannot bind incomplete private task mounts to pod`; 201 specs passed.
No Task 11-focused snapshot content-store test failed.

## Integration and environment limitation

`make test-hangar-integration` was attempted for the fake-GCS acceptance
target but stopped immediately with `ERROR: a running Docker daemon is
required`. No retry was performed. Task 10's accepted Borg run already
verified the identical Hangar GCS/fake-GCS core. This task's cache-loss
behavior is covered by the daemon HTTP integration regression; no repository
source or source-derived image was uploaded to Borg or a registry.

## Self-review

- Confirmed one durable `hangar-v1` location for new snapshots and no agentic
  peer fan-out.
- Confirmed cache-only legacy deletion cannot remove the durable object.
- Confirmed snapshot archive, digest, local symlink safety, signing and
  secret-mount boundaries remain untouched; no private-mount work from Tasks
  6, 7, or 9 was used or modified.
- `git diff --check` passed before commit.

## Deferred observations

- `DEFERRED-005`: a failure after Hangar commits but before durable location
  recording can leave an unreachable immutable object. This is a
  retention/cost concern, not Task 11 data loss.
- `DEFERRED-006`: Hangar egress is narrow when the optional daemon egress
  NetworkPolicy is enabled; clusters disabling that policy do not receive the
  enforcement.
- The unavailable local Docker daemon is recorded above as an environment
  limitation, not a product deferment.

## Commit

- `685d09104e feat(hangar): mirror agent snapshots through daemon`

## Fix round 1

### Finding and root cause

The full cache-root loss path returned 404 before Hangar recovery. The
strengthened behavioral regression changed its fault injection from removing
`<storage-path>/snapshots` to removing the entire `<storage-path>`. The RED
command and output were:

```text
go test ./cmd/artifact-daemon -run TestSnapshotArtifactPUTCommitsHangarBeforeSuccessAndGETRestoresLostLocalCache -count=1 -v
GET after cache loss = 404/"not found\\n", want 200/"durable snapshot cache-loss recovery"
FAIL
```

`openSnapshotForRead` handled `os.OpenRoot(storagePath) == ErrNotExist` as an
immediate 404, so `restoreSnapshotFromHangar`—which already creates the root
directory—was unreachable.

### Correction

Both a missing cache root and a missing cache entry now share the same
Hangar-restore response path. When Hangar restores the verified archive, the
daemon reopens the root and serves it; it returns 404 only when Hangar reports
the durable object absent. No retention, NetworkPolicy, or other deferred
observation was changed.

### Verification

```text
go test ./cmd/artifact-daemon -run TestSnapshotArtifactPUTCommitsHangarBeforeSuccessAndGETRestoresLostLocalCache -count=1 -v
PASS
go test ./cmd/artifact-daemon -count=1
PASS
git diff --check
PASS
```

### Commit

- `979ca6490a fix(hangar): restore snapshots after cache-root loss`

## Independent review

- Round 1: one High blocker. A missing entire cache root returned 404 before
  reaching Hangar restore; the regression removed only `snapshots/`.
- Correction: `979ca6490a` made missing roots use verified restoration and
  strengthened the test to remove the entire storage root.
- Round 2: prior blocker **ADDRESSED**; no new Critical, High, or blocking
  issue.
- Verdict: **ACCEPTED**.
