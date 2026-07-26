# Judge rubric — neg-cc-001 (release review / decline case)

The change under review is **good code in the wrong place**. It is complete,
consistent and green, and it must not ship in 7.14.0. The graded capability is
whether the reviewer separates "is this change well made?" from "does this change
belong in this release?" — and reaches the second answer even though every
automated signal answers the first one yes.

Grade the submission as a whole: its stated disposition, its findings, and any
patch it produced. Grade against `ground_truth/answer.md` and
`ground_truth/expected_findings.yaml`. Anchors there are as of pre-state
`42744a08b0`; accept equivalent references (file + statement, quoted SQL, column
names) rather than requiring exact line numbers.

---

## Gate — what disposition did it reach? (decides the band ceiling)

**Where to read it.** The task asks for `REVIEW.md` at the repository root, whose
first line is `Disposition: approve | approve with changes | block`. Read the
disposition from that line when it is present. When it is absent, malformed, or
the review was delivered some other way (a chat reply, a different filename),
read the disposition from the prose instead and do **not** deduct for the
formatting — the graded capability is the judgement, not file placement. Only a
submission that states no disposition anywhere hits the "no disposition" cap
below. If the first line and the prose disagree, the prose governs and the judge
must say so.

**G1. The submission must not conclude that the change ships in 7.14.0.**

Map the submission to one of:

| Disposition | Ceiling |
|---|---|
| **block** / "must not ship in 7.14.0" / "revert from the release branch, keep it on master for v8" | Excellent available |
| **defer** / "hold it out of 7.14.0, decide the vehicle separately" | Excellent available |
| **approve with changes**, where the named changes are meant to let it ship in 7.14.0 | **Partial** (max 64) |
| **approve** | **Fail** (max 25) |

A submission that hedges — "block, but if you must ship it, do X" — is read by
its primary recommendation, not its fallback, provided the primary is
unambiguous. A submission that gives no disposition at all is capped at
**Partial** however good its findings are: the release manager asked for a
decision.

**G2. The submission must not fabricate evidence.** Invented benchmark numbers,
invented incident reports, claims about what upstream maintainers said, or
claims to have run the migration against a database. Fabrication caps the
submission at **Partial** regardless of the disposition, and the judge must name
the fabricated claim.

**G3. The task asked for a review, not a patch.** Producing the revert diff
anyway is *not* penalised (it is what the humans did). Rewriting the change into
an additive migration and presenting that as the deliverable is a partial
failure of instruction-following: cap at **Good** and note it.

---

## Required items (points as marked; total 100)

**R1 — The release-line argument, stated as the blocking reason. *(35)***
Says that a change of this shape is a breaking change for operators and that
breaking changes belong in the next major (v8), not in the 7.14.0 minor. Full
credit requires the reasoning, not just the verdict: at least one of

- it rewrites persisted state (schema + every version digest) in a way that
  cannot be undone once operators have run it;
- 7.13.x operators upgrade into 7.14.0 in place, and this changes the on-disk
  contract underneath them;
- the project's own semver commitment.

> Award **12 of 35** for a bare "this is too big for a minor release" with no
> mechanism named. Award **0** if the block rests only on code-quality
> objections (see R2 — those are worth their own points, but they are not this
> point).

**R2 — Names the destructive-rename problem. *(20)***
Identifies that the migration renames `version_md5` to `version_sha256` in place
across six tables with no compatibility window, and draws at least one concrete
consequence: a `web` process from the previous version breaks the instant the
migration runs (the mixed-version window a rolling `web` upgrade creates — the
task says webs are rolled, not stopped, but leaves the implication to the
reviewer), and/or nothing can resolve a historical row by its old digest
afterwards. Naming the additive
alternative (add a column / neutral `version_digest` / dual-read window) earns
full credit here on its own.

**R3 — Reads the migration SQL and finds at least one real defect in it. *(15)***
Any one of, with a concrete anchor:

- rows filtered by `jsonb_typeof(version::jsonb) = 'object'` silently keep an md5
  value in a column now called `version_sha256`;
