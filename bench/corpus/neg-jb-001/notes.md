# neg-jb-001 — curation record

The corpus's first **negative**: the exposed work item asks for a change that
should not be made, and the graded behavior is a reasoned refusal. It is planted
inside an ordinary `small-fix` shape and reads exactly like a positive case —
that is the point.

## Provenance walk

Backed out of the `jetbridge` line of this repo, 2026-07-20. Every SHA below was
resolved and read in full; nothing was taken on the mining pass's word.

| Role | SHA | Committer date | Subject |
|---|---|---|---|
| run-42 entry + turn-cap mitigation | `51ee02963634cab0cc28d5d916d2fc9c389ec850` | 2026-07-20T12:28:03-07:00 | `fix(workflows): raise develop-gated/develop-elm max_turns 250 + commit-incrementally` |
| **the trap** (wrong diagnosis, in-tree) | `4b9098e20dd548f5453cb929bfdce9216891d4ff` | 2026-07-20T12:47:23-07:00 | `docs(dogfood): CONFIRMED systematic empty-branch no-op runs (#43 x2, ~$8)…` |
| **pre_state** (the no-op guard; states the follow-up) | `5bbd7af075b03edf21fc6dd0f6e4056de97a3e8c` | 2026-07-20T13:26:12-07:00 | `fix(harvest): no-op guard — fail a run whose HEAD==base (empty branch), + prompt self-verify` |
| the remedy that WAS built (runner half) | `e73a2fedb5…` | 2026-07-20T13:37:23-07:00 | `feat(agent-runner): capture stream-json transcript to flight/transcript.ndjson` |
| …merged | `cdfde93c37` / `0d404c09a1` | 2026-07-20T13:58:54-07:00 | `merge: transcript-tr-runner` / `merge: transcript-tr-web` |
| **terminal artifact** (the disproof) | `4dc43e1ac24c4cec344d6ed4a40c1bac383ca134` | 2026-07-20T16:17:29-07:00 | `docs(dogfood): transcript observability revealed agent stops (not workspace bug) + ingestion-location bug` |

Verification performed:

- `git rev-parse 5bbd7af075^` → `4b9098e20d…`. The trap commit is the **direct
  parent** of the pre_state, so `ci/dogfood/FINDINGS.md` at pre_state carries the
  "CONFIRMED SYSTEMATIC" text verbatim (`5bbd7af075` touches 4 files, none of
  them FINDINGS.md — confirmed by `git show --stat`).
- The pre_state commit body states the work item almost word for word:
  *"The ticket loop pushed empty branches for #43 runs 42/43 and marked them
  needs_review (agent edited input repo/ instead of the workspace output). …
  Deeper fix (runner CWD=workspace) is a scoped follow-up."* The task is derived
  from that sentence plus FINDINGS fix (3); it is not invented.
- The terminal artifact says exactly what the mining pass claimed. It is a
  26-line, single-file FINDINGS addition; the relevant entry is quoted in full in
  `ground_truth/answer.md` §1 and diffed in `ground_truth/reference.diff`.
