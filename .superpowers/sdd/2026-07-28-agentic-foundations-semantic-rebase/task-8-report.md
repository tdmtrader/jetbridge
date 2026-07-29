# Task 8 report — managed output-builder core

## Status

DONE_WITH_CONCERNS

## Implemented behavior

- Added `agent/outputbuilder`: mount-bound `NodeAuthority`, strict authority
  loader, declared-port description, typed raw-record authoring, content-copy
  path checks, staged `record.json` publication, preflight validation, content
  and entry limits, cancellation propagation, CLI, and fixed-loopback MCP.
- The builder only receives declared input bindings and output mounts. It has no
  snapshot sealer, storage, credential, or authority-minting surface.
- Added a closed raw JSON codec set for the current built-in record contracts:
  diagnosis, measurements, repository-change, review, and validation.
- Added `cmd/agent-output` which requires one absolute clean platform authority
  file and closes CLI/MCP adapters over one constructed Builder.
- Added `agent-output` to the agent-runner image and image contract test.
- Added parity coverage showing a post-builder mutation is independently
  rejected by the final contract validator.

## RED evidence

1. `go test ./agent/outputbuilder -count=1` failed because `NodeAuthority`,
   `OutputAuthority`, `New`, `WriteRequest`, and `ContentRequest` did not
   exist.
2. `go test ./agent/outputbuilder -count=1` failed because `NewCLI`,
   `ValidateMCPListenAddress`, and `NewMCPServer` did not exist.
3. `go test ./cmd/agent-output -count=1` failed because `runCLI` and its exit
   contract did not exist.
4. `go test ./deploy -run TestAgentRunnerDockerfile -count=1` failed because
   the image neither built nor shipped `agent-output`.
5. `go test ./agent/outputbuilder -run
   TestBuilderPreflightEnforcesConfiguredContentLimit -count=1` failed because
   an over-limit candidate was accepted.
6. `go test ./agent/snapshot/contracts -run
   TestBuiltinRawRecordCodecIsClosedAndStrict -count=1` failed because the raw
   codec API did not exist.

## Verification

- `go test ./agent/snapshot/contracts ./agent/outputbuilder ./cmd/agent-output ./deploy -count=1`
  — passed.
- `go test ./agent/outputbuilder ./cmd/agent-output ./agent/snapshot/... -count=1`
  — passed: outputbuilder, command, snapshot, contracts, and snapshotfakes.
- `git diff --check` — passed.

## Self-review

- Authority is immutable for a Builder lifetime: each input/output mount must
  be a direct, non-symlink child of the work root and its identity is checked
  before every operation.
- Request data cannot add ports, mounts, schemas, snapshot references, or seal
  authority. Unknown outputs and undeclared subject inputs fail before write.
- Unsafe content paths, traversal, symlinks, cancellation, schema errors, and
  configured size limits leave no newly published candidate.
- Final validation is intentionally independent: it reopens output bytes and
  sees a mutation after builder preflight.

## Deferred observations

- `DEFERRED-003`: copied multi-file content does not yet have a durable
  whole-tree crash-recovery journal; see the deferred catalog for evidence and
  follow-up.

## Commits

- Pending Task 8 commit at report creation.
