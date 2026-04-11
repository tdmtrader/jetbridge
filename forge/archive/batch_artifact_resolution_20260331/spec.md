# Spec: Batch Artifact Resolution

## Overview

When a job has multiple input artifacts, each gets its own Kubernetes init container. Since K8s runs init containers sequentially, N inputs means N serial artifact fetches — each potentially involving cross-node transfers. A job with 3 large inputs from different nodes takes 3× as long as necessary.

## Requirements

1. Add a `POST /resolve-batch` endpoint to the artifact daemon that accepts multiple key/dest pairs and resolves them concurrently
2. Collapse the N per-input init containers into a single init container that calls `/resolve-batch`
3. If any artifact in the batch fails, the init container must exit non-zero (pod fails, same as today)
4. Response must include per-key results so logs show what happened to each artifact
5. Keep the existing `/resolve` endpoint for backwards compatibility and single-input simplicity
6. Same-node and cross-node resolution must both work within the batch

## Technical Approach

### Daemon: `POST /resolve-batch`
- Request: `{"items": [{"key":"...","dest":"..."},...]}`
- Handler launches a goroutine per item using the same resolution logic as `handleResolve`
- Response: `{"status":"ok|error","results":[{per-key resolveResponse}]}`
- If any item fails, overall status is `"error"` and HTTP status is 500

### ATC: Single init container
- `BuildFetchInitContainers` generates one init container with all volume mounts
- New `daemonResolveBatchCommand()` builds a shell script that POSTs the batch JSON
- Uses `wget -T 180` consistent with the single-resolve timeout

## Acceptance Criteria

- [ ] Batch endpoint resolves multiple artifacts concurrently
- [ ] Single init container replaces N per-input init containers
- [ ] Partial failure returns error with per-key details
- [ ] Existing single `/resolve` endpoint still works
- [ ] All existing tests pass

## Out of Scope

- Multi-node integration test infrastructure
- Changes to peer resolution logic (already parallelized in prior track)
- Streaming/progress reporting during batch resolution
