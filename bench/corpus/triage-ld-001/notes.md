# triage-ld-001 — curation record

Case built 2026-07-25. Subject repo: `~/LightingDesign` (private, post-cutoff).
Domain: lighting-console show programming. No code in the task.

Shape: **triage** — a pile of semi-structured work items in, one ordered executable plan
out. The platform has no triage workflow yet, so this case doubles as a specification of
what one would have to emit.

## Provenance

### The terminal artifact

`~/LightingDesign/cue-fix-execution-plan-20260626.md`
- mtime `2026-06-26T11:14:10-0700`
- sha256 `4aef718486a925f94f5d2caf10303ca58a8b79a022e85f541f987e56edd45bc3`
- **untracked** — appears in `git status` as `??`, exists at no commit.
- Sealed verbatim as `ground_truth/answer-artifact.md` (byte-identical, `diff -q` clean).

Titled *"Cue Fix — Execution Plan (2026-06-26)"*, addressed "For: a model with the
lighting MCP (`mcp__lighting__*`) connected to the *DareToDream2026* show on the Eos
console. Self-contained — all channels resolved inline." Six sections: global rules, a
one-time mover calibration, a resolved channel reference, an execution order, nineteen
numbered tasks each with tool calls and a Verify line, and a blocked/done-criteria pair.
It cites `cue-notes-20260626.md` as its source of truth for intent.

### The input

`~/LightingDesign/cue-notes-20260626.md`
- mtime `2026-06-26T11:07:46-0700` — **six minutes and twenty-four seconds** before the plan
- sha256 `2b36135a1a1b7940b6fea23c096b719163f60234892e4d53e085c1d678d402a3`
- untracked

22 sections, one per operator note, each with the operator's verbatim words, a "Delta"
reading, apply notes, and a status line. Header carries a standing warning block and a
one-line method.

### Reconstructing the pre-state

Six files, all on disk at T, ordered by mtime:

| file | mtime | tracked? | role at T |
|---|---|---|---|
| `rig.yaml` | 2026-06-22T22:23:41-0700 | yes, **modified vs HEAD** | semantic rig map; `ladders.boom_order`, all 9 gel colours, `center_a/center_b` |
| `venue.yaml` | 2026-06-23T08:42:52-0700 | yes, unmodified | MCP connection config + named channel groups |
| `show-map-README.md` | 2026-06-24T19:40:56-0700 | no | show-map group vocabulary, `look:` schema, Lone Star home table |
| `README.md` → `repo-README.md` | 2026-06-25T14:04:03-0700 | yes, **modified vs HEAD** | the MCP server README, incl. the full 30-tool table |
| `verification.md` | 2026-06-25T14:05:14-0700 | yes, **modified vs HEAD** | live-hardware checklists; what is still unconfirmed |
| `cue-notes-20260626.md` | 2026-06-26T11:07:46-0700 | no | **the work item** |

**information_cut = 2026-06-26T18:07:46Z** (= 11:07:46 local), the mtime of the notes —
the instant the last exposed input came into existence.

Renamed `README.md` → `repo-README.md` in the sealed workspace so it does not collide
with `show-map-README.md`; task.md refers to it by the new name. That is the only edit
made to any exposed byte.

### Git refs

- Working tree at T was on `write-verification`, tip
  `8b17eb9cef630a0f5370963efb0b593459739d1e` (2026-06-25T15:31:30-07:00). Confirmed via
  reflog: checked out from `main` at 2026-06-25T15:24:57, next commit on the branch is
  2026-07-16T09:01:26.
- Tip of `main` at T: `66cfdcc6c2ab8eaa8a25b04236f3326ab073a9c3` (2026-06-23T14:03:03-07:00).
- Nothing was committed anywhere between 2026-06-25T15:31:30 and 2026-07-16T09:01:26, so
  T sits in a clean 20-day gap. No branch contamination is possible.

`8b17eb9` is recorded for provenance only. **It must not be used to materialize the
case**, for three independent reasons:

1. `cue-notes-20260626.md` is untracked and exists at no commit.
2. `rig.yaml` at that SHA still has the old `spec: [87, 88, ..., 96]` block. The
   working-tree version splits it into `center_a: 87 / center_b: 88`, which is what makes
   "cut the centre specials" resolve to 87+88 — six of the nineteen tasks depend on it.
   `README.md` and `verification.md` also differ from HEAD.
