# review-ld-001 — curation record

Case built 2026-07-25. Subject repo: `~/LightingDesign` (private, post-cutoff).
Domain: structured lighting-console show data, no code under review.

## Provenance

### The terminal artifact

`~/LightingDesign/cue-audit-0.1-4.99.md`
- mtime `2026-06-25T15:17:35-0700`
- sha256 `b8a06e9f481ec6b34db14e6e44a7790eb10f983d6c51457df31bd6a1429b6412`
- **untracked** — it appears in `git status` as `??` and exists at no commit.

The document is titled *"Cue Audit — live capture (0.1–4.99) vs show-current.yaml
spec"*. It carries five ranked findings, an explicit list of checks that passed, an
explicit statement of what was deliberately *not* flagged, a list of cues it could
not diff, and a suggested correction order. It is sealed verbatim into
`ground_truth/answer.md` Part 1.

This is an unusually good terminal artifact for a review case precisely because the
operator wrote down his non-findings. Most review artifacts give you recall material
only; this one gives a precision signal too.

### Reconstructing the pre-state

The audit compares two files. Neither is tracked, so there is no commit to pin and
the pre-state had to be reconstructed as a file set ordered by mtime:

| file | mtime | tracked? | role at T |
|---|---|---|---|
| `show-current.yaml` | 2026-06-25T12:00:01-0700 | no | design reference, 4443 lines, 93 cues in rig-group vocabulary |
| `show-captured-live-20260625.yaml` | 2026-06-25T14:38:49-0700 | no | live console re-read, 2237 lines, 374 cue entries |
| `show-map-README.md` | 2026-06-24T19:40:56-0700 | no | group vocabulary + how a `look:` is expressed |
| `rig.yaml` | 2026-06-22T22:23:41-0700 | yes, but **modified vs HEAD** | channel map incl. ladder colours and `boom_order` |
| `channel-hookup.csv` | 2026-06-16T20:48:05-0700 | yes, unmodified | patch sheet; independent confirmation of the boom mapping |

Every one predates the terminal artifact. The audit itself cites the first three by
name; `rig.yaml` and `channel-hookup.csv` are required because the two subject files
are in different vocabularies (groups vs. raw channels) and nothing else bridges them.

**information_cut = 2026-06-25T21:38:49Z** (= 14:38:49 local). Taken from the capture
file's own `captured_at:` header rather than a filesystem mtime — it is self-recorded,
survives copying, and is the instant the last exposed input came into existence. The
audit was written 39 minutes later.

### Git refs

- Tip of `main` at T: `66cfdcc6c2ab8eaa8a25b04236f3326ab073a9c3` (2026-06-23T14:03:03-07:00).
- Next commits anywhere in the repo are `c79c7ea6c9` (15:24:57) and `8b17eb9cef`
  (15:31:30) on `write-verification` — both **after** T, both unrelated (read-after-write
  verification design docs).
- `66cfdcc` is recorded in `case.yaml` for provenance only. It must **not** be used to
  materialize the case: two of the five inputs exist at no commit, `rig.yaml` at that
  SHA is an older form than the one on disk at T (missing the `specials` breakdown and
  the lone-star gobo/home blocks; the `ladders` block that actually matters is
  identical), and a checkout would drop the withheld audit documents into the exposure
  manifest.

### Sealed exposure manifest

`task/workspace/` holds byte-identical copies:

```
8aab01fe75a630a370aa7f364fd78e38aca581ba750f87d370ff4741c0e4d626  channel-hookup.csv
79ce2428ab537f72b37323a6c255de389abd62ec1473b9157133546fabd8e5ee  rig.yaml
07703a52d985f958277850381dae605c58c2148610fd4199c6f233dd3cffecbb  show-captured-live-20260625.yaml
50bef52093dbd045374ddddc98ddea4d3ab0b875685c3ba5b0d832f4dbf88825  show-current.yaml
ccc38982c06a8c774a98e5105cf26d7d543c9f95ef759577798187d6e53e8900  show-map-README.md
```

Nothing was edited. `show-map-README.md` refers to a `show-target.yaml` that is
deliberately not exposed; the dangling reference was left in place because trimming an
exposed input is itself a tell about where to look.

## Verification of the ground truth

All five findings were re-derived mechanically before the case was accepted.
`ground_truth/derive_discrepancies.py` (stdlib only — PyYAML is not installed on this
machine, so it carries a small parser for the YAML subset the `look:` blocks use)
expands each spec look into `{channel: level}` through the `rig.yaml` group tables and
set-diffs it against the captured channel list.

