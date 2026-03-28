# Spec: Fix Artifact Upload Timing Metrics for Alpine Compatibility

**Track ID:** `fix_artifact_upload_timing_metrics_for_alpine_compatibility_20260326`
**Type:** bugfix

## Overview

The two-phase artifact upload shell script in `uploadArtifact()` uses `date +%s%N` to capture nanosecond-precision timestamps. However, the artifact-helper sidecar runs Alpine Linux (BusyBox), whose `date` command does not support `%N`. Instead of nanoseconds, it outputs the literal character `N`, producing garbage arithmetic results (small integers like 1–6 instead of real durations).

This makes the `artifact.tar_duration_ns` and `artifact.transfer_duration_ns` span attributes unusable for performance analysis.

## Requirements

1. Replace `date +%s%N` with `/proc/uptime`-based timing that works on Alpine/BusyBox.
2. Report durations in milliseconds (not nanoseconds) — centisecond precision from `/proc/uptime` (~10ms granularity) is sufficient for artifact uploads.
3. Rename all fields, attributes, and variables from `*_ns`/`*Nanos` to `*_ms`/`*Millis` to accurately reflect the unit.
4. Update OTel histogram recording to convert from milliseconds.
5. Update tests to match new field names and units.

## Technical Approach

Use a shell function reading `/proc/uptime` (always available in Linux containers, even with dropped capabilities):

```sh
now_ms() { read up _ < /proc/uptime; ms=${up%.*}${up#*.}0; echo "$ms"; }
```

`/proc/uptime` outputs `12345.67 ...` (centisecond precision). The function strips the decimal and appends `0` to get milliseconds. Duration deltas between two calls give elapsed milliseconds.

### Key files (in worktree `artifact-helper-optimization`)
- `atc/worker/jetbridge/process.go` — shell script, `ArtifactUploadStats` struct, `ParseArtifactUploadStats`, `uploadArtifact`, span attributes
- `atc/worker/jetbridge/process_test.go` — tests for parsing and span attributes
- `atc/metric/otel_artifact_upload.go` — OTel histogram instruments and `RecordArtifactUpload`
- `atc/metric/otel_artifact_upload_test.go` — metric recording tests

## Acceptance Criteria

- [ ] Shell script uses `/proc/uptime` instead of `date +%s%N`
- [ ] Span attributes are `artifact.tar_duration_ms` and `artifact.transfer_duration_ms`
- [ ] OTel histograms use correct unit conversion (ms → seconds)
- [ ] All renamed fields compile and tests pass
- [ ] Values observed in traces are plausible millisecond durations (not single digits)

## Out of Scope

- Changing the artifact-helper base image (stays Alpine)
- Adding new metrics or telemetry beyond the rename/fix
- Modifying the upload logic itself (two-phase tar + mv)
