# Implementation Plan: Remote Native Worker Agent

## Phase 1: Remote uname -a
Thinnest end-to-end path: a trivial platform: darwin task runs on a remote Mac.

- [ ] Task: Define proto (Exec + Ping RPCs only) and generate Go code. Use oneof for StreamIn (meta first, then data chunks) per review feedback. Include Kill RPC stub.
- [ ] Task: Write agent binary (main.go: flags for --listen, --work-dir, --cache-dir; gRPC server; signal handler)
- [ ] Task: Write agent Exec handler (build exec.Cmd, stream stdout/stderr as ExecEvent chunks, send exit_status on completion, store process in sync.Map by ID)
- [ ] Task: Write agent Kill handler (lookup process by ID, SIGTERM → 10s grace → SIGKILL — falls out of Exec's process tracking)
- [ ] Task: Write agent Ping handler (return runtime.GOOS, runtime.GOARCH, version)
- [ ] Task: Write agent startup sweep for stale scratch dirs (same pattern as native/reaper.go startupSweep — scan work-dir/containers/ on boot, kill orphans, remove dirs)
- [ ] Task: Write agent Exec handler tests (start process, stream stdout, exit code, context cancellation)
- [ ] Task: Write RemoteWorker implementing runtime.Worker (DB lifecycle local, execution proxied to gRPC)
- [ ] Task: Write RemoteContainer implementing runtime.Container (Run calls client.Exec, streamInputs is no-op for Phase 1)
- [ ] Task: Write RemoteProcess implementing runtime.Process (reads ExecEvent stream, pipes to ProcessIO, Wait blocks until exit_status)
- [ ] Task: Write RemoteVolume stubs (StreamIn/StreamOut return errors — not yet implemented)
- [ ] Task: Write web-side remote worker tests (mock gRPC server, verify stdout streaming, exit code propagation)
- [ ] Task: Add --native-worker-address flag to atc/atccmd/command.go
- [ ] Task: Write web-side Ping registrar (calls Ping every 15s, saves db.Worker with platform/arch from response, handles connection failures gracefully)
- [ ] Task: Wire factory dispatch — RemoteAddress set → remote.NewWorker; empty → local native; nil NativeConfig → K8s
- [ ] Task: Phase 1 Manual Verification — fly execute uname -a on remote Mac; fly workers shows remote agent; co-located --native-worker still works

## Phase 2: Task with inputs (StreamIn)
K8s get → artifact streams to remote agent → darwin task runs with input files.

- [ ] Task: Add StreamIn to proto (oneof: first message is StreamInMeta with path + encoding, subsequent messages are data chunks) and generate code
- [ ] Task: Write agent StreamIn handler (receive meta, create target dir, wrap chunk stream as io.Reader, decompress, extract tar)
- [ ] Task: Write agent StreamIn tests (round-trip: send tar chunks, verify files on disk)
- [ ] Task: Wire RemoteContainer.streamInputs — for each cross-worker artifact, call artifact.StreamOut(), pipe tar stream to client.StreamIn() as chunks
- [ ] Task: Write web-side StreamIn integration test (mock gRPC server receives correct tar data)
- [ ] Task: Phase 2 Manual Verification — brine pipeline: K8s get → remote darwin cargo build

## Phase 3: Task with outputs (StreamOut)
Darwin task output flows back for downstream steps.

- [ ] Task: Add StreamOut to proto and generate code
- [ ] Task: Write agent StreamOut handler (tar requested path, compress, send chunks)
- [ ] Task: Write agent StreamOut tests (create files on disk, stream out, verify tar contents)
- [ ] Task: Wire RemoteVolume.StreamOut — call client.StreamOut(), return io.ReadCloser that reads from chunk stream
- [ ] Task: Write web-side StreamOut integration test
- [ ] Task: Phase 3 Manual Verification — darwin build output consumed by K8s put step

## Phase 4: Auth
Token auth for dev, mTLS for production. Must ship before Phase 5 (no unauthenticated fleet).

- [ ] Task: Add token auth — agent-side unary+stream interceptor checks authorization metadata; web-side dial option injects token
- [ ] Task: Add --token flag (agent) and --native-worker-token flag (web)
- [ ] Task: Write auth rejection tests (no token, wrong token, correct token)
- [ ] Task: Add mTLS configuration (--tls-cert, --tls-key, --tls-ca on both sides)
- [ ] Task: Write mTLS tests (valid cert accepted, invalid rejected, expired rejected)
- [ ] Task: Phase 4 Manual Verification — unauthenticated connections rejected, authenticated connections work

## Phase 5: Multi-agent and resilience
Fleet support with health checking.

- [ ] Task: Support comma-separated --native-worker-address (each gets its own gRPC conn, Ping registrar, db.Worker)
- [ ] Task: Add stalled worker detection — Ping fails 2x (30s), mark stalled in DB. Stalled workers excluded from pool selection. In-flight Exec streams on stalled workers are NOT killed (TCP may still be alive, let the stream fail naturally or succeed). If Ping succeeds again, mark running.
- [ ] Task: Write multi-agent pool routing tests (two darwin workers, verify round-robin or first-available selection)
- [ ] Task: Add connection retry with exponential backoff (gRPC built-in retry policy)
- [ ] Task: Add agent graceful drain on SIGTERM (stop accepting new Exec calls, wait for in-flight processes, then exit)
- [ ] Task: Write resilience tests (agent restart mid-build, connection drop, recovery after reconnect)
- [ ] Task: Phase 5 Manual Verification — kill agent mid-build, verify other agent picks up work; restart agent, verify re-registration

## Design decisions (reference)

**Kill RPC and cancellation flow:** Web calls Kill(processID) when a build is aborted. This sends SIGTERM → grace → SIGKILL on the agent side. Separately, the web cancels the Exec stream context, which causes the RemoteProcess.Wait() goroutine to return. Both Kill and context cancellation may race — the agent handles this gracefully (killing an already-dead process is a no-op).

**Stalled vs in-flight:** A stalled worker (failed Ping) may still have a live TCP connection with an active Exec stream. The web does NOT kill in-flight builds on stalled workers. The build either completes (stream succeeds) or fails (stream breaks). New builds are routed elsewhere.

**Retry on worker failure:** Concourse has existing build retry logic (max_in_flight, serial_groups). If an Exec stream fails mid-build, the build fails and the user's retry configuration determines what happens next. Phase 5 does NOT implement automatic retry-on-different-worker — that's existing Concourse behavior or a separate track.
