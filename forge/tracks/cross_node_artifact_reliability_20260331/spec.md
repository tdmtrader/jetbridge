# Spec: Cross-Node Artifact Reliability

## Overview

The artifact daemon's cross-node resolution path is unreliable for multi-node clusters. Init containers use `wget -T 5` to call the local daemon's `/resolve` endpoint, but cross-node resolution (peer discovery + tar stream fetch) routinely exceeds 5 seconds — especially for large artifacts (1-10+ GB). The daemon's peer HTTP client also has a hard 10s timeout that's too short for large transfers. Peer probing is sequential, adding unnecessary latency.

## Problem

The init container script (`storage_daemonset.go:167`) calls:
```bash
wget -qO- -T 5 --post-data='...' "${DAEMON}/resolve"
```

For cross-node resolution, the daemon's `/resolve` handler must:
1. Check local registry (fast)
2. Check local filesystem (fast)
3. Discover peers via K8s EndpointSlice API
4. HEAD-probe each peer **sequentially** (10s timeout per peer)
5. GET the tar stream from the peer
6. Extract tar to destination directory
7. Respond to init container

Steps 3-6 easily exceed 5s, and for large artifacts, step 5 alone can take minutes. The 10-retry loop with 2s backoff partially masks this, but each retry restarts the entire resolution from scratch.

## Requirements

1. Init container `wget` timeout must accommodate large cross-node transfers (minutes, not seconds)
2. Daemon's peer HTTP client must not have a fixed timeout that kills large transfers mid-stream
3. Peer probing should be parallelized to reduce discovery latency
4. The `/resolve` endpoint must remain synchronous — it blocks until the artifact is fully available at the destination or fails
5. Same-node resolution performance must not regress
6. Retry logic should distinguish transient errors (daemon not ready) from in-progress transfers

## Technical Approach

### 1. Fix init container timeout (`storage_daemonset.go`)
- Replace `wget -T 5` with `-T 180` (3 minutes) to accommodate cross-node transfers of large artifacts
- Keep the retry loop for transient failures (daemon not reachable), but add smarter retry logic: if wget gets a connection but the response takes long, that's the daemon working — don't retry

### 2. Fix daemon peer HTTP client timeout (`peers.go`)
- Replace the single 10s `http.Client` timeout with:
  - **Probe**: Keep a short timeout (5-10s) for HEAD requests — these are small
  - **Fetch**: Use no timeout or a very large one (30+ minutes) for GET tar streams. Use an idle timeout via `http.Transport.ResponseHeaderTimeout` + read deadline instead

### 3. Parallelize peer probing (`peers.go`)
- Replace sequential HEAD probe loop with concurrent goroutines
- Return the first peer that responds 200
- Cancel remaining probes on first hit

### 4. Unit tests (`cmd/artifact-daemon/`)
- Add large artifact transfer test (simulated via slow response)
- Add parallel probe test
- Add timeout behavior tests

## Acceptance Criteria

- [ ] Cross-node artifact resolution succeeds for artifacts that take >5s to transfer
- [ ] Peer probing happens concurrently, not sequentially
- [ ] Same-node resolution latency is unchanged
- [ ] Init container retries still handle daemon-not-ready scenarios
- [ ] Existing artifact daemon tests pass
- [ ] New unit tests cover timeout and parallel probe behavior

## Out of Scope

- Multi-node integration test infrastructure (separate track)
- Shared storage backends (GCS Fuse, NFS)
- Artifact pre-staging / async resolution
- Changes to the `/register` endpoint