3. A checkout drops nine sibling `cue-*.md` documents plus `tools/` into the exposure
   manifest — several of which restate the plan's rules and resolutions outright.

**Seal, don't pin.** Same conclusion review-ld-001 reached for the same directory.

### Sealed exposure manifest

`task/workspace/`, byte-identical (`diff -q` against source, all clean):

```
2b36135a1a1b7940b6fea23c096b719163f60234892e4d53e085c1d678d402a3  cue-notes-20260626.md
b08144972b24019ba6c6cd20dd74dabc2486f93f6b12d21ecf2d17da114493c8  repo-README.md
79ce2428ab537f72b37323a6c255de389abd62ec1473b9157133546fabd8e5ee  rig.yaml
ccc38982c06a8c774a98e5105cf26d7d543c9f95ef759577798187d6e53e8900  show-map-README.md
87cf3072093566b6be085c7ebb30a19d98e08f3d93bacf1e7ba8b17de345d3a6  venue.yaml
da07a7b4cc8816aaa4f029dbc4e2b7e1081e0d690898a1a7debc604489c56f3b  verification.md
```

`rig.yaml` and `show-map-README.md` hash identically to review-ld-001's sealed copies —
neither changed between 2026-06-25 and 2026-06-26.

## Verification of the ground truth

### The channel table re-derives exactly

`ground_truth/derive_channel_table.py --check` (stdlib only; PyYAML is not installed on
this machine so it carries a small parser for the flat subset `rig.yaml` uses) rebuilds
the boom table from `ladders.boom_order` and asserts it against the plan's §2:

```
CHECK OK -- every channel in the reference plan's section 2 table re-derives from rig.yaml.
```

All 9 colours × 6 booms plus house / stair / trough / ring / centre specials / Lone Stars
/ desires match the artifact character for character. The indirection that matters —
`boom_order: [1,3,5,2,4,6]` ⇒ boom1=idx0, boom2=idx3, boom3=idx1, boom4=idx4, boom5=idx2,
boom6=idx5 — is applied correctly in all nine rows, including the two the tasks actually
use (`white booms 3/4 = 64+67`, `yellow boost 14,17 / cut 15,18,16,19`).

### The task count is exact

22 note sections − 2 Song-8 sections − 1 Song-17 merge = **19 tasks**. The full
section→task mapping is in `expected_items.yaml#required_tasks`; every section lands
somewhere, nothing is invented, nothing is renumbered.

### Every tool the plan names exists

Twelve tools across the tasks — `health_check`, `go_to_cue`, `read_stage`, `list_cues`,
`set_cue`, `update_cue`, `record_cue`, `set_channel_level`, `adjust_channel_level`,
`set_lone_star` (incl. `home: true`), `adjust_lone_star`, `get_lone_star` — all present
in `repo-README.md`'s table with the signatures the plan assumes.

### What the oracle gets wrong

Recorded as `expected_items.yaml#reference_defects` and `answer.md`#"Where the reference
is wrong". The load-bearing one:

**D1 — `update_cue` and `record_cue` are described with their semantics reversed.**
Global rule 3 calls `update_cue` "captures the whole live stage" and `record_cue`
"selective merge". `repo-README.md` says the opposite: `record_cue` records the whole
live look; `update_cue` is Eos Update — "merge the current manual changes … not the whole
look". The error is confined to that one parenthetical (the per-task "selective record"
phrasing survives either reading), but the rubric has to award **more** points for
disagreeing with the reference than for matching it. Without this, an agent that read the
tool table properly would be marked down. Same lesson as review-ld-001's `also_true` set:
bound the oracle's error before grading against it.

Two minor ones: the input notes call cue 20.1 "the all-8-color rainbow" and "all 9 ladder
color groups" in one sentence (there are nine; the plan silently corrects it), and
`rig.yaml`/`venue.yaml` disagree with `show-map-README.md` about Lone Star home positions
and zoom (the plan uses the former without flagging the conflict).

### Downstream corroboration of the calibration step

