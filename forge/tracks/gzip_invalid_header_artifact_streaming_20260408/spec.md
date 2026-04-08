# Spec: Fix gzip invalid header on artifact StreamFile

## Overview

When a task step uses `file:` to load its config from a get step's output, the ATC calls `Streamer.StreamFile()` which calls `DaemonSetVolume.StreamOut()` → `GET /artifacts/` on the daemon. The daemon returns **raw tar**, but `StreamFile` then wraps the response in `gzip.NewReader` (via `s.compression.NewReader`), causing "gzip: invalid header".

This breaks any pipeline where a task step references its config via `file: some-repo/path/to/task.yml` and the repo comes from a get step. Tasks with inline `config:` are unaffected. Task inputs themselves are also unaffected (they use the init container `/resolve-batch` → `cp -a` path).

## Root Cause

`Streamer.StreamFile()` (`atc/worker/streamer.go:23-46`) has a fixed assumption that `StreamOut` returns gzip-compressed tar:

```go
out, err := artifact.StreamOut(ctx, path, s.compression)   // line 24: raw tar from daemon
compressionReader, err := s.compression.NewReader(out)       // line 29: gzip.NewReader → FAILS
tarReader := tar.NewReader(compressionReader)                 // line 33: never reached
```

`DaemonSetVolume.StreamOut()` (`atc/worker/jetbridge/volume_daemonset.go:82-120`) fetches from `GET /artifacts/` on the artifact daemon, which returns raw tar (`handleGetArtifact` calls `tarDirectory` with `Content-Type: application/x-tar`). The `enc compression.Compression` parameter passed to `StreamOut` is ignored — the daemon always returns raw tar.

This worked in the legacy Garden runtime because `Volume.StreamOut()` ran `tar` inside the container and the caller controlled compression. In the DaemonSet path, the daemon handles tarring but never compresses.

## Affected Paths

| Path | Broken? | Why |
|------|---------|-----|
| Task `file:` config fetch | **YES** | `StreamFile` → `StreamOut` → raw tar → `gzip.NewReader` fails |
| Task inputs (get → task) | No | Init container `/resolve-batch` → `cp -a`, no streaming |
| `fly execute` with `-i` | No (coincidence) | `fly` sends gzip to `CreateArtifact` API |
| `set_pipeline` step file | **Likely YES** | Also uses `StreamFile` |
| `load_var` step file | **Likely YES** | Also uses `StreamFile` |
| Cross-node peer fetch | No | `peers.go` reads raw tar correctly |

## Requirements

1. `DaemonSetVolume.StreamOut` must honor the `compression` parameter — if the caller requests gzip, the returned stream must be gzipped.
2. Alternatively, `Streamer.StreamFile` must detect the actual encoding of the stream (raw vs gzip) and handle accordingly.
3. The init container `/resolve` path must remain unaffected.
4. `fly execute` artifact upload must continue working.
5. All changes must have test coverage.

## Technical Approach

**Fix `DaemonSetVolume.StreamOut` to honor the compression parameter.**

The `Streamer` contract expects `StreamOut` to return a compressed stream when compression is provided. The regular `Volume.StreamOut` (`volume.go:209-269`) does this correctly — it wraps the tar stream in a gzip writer when `enc.Encoding() != RawEncoding`. `DaemonSetVolume.StreamOut` should do the same: pipe the raw tar response from the daemon through a gzip compressor before returning.

This is the minimal, contract-respecting fix. The alternative (making `Streamer` handle raw tar) would require changing the interface contract that all volume types implement.

## Acceptance Criteria

- [ ] Task steps with `file:` config referencing a get step output work without "gzip: invalid header"
- [ ] Task inputs from get steps continue working (init container path)
- [ ] `fly execute` artifact upload continues working
- [ ] `set_pipeline` and `load_var` steps reading files from artifacts work
- [ ] Unit tests cover `DaemonSetVolume.StreamOut` with gzip compression
- [ ] Unit tests cover `Streamer.StreamFile` against a DaemonSet artifact

## Out of Scope

- Reworking the init container `/resolve` path (uses filesystem copy, not streaming)
- Fixing `handleStreamIn` gzip assumption (separate, currently-latent issue)
- Compression negotiation via HTTP headers
