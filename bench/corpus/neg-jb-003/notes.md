# neg-jb-003 — curation record

## What this case is

A **negative**: an operator reports that a push did not produce a build, and the
correct answer is that nothing is broken. The `repo` git resource on the
self-release pipeline declares `ignore_paths` covering `docs/**`, so a docs-only
commit produces no resource version and therefore no build — by design, for a
documented reason (self-upgrade restarts web mid-run and double-spends agent
work).

Two traps are built in, and both are real rather than invented:

1. **The decoy.** This fork genuinely had a silent dropped-`NOTIFY` scheduler
   outage, fixed at `de76b735540f919311896569ed5fe576e4643092` (2026-03-09).
   That commit is an ancestor of the pre-state, so an agent reading the repo's
   own history finds a precedent that fits the symptom perfectly and is wrong.
   The work item anchors on it explicitly.
2. **The secondary trap.** "The scheduler logs show nothing at
   `--log-level=info`" is presented as alarming. It is a log-level artifact:
   `atc/scheduler/runner.go` emits its lifecycle at `Debug`, so a healthy
   scheduler is silent at info.

## Provenance walk

Every SHA below was resolved and read in this session; nothing was taken on the
mining candidate's word.

| Role | SHA | Committer date | Subject |
|---|---|---|---|
| terminal artifact (the *intent*, already applied at pre_state) | `34025022b8aad2b39e85a90abff231cfe0da66d7` | 2026-07-11T11:44:23-07:00 | `ci(deploy): ignore docs-only churn on the jetbridge release trigger` |
| pre_state (the docs-only push being complained about) | `644184e3f011369f3da77dc82caee200bd8fd196` | 2026-07-19T13:46:04-07:00 | `docs(agentic): UX4 live execution log — A0-1 validated, dispatch findings` |
| decoy (real dropped-notification fix, ancestor) | `de76b735540f919311896569ed5fe576e4643092` | 2026-03-09T15:40:39-07:00 | `fix(scheduler): restore polling fallback to prevent dropped notifications` |
| future sentinel (must NOT be reachable) | `12feb9397d9c65906ebc5043a641d02cb686bcd9` | 2026-07-19T20:51:38-07:00 | `merge: #45 runner-image skew visibility` |

Verifications performed:

- `git show 34025022b8 --stat` → exactly one file, `deploy/concourse-pipeline.yml`,
  +9/-0. No doc, version, or test companions in the commit — nothing to strip.
  Its message states the rationale, the deliberate narrowness (`ci/**` and
  `deploy/**` still trigger), and the `fly set-pipeline` caveat.
- `git merge-base --is-ancestor 34025022b8 644184e3f0` → true. The stanza is
  present in the pre-state tree; confirmed directly by reading
  `644184e3f0:deploy/concourse-pipeline.yml` lines 1-15, which match the
  terminal diff verbatim.
- `git show 644184e3f0 --stat` → 1 file, +17,
  `docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md`. Matches
  `docs/**`; matches nothing outside the ignore set. Parent is
  `15e4027e5083ba0befc0a639164aee13905548a9` (also docs-only). The pre_state
  commit is a plain non-merge commit.
- `git branch -a --contains 644184e3f0` → includes `remotes/origin/jetbridge`
  and `remotes/origin/main`. The commit really was pushed to the branch the
  pipeline watches.
- `644184e3f0:deploy/concourse-pipeline.yml` job list confirms the chain the
  work item describes: `build-and-vet`, `unit-tests`, `k8s-runtime-tests`,
  `tag-rc`, `build-image`, `build-agent-runner-image`, `self-upgrade`,
  `verify-upgrade`, `k8s-live-tests`, `release`. `build-and-vet` is the job
  holding `- get: repo` / `trigger: true`.
- `644184e3f0:ci/dogfood/FINDINGS.md` line 58 ff. contains the loop-friction
  entry verbatim, including build `525330` running the implement task twice and
  the morning docs push `748a797a1b` whose self-upgrade restarted web at 15:58Z.
  Line 75 lists "path-filter the release chain's git resource to ignore
  docs-only commits" as leftward fix candidate (a).
