# Implementation Plan: Artifact Daemon Resilience

> Phases ship in order. Each phase is independently reviewable but P2 depends
> on P1 (read fallback is the only thing that makes a mirrored copy useful).

## Phase 1: ATC-side read fallback [checkpoint: 9a636976ba]

Foundation. Plumbs peer-probe fallback into the artifact read path. No
behavior change today (no peer ever has the data) — becomes the recovery
path the moment Phase 2 lands.

- [x] Task: Write failing unit test — 0c77cfb96d
      `DaemonClient.ProbeStepArtifact(ctx, "h/o")` discovers daemon IPs
      via fake EndpointSlices, sends concurrent HEAD `/artifacts/steps/h/o`,
      returns the IP of the first 200, returns `("", false, nil)` when no
      daemon has it. Cover: 0 daemons, 1 daemon hit, 1 daemon miss, all-miss,
      one-hit-one-error.
      File: `atc/worker/jetbridge/daemon_client_test.go`.

- [x] Task: Implement `DaemonClient.ProbeStepArtifact` modeled on 55c8205b00
      `ProbeResourceCache`. Concurrent HEAD with cancel-on-first-hit.
      File: `atc/worker/jetbridge/daemon_client.go`.

- [x] Task: Write failing unit test — `DaemonSetVolume.StreamOut` with 9719c9933e
      a recorded node that returns connection-refused falls back to
      `ProbeStepArtifact`, succeeds from the discovered IP. Use
      `httptest` for both producer (refuses) and peer (serves) daemons.
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [x] Task: Write failing unit test — `DaemonSetVolume.StreamOut` 9719c9933e
      fallback returns the original "not found" error when
      `ProbeStepArtifact` finds nothing.
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [x] Task: Write passing-stays-passing unit test — happy-path 9719c9933e
      `StreamOut` with live recorded node performs ZERO peer probes
      (assert via fake daemon hit count).
      File: `atc/worker/jetbridge/volume_daemonset_test.go`.

- [x] Task: Implement peer-fallback in `DaemonSetVolume.StreamOut`. ca3f9f551f
      Decision tree: connection error → probe; `ErrNodeNameIsIP` →
      probe; HTTP 4xx/5xx → probe; success → return. Probe budget:
      5s inherited from existing patterns.
      File: `atc/worker/jetbridge/volume_daemonset.go`.

- [x] Task: Run `ginkgo ./atc/worker/jetbridge/...` and confirm all ca3f9f551f
      previously-red tests are green and no existing tests regressed.

- [x] Task: Phase 1 Manual Verification

---

## Phase 2: Async background mirror [checkpoint: e7b76f9808]

The main event. Adds the mirror endpoint, worker pool, peer selection,
and ATC-side triggers.

### Phase 2a: Daemon-side mirror subsystem

- [x] Task: Write failing unit test — `mirror.NewWorkerPool(concurrency=2)` 83078e9b0d
      enforces a max of 2 in-flight jobs (assert via blocking-on-channel
      fakes), drains cleanly on `Stop()`, and rejects new jobs after
      drain begins.
      File: `cmd/artifact-daemon/mirror_test.go` (new file).

- [x] Task: Write failing unit test — `peerSelector.Select(key, peers, rf)` 68cb14c5c0
      with consistent hashing: same key → same subset across calls;
      `rf=-1` returns all peers; `rf=2` with 1 peer returns that 1
      peer; `rf=2` with 0 peers returns empty without error;
      `rf=0` returns empty.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [x] Task: Write failing unit test — `mirrorJob.Run` tars the local 8f3c980770
      `steps/h/o/` dir and PUTs to each chosen peer's `/stream-in/h/o`,
      records per-peer outcome, never returns an error to the caller
      (best-effort). Use `httptest` peers including one that rejects
      and one that times out.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [x] Task: Implement `cmd/artifact-daemon/mirror.go`: 541877a0cc
      - `WorkerPool` (bounded goroutine pool with shutdown)
      - `peerSelector` (consistent hashing via fnv64)
      - `mirrorJob` (tar local dir, PUT to peers, log outcomes,
        update in-memory `mirrorStatus` map for Phase 3 evacuation)
      - `Mirror.Trigger(key)` public API.
      File: `cmd/artifact-daemon/mirror.go` (new file).

