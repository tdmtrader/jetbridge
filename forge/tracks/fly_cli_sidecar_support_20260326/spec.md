# Spec: Fly CLI Sidecar Event Support

**Track ID:** `fly_cli_sidecar_support_20260326`
**Type:** feature

## Overview

`fly watch` (and `fly execute`, `fly builds`, etc.) currently has no handling for sidecar events. The backend emits `event.Sidecar` events announcing sidecar sub-plans and `event.Log` events with sidecar-scoped origin IDs (e.g. `69c4a88c/sidecar/log-emitter`). Today:

- The `event.Sidecar` event is parsed but silently ignored (no `case` in the render switch).
- Sidecar log output appears interleaved with main container logs with no visual distinction.

Users have no way to tell which logs come from which sidecar vs the main container.

## Requirements

1. When a `Sidecar` event is received, print a header like `sidecar 'log-emitter' attached` (similar to how `selected worker` or `initializing` are shown).
2. Prefix sidecar log lines with the sidecar name so they're visually distinct from main container output (e.g. `[log-emitter] starting postgres`).
3. The origin ID format `<parent>/sidecar/<name>` should be used to map log events to sidecar names.
4. Main container logs remain unprefixed (no behavioral change for non-sidecar builds).
5. Color/styling should be subtle — a dim prefix or distinct color, not overwhelming.

## Technical Approach

### Key files:
- `fly/eventstream/render.go` — Add `case event.Sidecar` to the switch, track origin-to-name mapping, prefix sidecar logs.
- `atc/event/events.go` — `Sidecar` struct already has `Origin` and `PublicPlan` (contains sidecar name).
- `atc/public_plan.go` — `SidecarPlanID()` derives `<parent>/sidecar/<name>` format.

### Approach:
1. In `Render()`, maintain a `map[event.OriginID]string` mapping sidecar origin IDs to names.
2. On `event.Sidecar`, parse the plan to extract the sidecar name, register the mapping, print a header.
3. On `event.Log`, check if the origin matches a known sidecar — if so, prefix the log line with `[sidecar-name]`.

## Acceptance Criteria

- [ ] `fly watch` on a build with sidecars shows sidecar attachment headers.
- [ ] Sidecar log lines are prefixed with the sidecar name.
- [ ] Main container logs remain unprefixed.
- [ ] Builds without sidecars are completely unaffected.
- [ ] Unit tests cover sidecar event rendering.

## Out of Scope

- Filtering logs by sidecar name (e.g. `fly watch --sidecar=log-emitter`).
- Separate log streams per sidecar (would require protocol changes).
