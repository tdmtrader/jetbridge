# Plan: Cross-Node Artifact Reliability

## Phase 1: Fix Timeouts

### Task 1.1: Fix init container wget timeout
- [x] Write tests for timeout-tolerant init container script generation
- [x] Update `daemonResolveCommand()` in `atc/worker/jetbridge/storage_daemonset.go:142` — replace `wget -T 5` with `-T 180` (3 minutes) and add connection-specific retry logic

### Task 1.2: Fix daemon peer HTTP client timeout
- [x] Write tests for large artifact peer fetch (simulated slow response)
- [x] Split `PeerResolver` HTTP clients in `cmd/artifact-daemon/peers.go:41` — use short-timeout client for Probe (HEAD) and long/no-timeout client for Fetch (GET tar stream)

## Phase 2: Parallelize Peer Probing

### Task 2.1: Concurrent peer probe
- [x] Write tests for parallel probe behavior (first-hit-wins, cancellation)
- [x] Rewrite `PeerResolver.Probe()` in `cmd/artifact-daemon/peers.go:76` to probe peers concurrently using goroutines with context cancellation

## Phase 3: Verification

### Task 3.1: Run existing tests
- [x] Run `go test ./cmd/artifact-daemon/...` — all existing tests pass
- [x] Run `go test ./atc/worker/jetbridge/...` — all existing tests pass
