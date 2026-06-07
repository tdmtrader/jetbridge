# Conductor Growth Experience (CGX)

**Track:** `caching_behavior_and_pvc_20260324`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

<!-- Log moments of frustration, confusion, or repeated attempts -->
<!-- Format: - [YYYY-MM-DD] Description of friction point -->

- [2026-06-07] Reconciled & closed during status review. The track sat at 60% with "manual verification" tasks open, but the fix had long since shipped AND been adopted by the artifact-daemon work. Lesson: verification tasks framed as "manual KinD" went stale once the CI behavioral suite (caching_test.go) existed — periodic reconciliation against current CI would have closed this sooner.

## Closure Summary (2026-06-07)

- Core fix (stable `(jobID,stepName,path)` key + hostPath) shipped in `159fc0c482` and was **adopted** by `DaemonSetBackend.CacheVolume` (reuses `stableCacheKey`). Not superseded — built upon.
- Both original bugs fixed in production (daemon path persists caches; emptyDir is last-resort only).
- Verification satisfied by CI behavioral suite `topgun/k8s_behavioral/caching_test.go` (not among known k8s-e2e failures).
- **De-scoped:** on-disk cache-dir GC (stale `<hostPath>/caches/<key>`) — spun off as a follow-up hygiene task.

---

## Patterns Observed

### Good Patterns (to encode)
<!-- Workflows that worked well and should be automated/standardized -->

### Anti-Patterns (to prevent)
<!-- Mistakes or inefficiencies that should be caught earlier -->

---

## Missing Capabilities

<!-- Tools, commands, or features that would have helped -->
<!-- Format: - Description | Suggested solution | Scope (project/global) -->

---

## Insights & Suggestions

<!-- General observations about improving the development experience -->

---

## Improvement Candidates

<!-- Concrete suggestions for new/modified extensions -->
<!-- Format:
### [Type: skill|command|agent] Name
- **Scope:** project | global
- **Rationale:** Why this would help
- **Source:** Specific conversation/moment that inspired this
-->
