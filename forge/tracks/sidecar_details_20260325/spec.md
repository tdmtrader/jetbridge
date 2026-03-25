# Spec: Sidecar Details in Web UI

**Track ID:** `sidecar_details_20260325`
**Type:** feature

## Overview

Sidecar containers attached to task steps are currently invisible in the Concourse web UI. Their logs are mixed into the main task log stream with `[name]` prefixes, there's no per-sidecar status indicator, and no way to expand/collapse individual sidecar output. This track surfaces full sidecar details — status, logs, and events — as nested items under their parent task step, matching the same level of visibility that task steps themselves have.

## Motivation

- **Debugging**: When a sidecar (e.g., cloud-sql-proxy, artifact-helper) fails or misbehaves, operators must parse prefixed log lines manually. Separate per-sidecar log streams with status indicators make this immediate.
- **Observability parity**: Task steps show lifecycle state (pending/running/succeeded/failed), streaming logs, and timestamps. Sidecars should have the same.
- **Growing sidecar usage**: As the K8s runtime matures, sidecars are used for caching, artifact upload, database proxies, and more. UI visibility becomes essential.

## Requirements

1. Each sidecar container associated with a task step MUST appear as a nested, expandable item under that task step in the build page.
2. Each sidecar MUST show its own lifecycle state (pending, running, succeeded, failed, errored) with the appropriate status icon.
3. Each sidecar MUST have its own independent log stream (not mixed with the main task container logs).
4. Each sidecar MUST show timestamps on log lines, consistent with task step log rendering.
5. Sidecar items MUST be collapsible/expandable independently of the parent task step and of each other.
6. The main task container's log stream MUST no longer contain `[sidecar-name]` prefixed lines from sidecars (those go to their own streams). The `[main]` prefix on main container lines should also be removed since it's no longer needed for disambiguation.
7. The build plan sent to the UI MUST include sidecar metadata (name, image) so the UI can render sidecar placeholders before logs arrive.
8. `fly watch` and the raw events API MUST continue to work — sidecar events should be additive, not breaking.

## Technical Approach

### Precedent: imageCheck / imageGet

The existing `imageCheck` and `imageGet` nested steps on `Step` in the Elm model are the direct pattern to follow. They are nested `StepTree` nodes within a parent step, each with their own plan ID, event stream, log buffer, state, and expand/collapse. Sidecars follow the same pattern.

### Backend Changes

1. **Build Plan** (`atc/plan.go`): Add `Sidecars []Plan` to `TaskPlan` / `BuildPlan` so the UI receives sidecar plan entries (each with a unique plan ID, name, and image).
2. **Plan Construction** (`atc/exec/task_step.go`): When sidecars are resolved, generate sub-plan IDs for each sidecar and include them in the emitted plan.
3. **Event Emission** (`atc/worker/jetbridge/process.go`): Replace the current `[name]`-prefixed merged log streaming with per-sidecar event emission using each sidecar's plan ID as the `Origin.id`. Emit lifecycle events (initialize, start, finish) per sidecar.
4. **Event Types** (`atc/event/events.go`): Reuse existing event types (`Log`, `InitializeTask`, `StartTask`, `FinishTask`) with sidecar-specific Origin IDs. No new event types needed — the plan structure tells the UI which IDs are sidecars.

### Frontend Changes

5. **Elm Build Plan** (`web/elm/src/Concourse/BuildPlan.elm`): Parse sidecar sub-plans from the build plan JSON.
6. **Step Model** (`web/elm/src/Build/StepTree/Models.elm`): Add `sidecars : List StepTree` to the `Step` type alias (following `imageCheck`/`imageGet` pattern).
7. **Step Tree Init** (`web/elm/src/Build/StepTree/StepTree.elm`): When constructing a Task step, initialize sidecar sub-trees from the plan and register their leaf steps in the steps Dict.
8. **Event Routing** (`web/elm/src/Build/Output/Output.elm`): No changes needed — events already route by `Origin.id` to the steps Dict. Sidecar steps will be found by their plan IDs automatically.
9. **Rendering** (`web/elm/src/Build/StepTree/StepTree.elm`): Render sidecars as nested expandable items within the task step body, below the main container logs. Each sidecar renders with the standard step header (name, status icon) and log body.

## Acceptance Criteria

- [ ] A task with sidecars shows each sidecar as a named, expandable sub-item nested under the task step.
- [ ] Each sidecar displays its lifecycle state (pending → running → succeeded/failed) with the correct status icon.
- [ ] Each sidecar has its own streaming log output with per-line timestamps.
- [ ] Sidecars can be expanded/collapsed independently.
- [ ] The main task container logs no longer contain `[sidecar-name]` prefixed lines.
- [ ] A task with no sidecars renders identically to today (no regression).
- [ ] `fly watch` continues to display all output (main + sidecar logs).
- [ ] Build event stream is backward-compatible — older UI versions ignore unknown plan entries gracefully.

## Out of Scope

- `fly hijack` into sidecar containers (K8s native `kubectl exec` covers this)
- Sidecar resource usage metrics (CPU/memory) in the UI
- Sidecar configuration editing from the UI
- New sidecar-specific event types (reusing existing types with sidecar Origin IDs)
- Sidecar data in the DB beyond what's already in the build plan JSON