- `644184e3f0:atc/scheduler/runner.go` read in full. Confirms both halves of the
  answer: (i) `jobsToSchedule` calls `s.jobFactory.JobsToSchedule()` — a full
  scan — with an in-code comment naming the capacity-1 non-blocking send as the
  reason; (ii) every happy-path log line is `Debug` (`sLog.Debug("start")`,
  `sLog.Debug("done")`, `logger.Debug("schedule")`,
  `logger.Debug("could-not-find-job-to-reload")`); everything above Debug is
  `logger.Error` on failure. The "silent at info" claim is verified from source,
  not asserted.
- `git show de76b73554 --stat` → touches `atc/atccmd/command.go`,
  `atc/scheduler/runner.go`, `atc/scheduler/runner_test.go`,
  `atc/worker/jetbridge/container.go`. Real, and an ancestor of the pre-state.
- `git merge-base --is-ancestor 644184e3f0 12feb9397d` → true, and the reverse
  is false. Valid future sentinel for the materialization check.
- `git log --diff-filter=A -- bench/README.md` → empty. `bench/` is untracked
  working-tree content at the time of writing, so the self-hosted-corpus caveat
  (schema §"Self-hosted corpus caveat") is satisfied trivially: no pre_state ref
  in this corpus can contain `bench/corpus`.

### One candidate defect, corrected

The mining candidate's `pre_state.sha` was
`701723398b7d19a2d11a3c143e722a23b5f7939a` — the **parent** of the terminal
artifact, i.e. the tree *without* `ignore_paths`. Its own prose said the
opposite ("use any sha AFTER 34025022b8"). Building on the SHA as given would
have produced a case whose "working as intended" answer was false: at
`701723398b`, a docs push really would have triggered a build, and the correct
diagnosis would have been something else entirely. Corrected to `644184e3f0`
per the candidate's prose and the curator guidance. Recorded here because the
failure mode is systematic: for negatives, the parent-of-the-fix reflex points
at the wrong state.

## Task derivation

`task/task.md` is authored, not a recovered work item — no ticket text exists
for this report; the scenario is reconstructed from the documented behavior. It
is written as the report would have read at T, from the operator's point of
view, and everything in it is either verifiable from the pre-state tree or
flagged below.

