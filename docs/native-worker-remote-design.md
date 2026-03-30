# Remote Native Worker: Design & Roadmap

## Current state

The co-located native worker is proven end-to-end with the brine pipeline:
- Get on K8s → artifact streams to native → darwin task executes locally (14s release build)
- Enabled via `--native-worker` flag on the web process
- Web must run on macOS for darwin tasks

This document designs the remote native worker — a standalone agent binary that runs on a Mac and accepts work from a web process running anywhere (K8s, Linux, another Mac).

## Why remote matters

The co-located model requires the web process to run on macOS. This breaks down when:
- The web runs in K8s (standard production deployment) and darwin tasks need a Mac
- Multiple Macs need to serve as build workers (CI fleet)
- The Mac can't or shouldn't run the full web+scheduler+DB stack
- You want darwin workers without giving them DB credentials

## Architecture: what stays where

The runtime.Worker interface does two things that can be cleanly separated:

### Web side (DB lifecycle + orchestration)
- Container state machine: creating → created → destroying (DB)
- Volume state machine: creating → created → destroying (DB)
- Worker selection and platform routing
- Artifact repository (in-memory, tracks outputs between steps)
- Build event emission (logs, status, metrics)
- Resource cache initialization (DB writes after successful get/put)
- Task cache registration (DB writes after successful task)

### Agent side (execution + I/O)
- Process spawning via os/exec
- Signal handling (SIGTERM → grace → SIGKILL)
- Filesystem: create scratch dirs, extract tar, produce tar
- Environment merging (host defaults + pipeline env)
- PID tracking and orphan cleanup
- Compression/decompression (gzip, zstd, s2)

### The boundary

Only 5 operations cross the wire:

| Operation | Web → Agent | Agent → Web |
|-----------|-------------|-------------|
| **Exec** | path, args, env, dir | stdout stream, stderr stream, exit code |
| **Kill** | process ref | ack |
| **StreamIn** | dest path, tar stream | ack |
| **StreamOut** | source path | tar stream |
| **Ping** | (empty) | platform, arch, version |

## Protocol: gRPC

gRPC because it handles bidirectional streaming (stdout/stderr), large binary
transfers (tar), auth (mTLS), and connection health (keepalive) natively.

```protobuf
syntax = "proto3";
package native;

service NativeAgent {
  rpc Exec(ExecRequest) returns (stream ExecEvent);
  rpc Kill(KillRequest) returns (KillResponse);
  rpc StreamIn(stream StreamInMessage) returns (StreamInResponse);
  rpc StreamOut(StreamOutRequest) returns (stream StreamOutChunk);
  rpc Ping(PingRequest) returns (PingResponse);
}

message ExecRequest {
  string id = 1;
  string path = 2;
  repeated string args = 3;
  repeated string env = 4;
  string dir = 5;
  bytes stdin = 6;
}

message ExecEvent {
  oneof event {
    bytes stdout = 1;
    bytes stderr = 2;
    int32 exit_status = 3;
    string error = 4;
  }
}

message KillRequest { string id = 1; }
message KillResponse {}

message StreamInMessage {
  oneof message {
    StreamInMeta meta = 1;    // first message only
    bytes data = 2;           // subsequent chunks
  }
}
message StreamInMeta {
  string path = 1;
  string encoding = 2;
}
message StreamInResponse {}

message StreamOutRequest {
  string path = 1;
  string encoding = 2;
}
message StreamOutChunk { bytes data = 1; }

message PingRequest {}
message PingResponse {
  string platform = 1;
  string arch = 2;
  string version = 3;
}
```

Exec streams stdout/stderr chunks back and closes with a final exit_status event.
No separate Wait RPC needed. Kill uses the process ID for build cancellation from
a different context.

## Implementation: vertical slices

Each slice is end-to-end: proto + agent + web client + wiring. Each is independently
shippable and has a concrete verification step.

### Slice 1: Remote `uname -a`

The thinnest possible path. A trivial `platform: darwin` task with no inputs runs
on a remote Mac.

**Proto:** Exec + Ping only. No StreamIn, no StreamOut.

