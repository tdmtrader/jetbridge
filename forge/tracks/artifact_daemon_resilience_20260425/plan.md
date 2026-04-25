# Implementation Plan: Artifact Daemon Resilience

> Phases ship in order. Each phase is independently reviewable but P2 depends
> on P1 (read fallback is the only thing that makes a mirrored copy useful).

## Phase 1: ATC-side read fallback

Foundation. Plumbs peer-probe fallback into the artifact read path. No
behavior change today (no peer ever has the data) — becomes the recovery
path the moment Phase 2 lands.

- [~] Task: Write failing unit test —
      `DaemonClient.ProbeStepArtifact(ctx, "h/o")` discovers daemon IPs
      via fake EndpointSlices, sends concurrent HEAD `/artifacts/steps/h/o`,
      returns the IP of the first 200, returns `("", false, nil)` when no
      daemon has it. Cover: 0 daemons, 1 daemon hit, 1 daemon miss, all-miss,
      one-hit-one-error.
      File: `atc/worker/jetbridge/daemon_client_test.go`.

- [ ] Task: Implement `DaemonClient.ProbeStepArtifact` modeled on
      `ProbeResourceCache`. Concurrent HEAD with cancel-on-first-hit.
      File: `atc/worker/jetbridge/daemon_client.go`.

- [ ] Task: Write failing unit test — `DaemonSetVolume.StreamOut` with
      a recorded node that returns connection-refused falls back to
      `ProbeStepArtifact`, succeeds from the discovered IP. Use
      `httptest` for both producer (refuses) and peer (serves) daemons.
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [ ] Task: Write failing unit test — `DaemonSetVolume.StreamOut`
      fallback returns the original "not found" error when
      `ProbeStepArtifact` finds nothing.
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [ ] Task: Write passing-stays-passing unit test — happy-path
      `StreamOut` with live recorded node performs ZERO peer probes
      (assert via fake daemon hit count).
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [ ] Task: Implement peer-fallback in `DaemonSetVolume.StreamOut`.
      Decision tree: connection error → probe; `ErrNodeNameIsIP` →
      probe; HTTP 4xx/5xx → probe; success → return. Probe budget:
      5s inherited from existing patterns.
      File: `atc/worker/jetbridge/volume_daemonset.go`.

- [ ] Task: Run `ginkgo ./atc/worker/jetbridge/...` and confirm all
      previously-red tests are green and no existing tests regressed.

- [ ] Task: Phase 1 Manual Verification

---

## Phase 2: Async background mirror

The main event. Adds the mirror endpoint, worker pool, peer selection,
and ATC-side triggers.

### Phase 2a: Daemon-side mirror subsystem

- [ ] Task: Write failing unit test — `mirror.NewWorkerPool(concurrency=2)`
      enforces a max of 2 in-flight jobs (assert via blocking-on-channel
      fakes), drains cleanly on `Stop()`, and rejects new jobs after
      drain begins.
      File: `cmd/artifact-daemon/mirror_test.go` (new file).

- [ ] Task: Write failing unit test — `peerSelector.Select(key, peers, rf)`
      with consistent hashing: same key → same subset across calls;
      `rf=-1` returns all peers; `rf=2` with 1 peer returns that 1
      peer; `rf=2` with 0 peers returns empty without error;
      `rf=0` returns empty.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [ ] Task: Write failing unit test — `mirrorJob.Run` tars the local
      `steps/h/o/` dir and PUTs to each chosen peer's `/stream-in/h/o`,
      records per-peer outcome, never returns an error to the caller
      (best-effort). Use `httptest` peers including one that rejects
      and one that times out.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [ ] Task: Implement `cmd/artifact-daemon/mirror.go`:
      - `WorkerPool` (bounded goroutine pool with shutdown)
      - `peerSelector` (consistent hashing via fnv64)
      - `mirrorJob` (tar local dir, PUT to peers, log outcomes,
        update in-memory `mirrorStatus` map for Phase 3 evacuation)
      - `Mirror.Trigger(key)` public API.
      File: `cmd/artifact-daemon/mirror.go` (new file).

- [ ] Task: Write failing unit test — `POST /mirror {"key":"h/o"}`
      returns 202 immediately, enqueues a job. Empty key returns 400.
      File: `cmd/artifact-daemon/server_test.go`.

- [ ] Task: Implement `handleMirrorTrigger` and wire it into the
      `protect()` mTLS path on the mux.
      File: `cmd/artifact-daemon/server.go`.

- [ ] Task: Write failing unit test — `handleStreamIn` schedules a
      mirror job after extraction (assert via fake `Mirror`).
      File: `cmd/artifact-daemon/server_test.go`.

- [ ] Task: Hook `Mirror.Trigger(key)` after `s.registry.Register`
      in `handleStreamIn`.
      File: `cmd/artifact-daemon/server.go`.

- [ ] Task: Add daemon flags `--mirror-replicas` (default `2`,
      special `-1` = all), `--mirror-concurrency` (default `4`),
      `--mirror-timeout` (default `5m`). Wire `Mirror` into `main.go`
      after `PeerResolver` is created.
      File: `cmd/artifact-daemon/main.go`.

### Phase 2b: ATC-side triggers

- [ ] Task: Write failing unit test — `DaemonClient.TriggerMirror`
      POSTs `/mirror` with the right JSON body to the right daemon
      IP, returns nil on 202, returns no-error-but-logs on connect
      failure (best-effort).
      File: `atc/worker/jetbridge/daemon_client_test.go`.

