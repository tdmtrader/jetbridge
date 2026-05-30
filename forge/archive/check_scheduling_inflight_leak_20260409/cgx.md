# CGX: check_scheduling_inflight_leak_20260409

## Session Log

### 2026-04-09 — Initial investigation and fix
- Root cause identified: `inFlightChecks` sync.Map entries are orphaned when
  `engineBuild.Run()` exits via early-return paths that skip `Finish()`
- Two fixes applied: tracker safety net + engine release path