**Agent (`cmd/native-agent/`):**
- `main.go` — flags (--listen, --work-dir), gRPC server, signal handler
- `server.go` — Exec handler: build exec.Cmd from request, wire stdout/stderr to
  stream.Send(), wait for exit, send final ExecEvent. Ping handler: return
  runtime.GOOS/GOARCH. Duplicate `mergeEnv` and path resolution locally (~20 lines)
  rather than extracting a shared package prematurely.

**Web client (`atc/worker/native/remote/`):**
- `worker.go` — RemoteWorker: DB lifecycle identical to local native worker, but
  FindOrCreateContainer returns a RemoteContainer. Implements runtime.Worker.
- `container.go` — RemoteContainer.Run(): calls client.Exec(), returns RemoteProcess.
  streamInputs() is a no-op (no StreamIn yet). Properties/SetProperty: in-memory map.
- `process.go` — RemoteProcess: goroutine reads ExecEvent stream, pipes stdout/stderr
  chunks to ProcessIO writers. Wait() blocks until exit_status event arrives.
- `volume.go` — stub: StreamIn/StreamOut return errors (not yet implemented).

**Wiring:**
- `atc/worker/factory.go` — dispatch to remote.NewWorker when NativeConfig.RemoteAddress is set
- `atc/atccmd/command.go` — add `--native-worker-address` flag
- Web-side registrar: calls Ping() every 15s, saves db.Worker with platform/arch from response

**No auth.** Insecure gRPC for dev. Auth comes in slice 4.

**Verify:**
```yaml
# task.yml
platform: darwin
run:
  path: uname
  args: [-a]
```
```bash
# On Mac:
native-agent --listen=:7799

# Web (in K8s or elsewhere):
concourse web --native-worker-address=mac.local:7799 --kubernetes-namespace=concourse ...

# Test:
fly execute -c task.yml
# Output: Darwin mac.local 24.x.x ... arm64
```

**Files created:**
```
proto/native_agent.proto
cmd/native-agent/main.go
cmd/native-agent/server.go
atc/worker/native/remote/worker.go
atc/worker/native/remote/container.go
atc/worker/native/remote/process.go
atc/worker/native/remote/volume.go
```

**Files modified:**
```
atc/worker/factory.go         — remote dispatch
atc/atccmd/command.go          — --native-worker-address flag + registrar
```

**Estimate:** ~800 lines new, ~30 lines modified.

---

### Slice 2: Task with inputs (the brine path)

K8s get step → artifact streams to remote agent → darwin task runs with input files.

**Proto:** Add StreamIn.

**Agent:** Add StreamIn handler — reads chunks from client stream, creates target
directory, decompresses, extracts tar. Reuses the same logic as volume.go StreamIn
but reads from gRPC chunks instead of an io.Reader (wrap the stream as an io.Reader).

**Web client:**
- RemoteContainer.streamInputs() now works: for each cross-worker artifact, calls
  artifact.StreamOut() (hits K8s volume via jetbridge), pipes the tar stream to
  client.StreamIn().
- RemoteVolume.StreamIn() proxies to gRPC.

**Verify:** The actual brine pipeline runs with web in K8s and agent on Mac:
```yaml
jobs:
- name: build-darwin
  plan:
  - get: source-code     # runs on K8s, git clone
  - task: cargo-build     # runs on remote Mac
    platform: darwin
    inputs: [source-code]
    run:
      path: cargo
      args: [build, --release]
```

**Estimate:** ~150 lines new (StreamIn handler + client proxy + stream adapter).

---

### Slice 3: Task with outputs

Darwin task produces artifacts that downstream steps can consume (put to a registry,
feed into another task).

**Proto:** Add StreamOut.

**Agent:** Add StreamOut handler — tars the requested path, compresses, sends chunks.
Same logic as volume.go StreamOut but writes to gRPC stream.

**Web client:**
- RemoteVolume.StreamOut() proxies to gRPC, returns an io.ReadCloser that reads
  from the chunk stream.
- Output volumes are registered in the artifact repository (already happens — the
  task step calls registerOutputs with the VolumeMount list).

