# Conductor Growth Experience (CGX)

**Track:** `daemon_mediated_artifact_resolution_20260327`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

<!-- Log moments of frustration, confusion, or repeated attempts -->
<!-- Format: - [YYYY-MM-DD] Description of friction point -->
- [2026-06-06] Severe plan↔code drift: `/forge:implement` opened on this track expecting to build the next task (`SkipResourceCache=false`, marked `[~]`), but it — and essentially all of Phases 6–7 — were already implemented and tested in earlier commits/tracks. The plan never got its checkboxes updated. Only an explicit code audit revealed the real state; blindly "implementing" would have re-done or fabricated finished work.

---

## Patterns Observed

### Good Patterns (to encode)
<!-- Workflows that worked well and should be automated/standardized -->
- [2026-06-06] **Audit-before-implement**: grepping the codebase for each pending task's symbols + running the relevant tests *before* writing code surfaced that ~95% of the track was done. This should be a standard first step of `/forge:implement` when a track has been idle across other sessions.
- [2026-06-06] **TDD via the black-box HTTP surface**: daemon tests are `package main_test`, so the Prometheus metrics were driven entirely through `GET /metrics` assertions (Red→Green) without exporting internals — clean and faithful to how the feature is consumed.

### Anti-Patterns (to prevent)
<!-- Mistakes or inefficiencies that should be caught earlier -->
- [2026-06-06] Work landing in sibling tracks (security hardening, read-after-reap) implemented this track's tasks without checking its boxes — plans become untrustworthy. Completion/verification commits should reconcile *all* affected tracks' plans, not just the active one.

---

## Missing Capabilities

<!-- Tools, commands, or features that would have helped -->
<!-- Format: - Description | Suggested solution | Scope (project/global) -->
- A plan↔code reconciliation check | A `/forge` command (or pre-`implement` hook) that flags tasks whose described symbols/tests already exist in the tree | project
- Cross-track task awareness | When a commit satisfies a task described in *another* track's plan, surface it | project

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
