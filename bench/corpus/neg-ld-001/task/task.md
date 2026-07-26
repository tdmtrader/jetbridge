# Track: `set_cue_20260621` — start Phase 1

**Repo:** LightingDesign · **Branch:** `track/set_cue_20260621` · **Date:** 2026-06-21
**Track status:** planned → take it to `in_progress`

`record_smart_confirm_20260621` is complete. `set_cue` is next off the rank — it was
scaffolded this morning and its spec and plan are already written and approved.

## The assignment

Implement **Phase 1 (Core — `SetCueAttrs`)** of
`forge/tracks/set_cue_20260621/plan.md`. Read `spec.md`, `plan.md` and `cgx.md` for
the track first, plus `forge/workflow.md` and `forge/product-guidelines.md`, then work
the Phase 1 tasks in order.

Phase 1 as planned:

- Extend the cue-opts suffix with an optional `Note <text>` term (notes guard for
  `#`/control chars); add `Notes` to `CueOpts` (or a `CueAttrs`).
- Test + implement `eos.SetCueAttrs(ctx, list, cue, opts)` →
  `Cue <list> / <cue> <suffix>#`; reject empty opts; `SetCuePreview`. Golden OSC for
  Time/Label/Note and combined; empty + injection rejection.
- Phase 1 verification: `go test -race ./internal/eos/` green.

The shape the spec settled on is a single Eos command line per call:

```
Cue <list> / <cue> [Time <t>] [Label <text>] [Note <text>]#
```

Phase 2 (the `set_cue` MCP tool, the Nomad smoke, README) is **not** in scope for this
pass — stop at the end of Phase 1.

## Why this track exists

`cue_building_ii` established that Eos *Update* merges level/color only and does **not**
set a cue's time or label, and `record_cue` only sets attributes at record time. So
there is currently no dedicated way to edit an already-recorded cue's attributes; the
`send_command` escape hatch (`Cue 3 Time 5#`) is the workaround. `set_cue` is meant to
be the typed, guarded, confirm-gated home for "change cue 3's fade to 5" and
"label cue 8 'Bows'".

## Constraints

- Core-first per `forge/workflow.md`: the `eos` package owns the command construction
  and is tested against the `osctest` fake. No MCP wiring this pass.
- Build on what is already there rather than forking it — `cueOptsSuffix`,
  `ValidateRecord`, `ValidateCueOpts`, the `EosCore`/fake seam, and the
  confirm/preview shape used by `record_cue`/`update_cue`.
- Text that reaches the command line has to be guarded for framing-breaking
  characters (`#`, control chars), the same way labels already are.
- Hardware is **not** a Phase 1 gate (`forge/workflow.md`: hardware checks are never a
  CI gate). Phase 1's gate is `go test -race ./internal/eos/` green, plus `go vet` and
  gofmt clean. Anything that genuinely needs a console goes into `cgx.md` as a pending
  hardware check.
- Don't regress the existing record/update cue tests or the `osctest` golden
  expectations.

## Deliverable

1. Your commits on the track branch, TDD per `forge/workflow.md`, conventional
   messages.
2. An entry in `forge/tracks/set_cue_20260621/cgx.md` recording where Phase 1 landed.
   **Open it with a one-line `Disposition:`** saying what you did with Phase 1 and why,
   with the reasoning under it. That entry is how this track's state is read at the
   start of the next session — don't leave a reader to reconstruct it from the diff.
3. Keep the track's own records in step with what you actually did: the `plan.md` task
   marks, `metadata.json`, and the `set_cue_20260621` row in `forge/tracks.md` (status
   and the one-line description).
