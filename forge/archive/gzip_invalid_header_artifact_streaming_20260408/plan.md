# Plan: Fix gzip invalid header on artifact StreamFile

## Phase 1: Fix DaemonSetVolume.StreamOut compression

### Tasks

- [x] Write tests for DaemonSetVolume.StreamOut with gzip compression
  - Test that `StreamOut(ctx, path, gzipCompression)` returns a gzip-compressed tar stream
  - Test that `StreamOut(ctx, path, nil)` returns raw tar (current behavior, backward compat)
  - Use `httptest.Server` to mock the daemon's `GET /artifacts/` response

- [x] Implement compression in DaemonSetVolume.StreamOut
  - In `atc/worker/jetbridge/volume_daemonset.go` `StreamOut()`, when `enc` is non-nil and not `RawEncoding`:
    - Pipe `resp.Body` (raw tar) through a gzip writer before returning
    - Use `io.Pipe` + goroutine pattern (matching `Volume.StreamOut` in `volume.go:209-269`)
  - When `enc` is nil or `RawEncoding`, return `resp.Body` directly (current behavior)

## Phase 2: Verify end-to-end StreamFile path

### Tasks

- [x] Write integration test for Streamer.StreamFile with DaemonSet artifact
  - Create a `Streamer` with `compression.NewGzipCompression()`
  - Call `StreamFile` against a `DaemonSetVolume` backed by a mock daemon
  - Verify the file contents are correctly read (round-trip: raw tar from daemon → gzip → gunzip → untar → file bytes)

- [x] Verify set_pipeline and load_var steps also use StreamFile
  - Confirmed: `set_pipeline_step.go:350` and `load_var_step.go:138` both use `Streamer.StreamFile`
  - Both benefit from this fix automatically

## Phase 3: Fix latent handleStreamIn gzip assumption

### Tasks

- [x] Write tests for handleStreamIn with raw tar input
  - Test in `cmd/artifact-daemon/` that `PUT /stream-in/{key}` with raw tar extracts correctly
  - Test that gzipped tar still works (backward compat)
  - Test empty key returns 400

- [x] Implement gzip auto-detection in handleStreamIn
  - In `cmd/artifact-daemon/server.go` `handleStreamIn()`, use `bufio.NewReader` + `Peek(2)` to check for gzip magic bytes (`\x1f\x8b`)
  - If gzip: wrap in `gzip.NewReader` then `tar.NewReader`
  - If raw: use `tar.NewReader` directly
  - This fixes the latent bug for any future callers of `DaemonSetVolume.StreamIn`

## Phase 4: Fix DaemonSetVolume.StreamOut sub-path filtering

### Tasks

- [x] Write tests for DaemonSetVolume.StreamOut with a sub-path
  - Daemon serves a tar with multiple files (e.g., `ci/task.yml`, `README.md`, `src/main.go`)
  - `StreamOut(ctx, "ci/task.yml", gzip)` must return a tar containing only `ci/task.yml`
  - `StreamOut(ctx, ".", gzip)` must return the full tar (current behavior)
  - Verify round-trip via `Streamer.StreamFile` returns the correct file content

- [x] Implement sub-path filtering in DaemonSetVolume.StreamOut
  - After fetching the full tar from the daemon, filter entries client-side
  - When `path` is not `"."` or `""`, iterate the daemon's tar stream in the goroutine and re-tar only entries matching the requested path
  - Match `Volume.StreamOut` semantics: `tar cf - -C /mount path` produces a tar with the entry named `path`