| finding | claim | verified |
|---|---|---|
| F1 | no `lonestars:` in any spec cue before 4.1; 108–112 lit in every captured cue 0.1–3.6 | ✅ first spec `lonestars:` is cue 4.1 at L753; 108–112 present in all 17 cues carrying data, @35 except 1.4 @25 |
| F2 | spec 1.4 is `look: BLACKOUT`; capture holds house/alcove/lonestars/desires | ✅ spec L217–222 is the literal scalar; capture L231–271 has 3/4/5/6 @20, 108–112 @25, 113–119 @52 |
| F3 | pink 43/44/46/47 @52 in 3.1 and 3.2, neither of which specs pink | ✅ pink enters at 2.5; 3.1/3.2 specs have no pink block |
| F4 | white 64/65/67/68 @20 in 3.3–3.6, none of which spec white | ✅ white enters at 3.2 only |
| F5 | 3.4 shows all 7 desires and full booms against a booms-5/6 + desires-usl/usr spec | ✅ and **under-reported**: the circle 80–85 @33 is up too, though 3.4 specs no circle (3.3 and 3.5 both do) |

Two textual imprecisions in the artifact, both harmless and both noted in `answer.md`:
"~18 cues" is 17 by exact count (3.7 and 3.99 were never read back, so the true blast
radius is probably 19), and "ch5+6 @20 (alcove)" treats ch6 as part of the alcove when
neither `rig.yaml` nor `channel-hookup.csv` patches channel 6 at all.

### What the oracle missed

The same derivation turned up five further true discrepancies the operator never wrote
down. They are recorded in `expected_findings.yaml#also_true` and scored **neutral**,
with a bonus for A2:

- A1 — house / entrance aisle / alcove track into 1.2 and 1.3.
- A2 — cue 1.2's house is at 90 against a spec of 7, i.e. ×12.9. It is the *only*
  intensity difference in the audited range that the +30% bump does not explain, and
  the human missed it entirely.
- A3 — circle 80–85 @33 in cue 3.4 (sub-case of F5).
- A4 — desires 113 @52 / 114–116 @39 in cue 3.6, which specs no desires (that capture
  is marked `partial: true`).
- A5 — ch6 @20 in cue 1.4 is an unmapped channel.

Recording these is load-bearing, not decoration. Without them a grader would penalise
an agent that outperformed the human. The "specced but dark on console" set is empty
for every cue in range: every defect in this case is extra light, never missing light.

### Non-findings

The artifact declares three, all confirmed:

- ×1.3 bump — every intensity difference in range fits `captured ≈ spec × 1.3`
  (clipping at 100) except A2.
- ch2 at 100 in every captured cue 0.1–3.6, including the 1.4 blackout. Verified
  literally: `{cue: 100.0}` for all 17.
- Desire hue — ch113 reads 27.8–30 through 0.1–3.3 against a spec of `hue: 30 orange`,
  then 40.893 in 3.4–3.6 against spec 3.4's `hue: 40.9 amber`. Colour is correct
  throughout; only the fixture count in 3.4 is wrong.

A fourth was added by the curator (N4): cues 3.7, 3.99, 4.1, 4.2 and 4.99 have
`look.channels: []` — the read failed. They are not dark on the console, and asserting
that they are is a hallucination rather than a finding. The artifact handles this
correctly ("Not yet diffed").

## Leakage analysis

The dominant risk here is not subtle. Nine sibling documents in the same directory
restate these findings, several verbatim, and all of them predate or immediately
follow T.

**Mitigation: seal, don't pin.** Because the exposure manifest is the five files in
`task/workspace/` and nothing else, none of the following is reachable. They are still
enumerated in `case.yaml#withheld` so a future harvest adapter that materializes the
whole working tree excludes them:

| withheld | why |
|---|---|
| `cue-audit-0.1-4.99.md` | the terminal artifact itself |
| `cue-audit-0-9.5.md` | earlier audit over an overlapping cue range |
| `cue-fix-punchlist.md` (13:47) | pre-dates T by 51 min; carries overlapping corrections *and* the bump constraint |
| `cue-fix-actionlist.md` (14:14) | console-ready form of the same corrections |
| `cue-fix-execution-plan-20260626.md`, `cue-notes-20260626.md`, `cue-notes-20260716-session.md`, `cue-deltas-15-20.md` | post-T, restate the corrections |
| `lonestar-111-capture.md` | same-session lone-star material |
| `show-target.yaml` | hand-authored desired end-state; partially encodes the fixes |
| `cue-looks-list1-every5.yaml` | older raw dump of the same cues |
| `verification.md`, `tools/` | runbooks and generators that encode the corrections |

