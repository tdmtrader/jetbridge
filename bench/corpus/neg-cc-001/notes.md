# neg-cc-001 — curation record

Built 2026-07-25. Source: upstream `concourse/concourse` history, via the
`upstream` remote of the jetbridge working clone. All SHAs below are on the
**upstream** line (see "The duplicated-history trap").

## Provenance walk

Backwards from the terminal artifact:

```
ff09ee64fc  Revert "Merge pull request #9165 from IvanChalukov/sha256-switch"
            Taylor Silva, authored 2025-07-28T14:11:55-04:00,
                          committed  2025-08-08T16:03:02-04:00
            body: "This reverts commit 2eb97bf1d9…, reversing changes made to
                   c17cb83843…. This is a breaking change that will be
                   introduced in v8 of Concourse."
            21 files, +249/-513 — the exact inverse of the merge.
  ^
2b355de626  Revert "Merge pull request #9194 from vlfig/drop-deprecated-vars"
            same author, same day, IDENTICAL closing sentence.
  ^
bab53b82f9  Revert "Merge pull request #9197 from vlfig/fly-harmonize-strictness"
            "Needed to revert this to revert a prior PR with breaking changes."
  ^
42744a08b0  "fix how SpecContext gets passed in"  ← PRE-STATE
            = git merge-base upstream/master upstream/release/7.14.x,
            i.e. the commit release/7.14.x was cut from. %cI 2025-08-08T15:59:33-04:00.
```

The change under review:

```
2eb97bf1d9  Merge pull request #9165 from IvanChalukov/sha256-switch
            2025-05-10, first parent c17cb83843 (= master tip at merge time),
            so `git diff c17cb838 2eb97bf1` is the PR's whole net effect:
            21 files, +513/-249, including
            atc/db/migration/migrations/1743084615_switch_md5_to_sha256.{up,down}.sql
            branch commits: c2131f9093 "Implement migration from md5 hashing to
            sha256", bf1d50b5c6, d0a15e5fcf. None mention "breaking".
```

Verified by hand, each claim with the command that produced it:

| claim | check |
|---|---|
| ff09ee64fc is on release/7.14.x and nowhere else | `git for-each-ref --contains ff09ee64fc` → only `refs/remotes/upstream/release/7.14.x` |
| the revert is the exact inverse of the merge | `git show --stat` of each: same 21 paths, +513/-249 vs +249/-513 |
| both migration files are deleted by it | in the stat, `1743084615_switch_md5_to_sha256.{up,down}.sql` at -138/-131 |
| the pre-state is the branch point | `git merge-base upstream/master upstream/release/7.14.x` = 42744a08b0 = `bab53b82f9^` |
| PR #9165 is present at the pre-state | `git merge-base --is-ancestor 2eb97bf1d9 42744a08b0` → yes; `git ls-tree` shows `1743084615_*.sql` |
| the reference revert applies at the pre-state | the sibling reverts touch none of these 21 files (`git diff 42744a08b0 2b355de626 -- atc/db atc/gc atc/scheduler` is empty); `git apply --check` passes |
| the decline held | `git ls-tree upstream/release/7.14.x atc/db/migration/migrations/ \| grep sha256` → empty; the whole 7.14 line shipped on md5 |
| the work came back differently | `upstream/master` and `upstream/release/8.0.x` carry `1747084615_switch_md5_to_sha256.*`, added 2025-09-04 by 25e7f825e7 (which deleted 1743084615), then reworked by e2dad0b8b9, 20b5869a26, aa1b54c799, 46127b3a64 |

### Corrections to the mining candidate

1. **Wrong SHAs for the merge and its base.** The candidate gave
   `79863cfc22` / `7a40405b93`; the artifact's own body names `2eb97bf1d9` /
   `c17cb83843`. Both pairs exist in this clone with identical subjects, authors
   and timestamps — see the trap below. The case pins the upstream pair.
