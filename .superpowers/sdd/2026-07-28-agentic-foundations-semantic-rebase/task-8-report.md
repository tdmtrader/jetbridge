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

- `bb567a16c5 feat(agent): add managed output builder core`

## Fix round 1

### Status

All five round-1 High findings were corrected in scope. The final scoped
review remains the required adjudication point.

Commit: `fc31f229d8 fix(agent): harden managed output builder boundaries`

### RED evidence and focused regressions

- `TestBuilderUsesSnapshotDefaultsWhenLimitsAreUnset` failed with `(0, 0)`
  limits rather than the snapshot defaults.
- `TestBuilderRemovesStaleContentWhenStageHasNone` failed because `content/`
  survived a successful zero-content candidate write.
- `TestLoadAuthorityRejectsWritableAuthorityFile` failed because a `0600`
  caller-writable authority file loaded successfully.
- `TestRunCLIRejectsAuthorityOverride` failed because `--authority` was
  accepted and then attempted to load the caller-selected path.
- Added focused regressions `TestValidationContextOpensTheExactMountedInputArchive`
  and `TestBuilderKeepsWritingThroughItsBoundOutputRootAfterPathReplacement`.

### Files and behavior

- `cmd/agent-output/main.go` now uses only the fixed
  `PlatformAuthorityPath`; `--authority` and serve arguments are rejected.
- `authority.go` requires a read-only authority file and rechecks its identity
  after bounded reading.
- `builder.go` supplies snapshot default limits, streams copied content with a
  pre-copy byte bound, opens each declared input/output through retained
  `os.Root` descriptors, and supplies a canonicalized, exact-digest input
  opener to validators.
- Publication now moves any previous record/content to anchored backups,
  installs staged content, and renames `record.json` last; ordinary failures
  roll back the previous candidate and zero-content writes remove stale
  `content/` deterministically.

### Verification

- Focused: `go test ./agent/outputbuilder ./cmd/agent-output -count=1` —
  passed.
- Focused image contract: `go test ./deploy -run TestAgentRunnerDockerfile
  -count=1` — passed.
- Checkpoint: `go test ./agent/outputbuilder ./cmd/agent-output
  ./agent/snapshot/... -count=1` — passed.
- `git diff --check` — passed.

### Self-review

- No CLI/MCP request can select authority, ports, mount roots, or snapshot
  references. The command only consumes its fixed read-only authority mount.
- Bounded defaults match final snapshot capture and content copying does not
  allocate whole source files before enforcing the byte cap.
- Retained roots—not post-check path strings—serve staging, content reads,
  input archive construction, validation, and publication. The replacement
  regression proves writes remain in the originally bound root.
- Repository-change validation receives a canonical archive only after the
  retained declared input matches its exact reference digest.

### Residual deferred observation

- `DEFERRED-003` remains for abrupt-host-crash durability only. Ordinary
  publication errors now restore the prior candidate; no additional Task 9
  runtime wiring was added.

## Independent review

- Round 1: five High/blocking findings covering caller-minted authority,
  unbounded production defaults, missing repository-change input reopening,
  mount check/use races, and non-atomic/stale-content publication.
- Fix: `fc31f229d8 fix(agent): harden managed output builder boundaries`.
- Round 2: all five findings **ADDRESSED**; no new Critical/High/blocking
  breakage.
- Final verdict: **ACCEPTED**.