**Content leakage inside the exposed files: checked, none found.** Grepped both YAML
files for `bleed|track|audit|bump|should be|wrong|fix|park`. The only per-cue `notes:`
in range are:

- 1.1 — "Content changed THIS session … VERIFY vs backup." Unrelated to the five
  findings and a genuine T-time caveat.
- 3.4 — "Sparse booms-5/6-only moment + desires 113&119 only." This restates the
  *spec intent* for 3.4, which is the thing the reviewer must compare against. It is
  not a statement that the console violates it. Kept.

The capture file contains no prose at all. `show-map-README.md`'s "Anomalies to
resolve" section covers cue 1.1, 5.2/6.1/6.2, ch2 absence in 6.5+, song 5 audio and an
LED `has_color` quirk. Corrected 2026-07-25 (the original wording here said "all outside
the audited range", which is wrong for the first item): **cue 1.1 is inside the audited
range 0.1–4.99.** What that bullet says is that 1.1's content was changed this session by
a "save in 1.1 without lights" update and may have overwritten the original red-ladders
look — a T-time provenance caveat the operator raised himself. It names no fixture group
and no cue that appears in F1–F5, does not describe a spec-vs-console divergence, and is
duplicated in `show-current.yaml`'s own `notes:` for 1.1. Kept exposed (authenticity: it
is what the operator had in front of him), and priced in `ground_truth/rubric.md`, which
now tells the judge to score any repetition of these bullets **neutral** — no credit, no
penalty — because they are quoted operator caveats rather than derived findings. The
remaining four items are genuinely outside the range.

**Solution in the prompt: checked.** `task/task.md` names no cue, no channel, no
fixture group and no failure mechanism. It supplies only the two constraints the
operator provably held at T (the punchlist recording the baked-in +30% bump and the
ch2 convention was written at 13:47, 51 minutes before the capture and 90 before the
audit) plus three neutral reading notes about the capture format. Constraints that
suppress false positives are not leakage — omitting them would make the precision
score meaningless rather than harder to earn.

**Memorization: none.** Private repo, private show, post-cutoff, and the data is a
specific rig in a specific venue.

## Grading design

`rubric: reference` is the primary — recall over `expected_findings.yaml#required`.
It must be paired with the judge rubric in `ground_truth/rubric.md`, which carries
everything recall cannot see: precision against the declared non-findings (25 pts and
the actual point of the case), whether the systemic lone-star bleed is stated once or
seventeen times (10), ranking and actionability (10), the clean report the operator
explicitly asked for (8) and honesty about the five cues that were never read (7).

Not mechanically gradable — the deliverable is a document, there is no test
transition, so `grading.fail_to_pass` and `pass_to_pass` are empty by design.
`ground_truth/derive_discrepancies.py` is the adjudication tool: it reproduces the
full lit-but-not-specced set in about a second with no dependencies, and it is the
fastest way to settle whether an unmatched agent finding is real.

Difficulty **hard**, on mechanical proxies: 6680 lines across the two subject files in
two different vocabularies, a mapping layer that must be built before any comparison is
possible, a `boom_order: [1, 3, 5, 2, 4, 6]` indirection that silently mis-anchors F5's
channel list if missed, 5 findings spread over 17 cues, and a noise floor (the ×1.3
bump) that makes almost every numeric field differ. A naive diff produces hundreds of
lines of true-but-useless output; the case is mostly a test of suppressing that.

## Open questions

1. **Scope wording.** `task.md` says "cues 0.1 through 4.99" and tells the agent that
   later entries are list metadata. That mirrors the operator's own framing and the
   artifact's title, but it does hand over the scope. A harder variant would state no
   range and require the agent to discover that only 17 of 374 cue entries carry data.
   Worth building as review-ld-002 if this one turns out to be easy.
2. **Output port.** `signature.outputs` is `{findings: review-findings/v1}`, a type
   that does not exist yet — this is the corpus's first case whose output is a document
   rather than a repository change. The harvest adapter will need it.
