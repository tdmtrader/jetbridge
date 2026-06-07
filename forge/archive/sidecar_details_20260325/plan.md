> **Reconciled & closed 2026-06-07.** Completed-but-unreconciled: the per-sidecar status/logs/events UI (+ fly watch) shipped on the track's creation day (feat(ui) commits 806261a621..085190c57c) and is live; plan checkboxes were just never flipped.
>
> Reviewed via a parallel track audit; no further work needed (see closure reason). Original plan preserved below for the record.

# Implementation Plan: Sidecar Details in Web UI

## Phase 1: Backend — Sidecar Plan & Event Infrastructure

Wire sidecar metadata into the build plan and emit per-sidecar events so the UI has the data it needs.

- [~] Task 1.1: Add sidecar sub-plans to BuildPlan
  - Extend `Plan` / `TaskPlan` in `atc/plan.go` to include `Sidecars []Plan` entries
  - Each sidecar plan gets a unique ID (derived from parent task plan ID + sidecar index/name)
  - Include sidecar name and image in the plan metadata
  - Update plan serialization so the JSON sent to the UI includes sidecar plans
  - **Files:** `atc/plan.go`, `atc/plan_factory.go`

- [ ] Task 1.2: Generate sidecar plan IDs during task step execution
  - In `TaskStep.Run()`, after sidecars are resolved, assign plan IDs to each sidecar
  - Emit sidecar plan entries as part of the build event stream (similar to `ImageCheck`/`ImageGet` events)
  - **Files:** `atc/exec/task_step.go`

- [ ] Task 1.3: Emit per-sidecar lifecycle and log events
  - Modify `process.go` to emit `InitializeTask`, `StartTask`, `Log`, and `FinishTask` events per sidecar container using each sidecar's plan ID as the `Origin.id`
  - Remove the `[containerName]` prefix from sidecar log lines (no longer needed since each sidecar has its own event stream)
  - Remove the `[main]` prefix from main container log lines
  - Preserve combined output for `fly watch` (sidecar events are still emitted to the same build event stream, just with distinct origins)
  - Track per-sidecar container state (waiting/running/terminated) from K8s pod status and emit appropriate lifecycle events
  - **Files:** `atc/worker/jetbridge/process.go`

- [ ] Task 1.4: Write backend tests
  - Unit tests for sidecar plan ID generation and plan serialization
  - Unit tests for per-sidecar event emission in process.go (verify correct Origin IDs, lifecycle events, and log separation)
  - **Files:** `atc/plan_test.go`, `atc/exec/task_step_test.go`, `atc/worker/jetbridge/process_test.go`

- [ ] Task 1.5: Phase 1 Manual Verification
  - Deploy locally, trigger a build with sidecars
  - Verify sidecar plans appear in the build plan JSON (`/api/v1/builds/:id/plan`)
  - Verify per-sidecar events appear in the event stream (`/api/v1/builds/:id/events`)
  - Verify `fly watch` still shows all output

---

## Phase 2: Frontend — Elm UI Sidecar Rendering

Add sidecar support to the Elm step tree model, event handling, and rendering.

- [ ] Task 2.1: Parse sidecar plans in Elm BuildPlan decoder
  - Add sidecar plan decoding to `Concourse/BuildPlan.elm`
  - Sidecar plans should decode as child plans of a task step, similar to how `image` plans work
  - **Files:** `web/elm/src/Concourse/BuildPlan.elm`, `web/elm/src/Concourse.elm`

- [ ] Task 2.2: Add sidecars to Step model and StepTree initialization
  - Add `sidecars : List StepTree` field to the `Step` type alias in `Models.elm`
  - In `StepTree.init`, when constructing a `Task` node, initialize sidecar sub-trees from the parsed plan and register their leaf steps in the `steps` Dict
  - Each sidecar step starts in `StepStatePending`
  - **Files:** `web/elm/src/Build/StepTree/Models.elm`, `web/elm/src/Build/StepTree/StepTree.elm`

- [ ] Task 2.3: Render sidecars nested under task steps
  - In the task step `viewStepBody`, render sidecars after the main container logs
  - Each sidecar renders as a collapsible sub-step with: name header, status icon, streaming log body with timestamps
  - Follow the existing `viewTreeRecurse` pattern for nested rendering (z-index depth handling)
  - Add appropriate CSS classes for sidecar nesting indentation and styling
  - **Files:** `web/elm/src/Build/StepTree/StepTree.elm`, `web/elm/src/Build/Styles.elm`

- [ ] Task 2.4: Handle sidecar expand/collapse and state transitions
  - Sidecar steps should toggle expand/collapse independently (clicking sidecar header)
  - Sidecar state updates automatically via existing event routing (events with sidecar Origin IDs update the sidecar's step entry in the Dict)
  - Verify sidecars auto-expand on first log output (matching task step behavior)
  - **Files:** `web/elm/src/Build/StepTree/StepTree.elm`

- [ ] Task 2.5: Write Elm tests
  - Test sidecar plan parsing (decode sidecar sub-plans from JSON)
  - Test step tree initialization with sidecars (correct tree structure, step Dict entries)
  - Test event routing to sidecar steps (log events update correct sidecar)
  - Test rendering (sidecars appear nested, expand/collapse works)
  - **Files:** `web/elm/tests/` (new and existing test files)

- [ ] Task 2.6: Phase 2 Manual Verification
  - Load build page for a task with sidecars
  - Verify sidecars appear nested under the task step
  - Verify each sidecar shows status, logs stream in real-time, timestamps work
  - Verify expand/collapse works independently
  - Verify task without sidecars renders unchanged

---

## Phase 3: Integration Testing & Polish

End-to-end verification and edge case handling.

- [ ] Task 3.1: Handle sidecar edge cases
  - Sidecar that fails to start (ImagePullBackOff, CrashLoopBackOff) — should show errored state with diagnostic message
  - Sidecar that exits before main container — should show terminated state
  - Task with many sidecars (3+) — verify rendering doesn't break
  - Task where sidecar config comes from a file artifact — verify plan IDs are still correct
  - **Files:** `atc/worker/jetbridge/process.go`, `web/elm/src/Build/StepTree/StepTree.elm`

- [ ] Task 3.2: K8s behavioral test for sidecar UI events
  - Add or extend behavioral test in `topgun/k8s_behavioral/sidecar_test.go` to verify:
    - Build event stream contains per-sidecar events with correct Origin IDs
    - Sidecar lifecycle events (initialize, start, finish) are emitted
    - Sidecar logs appear under sidecar Origin, not mixed into main
  - **Files:** `topgun/k8s_behavioral/sidecar_test.go`

- [ ] Task 3.3: Phase 3 Manual Verification & Polish
  - Full end-to-end test: set pipeline with sidecars, trigger build, verify UI
  - Verify `fly watch` output is readable (no regressions)
  - Verify old builds (before this change) still render correctly
  - CSS polish: sidecar nesting indentation, status icon alignment, log readability

---
