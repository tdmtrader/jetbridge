# Learnings

### 2026-04-25 [missing-capability]

`forge_start_task` / `forge_complete_task` MCP tools fail to match multi-line task text — even when the literal text from plan.md is supplied. Tasks in this plan are intentionally multi-line (Task: leading line + 4-6 line description + File: footer) to keep diffs readable; the tool reports "Task not found in plan.md". Falling back to manual `Edit` on plan.md works but loses the atomic write / metadata.json sync that MCP would provide. Suggested fix: tool should normalize whitespace (collapse newlines+indent to single space) before matching, OR accept just the first non-empty line of the task as the lookup key.

### 2026-04-26 [good-pattern]

Three-phase TDD progression worked well for a multi-component feature touching both daemon and ATC layers: (P1) plumb the read-side fallback as a no-op-today foundation, (P2) add the actual mirror that makes the foundation useful, (P3) close the async-write window with preemption-triggered evacuation. Each phase shipped independently testable behavior. Splitting Phase 2 further into 2a (daemon subsystem), 2b (ATC triggers), 2c (Helm), 2d (behavioral) kept commits focused and reviewable.

### 2026-04-26 [anti-pattern]

Phase 1 unit tests (TestDaemonSetVolume_StreamOut_FallsBackToPeer_OnConnectionRefused) used contrived URLs (`/artifacts/h/o`) where the volume key happened to match a steps/-served path, masking a real production bug: in production, vol.key is the volume handle (no peer alias), so the peer-fallback URL `/artifacts/{key}` 404s on peers. The bug only surfaced when writing the Phase 2d integration test against a more realistic setup. Fix: peer-fallback URL must use `/artifacts/steps/{key}` (filesystem path) since peers receive mirrored data via /stream-in (no alias). Lesson: when designing tests around an HTTP API, ensure the test's URL pattern matches what the SUT actually emits in production, not just what happens to make the test pass.
