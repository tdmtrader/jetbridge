# CGX — resource_config_scope GC Race

## Session Log

### 2026-04-08 — Track Created
- Identified root cause: GC `CleanUnreferencedConfigs` cascading DELETE races with active check builds
- Existing codebase pattern: treat FK violations as expected transient errors (2 precedents)
- Fix approach: handle FK violations in check_step.go as non-fatal, consistent with existing patterns