- [x] Task: Write failing unit test — `POST /mirror {"key":"h/o"}` ebd59bc216
      returns 202 immediately, enqueues a job. Empty key returns 400.
      File: `cmd/artifact-daemon/server_test.go`.

- [x] Task: Implement `handleMirrorTrigger` and wire it into the a043ba7b9a
      `protect()` mTLS path on the mux.
      File: `cmd/artifact-daemon/server.go`.

- [x] Task: Write failing unit test — `handleStreamIn` schedules a 8282f253b1
      mirror job after extraction (assert via fake `Mirror`).
      File: `cmd/artifact-daemon/server_test.go`.

- [x] Task: Hook `Mirror.Trigger(key)` after `s.registry.Register` d978246563
      in `handleStreamIn`.
      File: `cmd/artifact-daemon/server.go`.

- [x] Task: Add daemon flags `--mirror-replicas` (default `2`, 35c9ca9dab
      special `-1` = all), `--mirror-concurrency` (default `4`),
      `--mirror-timeout` (default `5m`). Wire `Mirror` into `main.go`
      after `PeerResolver` is created.
      File: `cmd/artifact-daemon/main.go`.

### Phase 2b: ATC-side triggers

- [x] Task: Write failing unit test — `DaemonClient.TriggerMirror` a957c9cee2
      POSTs `/mirror` with the right JSON body to the right daemon
      IP, returns nil on 202, returns no-error-but-logs on connect
      failure (best-effort).
      File: `atc/worker/jetbridge/daemon_client_test.go`.

- [x] Task: Implement `DaemonClient.TriggerMirror(ctx, daemonIP, key)`. 7044b4084d
      File: `atc/worker/jetbridge/daemon_client.go`.

- [x] Task: Write failing unit test — `RecordOutputs` calls 632c5c85af
      `daemonClient.TriggerMirror` for each output AFTER
      `registerDaemonAlias` succeeds. Trigger failure does NOT
      bubble up.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Wire `TriggerMirror` into `RecordOutputs` after f09bbef5a0
      `registerDaemonAlias`. Best-effort logging on error.
      File: `atc/worker/jetbridge/storage_daemonset.go`.

- [x] Task: Write failing unit test — `RegisterResourceCache` 0787f36eeb
      calls `TriggerMirror` BEFORE `RegisterAlias`. Use a recording
      fake to assert call ordering.
      File: `atc/worker/jetbridge/storage_daemonset_test.go`.

- [x] Task: Wire `TriggerMirror` call into `RegisterResourceCache` f42f95348b
      ahead of `RegisterAlias`.
      File: `atc/worker/jetbridge/storage_daemonset.go`.

### Phase 2c: Helm + chart

- [x] Task: Add `artifactDaemon.mirror` block to 4f41f4e311
      `deploy/chart/values.yaml` with default `replicas: 2`,
      `concurrency: 4`, `timeout: "5m"`.
      File: `deploy/chart/values.yaml`.

- [x] Task: Plumb the new values into the daemon container args 4f41f4e311
      in `deploy/chart/templates/artifact-daemon-daemonset.yaml`.
      Support string `"all"` → `-1` translation in the template.
      File: `deploy/chart/templates/artifact-daemon-daemonset.yaml`.

- [x] Task: Add `helm template` smoke checks: `replicas: 0`, 4f41f4e311
      `replicas: 2`, `replicas: "all"` all render valid YAML and
      pass through to the container args correctly.
      File: extend `deploy/chart` test fixtures or add a Go test
      under `deploy/chart/test/`.