3. **Ranking scored at 5 pts** is the softest part of the rubric. "Worst first" is a
   judgement call; the defence is that the lone-star bleed is objectively the widest
   (17 cues) and the human ranked it first for that reason. If judges disagree on this
   sub-score across repetitions, drop it.
4. **A2 bonus.** Awarding points for beating the oracle is unusual and could reward
   noise if an agent shotguns level anomalies. Mitigated by the ×1.3 filter being
   stated in the task: to surface A2 the agent must have applied the filter correctly,
   which is exactly the skill being measured. Revisit if it produces gaming.
5. **`partial: true` on cue 3.6.** The extra desires there (A4) are real, but a
   defensible answer could withhold them pending a clean re-read. Both behaviours are
   scored neutral-to-positive; if that turns out to be too generous, split A4 out.

## Validation

Status: **unvalidated**. Filled by a later stage.

Pre-flight checks already done at build time:

- [x] All five findings re-derived mechanically from the sealed inputs.
- [x] Full discrepancy set computed; oracle gaps recorded as `also_true`.
- [x] All three declared non-findings confirmed against the data.
- [x] `derive_discrepancies.py` runs clean against `task/workspace/` (python3, stdlib,
      no arguments needed from the case directory).
- [x] Exposed inputs grepped for finding-revealing prose; none found.
- [x] `task.md` reviewed for solution leakage; names no cue, channel or mechanism.

Still to do:

- [x] Two independent leakage audits → `case.yaml#leakage_audit` (opus: borderline,
      sonnet: fail; both adjudicated in *Fixup 2026-07-25* below, residual **pass**).
- [ ] One end-to-end run to confirm the ×1.3 trap actually bites (if every model
      filters it cleanly, section B is not measuring anything and the case's quality
      score should drop).
- [ ] Confirm judges can agree on section C (systemic vs. per-instance) across
      repetitions.

## Fixup 2026-07-25

Curator pass over the two leakage audits. Every flagged item is resolved below; residual
verdict **pass**, recorded as the third `leakage_audit` entry in `case.yaml`. The stale
`# BORDERLINE: needs human spot-check` header at the top of `case.yaml` was replaced with
a pointer to that adjudication.

### Dissolved by the exposure contract — no edit