`cue-notes-20260716-session.md` (withheld, three weeks post-cut) records the operator
running exactly the sign test the plan's §1 asks for: *"increasing tilt = aim UP (not down
as the tool description states) … verified live by the operator via a deliberate ±20 swing
test."* So the calibration was necessary — the tool's own documentation had the convention
backwards for this rig — and the plan's stated working assumption ("assume tilt up =
+tilt; FLIP if calibration says otherwise") was right. This is post-cut evidence; it lives
in `ground_truth/answer.md` only.

**Per-task execution is not attested.** That same 2026-07-16 document is a different sweep
(fade times on songs 5/10, a whole-show Lone Star position audit); it never references the
plan or its T-numbering and it works on Song 8, which the plan excluded. `outcome: merged`
here means *the plan was accepted as the deliverable*, not *the tasks got done*. Stated
explicitly in `answer.md` so no grader infers otherwise.

## Leakage analysis

### The correction that reshaped this case

The mining pass described the input as *"raw operator deltas, verbatim quotes"* and the
case as *"expand every named group to channels via the boom-order mapping … order the
walk ascending through the show"*. **Verification shows both claims are wrong**, and this
is the most important thing in this file.

`cue-notes-20260626.md` is not raw. It is itself a worked-up capture, and it already
contains:

| already in the input | consequence |
|---|---|
| `white booms 3/4 = ch 64 + 67`, with the boom-order rule and the 20.1 decode + yellow worked example | channel resolution is largely **carried**, not discovered |
| "spots = Lone Stars 108–112, not the LED spots 98/99" | the ambiguity the candidate flagged is already settled |
| 14.2 marked `⛔ BLOCKED — needs the intended look` | the blocked-item detection is carried |
| a body in **ascending show order** | "order the walk ascending" is nearly free |
| the header's four-part method: trust live over files · +30% bump baked in · typed tools · bounce-verify | four of the eight global rules are carried |
| the `.99` neutral-bridge template quoted inline | T6's content is carried |

`rig.yaml`'s own header comment independently states the boom indirection with a pink
worked example, so even the mapping rule is pre-state.

Rather than discard, the case was **re-scoped to the transformation that actually
happened**, and `expected_items.yaml` tags every single expectation `carried` / `synth` /
`given` so the judge cannot award discovery credit for preservation. What remains genuinely
synthesised: the completed nine-row table, the one-time mover calibration, live-show
safety as a standing gate, "per-cue not per-song" generalised past Song 17, `health_check`
first, twelve grounded tool calls, per-task verify + done-criteria, the collected
blocked/dial-live section, and the 20.1 scope guard.

