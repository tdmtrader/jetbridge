# Conductor Growth Experience (CGX)

**Track:** `behavioral_test_fixes_20260327`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

---

## Patterns Observed

### Good Patterns (to encode)

- [2026-03-27] Running the full behavioral suite before pushing caught these pre-existing failures. Validates the pattern of always running the full suite on feature branches.

### Anti-Patterns (to prevent)

- [2026-03-27] The sidecar log race is a classic "works in happy path, fails under timing pressure" bug. Detached goroutines streaming to DB without synchronization gates means the build can complete and pod can be deleted before all events are persisted. Any new streaming goroutine should have explicit lifecycle management.

---

## Missing Capabilities

---

## Insights & Suggestions

### Key codebase facts for implementors

**Sidecar log streaming flow:**
- `streamLogs()` in process.go launches goroutines per sidecar via `streamContainerLogsDirect()`
- These goroutines write Log events to the DB via `newDBEventWriter`
- Pod deletion happens in `execProcess.Wait()` after main container exits
- No synchronization between sidecar log goroutines and pod deletion

**metadata-only fetch:**
- `build_step_delegate.go:372` — `metadataFetchImage()` hard-codes `registry-image` as the only supported type
- Custom types need chain resolution: walk `resource_types` until reaching `registry-image`
- The `imageResolver` field on the delegate controls whether metadata-only fetch is attempted

**Notification system (from memory):**
- `handleNotification` does non-blocking send (capacity 1 channels)
- Notifications silently dropped when channel full
- Scheduler MUST poll as fallback, not rely solely on notifications