2. **"Two other breaking PRs already backed out" overstates the corroboration.**
   bab53b82f9 (#9197) is mechanical — its own message says it was reverted only
   so the #9194 revert would apply. Two independent policy decisions, not three.
   Recorded in `case.yaml#source.terminal_corroboration`.
3. **The March 2025 revert is not a second instance of the same reason.**
   9ab18f5729 (reverting PR #9021, the earlier md5→sha256 attempt) states no
   reason; #9021 carried no migration at all, and #9165 looks like the redo that
   added one. Demoted to "this feature has a history", which is all it supports.
4. **"Shipped in v8" is not what happened.** The maintainers said the change
   would arrive in v8; what arrived is a *different* migration
   (`1747084615`) with an additive design — `ADD COLUMN version_sha256` on
   `resource_config_versions`, the other five columns renamed to a neutral
   `version_digest`, `version_md5` kept and re-indexed, with the in-file comment
   "Keep version_md5 so the scheduler/algorithm planning keeps working with
   historical data". The reviewers' objection was to the vehicle *and* the
   shape. This strengthens the case; it is in `ground_truth/answer.md`.
5. **One candidate hypothesis checked and dropped.** `1743084615` (2025-03-27)
   sorts below `1746768931_add_signing_keys`, which suggested an out-of-order
   migration hole at the pre-state. It is not one: signing-keys landed on this
   line on 2025-06-23, *after* #9165 merged, so continuous deployments applied
   them in number order. The renumber in September is explained by the migrator
   only running versions strictly above the recorded high-water mark
   (`atc/db/migration/migration.go`: `if currentVersion < m.Version && …`), which
   makes re-landing a deleted migration under its old number a silent no-op.
   That semantic survives in the findings as F6 (a *timing* argument), not as a
   defect visible at the cut.

### The duplicated-history trap

This working clone contains two parallel copies of Concourse history that
diverge at `a822c69c7a` (2018-10-23). Every commit in this case has a
subject-, author- and timestamp-identical twin on the other line
(#9165's merge is `79863cfc22` there; `42744a08b0` appears as `57fd0c7b31`).
Local tags mostly resolve to that other line — `v7.14.0` and `v8.0.0` are on it,
while `v8.0.2`, `v8.1.0`, `v8.1.1` are on the upstream line — so
`git tag --contains` and any "which release shipped it" reasoning based on local
tags is unreliable. Everything asserted in this case was re-derived from
`upstream/*` remote-tracking refs only, and `case.yaml` materializes from
`https://github.com/concourse/concourse` for the same reason.

## Case design

- **Shape.** Code review, not small-fix. The curator brief said "task = the
  original change request; correct answer = decline", and on this branch there
  is no snapshot in which the change is absent *and* the decline is not already
  written in the git log — the release branch inherits the change from its
  cut point. So the original change request is presented the way it actually
  reached the decision point: as content already merged, being reviewed for
  inclusion in a release. That is exactly what the maintainers did.
- **Pre-state = the branch point, not the revert's parent.** The revert's parent
  is a sibling revert whose body contains the graded sentence verbatim. One
  commit further back the record is clean.
- **Three real dispositions.** approve / approve-with-changes / block. A
  non-breaking change reviewed under the identical prompt would come back
  "approve", so the framing is symmetric and carries no tell. The nearest thing
  to a tell is naming "release-line fit" as one of three review dimensions; it
  stays because without it the case grades whether the model guesses that
  release policy is in scope, and because a positive arm would carry the same
  three dimensions.
- **The task supplies the policy.** The repository does not state its semver
  commitment in tree (CONTRIBUTING.md is build instructions;
  `.github/ISSUE_TEMPLATE/release-*.md` and `.github/release.yml` treat breaking
  changes as a distinct, major-bump category but do not state the rule). So
  task.md states it: breaking changes ship in a major only; v8 is unscheduled;
  webs roll; migrations run at web startup; `concourse migrate
  --target-version` exists. All four are true of Concourse. Supplying them is
  the price of a fair case and is why `curation.quality` is 4: this measures
  "given the policy, do you apply it", not "do you know it exists".

## Leakage analysis

**Withheld (never exposed):** the whole `ground_truth/` directory, and the three
revert commits by reachability. The materialization recipe in `case.yaml` is
load-bearing — a naive `git clone && git checkout <sha>` leaves
`upstream/release/7.14.x` in the clone, and `git log --all --grep=Revert | head`
then hands over the answer key verbatim. The recipe's five `git cat-file -e`
assertions are the check.

**Deliberately exposed, and why:**

- `9ab18f5729`, the 2025-03 revert of the earlier attempt at this feature, is an
  *ancestor* of the pre-state. It cannot be removed without rewriting history,
  and it should not be: it states no reason, so it is a lead rather than an
  answer, and finding it is exactly the kind of work a good reviewer does. Worth
  +3 bonus in the rubric, no more.
- `.github/ISSUE_TEMPLATE/release-patch.md` ("to avoid accidentally shipping
  breaking changes … with patch releases") and `release-major-minor.md`
  ("If the changes involve a breaking changes, that should be a major version
  bump") are in tree at the cut. They corroborate the policy the task states.
  Not a leak — they name the category, not this change.
- The change under review is in the snapshot as well as in
  `task/change-under-review.diff`. Intentional: it is what "already merged to
  master" means.

**Network must be off.** `memorization_risk: high`, this is public pre-cutoff
upstream history, and the task names PR #9165 by number. One `gh pr view 9165`,
or one search for `1743084615_switch_md5_to_sha256`, returns the revert and its
sentence. Scrubbing the PR number would not help — the migration filename in the
diff is just as searchable — so the mitigation is environmental, recorded in
`case.yaml#grading.environment`. A result produced with network access is void.

**No `withheld` paths.** Nothing present at the pre-state gives the answer away.

**The nominal cut.** `information_cut` is the pre-state's commit timestamp,
2025-08-08T15:59:33-04:00, and the reverts were committed four minutes later —
but they were *authored* on 2025-07-28, on an earlier iteration of the release
branch that was replaced. So the humans had already decided eleven days before
the snapshot exists. Nothing from that decision is reachable from the pre-state,
so the exposure manifest is clean, but the case should be described honestly as
"would you reach the same decision from this branch state", not as a prediction
task.

## Open questions

- **Is one bit enough?** The primary signal is block-vs-approve. The judge
  rubric carries the discrimination that matters (R1's mechanism, and the
  Partial ceiling for approve-with-changes-that-still-ships), but a single case
  cannot separate "reasoned decline" from "declines when asked about release
  branches". If pilot runs show models blocking everything put in front of them
  in this framing, the corpus needs a **positive control** with the same task
  shape — a change that landed on a release branch and should have — before any
  result here means anything. That control does not exist yet and is the single
  most valuable follow-up to this case.
- **Postgres validation not run.** The claim "green at both ends" is verified as
  build + test-package compile only. Running `ginkgo ./atc/db/
  ./atc/scheduler/algorithm/` at both ends with Postgres would upgrade
  `validation.status` toward `validated`; the expected result is green at both
  ends, which is the case's claim rather than a transition.
- **Is `review/v1` the right output port** for a disposition plus ranked
  findings, or does a release-gate review want its own type with an explicit
  verdict field? The outcome rubric here has to be applied by reading prose;
  the platform has no "recommendation" slot to grade directly. Same gap
  neg-jb-001 records from the other direction ("the correct change is empty").
- **Difficulty is a guess.** 21 files / 1866 diff lines is large, but the
  blocking signal (`RENAME COLUMN` × 6 plus a whole-table rewrite) is salient
  once the reader knows this is a minor release. Set `moderate`; revisit after
  pilot scores.

## Validation

Run at curation time on the extracted pre-state tree
(`git archive 42744a08b0 | tar -x`), go1.25.1 darwin/arm64, module cache warm:

| check | result |
|---|---|
| `git apply --check ground_truth/reference.diff` at pre-state | pass |
| `go build ./atc/db/... ./atc/scheduler/...` at pre-state | exit 0 |
| `go build ./atc/db/... ./atc/scheduler/...` with the revert applied | exit 0 |
| `go test -run NONE_COMPILE_ONLY ./atc/db/ ./atc/scheduler/algorithm/ ./atc/gc/` at pre-state | exit 0 |
| same, with the revert applied | exit 0 |

Conclusion: no mechanical transition exists in either direction; a build- or
test-based grader accepts the change the maintainers rejected. That is the
case's premise and it is now measured rather than assumed.

Still to do, in priority order:

1. ~~A leakage audit by two independent models~~ — done (opus + sonnet, both
   borderline), resolved by the fixup pass below.
2. The positive control described in Open questions.
3. Optional: the Postgres suite run at both ends.

## Fixup 2026-07-25

Curator-fixup pass over the dual audit. Every flag resolved into one of the four
buckets; residual verdict **pass**.

### Dissolved by contract (no edits, deliberately)

The exposure contract in `schema/benchmark-case-v1.md` says the solver sees
exactly `pre_state − withheld + task/`; `case.yaml`, `notes.md`, `ground_truth/`
and the case id/path are harness-side and never exposed. That dissolves, with no
rename and no retitle:

- **sonnet, all of it** — the title stating the verdict, `source` quoting the
  terminal's "breaking change … in v8" sentence, and `curation.learnings`
  narrating the answer. Dataset-browser visibility is a viewer concern, not an
  exposure-manifest one; sanitizing these fields would make the manifest less
  useful to curators for no gain to the solver's information set.
- **opus item 1** — the `neg-` id prefix and the directory name announcing the
  negative. Same reason. The contract's own caveat already covers the residual:
  a hand-run must materialize `task/` into a neutrally-named directory.
- **opus item 2, first half** — `ground_truth/` sitting two directories from
  `task/`. Directory adjacency is irrelevant when the harness never mounts it.

### Real defects (fixed)

1. **`task/task.md` had drawn two of the reviewer's inferences for them.** The
   house rules said "…so a shipped migration needs a working down path" (that
   *is* R5) and "during an upgrade an operator normally has old and new `web`
   processes alive against the same database at the same time" (that *is* R2's
   mixed-version window). Both editorial clauses removed; the underlying facts
   stay, because the repo does not state them in tree and a real release manager
   would: webs are rolled not stopped, migrations run at `web` startup,
   `concourse migrate --target-version` exists. The semver rule stays too, for
   the reason in "Case design" — without it the case grades whether the model
   guesses that release policy is in scope. The trigger is still authentic: a
   release manager listing house rules, not one telegraphing a verdict.
2. **The ask tilted toward one disposition.** "If it is a block, I need something
   I can put in a comment on the PR…" spent its only actionability sentence on
   the block branch. Rewritten to apply to all three ("whatever the disposition
   … if you are recommending anything other than 'ship it as-is'"), which keeps
   R6 gradeable and restores the symmetry the positive control will need.
3. **Review dimension 3 rephrased**, "is 7.14.0 the right *vehicle* for this
   change" → "whether 7.14.0 is the right release for this change". The
   dimension stays (see "Case design"); "vehicle" carried a faint wrong-vehicle
   connotation the neutral phrasing does not.
4. **No delivery channel for the decline.** `grading.outcome_match` matches
   disposition tokens, but nothing told the agent where to put the disposition —
   the outcome rubric was left to be applied by reading prose (recorded in Open
   questions). `task.md` now names the deliverable: `REVIEW.md` at the snapshot
   root, first line `Disposition: approve | approve with changes | block`.
   Aligned in `ground_truth/rubric.md` with a new Gate paragraph, "Where to read
   it", which makes the format explicitly **non-graded**: a review delivered in
   prose or under another filename is mapped by reading it with no deduction,
   only a submission with no disposition anywhere hits the Partial cap, and if
   the header line and the prose disagree the prose governs. The grading caveat
   is also recorded inline in `case.yaml#grading.outcome_match`, so a location
   the task leaves flexible is never pinned by the grader.
5. **Rubric text that referenced the removed clause.** R2 said "(the
   mixed-version window the task describes)" — no longer true. Now "(the
   mixed-version window a rolling `web` upgrade creates — the task says webs are
   rolled, not stopped, but leaves the implication to the reviewer)". This was
   load-bearing: left alone it would have credited the agent for repeating the
   prompt.

### Difficulty (recalibration considered, held at `moderate`)

Opus's premise-supply flag is a difficulty argument as much as a leakage one,
and it does flatten the **gate** bit toward trivial. Rejected as the basis for a
downgrade: difficulty must describe the graded band, and Excellent here requires
a named breaking mechanism, the destructive-rename consequence, and a real SQL
defect found by reading a migration inside an 1866-line / 21-file diff. Fix 1
also removes the two clauses that most flattened the gate. Reasoning recorded as
a comment at `case.yaml#difficulty`.

### Known leak channel (declared, not fixable)

**`known_leak_channels: [local-clone-upstream-refs]`.** Opus's item 2, second
half, is real and it is operator-environment leakage of exactly the kind the
README bullet describes — just not the auto-memory instance. Verified this pass:
in the curation working clone, `git cat-file -e ff09ee64fc^{commit}` succeeds and
`git for-each-ref --contains ff09ee64fc` returns
`refs/remotes/upstream/release/7.14.x`, so one `git log upstream/release/7.14.x`
inside that clone yields the terminal artifact and its verbatim rationale. We do
not rewrite the operator's clone, so the channel is declared: a hand-run inside
any clone carrying upstream release refs is invalid, and materialization must
follow `pre_state.repository.materialize` with its five `git cat-file -e`
assertions. The project auto-memory was checked and says nothing about this
upstream revert, so `project-auto-memory` is **not** among the channels.

### Consistency checks re-run

- `information_cut` (2025-08-08T15:59:33-04:00) re-verified against
  `git log -1 --format=%cI 42744a08b0` — exact match. `task.md`'s internal dates
  (branch cut "today", PR merged 2025-05-10) remain consistent with the cut.
- No priced-deflator in-tree doc to weigh: `withheld: []` stands, and the two
  deliberately-exposed items (the 2025-03 ancestor revert, the `.github`
  issue templates) were re-read and still name the category, not this change.
  `rubric.md` already credits the ancestor revert as a +3 lead rather than an
  answer, and its "Notes for the judge" already grade reasoning over quotation.
- Grading collisions: none to fix. `fail_to_pass` is empty by design, all three
  `pass_to_pass` entries are `grades: environment_only`, and nothing in the
  grading overlay touches a file the task asks the agent to write (`REVIEW.md`
  is new and untested by any gate).

Run preconditions were promoted to a comment at the top of `case.yaml`: network
off, and materialize per the recipe rather than running inside this clone.
