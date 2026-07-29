# Task 9 — managed output-builder wiring

## Delivered

- ATC derives a canonical `outputbuilder.NodeAuthority` only from resolved
  typed input refs/exposures and eligible built-in record outputs. Optional
  absent inputs are omitted; non-record outputs do not activate the builder.
- Eligible admitted-runtime AgentSteps receive the fixed `output-builder`
  sidecar at `127.0.0.1:7783`, with the fixed `agent-output serve` contract.
  Authority bytes are a one-file private Secret projection mounted read-only
  with `subPath` only into that sidecar at
  `/run/concourse/output-builder/authority.json`.
- Jetbridge reuses the Task 6 private Secret create/CAS-bind/reap lifecycle.
  The builder receives only exact typed input (read-only) and record-output
  (writable) mounts; it receives no work root, flight, cache, scratch,
  ordinary Secret, model credential, or service-account token.
- Runner admits the fixed builder marker/name/endpoint server-side, rejects
  authored collisions, waits for health, writes strict temporary MCP config,
  and removes its private directory on every return after creation.
- `cmd/agent-output` now uses the fixed mounted authority path; the image
  installs the binary read-only. Existing post-step sealing remains unchanged.

## Verification

- `go test ./atc/exec ./atc/worker/jetbridge ./agent/runner ./cmd/agent-output -count=1`
- `go test ./deploy -count=1`
- `git diff --check`

All commands above passed at the Task 9 checkpoint.

## Review round 1 correction

- Tightened the runtime trust boundary: only an Agent container with a pinned
  admitted image can carry the builder; its strictly decoded authority must
  describe the canonical direct-child typed input/output layout, use built-in
  record output types, and exactly match the builder projections and sidecar
  image. Task 8's in-sidecar filesystem validation remains unchanged.
- The layout check rejects work-root and mismatched projections, input/output
  cross-root and untyped overlaps, ordinary-secret, cache, and scratch
  overlaps, mutable images, Task specs, non-record ports, and malformed
  authority layouts. Jetbridge additionally
  rejects an otherwise typed projection when its Kubernetes volume is aliased
  to any other main-container mount path.
- Focused negative coverage includes the explicit work-root projection case;
  the builder's own typed-input projection remains read-only without changing
  the main agent container's input mount behavior.

Verification for this correction:

- `go test ./atc/exec ./atc/worker/jetbridge ./agent/runner ./cmd/agent-output -count=1`
- `go test ./atc/runtime ./atc/worker/jetbridge ./agent/runner ./cmd/agent-output -count=1`
- `go test ./deploy -count=1`
- `git diff --check`