**Verify:** Pipeline with darwin task output feeding into a K8s put step:
```yaml
jobs:
- name: build-and-publish
  plan:
  - get: source-code
  - task: cargo-build
    platform: darwin
    inputs: [source-code]
    outputs: [binary]
    run:
      path: sh
      args: [-c, "cargo build --release && cp target/release/brine binary/"]
  - put: github-release    # runs on K8s, uploads binary
    params:
      globs: [binary/brine]
```

**Estimate:** ~150 lines new.

---

### Slice 4: Auth

Same functionality, now secured. Two options, implement token first.

**Token auth (dev):**
- Agent: `--token=<secret>` flag. gRPC interceptor checks `authorization` metadata on every call.
- Web: `--native-worker-token=<secret>` flag. gRPC dial option adds metadata.
- ~50 lines.

**mTLS (production):**
- Agent: `--tls-cert`, `--tls-key`, `--tls-ca` flags. RequireAndVerifyClientCert.
- Web: `--native-worker-tls-cert`, `--native-worker-tls-key`, `--native-worker-tls-ca`.
- ~80 lines (mostly TLS config wiring).

**Verify:** Agent rejects unauthenticated connections. Authenticated connections work as before.

**Estimate:** ~130 lines new.

---

### Slice 5: Multi-agent + resilience

**Multiple agents:**
- `--native-worker-address=mac1:7799,mac2:7799` — comma-separated.
- Each address gets its own gRPC connection, its own Ping heartbeat, its own db.Worker
  registration (with unique names from PingResponse or address-derived).
- Pool selects among darwin workers using existing logic (first available from
  team-scoped or general pool).

**Resilience:**
- Connection retry with exponential backoff (gRPC has built-in retry policies).
- Stalled detection: if Ping fails 2x consecutively (30s), mark worker as stalled
  in DB. Pool excludes stalled workers. If Ping succeeds again, mark running.
- Graceful agent restart: in-flight Exec streams fail with transport error. Web
  retries the build step on another worker (existing Concourse retry logic).
- Agent draining: agent accepts `SIGTERM`, stops accepting new Exec calls, waits
  for in-flight processes to complete, then exits.

**Verify:** Kill an agent mid-build. Build retries on the other agent. Restart the
agent. It re-registers and accepts new work.

**Estimate:** ~300 lines new.

---

## Deployment configurations

| Setup | Web flags | Agent | Where |
|-------|-----------|-------|-------|
| K8s only | `--kubernetes-namespace=...` | none | Web in K8s |
| Co-located native | `+ --native-worker` | none | Web on Mac |
| Remote native | `+ --native-worker-address=mac:7799` | `native-agent --listen=:7799` | Web in K8s, agent on Mac |
| Multi-Mac fleet | `+ --native-worker-address=mac1:7799,mac2:7799` | one per Mac | Web in K8s, agents on Macs |

Each is additive. K8s-only users never see native worker code. Co-located native
continues to work unchanged.

## What this doesn't become

1. **No container runtime.** os/exec, not Garden/runc/containerd.
2. **No protocol invention.** gRPC handles streaming, auth, health, multiplexing.
3. **No state on the agent.** DB lifecycle stays on the web. Agent is stateless.
4. **No scheduling on the agent.** Web decides what runs where.
5. **No mandatory components.** Remote is opt-in. Co-located works without it.

## Comparison to TSA/Garden

| Aspect | TSA/Garden | Remote native agent |
|--------|-----------|-------------------|
| Transport | Custom SSH tunnels with multiplexing (~3k lines) | gRPC (library) |
| Container runtime | Garden: OCI, namespaces, cgroups, networking (~10k+ lines) | os/exec (~20 lines) |
| Volume management | BaggageClaim: COW filesystems, GC (~5k lines) | Local directories + tar |
| Registration | SSH key exchange, team scoping, capability negotiation | Ping() → platform string |
| Auth | SSH keys + team-scoped authorization | Token or mTLS (library) |
| Worker binary | Garden + BaggageClaim + beacon (3 daemons) | Single binary, zero dependencies |
| Total complexity | ~20k lines, 3 processes | ~1500 lines, 1 process |
