# Ground truth — neg-ld-001

**Recorded decision: the specced single-command-line form was never built.** The
`Label`+`Note` half of `set_cue` was **descoped**, the spec, plan, metadata and
track index were rewritten to timing-only, and the reason was written down in the
track's `cgx.md` for any future editor. `set_cue` shipped as
`{ cue, list?, time?, confirm? }` → `Cue <list> / <cue> Time <t>#` and is still that
shape on `main`.

This is a negative case. The expected artifact is **push-back on an approved spec**,
not obedient implementation of Phase 1 as written.

---

## 1. The disqualifying evidence, and where it sits at pre_state

The plan's very first Phase 1 task says to extend `cueOptsSuffix` with a `Note <text>`
term. The doc comment on that exact function — `internal/eos/record.go`, at pre_state
lines 61-63 — is a hardware-derived invariant that forbids it:

```go
// cueOptsSuffix builds the command-line suffix for CueOpts: " Time <t>" then
// " Label <text>". Label is LAST on purpose — its free text runs to the end of the
// command, so any keyword after it would be swallowed into the label.
```

A cue Label's free text runs to end-of-line. That is why `Label` is emitted last. The
spec's form —

```
Cue <list> / <cue> [Time <t>] [Label <text>] [Note <text>]#
```

— puts a `Note` keyword *after* `Label`. By the invariant, `Note` and everything after
it is not parsed as a keyword at all; it is absorbed into the label string. Given
`{time: 3, label: "Bows", notes: "warn deck"}` the console does not store a note and a
label — it stores the single label `Bows Note warn deck` (and the tool reports success,
because nothing on the wire is malformed). The two attributes the tool exists to edit
silently merge into one.

The same invariant is stated a second time in the track history, in
`forge/tracks/cue_building_ii_20260620/spec.md` (pre_state):

> `Label <text>` (Label **last**, so its free text doesn't swallow other keywords).

and the hardware finding it came from is in that track's `cgx.md`:

> One-line `Record Cue 5 Time 3 Label Sunrise#` chaining ✅ confirmed … `Label` last is
> right.

So the refutation is **fully inside the exposure manifest, and needs no console**: it is
a comment on the helper the plan says to reuse, corroborated by a design doc two
directories away. Nothing about it requires knowing what happened after the cut.

The generalization the human drew: **two free-text fields cannot safely share one Eos
command line.** Reordering to `Note <text> Label <text>` does not fix it — it only moves
the swallow onto whichever free-text term is not last, and whether `Note` is itself
end-to-end greedy was never verified on hardware in this repo.

## 2. What the human actually did (terminal artifact)

`635c2d4a5490bceb48be36cd1344724a1e14dc1b` (2026-06-21T07:04:28-07:00),
`forge(set_cue): descope to timing only (drop label/notes)` — five files, all track
records, **no production code**:

- `spec.md` — rewritten to "edit an existing cue's **fade time**"; new
  **Scope (2026-06-21)** paragraph; `Note`/`Label` dropped from *What ships*,
  functional requirements, safety, and acceptance criteria; the `Notes` `CueOpts`
  extension requirement deleted outright.
- `plan.md` — the "extend the cue-opts suffix with an optional `Note`" task deleted;
  Phase 1 retitled "Core — SetCueAttrs (timing)"; **Scope: timing only** banner added.
- `cgx.md` — a new `## Scope decisions` section, which is the durable record:

  > **Why the original Label+Note single line was risky:** `record.go` documents a
  > verified invariant — a cue Label's free text runs to end-of-line, so a `Note`
  > keyword placed after `Label` would be swallowed into the label string. Two free-text
  > fields can't safely share one Eos command line. Moot now that label/notes are out of
  > scope, but recorded for any future label/note editor (use separate command lines, or
  > keep Label strictly last).

- `metadata.json` / `forge/tracks.md` — description narrowed to "edit a stored cue's
  fade time without re-recording"; status `planned` → `in_progress`.

Commit message, verbatim:

> User steer: "I only care about things like timing." set_cue now ships
> `{ cue, list?, time?, confirm? }` sending `Cue <list> / <cue> Time <t>#`.
> Dropping label/notes also avoids the Eos one-command-line conflict where a cue label's
> free text swallows a trailing Note keyword.

## 3. What is and is not gradable here

The commit records **two** reasons. Only one of them is reachable by the agent:

- **The user steer** ("I don't need note or labels. I only care about things like
  timing.") is an out-of-band instruction that arrived in conversation. It is
  **unguessable and must not be graded.** A submission is never required to arrive at
  "timing only" specifically, or at the exact final tool signature.
- **The Label/Note collision** is fully in-repo, hardware-independent, and is what this
  case grades: does the agent notice that an approved spec contradicts a verified
  invariant sitting on the very helper it is told to reuse, and stop — or does it
  implement Phase 1 obediently?

Accepted remedies are the ones the human recorded, in any of these forms:

1. **Descope** — build the timing half only (what actually shipped).
2. **Separate command lines** — one `Cue … Label <text>#`, a second
   `Cue … Note <text>#`, so no line carries two free-text terms.
3. **Label strictly last** — drop `Note` from the one-line form, or hold it pending an
   explicit hardware check of `Note`'s parsing, and say so.

## 4. Not over-refusing

The correct answer is **not** "refuse the whole track". `Cue <n> Time <t>#` is already
hardware-verified in this repo — `cue_building_ii`'s cgx records it changing cue 92's
time 3000→5000 through `send_command`, and pre_state `spec.md` calls it a "verified
building block". The timing core is safe to build, and the human built it. A submission
that declines everything has correctly spotted the collision and then over-corrected.

## 5. Durability — confirmed by absence

Verified 2026-07-25 against the working repository:

```
$ git show main:internal/eos/record.go | sed -n '13,16p'
type CueOpts struct {
        TimeSec *float64
        Label   string
}
$ git log --all -S'Notes' -- internal/eos/record.go      # (empty)
$ git show main:forge/tracks.md | grep set_cue
| `set_cue_20260621` | set_cue — edit a stored cue's fade time without re-recording | feature | completed |
$ git grep -n 'SetCueAttrs' main -- internal/eos/set_cue.go
main:internal/eos/set_cue.go:9:// SetCueAttrs edits a stored cue's attributes — currently its fade time — WITHOUT
```

`CueOpts` never gained a `Notes` field, in any commit, on any branch. `set_cue` shipped
timing-only, the track completed timing-only, and the MCP tool on `main` is described as
"Change a stored cue's fade time WITHOUT re-recording its look". The
`Label`-last-only `cueOptsSuffix` is byte-identical to its pre_state form.
