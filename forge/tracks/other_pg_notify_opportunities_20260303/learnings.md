# Learnings

### 2026-03-04 [good-pattern]

- [2026-03-03] The component.Runner TestNotifyOnly already validates that Interval=0 components never poll and only wake on NOTIFY. Individual components (scanner, drainer, etc.) don't need runner-level tests — they just need to verify NOTIFY calls are added at the correct DB mutation points. The Coordinator wraps Runnable.Run() for both RunPeriodically and RunImmediately, so the scanner's Run() works identically in both modes.