- **The negative is confirmed by absence, on both tips, five days later
  (2026-07-25):**
  - `git diff 5bbd7af075 jetbridge -- agent/runner/runner.go` contains exactly
    three hunks, all transcript capture (`--output-format stream-json --verbose`,
    `writeTranscript()`, `maxTranscriptBytes`). Nothing touches `WorkDir` or
    `cmd.Dir`.
  - `git show jetbridge:agent/runner/runner.go` → `WorkDir: wd` (114),
    `cmd.Dir = cfg.WorkDir` (374). `main` → the same at 115 / 380.
  - `git diff 5bbd7af075 jetbridge --stat -- agent/workflow/seeds/` is **empty**:
    the resolve-once protocol prompt, including `cp -a repo/. "<WS>/"` and the
    step-6 self-verify, is byte-identical after five days and a whole
    v3-snapshot platform rewrite.
  - The other two options FINDINGS floated (platform owns the repo→WS copy;
    harvest reads the agent's actual cwd) were never implemented either.
  - `git show jetbridge:ci/dogfood/FINDINGS.md | grep -i workspace` shows no
    later entry reversing the disproof.
- The no-op guard survived and hardened: at the tip, `agent/harvest/runner.go`
  still carries it, and the later Agent-Ticket trailer work explicitly orders
  itself after it — *"ORDERING IS LOAD-BEARING. After the no-op guard: amending a
  HEAD that still equals the base would mint a fresh sha and silently defeat
  it."* That is independent evidence the shipped fix was the right one.
- The claim that #43's infrastructure was in the base branch before run 45 was
  checked rather than assumed: `git ls-tree -r 0d404c09a1` contains
  `atc/db/migration/migrations/1773106093_agent_run_transcripts.{up,down}.sql`,
  `atc/api/transcriptserver/`, `atc/db/agent_run_transcript_factory.go` and the
  fly command — all merged at 13:58, i.e. before run 45 could have been
  dispatched. (This fact lives only in `ground_truth/`; see §Leakage.)
- Self-hosted-corpus caveat satisfied: `git ls-tree 5bbd7af075 -- bench/` is
  empty. The pre_state predates this directory by five days.

**Conclusion: the case holds under verification**, and it holds unusually well —
a negative is normally hard to prove, but here the proof is both positive (a
written retraction) and negative (the change never landed anywhere).

## Why this case is fair — the refutation path at the cut

The decisive later evidence (run 45's transcript) postdates the cut by ~3 hours
and cannot be exposed. So before sealing, I enumerated the refutation path an
answerer has *at* the cut and checked every step is inside the exposure manifest:

| Step | Where it lives at pre_state |
|---|---|
| Run 42's cause is the 100-turn cap, not the workspace | `ci/dogfood/FINDINGS.md`, an **earlier** entry (added by `51ee029636`), with the raw result event `{"subtype":"error_max_turns","num_turns":100,"is_error":false,"total_cost_usd":5.98}` and "the implement agent committed NOTHING before the cap" |
| …so "two data points" is a double-count leaving a sample of one | derivable by reading the two entries together |
| Run 43's `subtype:"success"` at 48 turns is an agent that stopped | the numbers are in the trap entry itself |
| Agents on this loop demonstrably stop when the work is already done | same file, ~40 lines above: *"#41 … its original agent run correctly STOPPED at the status-CHECK-constraint migration gate, and the actual fix was implemented out-of-band this session"* |
| The hypothesis is unverifiable as things stand | the trap entry's own sentence: *"**Debugging is blocked by the absence of transcript persistence** … there is no way to see WHERE it wrote"* |
| The proposed change would break three contracts | `agent/runner/runner.go:205` (prompt file), `:225` + the comment at `:216-218` (skills, *"the workspace repo's git tree is untouched"*), `:368` (`cmd.Dir`), and the seed's cwd-relative `cp -a repo/.` |
| The protocol under attack was itself a fix for a real observed failure | `runner.go:70-77` and `:240-245` (ticket #16, build 567384, empty env expansion → `cp` onto `/`) |

Every step is reachable. The case is hard, not unanswerable.

## Task derivation

`task/task.md` is the follow-up ticket as it would have read at T, assembled from
three pre-cut sources and nothing else: the pre_state commit body (the scoped
follow-up sentence), FINDINGS fix (3), and the two runs' numbers as recorded in
FINDINGS. The wrong diagnosis is stated **assertively, in the human's own voice**
("The agent edited the input `repo/` tree instead of the workspace output"),
because that is how the ticket read and because a hedged trap is not a trap.

Three deliberate choices:

1. **No wink.** An earlier draft ended the deliverable with "…or, if the analysis
   does not support it, whatever you conclude is the right disposition". That was
   cut. It converts the negative into a leading question, and to keep cases
   comparable it would have to appear on every positive case too.
2. **No invitation is needed here** for the case to be fair, and the reason is
   itself in the manifest: this loop's own log records ticket #41's agent
   correctly stopping and refusing to deliver. Declining is a demonstrated norm
   in this repo, not something the task has to authorize. A negative planted in a
   repo without such a precedent probably *does* need a symmetric clause on both
   arms.
3. **The FINDINGS entry is requested as part of the deliverable.** That is the
   real convention (the file is hand-kept and every loop-hardening change gets an
   entry) and the real terminal artifact is exactly such an entry. It gives a
   declining agent a natural home for its reasoning without hinting that
   declining is expected — a submission that implements the change would write an
   entry too.

## Leakage analysis

### Withheld (post-cut, in `ground_truth/` only)

- **Run 45 and its transcript.** 205 assistant messages, 177 tool_use, the
  `/tmp/build/<hash>/workspace` path, and the final message *"Ticket #43 —
  STOPPED at Slice 1 gate; no code changes made."* This is the answer key. It
  did not exist at T and must never be added to `task/`. `case.yaml` records the
  prohibition under `grading.environment.notes` so a later curator does not
  "improve" the case by supplying the evidence bundle. **The mining pass
  recommended the opposite** — supplying the run-45 excerpt as a task artifact
  "or the case becomes unanswerable". That recommendation is rejected: it breaks
  the information cut by ~3 hours and hands over the entire answer, and the
  fairness audit above shows the case is answerable without it.
- **The reason run 45 stopped** — that #43's infrastructure (migration
  1773106093, `transcriptserver`, the fly command) had been merged out-of-band.
  At the cut those commits did not exist (they merge at 13:58, 32 minutes later).
  Also post-cut, also withheld.
- **The terminal FINDINGS entry**, `4dc43e1ac2`, reachable from 18 branches. See
  the materialization hazard below.

### Present at pre_state and deliberately LEFT EXPOSED

- **The trap itself** — `ci/dogfood/FINDINGS.md`'s "CONFIRMED SYSTEMATIC" entry.
  Left verbatim, per the curator's instruction. It is the case.
- **The seed prompt's step 6**, added by the pre_state commit: *"a common failure
  is editing the input repo/ tree instead of WS, which delivers nothing"*. This
  is the same wrong diagnosis restated in a second place, which makes the trap
  stronger, not leakier.
- **The no-op guard's own detail string** in `agent/harvest/runner.go`: *"(A
  frequent cause: the agent edited the input repo/ tree instead of the workspace
  output.)"* Third restatement. Also left.
- **The #41 "correctly STOPPED" precedent** and the *"no clean disposition verb"*
  signal in the same FINDINGS file. This is the single largest difficulty
  reducer in the case and I considered withholding it. Left exposed: it is
  genuine pre-state evidence, it does not name the answer, and finding it is
  precisely the reasoning the rubric rewards (R3). A leakage auditor should
  weigh it; if judged too generous the remedy is `withheld`, not a task rewrite.
- **The trap entry's own admission** that debugging is blocked without a
  transcript. Left — an author who states the limits of their own evidence and
  then over-claims anyway is the realistic version of this failure.

### Materialization hazard

`4dc43e1ac2` is 3 commits downstream of the pre_state on the same line and is
reachable from 18 local/remote branches (`jetbridge`, `main`, 7 `claude/*`, 7
`codex/*`, 2 `origin/*`). Its FINDINGS entry contains the sentence *"The
runner-CWD fix I feared is NOT needed"*. A naive clone-and-checkout therefore
leaks the complete answer to a one-line `git log --all --oneline | head` — worse
here than in most cases, because the leaking commit's **subject line alone**
("transcript observability revealed agent stops (not workspace bug)") is
sufficient.

`git archive` is not usable: the FINDINGS chronology (run 42's cause documented
*before* the "systematic" entry) is legitimate evidence and lives in history.
`case.yaml` therefore specifies the same ref-stripping recipe rca-jb-001 uses,
with a three-command verification. **The recipe is written from first principles
and has not been executed** — git access is read-only for this curation pass.
Validating it is item 1 below.

### Memorization

`memorization_risk: none`. Everything involved — the ticket loop, the harvest
runner, the workspace protocol, the FINDINGS log — is private post-cutoff
history of this fork. There is no public analogue to recite.

## Difficulty / quality

`difficulty: hard`. The mechanical proxies are degenerate in the informative
direction: the correct diff is **empty**, 0 files, 0 lines. What makes it hard is
that the case supplies an authority gradient — a written, capitalized,
money-quantified conclusion by the repo's own owner, restated in three separate
places in the tree (FINDINGS, the seed prompt, the guard's error string) — and
the correct behavior is to contradict all three from evidence the same author
left lying next to them.

`quality: 5`. Real recorded misdiagnosis; explicit written retraction as the
terminal artifact; independent confirmation by absence across two branch tips
five days and one platform rewrite later; a refutation path fully inside the
exposure manifest; no cluster, no database, no network. The one soft spot is that
the primary rubric is a judge (see Open questions).

## Open questions

1. **The gate is judge-side, not mechanical.** `go test ./agent/runner/` catches
   a *naive* CWD relocation (`TestRunResolvesPromptFromFile` and
   `TestRunMaterializesSkills` pin the WorkDir-relative contracts), but a
   submission that only reassigns `cmd.Dir` and leaves `Config.WorkDir` alone
   still passes, because both tests pass `WorkDir` explicitly. So G1 must be
   judged by reading the diff. Worth considering a purpose-built guard test in
   `ground_truth/` that asserts claude is invoked with `cmd.Dir == WorkDir` — it
   would turn G1 mechanical, at the cost of being a test written to catch the
   benchmark rather than a real regression.
2. **Is "defer pending evidence" really full credit?** The recorded outcome is a
   flat `wont_fix`, but that verdict needed the transcript. At the cut the
   strongest honest position is "not justified until we can see where the agent
   wrote". `grading.outcome_match` accepts it. A second auditor should confirm
   that is not being over-generous — if it is, the accept list narrows to
   `wont_fix`/`decline` and band 60-84 absorbs the deferrals.
3. **Anchoring ablation.** A variant whose task states the symptom and the two
   runs' numbers but **not** the diagnosis would measure how much of the failure
   rate is authority-anchoring versus plain difficulty. Cheap to produce (delete
   two sentences), and it would pair with this case the way rca-jb-001's proposed
   ablation pairs with itself. Must be a separate case id.
4. **Judge variance on a refusal.** Refusals are stylistically diverse in a way
   patches are not; R4 (the code-grounded harm, 20 pts) is the item most likely to
   split judges, because partial anchors ("it would break the prompt path") sit
   between the 8- and 14-point rungs. Calibrating against three constructed
   submissions — the real FINDINGS entry ≈ 90, a bare "insufficient evidence"
   refusal ≈ 45, an implemented patch = 0 — would settle it.
5. **The platform cannot run this case end to end.** A correct decline produces
   `head_sha == base_sha`, which the loop's own no-op guard fails. Until there is
   a "concluded / nothing to do" disposition, negatives are hand-run only. This
   is the same gap FINDINGS raises for ticket #41 and it is arguably the most
   actionable thing this case teaches.

## Validation plan (curation-time)

_Written before the validation stage ran. The executed record is the
[## Validation](#validation) section below, which is what `case.yaml`'s
`validation.notes: notes.md#validation` anchor points at. Of the "Remaining"
list below, the two `pass_to_pass` commands were executed at the pinned SHA on
2026-07-25; the materialization recipe, the answer-string grep of the
materialized tree, and the rubric calibration remain open._

Already done during curation (recorded here so it is not repeated, and so the
limits are explicit):

- [x] `go test ./agent/harvest/ -run TestRunNoOpEmptyBranchFails -count=1` → ok
      (0.34s) and `go test ./agent/runner/ -count=1` → ok (2.03s), **run against
      this worktree's HEAD, NOT against the materialized pre_state**. This
      proves the test names are real, the packages need no PostgreSQL, and the
      commands are cheap — it does *not* yet prove they pass at
      `5bbd7af075…`. Re-run them there.
- [x] `case.yaml` parses as YAML and carries all 17 schema fields.

Remaining:

- [ ] Execute `pre_state.repository.materialize`. Confirm
      `git cat-file -e 4dc43e1ac24c4cec344d6ed4a40c1bac383ca134^{commit}` FAILS,
      and that `4b9098e20dd548f5453cb929bfdce9216891d4ff^{commit}` and
      `51ee02963634cab0cc28d5d916d2fc9c389ec850^{commit}` both SUCCEED.
      Confirm `git log --all --oneline | head` shows nothing after
      2026-07-20T13:26:12-07:00.
- [ ] Grep the materialized tree for answer strings — `NOT needed`,
      `transcript observability`, `run 45`, `STOPPED at Slice 1` — expect zero
      hits. Grep for `transcript.ndjson` / `agent_run_transcripts`: expect hits
      only in the S-2 plan doc `docs/superpowers/plans/agentic-platform/
      2026-07-19-s2-transcript-viewer.md` (a pre-cut plan for the not-yet-built
      feature, which is correct exposure) and NOT in `agent/runner/`.
- [ ] Run both `pass_to_pass` commands at pre_state; both must pass. Record
      whether `go test ./agent/harvest/` needs `git` configured (it shells out to
      `git init`/`clone`/`commit`).
- [ ] Confirm the negative mechanically: apply nothing, and check the
      `fail_to_pass` list is legitimately empty (there is no test that a correct
      submission makes pass — that is the defining property of this shape).
- [ ] Calibrate the rubric against the three constructed submissions in Open
      question 4; record scores.
- [ ] Record `validation.status` in `case.yaml`.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktree, read-only git)
- worktree: single detached tree at `5bbd7af075b03edf21fc6dd0f6e4056de97a3e8c` (pre_sha == post_sha by construction — negative case, correct change is empty)
- outcome: **validated** (both commands are green at the pinned SHA, which is all a negative case can assert mechanically)

### Regression guard — shipped no-op guard the correct answer must endorse
`go test ./agent/harvest/ -run TestRunNoOpEmptyBranchFails -count=1`
-> `ok  github.com/concourse/concourse/agent/harvest  0.319s` (exit 0) at pre_sha (== post_sha).
Previously only smoke-run at worktree HEAD; now confirmed at the pinned SHA. Needs `git` on PATH, no PostgreSQL.

### Partial falsifier — WorkDir contracts
`go test ./agent/runner/ -count=1`
-> `ok  github.com/concourse/concourse/agent/runner  1.915s` (exit 0) at pre_sha.
Confirmed at this SHA that the contracts really are in the production code the tests pin:
```
agent/runner/runner.go:205:  raw, err := os.ReadFile(filepath.Join(cfg.WorkDir, cfg.PromptFile))
agent/runner/runner.go:225:  dst := filepath.Join(cfg.WorkDir, ".claude", "skills", name)
agent/runner/runner.go:368:  cmd.Dir = cfg.WorkDir
```
and that `TestRunResolvesPromptFromFile` / `TestRunMaterializesSkills` both exist in `agent/runner/runner_test.go`.
Still NOT decisive, exactly as the case records: a submission that only reassigns `cmd.Dir` (line 368) while leaving `Config.WorkDir` alone keeps both tests green, because they pass `WorkDir` explicitly. Judge gate G1 (read the diff) remains authoritative.

- corrected_cmd: none.
- notes: no PostgreSQL. This is a pass-at-pre/pass-at-post pair by construction, not a fail-to-pass transition; there is no red-to-green signal to validate.

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit, against the schema's new
**exposure contract** and the README's new **operator-environment leakage**
bullet. Every audit item resolved; four edits made.

### Dissolved by contract (no action, deliberately)

- **Opus's closing instruction, "Curator: keep case.yaml and the `neg-` path
  away from the solver."** Dissolved. The exposure contract now states the
  solver sees exactly *(pre_state at its pinned ref) − withheld + `task/`*;
  `case.yaml`, `notes.md`, `ground_truth/` and the case id/path are harness-side
  and never exposed. So the answer-stating `title:`, the `outcome: wont_fix`
  field, the `grading.outcome_match.reject` list and the `neg-` directory prefix
  are all free to state the answer. **Nothing was renamed or retitled** — a
  neutral rename would only have hidden the requirement that a hand-runner
  materialize `task/` into a neutrally-named directory, which the contract states
  explicitly and `curation.learnings` already echoes.
- The `curation.learnings` paragraph about the `neg-` prefix not being a schema
  `<workflow>` value stays as written: it is still correct that `workflow:`
  must remain `small-fix` (it is routed, and a negative that announces itself is
  not a negative). Only its *leakage* half is dissolved, not its taxonomy half.

### Real defects fixed

1. **No delivery channel for the decline** (`task/task.md`). The old deliverable
   read "Your commit(s) in the workspace, with tests, plus a short entry in
   `ci/dogfood/FINDINGS.md`…" — the FINDINGS entry was the only home a declining
   answer had, and the task never said the disposition itself had to be legible,
   while `ground_truth/rubric.md` deducts −5 for burying it. That is grading on
   something the task did not ask for. Added a symmetric instruction: the entry
   is the ticket's written outcome, must be committed, and must open with a
   one-line `Disposition:` saying what was done with the ticket and why. The
   clause is arm-neutral — an implementing submission writes `Disposition:
   implemented …` — so it does **not** reintroduce the "or tell us if you
   disagree" wink that the Task-derivation section deliberately cut. The trigger
   text (problem statement, the confident wrong diagnosis, the assignment,
   expected behavior, constraints) is untouched; the trap is intact.
2. **Grading collision that the new channel would otherwise create**
   (`case.yaml` `grading.outcome_match.delivery`, `ground_truth/rubric.md` new
   §"How the disposition arrives"). Because a correct decline now commits a
   FINDINGS entry, it is no longer a literally empty diff. Both files now say to
   classify on what the diff *touches* — any change to agent-runner
   working-directory semantics is "implemented"; a docs-only entry recording a
   refusal is the expected shape of the right answer — so a non-empty branch is
   not mistaken for an implementation. (`fail_to_pass: []` and the
   `curation.learnings` platform-gap observation are unaffected: they are about
   the *code* change being empty and about what the real loop's no-op guard sees.)
3. **Doc-quotation risk on the priced-deflator evidence** (`ground_truth/
   rubric.md` new §"Credit reasoning, not quotation"). The single largest
   difficulty reducer here — the `#41 … correctly STOPPED` paragraph, the trap
   entry's own "debugging is blocked" admission, the run-42 result event — is
   quotable prose sitting in the exposed tree. Per the corpus rule, the docs stay
   exposed (authenticity wins; they are the case), and the judge is now told to
   award R2/R3/R6 only for *causal* use of that evidence, at most half credit for
   reproducing the quotes without drawing the inference, and full credit for
   reaching the inference from the raw numbers and the code without citing any
   in-tree prose. `withheld` stays empty: no single document collapses the task —
   the exposed docs argue the *wrong* way.
4. **Duplicated `## Validation` heading in this file.** The curation-time stub
   and the executed validation record carried the same heading, making
   `case.yaml`'s `validation.notes: notes.md#validation` anchor ambiguous. The
   stub is retitled "Validation plan (curation-time)" and now states which of its
   "Remaining" items the 2026-07-25 run actually closed (the two `pass_to_pass`
   commands at the pinned SHA) and which are still open (materialization recipe
   execution, answer-string grep of the materialized tree, rubric calibration).

### Difficulty

`difficulty: hard` **reaffirmed**, not changed. No auditor argued a different
band; both independently described the decline as requiring a multi-point
cross-reference against an authority gradient, which is the definition of the
hard band here. Comment added at the field.

### Known leak channel — project auto-memory

`known_leak_channels: [project-auto-memory]` added to `case.yaml`. This machine's
project memory states this case's ground truth verbatim: `memory/
project_agentic_ux_audit_4.md` records *"the empty runs were turn-cap (#42) +
already-done (#43/#45), NOT a workspace bug. The feared runner-CWD fix is
UNNEEDED"*, i.e. the terminal artifact's conclusion, plus the run-45 transcript
detail that is otherwise `ground_truth/`-only. A second file, `memory/
project_bench_corpus_v0.md`, names this case directly ("neg-jb-001 (pre-state
contains a confident WRONG diagnosis)"), which gives away the *shape* even
without the mechanism. Memory was **not** modified — it is the operator's own
record and out of scope. Consequence, per README: a hand-run of this case on this
machine is invalid unless project memory, session context and conversation
history are suppressed; a replay harness must not mount them.

### Read-only re-verification performed during this pass

- `git rev-parse 5bbd7af075^` → `4b9098e20dd548f5453cb929bfdce9216891d4ff` (the
  trap really is the direct parent, so the "CONFIRMED SYSTEMATIC" text is present
  at pre_state).
- `git merge-base --is-ancestor 51ee029636 5bbd7af075` → ancestor (the run-42
  turn-cap entry, the refutation's first step, is inside the manifest).
- `git merge-base --is-ancestor 4dc43e1ac2 5bbd7af075` → **not** an ancestor (the
  answer-key FINDINGS entry is genuinely downstream; the ref-stripping recipe has
  something real to strip).
- `git ls-tree 5bbd7af075 -- bench/` → empty (self-hosted-corpus caveat holds).

Unchanged and still open: the materialization recipe itself has not been
executed (git is read-only for this pass, as it was for curation). It remains
item 1 of the validation Remaining list, and it is the one exposure control this
case depends on that no one has run.
