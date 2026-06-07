# Conductor Growth Experience (CGX)

**Track:** `native_registry_image_check_resolution_20260324`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

<!-- Log moments of frustration, confusion, or repeated attempts -->
<!-- Format: - [YYYY-MM-DD] Description of friction point -->

- [2026-06-07] Reconciled & closed during status review. The plan committed to "Option A" (intercept `TryCreateCheck`) and listed 14 open tasks against it; the team shipped "Option B" (intercept `CheckStep`, the plan's own "simpler" alternative) in `f8a29d4e31`. The plan never got updated, so it read as 39% when the feature was actually done and live. Anti-pattern: a plan that enumerates one implementation option in detail goes badly stale when a different option is chosen — track the GOAL/acceptance-criteria, keep the approach lightweight.

## Closure Summary (2026-06-07)

- Feature DONE & live via Option B: `CheckStep.resolveNatively` (`check_step.go:218,352`) + `WithCheckResolver` wired into every CheckStep (`step_factory.go:144`, `command.go:1143`). All check paths (Lidar/manual/webhook/job-trigger) resolve registry-image natively, no check pods.
- GCP Workload Identity via `authn.NewMultiKeychain(google.Keychain, authn.DefaultKeychain)`; live-verified on theborg by sibling track `fix_native_check_self_notification_feedback_loop_20260413` (2026-06-03).
- Sidecar `image_artifact` E2E covered by `topgun/k8s/integration/skip_image_get_test.go:174`.
- Plan's "`((var))` not resolved" limitation is FALSE for the CheckStep path (source is already credential-evaluated).
- **Residual gap → new track `native_resolver_insecure_ca_certs_20260607`:** native resolver ignores `source.insecure` / `source.ca_certs`.

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
