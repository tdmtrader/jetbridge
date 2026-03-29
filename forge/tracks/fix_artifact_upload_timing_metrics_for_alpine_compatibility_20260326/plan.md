# Implementation Plan: Fix Artifact Upload Timing Metrics for Alpine Compatibility

## Phase 1: Fix Timing and Rename Attributes [checkpoint: f2effa200]

### 1.1 Update shell script and Go struct
- [x] Replace `date +%s%N` with `/proc/uptime`-based `now_ms()` function in `uploadArtifact()` shell script (`process.go`) f2effa200
- [x] Rename shell output fields: `TAR_NS` → `TAR_MS`, `TRANSFER_NS` → `TRANSFER_MS` f2effa200
- [x] Rename `ArtifactUploadStats` fields: `TarNanos` → `TarMillis`, `TransferNanos` → `TransferMillis` f2effa200
- [x] Update `ParseArtifactUploadStats` to parse `TAR_MS`/`TRANSFER_MS` f2effa200
- [x] Rename span attributes: `artifact.tar_duration_ns` → `artifact.tar_duration_ms`, `artifact.transfer_duration_ns` → `artifact.transfer_duration_ms` f2effa200
- [x] Update span event attribute names from `duration_ns` to `duration_ms` f2effa200
- [x] Fix OTel histogram conversion: `time.Duration(stats.TarMillis) * time.Millisecond` (was `* time.Nanosecond`) f2effa200

### 1.2 Update OTel histogram instruments
- [x] Update histogram descriptions in `otel_artifact_upload.go` to reflect correct unit conversion from milliseconds f2effa200

### 1.3 Update tests
- [x] Update `ParseArtifactUploadStats` test cases in `process_test.go` to use `TAR_MS`/`TRANSFER_MS` fields and `TarMillis`/`TransferMillis` assertions f2effa200
- [x] Update span attribute assertions in `process_test.go` to expect `artifact.tar_duration_ms` / `artifact.transfer_duration_ms` f2effa200
- [x] Update `otel_artifact_upload_test.go` if it references the old field names f2effa200
- [x] Verify all tests pass: `go test ./atc/worker/jetbridge/... ./atc/metric/...` f2effa200

- [x] Task: Phase 1 Manual Verification — build compiles, tests pass, shell script produces plausible ms values f2effa200

---