### Phase 2d: Behavioral

- [x] Task: Extend `cmd/artifact-daemon/behavioral_cross_node_test.go` cdd1a99eb7
      with `Mirror_AfterStepStreamIn_PeerServesAfterProducerDeath`:
      stream in to producer, wait for mirror to settle, kill producer,
      GET from peer succeeds.
      File: `cmd/artifact-daemon/behavioral_cross_node_test.go`.

- [x] Task: Add jetbridge-side integration test — d175af487e
      `RecordOutputs` → mirror trigger → peer holds the data; remove
      producer's daemon endpoint; downstream `StreamOut` peer-probes
      and reads from the survivor.
      File: `atc/worker/jetbridge/daemonset_integration_test.go`.

- [x] Task: Run `make test-unit`, `ginkgo ./atc/worker/jetbridge/...`, d175af487e
      `go test ./cmd/artifact-daemon/...`. All pass.

- [x] Task: Phase 2 Manual Verification

---

## Phase 3: GCP preemption-triggered evacuation

Closes the async-mirror window for graceful spot preemption. Crash
recovery is intentionally out of scope — those builds rerun.

- [x] Task: Write failing unit test — 0cd933def0
      `preemption.Watcher.Run(ctx)` long-polls
      `metadata.google.internal/computeMetadata/v1/instance/preempted?wait_for_change=true`
      with `Metadata-Flavor: Google` header, fires its callback once
      when the response transitions to `TRUE`. Use a fake metadata
      `httptest` server.
      File: `cmd/artifact-daemon/preemption_test.go` (new file).

- [x] Task: Write failing unit test — watcher logs and retries on 0cd933def0
      transient metadata server errors without exiting.
      File: `cmd/artifact-daemon/preemption_test.go`.

- [x] Task: Implement `cmd/artifact-daemon/preemption.go`: 1fe584bec5
      `Watcher` struct, `Run(ctx)` long-poll loop, `OnPreempted`
      callback hook.
      File: `cmd/artifact-daemon/preemption.go` (new file).

- [x] Task: Write failing unit test — `Mirror.Evacuate(ctx, budget)`: 7cc581b7b2
      stops accepting new jobs, iterates over local step dirs whose
      `mirrorStatus` is missing or partial, fans out PUTs within the
      budget, returns when budget elapses.
      File: `cmd/artifact-daemon/mirror_test.go`.

- [x] Task: Implement `Mirror.Evacuate` and `Mirror.Drain` (called d69be6842a
      by the preemption callback). Per-peer timeout = 5s; total
      budget = 25s by default.
      File: `cmd/artifact-daemon/mirror.go`.

- [x] Task: Add daemon flags `--preemption-watch` (default `false`), 3878424cd5
      `--preemption-budget` (default `25s`). Wire watcher into
      `main.go` so its `OnPreempted` callback invokes
      `Mirror.Evacuate`.
      File: `cmd/artifact-daemon/main.go`.

- [x] Task: Add `artifactDaemon.preemption` block to 235a23f21d
      `deploy/chart/values.yaml` (default `enabled: false`,
      `budget: "25s"`); plumb into DaemonSet args.
      Files: `deploy/chart/values.yaml`,
      `deploy/chart/templates/artifact-daemon-daemonset.yaml`.

- [x] Task: Behavioral test — fake metadata server fires preempt fe0986d4c8
      mid-mirror; `Evacuate` flushes the unmirrored artifact to a
      peer before the budget expires.
      File: `cmd/artifact-daemon/preemption_test.go`.

- [x] Task: Run all test suites (`make test-unit`, fe0986d4c8
      `ginkgo ./atc/worker/jetbridge/...`,
      `go test ./cmd/artifact-daemon/...`,
      `helm template deploy/chart`).

- [x] Task: Phase 3 Manual Verification