1. **opus + sonnet: `curation.learnings` states A2 quantitatively** ("cue 1.2's house at
   90 against a spec of 7, the one level anomaly the ×1.3 bump does not explain"), and
   discloses the F/N/A set sizes. `schema/benchmark-case-v1.md` §"The exposure contract"
   is explicit: the solver sees pre_state − withheld + `task/`, and `case.yaml`,
   `notes.md`, `ground_truth/` and the case id/path are harness-side, never exposed.
   The sentence is therefore not in the exposure manifest, and sonnet's proposed remedy
   — move it to `notes.md` — moves it from one unexposed file to another. **Left
   verbatim.** Sonnet's FAIL rests on this item alone and dissolves with it. Recorded as
   a corpus lesson in `case.yaml#curation.learnings` ("Audit lesson (2026-07-25)").
2. **Case id / title.** `review-ld-001` and "Audit a live lighting-console capture
   against its design reference" describe the task, not the answer, and are harness-side
   regardless. Not renamed, not retitled.

### Real defects — fixed

3. **`task/task.md`, empty-channel bullet (opus).** It supplied both the interpretation
   *and* the mandate: "Say so rather than concluding the cue is empty on the console."
   That handed over 4 of the 7 points in rubric section F. The interpretation is kept —
   the operator did the capture, provably knew, and without it N4 ("3.7–4.99 are dark")
   stops being a hallucination and becomes a reasonable inference, which would make the
   precision measurement dishonest rather than harder. The **mandate is gone**; the
   agent must now decide for itself that unread cues belong in the report. New text:
   *"A cue whose `look.channels` is an empty list means the read came back with nothing.
   That is my capture failing on that cue, not the console holding an empty cue."*
4. **`task/task.md`, `partial: true` bullet.** Same shape, same fix: dropped "Weight
   anything you conclude from it accordingly" (rubric F's other 3 points), kept the
   statement of what the flag means.
5. **`ground_truth/rubric.md` section F** re-written to match: it now states plainly
   that `task.md` supplies the interpretation but not the instruction, that F scores
   follow-through into the deliverable rather than discovery, that the 4 pts require the
   report to enumerate the affected cues, and that the 3 pts require the `partial` caveat
   to be attached to a conclusion. The same caveat is summarised in `case.yaml#grading`
   so a future reader sees what part of the score is not discovery (this also covers N1
   and N2, which `task.md` states by design).
6. **Missing delivery channel.** The output port is `review-findings/v1` and the rubric
   grades a document, but nothing told the agent where to put one. `task.md` gained a
   "How to hand it back" section: a single Markdown document, `AUDIT.md`, at the top
   level of the provided files, and an explicit read-and-report-only instruction (the
   operator makes the console changes himself). This also gives the rubric's
   "changes any file in `task/workspace/`" hard-failure condition something in the task
   to hang on, instead of penalising an unstated rule. `rubric.md` gained a matching
   **Deliverable** clause including the fallback: an agent that answers in-channel and
   writes no file is graded normally, −3 in section D, not zeroed.
7. **Date coherence in the exposed prose.** The capture's own `captured_at` is
   `2026-06-25T21:38:49Z` = **14:38 local**, so "end of the evening console session" and
   three uses of "tonight" were mildly at odds with the case's own information cut. The
   solver could not detect this (every exposed timestamp is UTC), but a future curator
   could: neutralised to "at the end of the console session", "in this session" and
   "today". No information content changed. The manifest's own dates were re-checked and
   are internally consistent (cut 14:38 local → artifact 15:17 = 39 min; punchlist 13:47
   → cut = 51 min, → artifact = 90 min).
8. **Provenance error in this file.** The leakage-analysis paragraph claimed
   `show-map-README.md`'s "Anomalies to resolve" items were "all outside the audited
   range". Cue 1.1 is *inside* 0.1–4.99. Corrected in place above with the reasoning for
   keeping it exposed.

### Priced deflator — kept exposed, priced in the rubric

9. **`show-map-README.md`'s "Anomalies to resolve" (opus).** Authentic pre-state content
   the operator was working from; it names none of F1–F5 and does not describe a
   spec-vs-console divergence, so it does not collapse the task and stays in the
   manifest. `rubric.md`'s judge notes now carry a **credit-derivation-not-quotation**
   rule: a finding earns points only when the agent can point at the expanded spec look
   and the captured channels, and repeating the README's (or `show-current.yaml`'s) own
   caveats — the 1.1 overwrite included — scores neutral, no credit and no penalty.

### Difficulty

10. **Unchanged: `hard`.** Neither auditor argued for recalibration; both affirmed it
    ("honest difficulty", "legitimately hard"). Nothing in this pass touched the graded
    work — the group→channel expansion through `boom_order: [1, 3, 5, 2, 4, 6]`, the
    ×1.3 noise floor over ~6700 lines, 5 findings across 17 cues. Removing the two
    report-this mandates makes section F marginally harder, not easier. Rationale
    recorded inline in `case.yaml`.

### Known leak channels

11. The dev machine's project auto-memory was checked directly
    (`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/`): the only
    lighting-related content is a one-line source-table entry in
    `project_bench_corpus_v0.md` giving the repo path and its risk labels. **It does not
    state this case's answer**, so `project-auto-memory` is *not* declared.
12. A different operator-environment channel is declared instead:
    `known_leak_channels: [source-working-tree]`. This case seals its inputs as bytes in
    `task/workspace/`, but all thirteen withheld documents — the terminal artifact among
    them — still sit in `~/LightingDesign` on the machine that curated it. A solver with
    shell or filesystem access there can read the audit outright. Replay must confine the
    solver to `task/workspace/`; a local hand-run with `~/LightingDesign` reachable is
    invalid, exactly as a hand-run of a memory-leaking case is invalid without memory
    suppressed.

### Untouched

`task/workspace/` was not modified — the five sha256 values recorded above still verify.
`ground_truth/expected_findings.yaml`, `ground_truth/answer.md` and
`derive_discrepancies.py` were read but not edited; the ×1.3 and ch2 constraints stay in
`task.md` for the reason already argued (they are what the operator held at T, and
removing them would make section B meaningless rather than harder).

## Retarget 2026-07-30

The output port type changed from the curator placeholder `review-findings/v1`
to the registered `review/v1`. Nothing exposed changed: `case.yaml` is
harness-side, `task/` and the pre-state are byte-identical, and
`ground_truth/expected_findings.yaml` is untouched. No results existed against
this case at the time of the change, so the corpus-versioning rule in
bench/README.md is satisfied. Any result must cite the corpus commit it ran
against.