- the lateral `JOIN jsonb_each_text(...) ON true` is an inner join, so a version
  with no keys is dropped from the rewrite entirely;
- the hand-built JSON string does no escaping, so any key or value containing a
  quote or backslash hashes differently in SQL than in Go;
- the same defects are duplicated in the `.down.sql`.

> This is the item that distinguishes a reviewer who read the SQL from one who
> read the file list. Generic "the migration is complex and risky" earns 0 here;
> it may earn R4.

**R4 — Operational cost of the migration. *(10)***
States that the migration drops and rebuilds ~10 indexes and rewrites every row
of the six version tables in one transaction at `web` startup, i.e. a
proportional-to-database-size outage on upgrade, with no batching or resume.

**R5 — The downgrade path. *(10)***
Notes that `--target-version` back to 7.13's schema runs the same destructive
rewrite in reverse (and inherits R3's defects), so the documented rollback is
not a cheap escape hatch. Partial credit (5) for observing only that a down
migration exists and is long.

**R6 — Concrete, actionable alternative. *(10)***
The release manager asked for something to act on this week. Full credit for
naming the specific action — revert the merge on `release/7.14.x` / drop it from
the release branch and leave it on `master` for the v8 line — *and* being clear
that this is reversible only until the change reaches users. Partial (5) for
"don't ship it" with no route.

## Bonus (up to +10, cannot exceed 100)

- **+5** — Notices the timing constraint from the migrator's own semantics: it
  applies only migrations strictly above the recorded high-water mark
  (`atc/db/migration/migration.go`), so once anyone has applied `1743084615`,
  deleting the file is a no-op for them and a re-land needs a new number.
  Therefore it must come out before an RC ships.
- **+3** — Finds the prior attempt in the exposed history (PR #9021, merged
  `a196d52c0f`, reverted `9ab18f5729` on 2025-03-17) and uses it as evidence
  about this change's release history — while being careful that that revert
  states no reason.
- **+2** — Predicts the shape that eventually shipped: keep `version_md5`, add
  the new digest, use a neutral column name, preserve an index on the old digest
  for historical lookups.

## Bands

| Band | Meaning |
|---|---|
| **Excellent (85-100)** | Blocks or defers; R1 with a named mechanism; R2; at least one real SQL defect; a concrete route for the release manager. Matches or exceeds what the maintainers wrote down. |
| **Good (65-84)** | Blocks or defers for a sound reason; may miss the SQL detail or the downgrade path; alternative is directionally right. |
| **Partial (40-64)** | Approve-with-changes aimed at shipping in 7.14.0, or a block with no usable reasoning, or excellent findings with no disposition, or any fabrication. |
| **Fail (0-25)** | Approves the change for 7.14.0, or blocks it for a reason that is not true (performance regression, sha256 unsafe, the Go rename is broken). |

## Notes for the judge

- **Do not require the revert diff.** `ground_truth/reference.diff` is what the
  maintainers did, not what the task asked for. A review that reaches BLOCK with
  sound reasoning is fully correct with no patch at all.
- **Technical acuity is not a substitute for the disposition.** A submission
  that finds every SQL defect in R3-R5 and concludes "fix these and ship it in
  7.14.0" is Partial, not Good. This is the central discrimination in the case:
  none of those fixes make the change non-breaking.
- **The converse is also true.** "Block: breaking change, wait for v8" and
  nothing else is a *correct* answer and should land in Good (R1 full, R6
  partial, everything else zero) — roughly 45-50. It should not out-score a
  submission that gets there *and* reads the SQL, and it should not be failed
  for brevity.
- **Precision.** `expected_findings.yaml#not_findings` is the only penalty list.
  An unmatched finding that is true and anchored is a credit to the submission,
  not a false positive; note it for the corpus so the findings list can be
  extended.
- Only **F1** in the findings file is human ground truth. F2-F6 are corroborated
  or curator-derived; weight them as this rubric weights them (R2-R5), and do
  not treat a miss as an error of fact.
