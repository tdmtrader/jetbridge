# Plan: Batch Artifact Resolution

## Phase 1: Daemon batch endpoint

### Task 1.1: Add `/resolve-batch` endpoint
- [x] Write tests for batch resolve (happy path, partial failure, empty batch)
- [x] Implement `handleResolveBatch()` in `cmd/artifact-daemon/server.go` — concurrent resolution with per-item goroutines, aggregated response

## Phase 2: Single init container

### Task 2.1: Collapse init containers
- [x] Write tests for single init container with batch command
- [x] Replace per-input init containers in `BuildFetchInitContainers()` in `atc/worker/jetbridge/storage_daemonset.go` with a single init container calling `/resolve-batch`
- [x] Add `daemonResolveBatchCommand()` that generates the batch wget script

## Phase 3: Verification

### Task 3.1: Run existing tests
- [x] Run `go test ./cmd/artifact-daemon/...` — all tests pass
- [x] Run `go test ./atc/worker/jetbridge/...` — all tests pass