- [ ] Task: Implement `DaemonClient.TriggerMirror(ctx, daemonIP, key)`.
      File: `atc/worker/jetbridge/daemon_client.go`.

- [ ] Task: Write failing unit test — `RecordOutputs` calls
      `daemonClient.TriggerMirror` for each output AFTER
      `registerDaemonAlias` succeeds. Trigger failure does NOT
      bubble up.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [ ] Task: Wire `TriggerMirror` into `RecordOutputs` after
      `registerDaemonAlias`. Best-effort logging on error.
      File: `atc/worker/jetbridge/storage_daemonset.go`.

- [ ] Task: Write failing unit test — `RegisterResourceCache`
      calls `TriggerMirror` BEFORE `RegisterAlias`. Use a recording
      fake to assert call ordering.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [ ] Task: Wire `TriggerMirror` call into `RegisterResourceCache`
      ahead of `RegisterAlias`.
      File: `atc/worker/jetbridge/storage_daemonset.go`.

### Phase 2c: Helm + chart

- [ ] Task: Add `artifactDaemon.mirror` block to
      `deploy/chart/values.yaml` with default `replicas: 2`,
      `concurrency: 4`, `timeout: "5m"`.
      File: `deploy/chart/values.yaml`.

- [ ] Task: Plumb the new values into the daemon container args
      in `deploy/chart/templates/artifact-daemon-daemonset.yaml`.
      Support string `"all"` → `-1` translation in the template.
      File: `deploy/chart/templates/artifact-daemon-daemonset.yaml`.

- [ ] Task: Add `helm template` smoke checks: `replicas: 0`,
      `replicas: 2`, `replicas: "all"` all render valid YAML and
      pass through to the container args correctly.
      File: extend `deploy/chart` test fixtures or add a Go test
      under `deploy/chart/test/`.

### Phase 2d: Behavioral

- [ ] Task: Extend `cmd/artifact-daemon/behavioral_cross_node_test.go`
      with `Mirror_AfterStepStreamIn_PeerServesAfterProducerDeath`:
      stream in to producer, wait for mirror to settle, kill producer,
      GET from peer succeeds.
      File: `cmd/artifact-daemon/behavioral_cross_node_test.go`.

- [ ] Task: Add jetbridge-side integration test —
      `RecordOutputs` → mirror trigger → peer holds the data; remove
      producer's daemon endpoint; downstream `StreamOut` peer-probes
      and reads from the survivor.
      File: `atc/worker/jetbridge/daemonset_integration_test.go`.

- [ ] Task: Run `make test-unit`, `ginkgo ./atc/worker/jetbridge/...`,
      `go test ./cmd/artifact-daemon/...`. All pass.

- [ ] Task: Phase 2 Manual Verification

---

## Phase 3: GCP preemption-triggered evacuation

Closes the async-mirror window for graceful spot preemption. Crash
recovery is intentionally out of scope — those builds rerun.

- [ ] Task: Write failing unit test —
      `preemption.Watcher.Run(ctx)` long-polls
      `metadata.google.internal/computeMetadata/v1/instance/preempted?wait_for_change=true`
      with `Metadata-Flavor: Google` header, fires its callback once
      when the response transitions to `TRUE`. Use a fake metadata
      `httptest` server.
      File: `cmd/artifact-daemon/preemption_test.go` (new file).

- [ ] Task: Write failing unit test — watcher logs and retries on
      transient metadata server errors without exiting.
      File: `cmd/artifact-daemon/preemption_test.go`.

- [ ] Task: Implement `cmd/artifact-daemon/preemption.go`:
      `Watcher` struct, `Run(ctx)` long-poll loop, `OnPreempted`
      callback hook.
      File: `cmd/artifact-daemon/preemption.go` (new file).

- [ ] Task: Write failing unit test — `Mirror.Evacuate(ctx, budget)`:
      stops accepting new jobs, iterates over local step dirs whose
      `mirrorStatus` is missing or partial, fans out PUTs within the
      budget, returns when budget elapses.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [ ] Task: Implement `Mirror.Evacuate` and `Mirror.Drain` (called
      by the preemption callback). Per-peer timeout = 5s; total
      budget = 25s by default.
      File: `cmd/artifact-daemon/mirror.go`.

- [ ] Task: Add daemon flags `--preemption-watch` (default `false`),
      `--preemption-budget` (default `25s`). Wire watcher into
      `main.go` so its `OnPreempted` callback invokes
      `Mirror.Evacuate`.
      File: `cmd/artifact-daemon/main.go`.

- [ ] Task: Add `artifactDaemon.preemption` block to
      `deploy/chart/values.yaml` (default `enabled: false`,
      `budget: "25s"`); plumb into DaemonSet args.
      Files: `deploy/chart/values.yaml`,
      `deploy/chart/templates/artifact-daemon-daemonset.yaml`.

- [ ] Task: Behavioral test — fake metadata server fires preempt
      mid-mirror; `Evacuate` flushes the unmirrored artifact to a
      peer before the budget expires.
      File: `cmd/artifact-daemon/preemption_test.go`.

- [ ] Task: Run all test suites (`make test-unit`,
      `ginkgo ./atc/worker/jetbridge/...`,
      `go test ./cmd/artifact-daemon/...`,
      `helm template deploy/chart`).

- [ ] Task: Phase 3 Manual Verification