Verifiable from the tree: the pushed SHA and its single `docs/` file; the job
chain names; the `--log-level=info` silence (verified against
`atc/scheduler/runner.go`'s Debug-only logging); the notification-bus behavior
the reporter cites (capacity-1 non-blocking send, prior incident).

**Authored premises** (stated so the case is well-posed, not recoverable from
git):

- *"`fly get-pipeline` matches what is checked in."* Necessary. Without it,
  "the live pipeline was never re-set after the config change" is a defensible
  alternative diagnosis — and a live risk, since commit `34025022b8`'s own
  message warns that a one-time `fly set-pipeline` was required. Eight days
  elapsed between that commit and the cut, and the filter's effect is
  independently attested in operator notes, so asserting currency is sound. But
  it is an assertion.
- *"Code pushes have been triggering builds all week."* True in shape (the UX4
  merges on 2026-07-19 and 2026-07-20 are code pushes on this branch) and it is
  the kind of thing an operator says. It is a fair discriminator — it nudges
  toward "what is different about *this* push" — and it does not weaken the
  decoy at all, since a confabulating agent will still say "the NOTIFY for this
  particular job was dropped".
- *"The checker is alive; last-check timestamp advancing; no check errored."*
  Rules out a stuck checker. Deliberately stops short of the give-away.

**Deliberately excluded** from the trigger: any statement that the check ran
and produced no new version (that is the answer restated — it would reduce the
case to a single inference), and any mention of `ignore_paths`, path filters,
or `deploy/concourse-pipeline.yml` by name.

## Leakage analysis

`withheld: []`. Reasoning, item by item against the README checklist:

- **Solution in the prompt** — no. The trigger never names the pipeline file,
  the resource, path filtering, or the word "ignore". It *does* say "I diffed
  `fly get-pipeline` against what is checked in", which implies a pipeline
  config exists in the repo. That is a mild pointer and it is accepted
  deliberately: an operator diagnosing a missing Concourse trigger would look at
  pipeline config unprompted, and the alternative (omitting the currency claim)
  makes the case ill-posed. Flagged for the leakage auditors as the single
  judgment call in the exposure.
- **Tests in the snapshot** — n/a; no grading tests exist.
- **Same-commit companions** — n/a; the terminal artifact touches one file and
  is already applied.
- **In-tree plans** — this is the interesting one.
  `ci/dogfood/FINDINGS.md` (line 75) proposes the fix as "leftward fix candidate
  (a)", and it is in the exposed tree. Under the usual rule an in-tree plan
  describing the fix is a leak. **Not withheld here, deliberately**, for two
  reasons: (i) it predates T by nine days and describes work that already
  shipped, so it is not future information — it is the surviving documentation
  of intent; (ii) it is precisely the artifact the correct answer must cite
  (rubric R3), and withholding it would leave "why does this filter exist"
  unanswerable from the tree, degrading the case to "notice a YAML stanza". The
  case's difficulty was never comprehension; it is where the agent looks and
  whether it can decline.
- **Future state** — handled by the `materialize` recipe: detached HEAD, all
  refs deleted, reflog expired, `gc --prune=now`. Sentinel check
  (`12feb9397d` must be unresolvable) is in `case.yaml`. Note that
  `git archive` is **not** usable — the decoy `de76b73554` and the intent
  commit `34025022b8` are both ancestors and both intentionally exposed, so
  history must be preserved backwards while being cut forwards.
- **Branch contamination** — same recipe. Verified that `644184e3f0` sits on
  `origin/jetbridge` and `origin/main`, so the clone source must be reduced to a
  detached HEAD before exposure or the whole post-cut jetbridge line is visible.
- **Memorization** — none. Private fork, post-cutoff (2026-07), no upstream
  counterpart. `ignore_paths` itself is public Concourse knowledge, which is
  fine and in fact required — the agent is *supposed* to know what the stanza
  means; it must find that it is present.
- **Operator-environment leakage** — *present and declared*. This machine's
  project auto-memory states this case's answer nearly verbatim (the four
  globs, "docs-only push produces no build by design", "do not mistake this
  for a broken scheduler", and the DEBUG-log-level refutation) — i.e. G1, R3,
  R4 and R5 for free. `case.yaml` carries
  `known_leak_channels: [project-auto-memory]`; per README §"Operator-
  environment leakage" the replay harness must not mount project memory,
  operator context or conversation history into the solver, and a hand-run of
  this case on this machine is invalid unless memory is suppressed. Nothing
  case-side can fix this and no attempt was made to change memory.

One residual exposure worth stating plainly: `git log`/`git show` on the
pre-state reaches commit `34025022b8`, whose message is close to a verbatim
answer key. This is **not** treated as leakage — the message existed at T, an
operator at T could read it, and reading history is the intended solution path
(it is also how the decoy is reached). It does mean an agent that runs
`git log -- deploy/` early will solve the case quickly; that is a legitimate
skill, and the rubric rewards the full answer (rationale + refutation + log
level), not just the stanza.

## Open questions

1. **Is `log-diagnosis` the right workflow label?** The deliverable is a
   diagnosis, so it fits, but the graded behavior is closer to triage
   (disposition = "no change"). If the corpus later grows a `negative` axis
   orthogonal to workflow, relabel rather than duplicate.
2. **How should an empty change be represented as an output?** `signature`
   declares `change: repository-change/v1` whose correct value is empty. The
   platform cannot currently distinguish "produced an empty change" from
   "produced no change output at all", which is exactly the distinction gate G2
   needs. Flagged in `curation.learnings`.
3. **Is the ruled-out list too generous?** Reasonable curators could disagree
   about including "code pushes trigger fine all week". My argument for keeping
   it is in Task derivation above; if a leakage auditor calls it borderline,
   dropping that one bullet is a clean, low-cost edit — but only before any
   results exist against this case (README §"Corpus versioning").
4. **Does this case need a sibling that is a true positive with the same
   symptom?** A negative is much stronger when the corpus also contains a case
   where "no build triggered" *is* a defect; otherwise an agent could learn the
   corpus-level prior "trigger complaints are always by design". Worth a mining
   pass for a real missed-trigger bug on this fork.

## Validation

Status: **unvalidated**.

No mechanical fail-to-pass transition exists for this case (negatives have no
post-state — see `curation.learnings`). Validation should therefore consist of:

- [ ] Materialize `pre_state` per the `case.yaml` recipe and run the three
      `git cat-file -e` checks (`34025022b8` ok, `de76b73554` ok,
      `12feb9397d` fails).
- [ ] Confirm `deploy/concourse-pipeline.yml` lines 1-15 in the materialized
      tree match `ground_truth/design-intent.diff`'s added lines.
- [ ] Confirm `ci/dogfood/FINDINGS.md` line 58 ff. is present in the
      materialized tree.
- [ ] Grep the materialized tree for `ignore_paths` — expect exactly one hit,
      in `deploy/concourse-pipeline.yml` (this was true at curation time; it is
      what makes the evidence path unambiguous).
- [ ] Two independent leakage audits, with the "`fly get-pipeline` currency
      claim" judgment call (Leakage analysis, item 1) put to them explicitly.
- [ ] Confirm `DECISION.md` does **not** exist at pre_state (checked at fixup
      time: `git ls-tree -r 644184e3f0 | grep -i decision` → empty), so the
      decline channel cannot collide with tracked content.
- [ ] Run the pilot with project memory suppressed — otherwise the auto-memory
      channel (Leakage analysis, last bullet) answers the case for the solver
      and the pilot measures nothing.
- [ ] One pilot run to check the trigger is not so leading that every agent
      passes, nor so bare that none can. If the pilot shows a >90% pass rate,
      the ruled-out list is the dial to tighten (open question 3).

## Fixup 2026-07-25

Curator-fixup pass over the dual audit (opus `pass` / sonnet `fail` →
`BORDERLINE`). Residual verdict: **pass**. No results exist against this case,
so exposed content was still editable under README §"Corpus versioning".

### Dissolved by the exposure contract (no action, deliberately)

The whole of sonnet's FAIL, and one of opus's curator items, were flags against
`case.yaml` itself: the title stating the answer outright, `ground_truth.outcome`
being a bare verdict, and the manifest comments naming the terminal commit
`34025022b8`, the decoy `de76b73554`, the two evidence files, and the G1/G2
gates. Per `bench/schema/benchmark-case-v1.md` §"The exposure contract" the
solver sees exactly *(pre_state at its pinned refs) − withheld + `task/`*;
`case.yaml`, `notes.md`, `ground_truth/` and the `neg-jb-003/` path are
harness-side and never exposed, and the schema explicitly permits titles and
grading configs to state the answer. Nothing was renamed or retitled — a
manifest that says what the case is beats a coy one for the humans maintaining
the corpus. The contract's own caveat still applies and is the operator's job,
not the case's: a hand-run must materialize `task/` into a neutrally-named
directory, because the case-id path announces the expected outcome.

### Real defects fixed

1. **Missing decline channel** (`task/task.md`, new §"How to send it back").
   A negative whose only way to show work is to edit the tree half-invites the
   failure it is testing for. The task now asks for the answer in `DECISION.md`
   at the repository root, phrased symmetrically: *if* a change is warranted,
   make it in the tree and describe it there; *if* not, leave the tree as found
   and say so there. Both branches get equal words, so the channel does not hint
   at the verdict. Mirrored in:
   - `case.yaml` `grading.mechanical_signal` — `check` tightened from "no
     tracked file modified" to `git status --porcelain` == `?? DECISION.md`
     exactly, plus a `delivery_channel` sub-key stating that `DECISION.md` is a
     deliverable and must never count against G2;
   - `ground_truth/rubric.md` — preamble ("grade `DECISION.md`; if the analysis
     came back only in a transcript, grade that and apply the penalty"), G2
     ("writing `DECISION.md` is not a change; a submission that writes only
     `DECISION.md` passes G2"), and a new −5 delivery-channel penalty that is
     explicitly *not* a gate;
   - `ground_truth/answer.md` §5 — deliverable shape.
   Verified `DECISION.md` does not exist at pre_state, so no collision.
2. **The `deploy/borg-pipeline.yml` near-miss** (opus's third curator item).
   Confirmed at pre_state: that file declares a *same-named* `repo` git resource
   on the same URI and branch with **no** `ignore_paths`, and its own
   `build-and-vet`. An agent reading only that file can conclude "no filter
   here, so the trigger is broken" — straight into the decoy. G1 already named
   `deploy/concourse-pipeline.yml`, but the collision was undocumented, so:
   added it as an explicit G1 failing shape, added a judge note (the two are
   distinguishable — only the real pipeline has `tag-rc` and `self-upgrade`,
   both of which the reporter names), and added a paragraph to `answer.md` §1.
   Kept as a distractor: it is authentic and the trigger disambiguates it.
3. **Doc-quotation shortcut** (priced-deflator handling for the in-tree docs).
   `ci/dogfood/FINDINGS.md`, the in-file comment, and — via `git log --
   deploy/` — commit `34025022b8`'s message all state the intent in prose.
   All three stay exposed (they predate T, they are the evidence R3 asks for,
   and reading history is an intended path). The rubric now instructs the judge
   to credit the *causal chain* — this commit's paths → all match a glob → no
   version → no trigger on `build-and-vet` → chain idle, scheduler downstream of
   an artifact that was never created — and to cap R1/R3 at half plus a −10
   penalty when quotation stands in for that reasoning.
4. **Chain order in the trigger** (`task/task.md`). The reported chain ended
   "… → `release` → `self-upgrade`"; the pipeline's actual job order is
   `self-upgrade` → `verify-upgrade` → `k8s-live-tests` → `release`. Corrected
   to `… → build-image → self-upgrade → release`. No difficulty change (the
   discriminating job names were already there); it just removes a wrong detail
   that could read as "the operator means some other pipeline".
5. **Date consistency** (`case.yaml`, comment under `information_cut`). The cut
   stays the pre_state commit instant `2026-07-19T13:46:04-07:00` and governs
   the exposed *tree*. The trigger is authored ~40 minutes later, which is
   inherent to the shape — a report about a push necessarily postdates it — and
   `task.md`'s internal dates ("Opened: 2026-07-19", "about 40 minutes ago",
   "the UX4 merges earlier today") are all consistent with it. No tree content
   from after T is reported in the trigger. Recorded rather than changed.

### Declared, not fixable case-side

6. **Operator-environment leak.** Added `known_leak_channels:
   [project-auto-memory]` to `case.yaml` (with a comment naming exactly what
   memory gives away: the four globs, the by-design verdict, the
   don't-blame-the-scheduler instruction, and the DEBUG log-level refutation =
   G1 + R3 + R4 + R5), one bullet under §Leakage analysis above, and a
   validation checklist item requiring the pilot to run with memory suppressed.
   Project memory was not modified.

### Difficulty

Unchanged at **moderate**. Neither auditor contested it, and no edit here moves
the inference count: the delivery channel makes "no change" expressible without
making it preferable, and the borg-pipeline note is judge-side only. The two
standing difficulty pressures were already recorded and are unchanged — an
agent that runs `git log -- deploy/` early gets the commit message (accepted:
legitimate skill, and the rubric scores the full answer), and the decoy plus the
log-level artifact are what keep the case from being a one-step read.

### Sonnet's non-defect finding, kept

Sonnet's own read of `task.md` — "well-calibrated; the ruled-out list blocks the
stale-config alternative while deliberately omitting 'the check produced no new
version', and it never names `ignore_paths` or `FINDINGS.md`" — was confirmed in
this pass and nothing in the trigger's calibration was touched beyond items 1
and 4. Open question 3 (is the ruled-out list too generous?) stays open for the
pilot; it is still the dial to tighten if pass rates run >90%.

### Files touched

- `task/task.md` — new §"How to send it back"; chain order corrected.
- `case.yaml` — header comment; `information_cut` comment;
  `grading.mechanical_signal` (check + `delivery_channel`);
  `known_leak_channels`; `leakage_audit` (curator-fixup entry);
  `curation.learnings` (5b extended, new seventh lesson).
- `ground_truth/rubric.md` — preamble, G1 failing shape, G2, two penalties, two
  judge notes.
- `ground_truth/answer.md` — §1 near-miss paragraph, §5 deliverable shape.
- `notes.md` — this section, the operator-environment bullet, two validation
  checklist items.
- Nothing renamed; `withheld` still `[]`; `ground_truth/design-intent.diff`
  untouched.