Difficulty looked **hard** on mechanical proxies — ~420 lines of notes plus a 30-tool
table in, ~200 lines with 19 tasks out, nine applications of an inverted index, and six
distinct trap conditions — but it is *long-context item-preservation and hoisting* hard,
not *inference* hard. Flagged in `case.yaml#curation.learnings`.
**Recalibrated to `moderate` on 2026-07-25** for exactly that reason — see
[Fixup 2026-07-25](#fixup-2026-07-25).

### Constraints deliberately placed in task.md

Three, each provably known to the human at T. These are constraints, not leakage — the
review-ld-001 precedent: omitting a constraint the author demonstrably held makes the
measurement meaningless rather than harder.

1. **"Song 8 is out of scope."** The plan's scope note says "per operator, 2026-06-26" and
   the exclusion appears nowhere in the notes — which mark end-of-Song-8 `🔴 TOP PRIORITY`.
   It is an instruction given in the 6-minute gap between the two files. Without it in
   task.md every agent keeps Song 8 and is penalised for obeying its input. **With** it,
   the exclusion becomes the case's best trap: the notes argue loudly against it.
2. **"Command-line cue editing does NOT store on this console."** Written down
   2026-06-25 in `cue-fix-punchlist.md` (13:47) and `cue-fix-actionlist.md` (14:14), one
   day before T. Not derivable from any exposed file — `repo-README.md` and
   `verification.md` never mention it — so it had to be given. Scored as *carried
   forward correctly*, never as a discovery (`expected_items.yaml#global_rules.R3`).
3. **"This may be run against the live show console."** True at T and the premise for
   the live-show-safety rule. It states the risk, not the mitigation; the mitigation
   ("recording writes to the show → Nomad/offline or explicit go-ahead") still has to be
   recovered from `verification.md`.

The `+30% bump` constraint was **not** added to task.md — the notes' header already
states it.

### The exclusion/rule interaction, stated plainly

The live-show-safety rule appears in the notes exactly once, inside the End-of-Song-8
section. task.md tells the agent to delete Song 8. So an agent that applies the exclusion
mechanically **deletes its only in-notes statement of the safety rule** and must recover
it from `verification.md` ("Anything that RECORDs or UPDATEs a cue … must run on Nomad
offline or a throwaway show — NOT the live show") plus task.md's live-console premise.
This was not designed in; it was found during verification, and it is now the single
highest-weighted rubric item (C1, 9 pts). Worth hunting for deliberately in future cases.

### Solution in the prompt: checked

`task/task.md` names no cue, no channel, no fixture group, no tool, no section structure
and no count. It states the deliverable's *properties* — self-contained, executable by a
model that will not reopen the rig files mid-session, operator decisions surfaced up front
— because those are the operator's actual requirements and the artifact's own header says
so ("Self-contained — all channels resolved inline"). The phrase "all channels resolved
inline" was deliberately **not** reused; task.md says only that the executing model will
not be going back to the rig files, which states the constraint without naming the
mechanism (a resolved table).

One softening: task.md tells the agent that the notes' cross-references to other working
documents are not part of the handoff. That is a workspace fact, not a solution hint,
and without it a well-behaved agent may stall trying to open `cue-fix-actionlist.md`.
Everything those cross-references carry that the plan uses is restated inline in the
notes (verified: the `.99` bridge template, the 17.1–17.8 cue range, the 20.1 house
conflict).

### Content leakage inside the exposed files: checked

- `verification.md` is a hardware checklist. It carries the two facts the plan's rules 6
  and 7 rest on and nothing about the cue fixes. Its exposure is the point — the plan is
  supposed to have read it.
- `show-map-README.md`'s "Anomalies to resolve" covers cue 1.1, 5.2/6.1/6.2, ch2 absence,
  Song-5 audio and an LED `has_color` quirk. None overlaps a task.
- `repo-README.md` is the tool surface. No cue content.
- `rig.yaml` / `venue.yaml` are reference data.

### Withheld

`case.yaml#withheld` lists fourteen paths, none reachable through `task/workspace/`. The
four that matter most: the terminal artifact itself; `cue-fix-punchlist.md` and
`cue-fix-actionlist.md` (both pre-T, both stating the store method and the bump, both
carrying overlapping per-cue corrections); and `cue-deltas-15-20.md` (2026-06-26 10:13,
one hour before T — carries the `.99` neutral-bridge template and the songs 15–20 deltas
in console-ready form). The three big show YAMLs are withheld as noise, not as leaks: the
plan's whole first rule is that they are stale, and the notes' header already says so, so
nothing is lost by leaving them out.

### Memorization: none

Private repo, private show, post-cutoff, a specific rig in a specific venue.

## Grading design

`rubric: judge` over `ground_truth/rubric.md` — 100 points across coverage (25),
channel resolution (20), hoisting (25), tool grounding (15), blocked items (10) and
executability (5), plus up to +8 in bonuses and six automatic failures.

The weighting follows the provenance tags: hoisting is the largest section because it is
almost entirely `synth`; ordering is worth 2 points because it is `carried`.

Not mechanically gradable end to end — there is no test transition and the deliverable is
a document, so `grading.fail_to_pass` / `pass_to_pass` are empty by design. But the
channel table *is* mechanically checkable, and `derive_channel_table.py --check` runs in
about a second with no dependencies. Rubric item B2 is scored against it, not against a
judge's reading.

## Open questions

1. **Is the input too processed?** The honest answer is "partly". The natural harder
   variant, **triage-ld-002**, is to expose only the 22 verbatim operator quotes — they
   are all present in the notes as `**Note (verbatim):**` lines — and require the agent to
   do the classification, the disambiguation ("spots"), the channel resolution and the
   blocked-item detection itself. That is the case the mining pass thought this was, it is
   buildable from the same sealed bytes, and it would isolate inference from preservation.
   Build it if triage-ld-001 turns out to be easy.
2. **Judge variance on section C.** "Stated once as a standing rule" versus "repeated per
   task" is the core distinction and it is a judgement call. If judges disagree across
   repetitions on C1/C2, the fallback is a mechanical proxy: count occurrences of the
   safety and sign warnings and score `1 occurrence in a rules section` over `n
   occurrences inline`.
3. **The D3 bonus rewards beating the oracle**, which is unusual and could reward an agent
   that shotguns disagreements. Mitigated by it being checkable against one line of
   `repo-README.md`. Revisit if it produces gaming.
4. **`execution-plan/v1` does not exist.** Second output-port type this corpus has needed
   for a non-code case (after `review-findings/v1`). Worth deciding whether they collapse
   into one `document/v1` before the harvest adapter is written.
5. **No workflow to run it against.** Triage is unimplemented on the platform. Until it
   exists, this case is run by hand: present `task/`, collect the document, judge.

## Validation

Status: **unvalidated**. Filled by a later stage.

Pre-flight checks already done at build time:

- [x] Terminal artifact read in full; it says what the candidate claimed about structure
      (global rules, calibration, resolved table, ascending order, ~20 numbered tasks with
      tool calls and verify steps).
- [x] All 9 ladder colours × 6 booms re-derived from `rig.yaml` and asserted against the
      artifact's §2 — `derive_channel_table.py --check` exits 0.
- [x] 22 note sections mapped one-to-one onto 19 tasks + 2 exclusions + 1 merge.
- [x] All 12 tools named in the artifact confirmed present in `repo-README.md`'s table.
- [x] Reference defect D1 (`update_cue`/`record_cue` reversal) confirmed against the tool
      table and folded into the rubric as a bonus rather than a deviation.
- [x] Git history checked for branch contamination: 20-day commit gap around T.
- [x] Every sealed byte `diff -q`-verified against its source; sha256 recorded.
- [x] Exposed files grepped for prose that gives away the plan's rules; the only hits are
      the legitimate pre-state ones, all tagged `carried` in `expected_items.yaml`.
- [x] `task.md` reviewed for solution leakage; names no cue, channel, tool or section.

Still to do:

- [ ] Two independent leakage audits → `case.yaml#leakage_audit`. **Point them at the
      carried/synth tagging specifically** — the question is not "can the answer be found
      in the input" (much of it can, by construction) but "does the rubric award credit
      for the parts that can".
- [ ] One end-to-end run to check the Song-8 exclusion actually bites. If every model
      applies it cleanly despite the `🔴 TOP PRIORITY` marker, that trap is not measuring
      anything.
- [ ] One end-to-end run to check whether the live-show-safety rule survives the Song-8
      deletion. This is the case's headline claim; if models recover it from
      `verification.md` every time, C1's weight should drop.
- [ ] Confirm judges can agree on "hoisted once" vs "repeated per task" across repetitions.

## Fixup 2026-07-25

Curator-fixup pass over the two leakage audits (`case.yaml#leakage_audit`: opus
*borderline*, sonnet *pass*). Every flagged item is resolved below into one of four
buckets — dissolved by the exposure contract, real defect, difficulty recalibration, or
known leak channel. Residual verdict: **pass**; the `# BORDERLINE: needs human
spot-check` banner at the top of `case.yaml` was replaced with a pointer to this section.

### Real defects fixed

**1. `task/task.md` — leading sentence softened (§"What happened").**
Before: *"The notes are a capture, not a plan: they are in the order he said them, **each
one stands alone, and several of them repeat the same warning** because I wrote each
section as I went."*
After: *"The notes are a capture, not a plan — I wrote each section as I went, in the
order he said things, and I have not been back over them since."*
Why: rubric §C (Hoisting, 25 pts, "the point of the case") measures turning scattered,
repeated per-item warnings into standing rules stated once. Telling the agent up front
that several sections repeat the same warning names the move it is being scored on —
most directly C2 (three repeated mover-sign warnings → one calibration step, 7 pts). The
replacement keeps everything authentic and load-bearing: nothing applied yet, a capture
rather than a plan, written section-by-section in the order the operator spoke, never
revised. The agent still learns the input is unprocessed; it is no longer told which
property of the input to exploit. No other text in `task.md` changed — the three T-time
constraints and the deliverable properties stand as documented in
[Constraints deliberately placed in task.md](#constraints-deliberately-placed-in-taskmd).

**2. Grading fairness — `ground_truth/rubric.md` §D3 and `expected_items.yaml`
`reference_defects.D1`.**
The sonnet auditor's suspicion is correct and checkable: `show-map-README.md` (exposed,
in the workspace) carries an "Execution recipe" that says to build the **full target
look** — *"anything not listed in target → 0 … a full end-state, not a delta"* — and then
store it with `update_cue`. That is the reference's reversed semantics, planted in the
pre-state independently of the reference. Two consequences, both now written down:
- `expected_items.yaml#reference_defects.D1` claimed a reversal "has almost certainly
  copied the reference's phrasing rather than read the tool table". That inference is
  withdrawn (new key `reversal_is_defensible`).
- `rubric.md` §D3 gains a caveat block: a reversal caps at 2/5 with **no further
  deduction**, `repo-README.md`'s tool table is the authoritative surface (task.md names
  it), and only reconciling the two in the table's favour earns the full 5 + the +4 bonus.
The +4 "beats the oracle" bonus survives; the risk it created — penalising a candidate
for following an exposed document — does not.
Both caveats are also summarised in the new `case.yaml#grading.grading_caveats` so a
grader who reads only the manifest sees them.

**3. `case.yaml#withheld` — three untracked paths the original enumeration missed.**
`augment3d/` (pre-T, 2026-06-21: an Augment3d import kit generated from `rig.yaml` +
`channel-hookup.csv`; its `README.md` restates the boom layout and `positions.csv` /
`eos-import.csv` carry the channel→fixture mapping — the same independent confirmation
`channel-hookup.csv` is already withheld for), plus `AGENTS.md` and `.agents/` (both
2026-07-20, i.e. three weeks **post-cut**, 47 bytes / imported skill definitions, no show
content). None is reachable through `task/workspace/`; they are listed so a future
harvest adapter that materializes the whole working tree excludes them too.

### Resolved with evidence (opus's "unresolved" items)

**`send_command` missing from the exposed tool surface — settled: authentic pre-T state.**
The opus auditor could not tell whether `repo-README.md` / `verification.md` (which have
the `send_command` escape-hatch row removed relative to the pinned ref
`8b17eb9`, and are reachable from no ancestor commit) were uncommitted edits at T or
post-cut state accidentally sealed in. Filesystem evidence settles it:

```
2026-06-25T14:04:03-0700  README.md      (= workspace/repo-README.md)
2026-06-25T14:05:14-0700  verification.md
2026-06-26T11:07:46-0700  cue-notes-20260626.md   <- the cut
```

Both files were last written **the day before the cut** and have not been written since
(re-checked 2026-07-25; the sealed copies are still `diff -q` clean against the working
tree, so current bytes = bytes at T). They are uncommitted working-tree edits that
existed at T — exactly the tool surface the human had when writing the plan. Recorded in
`case.yaml#pre_state.repository.uncommitted_at_T`. Note the direction of the effect: with
`send_command` absent from the table, task.md's R3 constraint ("command-line editing does
not store") is *redundant* with the exposed surface rather than a hint — and R3 is already
tagged `given` with zero credit.

**`AGENTS.md` untracked and absent from `withheld`** — fixed above; it is post-cut anyway.

**"task.md hands over two standing rules plus the transformation strategy"** — split:
the transformation-strategy half is fixed (defect 1 above); the two standing rules are
*given* constraints, already provably held by the author at T and tagged `given` in
`expected_items.yaml` (zero discovery credit, full penalty for violating). Rubric §C
awards nothing for R3 or for the live-console premise; what C1 measures is the
*mitigation* (Nomad/offline or explicit go-ahead), which task.md does not state. Now
spelled out in `case.yaml#grading.grading_caveats` so no future reader has to re-derive it.

### Dissolved by the exposure contract

Per `schema/benchmark-case-v1.md`#"The exposure contract", the solver sees exactly
`pre_state` − `withheld` + `task/`. `case.yaml`, `notes.md` and `ground_truth/` are
harness-side and never exposed. So the sonnet auditor's observation that
`case.yaml#curation.learnings` "names the synthesis moves and the reference's
`update_cue`/`record_cue` error" is **not** a leak — that is the manifest doing its job,
and the same is true of `grading.caution`, the `withheld` list (which names the terminal
artifact), and the case id/title. **No renaming, retitling or redaction was done, and none
is needed.** The one operational consequence, already true corpus-wide: a hand-run must
materialize `task/` into a neutrally-named directory.

### Difficulty recalibration: `hard` → `moderate`

The sonnet auditor argued the effective difficulty is narrower than `hard` because the
notes are already resolved. This file's own
[correction that reshaped this case](#the-correction-that-reshaped-this-case) says the
same thing in more detail: the channel resolutions, the ascending order, the "spots"
disambiguation, the blocked-item detection and four of the eight global rules are all
`carried`. What remains is competent long-context transformation plus a handful of traps
— one of which (C1, the safety rule deleted along with Song 8) is genuinely sharp but
**unvalidated**. On a three-value scale, `moderate` is the defensible label; the honest
`hard` variant is triage-ld-002 (expose only the 22 verbatim quotes — see
[Open questions](#open-questions) №1). Revisit if an end-to-end run shows models
consistently failing the Song-8 exclusion or losing the safety rule.

### Known leak channel: project auto-memory

`known_leak_channels: [project-auto-memory]` added to `case.yaml`. The dev machine's
memory for this case's subject repo —
`~/.claude/projects/-Users-tdmtrader-LightingDesign/memory/` — states this case's
highest-weighted answers verbatim:

| memory file | what it hands over |
|---|---|
| `live-console-safety-guardrails.md` | rule R6 / rubric **C1 (9 pts, the headline discriminator)**: never record/update/delete cues on the live console; levels, colours and positions are fine |
| `eos-cue-editing-method.md` | R3 (command-line editing does not store), R2 (the +30% bump is baked into stored values), R4 (bounce-verify), and the **correct `update_cue` / `record_cue` semantics** — i.e. the answer to the D3 bonus that the human reference gets wrong |
| `lonestar-home-positions.md`, `lonestar-verified-params.md`, `stage-specials-mapping.md` | mover home positions and specials mapping |

Nothing in the case directory leaks these; the environment does. Per
`bench/README.md`#"Operator-environment leakage", a replay harness must not mount project
memory, session context or conversation history into the solver, and **a local hand-run of
this case on this machine is invalid unless memory is suppressed**. Memory itself was not
touched. This is now also the lead item in `case.yaml#curation.learnings`, because it is
the corpus's cleanest example: the leak lands squarely on the single item the case exists
to measure.

### Re-verified during this pass

- `ground_truth/derive_channel_table.py --check` → `CHECK OK`, exit 0 (still stdlib-only,
  ~1s).
- Sealed workspace bytes still `diff -q` clean against all six source files.
- `information_cut` (2026-06-26T18:07:46Z) = the notes' mtime 11:07:46 −07:00 ✓; terminal
  artifact 11:14:10 = 6m24s later ✓. No internal date inconsistency in the manifest.
- `grading.fail_to_pass` / `pass_to_pass` remain empty **by design** (the deliverable is a
  document); no grading overlay exists, so there is no apply-collision to fix.
- `task.md` re-checked after the edit: still names no cue, channel, fixture group, tool,
  section structure or count.

### Files touched

- `task/task.md` — one sentence softened (defect 1). **This is exposed content**; per
  `bench/README.md`#"Corpus versioning" it is only safe because no results exist against
  this case yet (`validation.status: unvalidated`).
- `ground_truth/rubric.md` — §D3 caveat block added.
- `ground_truth/expected_items.yaml` — `reference_defects.D1.reversal_is_defensible` added.
- `case.yaml` — banner replaced; `pre_state.repository.uncommitted_at_T` added;
  `grading.grading_caveats` added; three paths added to `withheld`; `difficulty`
  `hard` → `moderate`; `known_leak_channels` added; `leakage_audit` entry appended;
  `curation.learnings` addendum appended.
- `notes.md` — the difficulty paragraph now points here; this section appended.
