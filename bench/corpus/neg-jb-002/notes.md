# neg-jb-002 — curation record

## Provenance walk

The candidate was mined as a "negative-decline": a plausible change that was
deliberately *not* made. Every claim below was re-verified in this worktree by
reading the pinned objects; nothing is taken from the mining hand-off on trust.

### The terminal artifact — a documented rejection

`03ef35b23559bac4d221b220e6a27ae1ee9daf04` (2026-07-04T22:36:36-07:00,
"chore(forge): create build_survival_across_web_restart track; Phase 1
complete") adds `forge/tracks/build_survival_across_web_restart_20260704/`.
Two files in it are the answer key:

- `spec.md`, under the heading **"Root cause (verified 2026-07-04 — supersedes
  the earlier hypothesis)"**: *"The initial hypothesis ('jetbridge's
  `process.go` deletes task pods on context cancellation during SIGTERM') is
  **wrong for production**. `Process.Wait`'s delete-on-cancel branch belongs to
  direct mode, which only runs when no `PodExecutor` is configured (tests).
  Production always runs exec mode (`factory.K8sExecutor =
  jetbridge.NewSPDYExecutor(...)`, atc/atccmd/command.go:1416), and
  `execProcess.Wait` deliberately never deletes pods — the reaper owns
  cleanup."*
- `cgx.md`, under "Origin": *"**Scoping research overturned the initial
  hypothesis.** The first analysis blamed `jetbridge/process.go`'s
  delete-pod-on-ctx-cancel branch… Lesson: verify which code path production
  actually takes before designing a fix around a plausible-looking branch."*

Both quotes were read directly out of the commit (`git show <sha>:<path>`), so
the candidate's description of the terminal artifact is accurate.

### The change made instead

`7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3` (2026-07-04T22:35:47-07:00, one
minute before the track commit), "fix(engine): release in-flight job builds on
drain instead of erroring them". `git diff --stat 1127c59301 7c59cbbfa6`:

```
 atc/builds/tracker.go      | 11 ++++++++---
 atc/builds/tracker_test.go | 47 ++++++++++++++++++++++++++++++++++++++++------
 atc/engine/engine.go       |  8 +++++++-
 atc/engine/engine_test.go  | 21 +++++++++++++++++----
```

Nothing under `atc/worker/`. `git diff 1127c59301 7c59cbbfa6 --
atc/worker/jetbridge/process.go` is empty. That is the mechanical form of the
decline.

### Durability of the decline

`process.go` was edited several times after the cut — `d0d4d4217a`
("feat(jetbridge): resumable task exec via in-pod supervisor", Phase 2 of this
same track), `b83a7932a3`, `4db9fbd313`, `3b81488ef1`, `dd9967b8ce`,
`c0e52a562b`, `133ceba305`. The delete-on-cancel branch survives all of them:
at this repo's head, `Process.Wait` still has

```go
case <-ctx.Done():
    ...
    if err := p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
        cleanupCtx, p.podName, metav1.DeleteOptions{},
    ); err != nil {
        logger.Error("failed-to-cleanup-pod-on-cancel", err)
    }
```

So the "no change" is a decision that held for three weeks and through a
deliberate rework of the same function — not a gap in the record.

### The pre-state is coherent

`1127c59301e2f865b4d2420e909ae5344e05661f` (2026-07-04T21:58:44-07:00,
"fix(ci): gate verify-upgrade behind a settle timer to survive ATC restart") is
the parent of `7c59cbbfa6` and the last commit before the investigation
started. `information_cut` is its committer date. Everything the case exposes
existed at that instant.

Facts re-verified in the pre-state tree (all cited in `ground_truth/answer.md`):

| Claim | Verified at |
|---|---|
| `Process.Wait` deletes the pod on ctx cancel, and says so in its doc comment | `atc/worker/jetbridge/process.go:59`, `:96-107` |
| direct vs exec selection is `c.executor != nil` | `atc/worker/jetbridge/container.go:108` |
| direct mode is labelled in-tree as test-only | `container.go:162-163` ("Fallback direct mode: only used when no executor is configured (e.g. tests that don't set up SPDY)") |
| the `*Process` constructor is reachable only from the two non-exec branches | `container.go:174`, `:275` |
| production always sets the executor | `atc/atccmd/command.go:1416`, inside the `if cmd.Kubernetes.Namespace != ""` block that also builds the clientset/config; `atc/worker/factory.go:43-48` |
| exec mode deliberately never deletes on cancel | `process.go:743-745` (and `:858` for completion) |
| an existing spec asserts exec-mode pod preservation | `process_test.go:1124` |
| an existing spec asserts direct-mode deletion (the decoy's guard) | `process_test.go:128` |
| drain finishes builds errored | `atc/engine/engine.go:225-227`, `finish` at `:255-262` |
| tracker safety net errors any running build | `atc/builds/tracker.go:99-106` |
| pod deletion is downstream via GC → reaper | `atc/worker/jetbridge/reaper.go:146-166`; symptom strings at `container.go:254-256` |
| provenance of the real cause | `8b5476828f334d0ada0885d2bdbe59694e0e9a6b`, 2026-04-10, "fix(check): prevent in-flight check tracking leak…" |

`worker/factory.go`'s `newK8sWorker` dereferences `*f.K8sConfig`, so a jetbridge
worker cannot even be constructed unless the `cmd.Kubernetes.Namespace != ""`
block ran — and that block sets `K8sExecutor` unconditionally. This is the
strongest form of the unreachability argument and it is available entirely from
the pre-state tree.

## Task derivation

The exposed work item (`task/task.md`) is a reconstruction of the *initial
hypothesis*, written as it would have been filed at T.

**The trigger names the fix location and approach, deliberately.** The schema
says not to do that unless the real trigger did — and here it did: `cgx.md`
records that "the first analysis blamed `jetbridge/process.go`'s
delete-pod-on-ctx-cancel branch", and the investigation that followed was
scoped as "verify this before building a fix around it". A negative case whose
task did *not* name the decoy would not be the case that actually occurred, and
would not test authority calibration at all.

Deliberate choices in the task text:

- **Symptom section is deliberately near-identical to `fix-jb-005`'s.** Same
  incident, same reporter voice. Divergence would itself be a tell.
- **The causal arrow is inverted relative to `fix-jb-005`.** That case's task
  says the pod errors "look like a downstream consequence… but confirm rather
  than assume"; this one asserts pod deletion as the cause. Faithful to the two
  real readings, and it keeps this case's R4 out of the other case's task.
- **No escape hatch, no hedge, no "push back if you disagree".** The only
  verification-flavoured line is the repo's ordinary bugfix house rule ("cite
  `file:line` evidence for the mechanism… and say how you confirmed it"), which
  appears in in-family form on `fix-jb-005` too. An earlier draft ended that
  constraint with "we have been burned by fixes argued from a plausible-looking
  code path rather than the one that actually runs" — lifted almost verbatim
  from the recorded lesson. That was cut: it is the answer wearing a
  constraint's clothes.
- **The "do not regress an earlier deliberate fix" constraint is kept**, also
  in-family with `fix-jb-005`. It points at `forge/archive/`, which is where the
  check-leak track lives, so it nudges toward history-reading generally without
  naming the mechanism. It is also load-bearing for rubric R6.
- **Out-of-scope section** mirrors the real track's Phase 2 split, so the agent
  is not led into the supervisor/resume rabbit hole.

## Leakage analysis

**Excluded because it postdates the cut** (not reachable at `pre_state`, so no
`withheld` entry is needed): the entire
`forge/tracks/build_survival_across_web_restart_20260704/` directory
(`03ef35b235`) and the fix commit `7c59cbbfa6`. Both are direct descendants of
the pre-state ref.

**The one real hazard is git reachability**, not the tree. `7c59cbbfa6` is the
immediate child of the pre-state commit and its message is a verbatim answer
key ("Downstream this also stops the pod-deletion cascade… Pod deletion is a
consequence, not the cause"); `03ef35b235` is one further along. Both are
reachable from the `jetbridge` and `main` branch tips in the source repo.
`pre_state.materialize` therefore strips all refs and the reflog after a
detached checkout, with an explicit verify step. Same treatment as
`rca-jb-001`; the same failure mode would silently make this case trivial.

**Deliberately left exposed** (all present at T; all part of the intended
solution path):

- `forge/archive/check_scheduling_inflight_leak_20260409/` — the track behind
  `8b5476828f`. It describes the drain-finish and the safety net as *the fix*
  for a check leak, never as a bug. Reading it is how a strong submission earns
  R5's provenance points and R6. This is history, not an answer.
- `forge/archive/k8s_runtime_behavioral_spec_20260331/` — its `cgx.md` says
  "Direct mode = worker WITHOUT SetExecutor" and its plan/spec describe PE-02 /
  PE-09 as direct-mode coverage. This is corroborating evidence for R1 that a
  careful agent can find, but it does not close the argument: it never says
  production always configures an executor. The discriminating step (reading
  `command.go`/`factory.go`) is still required.
- `process_test.go:128` and `:1124` — the two specs that pin both modes'
  cancellation contracts. Finding them is part of the intended path, and `:128`
  is what makes the wrong answer mechanically detectable.

**Not leaked by the task:** the words "engine", "tracker", "drain", "check
build", `8b5476828f`, and any mention that the diagnosis might be wrong.

**Memorization risk: none.** Private repo, post-cutoff (2026-07).

## Shares a terminal artifact with two other cases

| Case | Shape | Shared with this case |
|---|---|---|
| `fix-jb-005` | small-fix, mechanical | same pre_state ref; its ground truth is this case's *redirect* |
| `rca-jb-001` | log-diagnosis, judge | same pre_state ref; same terminal pair; its task carries the same wrong hypothesis as a *standing hypothesis to refute*, and its R4 is this case's R1 |

Results across the three are **not independent samples**. `rca-jb-001` is the
tightest coupling: an agent that has produced its analysis has already written
most of this case's correct answer. Recommended handling: keep all three in one
split and never aggregate them as three data points, or assign them to disjoint
runs. A corpus roll-up needs a "shares terminal with" column.

## id convention

The schema's id form is `<workflow>-<source>-<NNN>`, which for a `small-fix`
case would be `fix-jb-0NN`. The curation pass assigned `neg-jb-002` so that
decline/negative cases form a greppable family independent of the workflow they
are dressed as (a negative can be dressed as a fix, a review, or an upgrade).
`workflow:` still carries the real shape, so the harvest adapter is unaffected.
If the convention is rejected later, rename before any results exist.

## Open questions

1. **Does the harness allow "no change" as a terminal state?** If the small-fix
   runner requires a non-empty diff to consider the run successful, this case
   measures harness compliance rather than judgement. Worth running once with
   an instrumented harness to see whether the model *wanted* to decline and was
   forced to patch. This is the same gap flagged in `case.yaml`'s
   `curation.learnings` (no `decision/v1` output port).
2. **Is the R1 bar too strict at 25 points?** An agent might decline on the
   weaker but still-correct observation that exec mode already preserves pods
   (R2) without ever articulating unreachability. Current rubric gives that
   path Good, not Excellent. Reviewers should check whether that ranking
   survives contact with real submissions.
3. **Should a variant exist without the "do not regress an earlier fix"
   constraint?** It is in-family, but it is also the single strongest nudge
   toward reading history. An A/B on that one line would measure how much of
   the case's difficulty lives in the task text rather than the code.

## Validation

_(stub — filled by the validation stage)_

**Already done at corpus-build time (recorded here so the validation stage does
not redo it):**

- `PGHOST=127.0.0.1 PGPORT=1 go test ./atc/worker/jetbridge/ -count=1` at this
  repo's head: **PASS, 58.7s**. No PostgreSQL, no cluster, `//go:build live`
  files excluded by default.
- **The negative gate was proven, not assumed.** The decoy edit (replace the
  `case <-ctx.Done():` pod-`Delete` call in `Process.Wait` with a comment) was
  applied *without touching the repo*, via a `go test -overlay` JSON map
  pointing at a modified copy in scratch, and the suite was re-run focused:

  ```
  go test -overlay=<overlay.json> ./atc/worker/jetbridge/ -count=1 \
      -args -ginkgo.focus="returns the context error and deletes the Pod"
  ```

  Result: `[FAIL] Process Wait when the context is cancelled [It] returns the
  context error and deletes the Pod` at `process_test.go:153`
  (`Expect(pods.Items).To(BeEmpty())`). Rubric gate G1 therefore has a real
  mechanical detector for the wrong answer.

  Caveat: this was run at the repo head, not at the pre-state ref, because the
  pre-state tree is not materialized in this worktree. Both the branch and the
  spec are byte-identical at `1127c59301` (`process.go:96-107`,
  `process_test.go:128`), so the transfer is safe — but the validation stage
  should re-run it against the materialized pre-state to close the loop.
  `-overlay` is the recommended technique: it keeps the working tree clean.

**Remaining checks:**

- [ ] Materialize `1127c59301e2f865b4d2420e909ae5344e05661f` per
      `pre_state.materialize`; confirm `git cat-file -e 7c59cbbfa6^{commit}`
      and `git cat-file -e 03ef35b235^{commit}` both FAIL and
      `git cat-file -e 8b5476828f^{commit}` SUCCEEDS.
- [ ] `go build ./atc/...` at pre_state — expect success.
- [ ] `go test ./atc/worker/jetbridge/ -count=1` at pre_state — expect PASS
      (~60s, no PostgreSQL; measured at ~59s on the corpus-build machine at
      this repo's head).
- [ ] Negative-gate sanity check: apply the *decoy* change (delete the pod
      `Delete` call in the `case <-ctx.Done():` branch of `Process.Wait`) and
      re-run `go test ./atc/worker/jetbridge/ -count=1` — expect FAIL at
      `process_test.go:128` ("returns the context error and deletes the Pod").
      If this does not fail, the mechanical half of gate G1 is not real and
      `case.yaml`'s `pass_to_pass` note must be corrected.
- [ ] Confirm `git diff 1127c59301 7c59cbbfa6 -- atc/worker/jetbridge/` is
      empty.

status: SUPERSEDED — see the "## Validation" record dated 2026-07-25 below, which
resolves the test/decoy/outcome/build legs (case.yaml carries
`validation.status: validated`). The one leg that record did *not* cover is the
first checkbox above: the ref-stripping `materialize` recipe was never exercised,
because validation used detached worktrees of the source repo rather than a
stripped clone. That check is still open and is the only thing standing between
this case and a trivially-answerable one (`7c59cbbfa6`'s message is a verbatim
answer key one commit away).

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `1127c59301e2f865b4d2420e909ae5344e05661f`, post `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`
- outcome: **validated** (all four legs)

### G1 negative gate — `go test ./atc/worker/jetbridge/ -count=1`
PRE `ok  github.com/concourse/concourse/atc/worker/jetbridge  34.810s` (exit 0)
POST `ok  github.com/concourse/concourse/atc/worker/jetbridge  34.806s` (exit 0)
(measured 35s here, not the 59s recorded earlier; no Postgres, no cluster)

### DECOY-DETECTOR PROOF — `-overlay` with the pod Delete stripped from the `case <-ctx.Done():` branch of `Process.Wait`
Overlay built by deleting the 9-line cleanup block (`cleanupCtx ... Pods(...).Delete(...) ... logger.Error("failed-to-cleanup-pod-on-cancel", err)`) from `atc/worker/jetbridge/process.go`, then:
`go test -overlay=<overlay.json> ./atc/worker/jetbridge/ -count=1 -args -ginkgo.focus='returns the context error and deletes the Pod'`

PRE (FAIL, exit 1):
```
Summarizing 1 Failure:
  [FAIL] Process Wait when the context is cancelled [It] returns the context error and deletes the Pod
  .../atc/worker/jetbridge/process_test.go:150
Ran 1 of 332 Specs in 0.005 seconds
FAIL! -- 0 Passed | 1 Failed | 331 Skipped
```
POST (FAIL, exit 1): identical — `Ran 1 of 332 Specs`, `Summarizing 1 Failure`, anchored at `process_test.go:150`.

Note: the failure line is **process_test.go:150** at these two SHAs (the case.yaml records 153 from a later repo HEAD). The spec itself starts at process_test.go:128. Working trees stayed clean (`-overlay` writes nothing).

### OUTCOME CHECK
`git diff --quiet 1127c59301e2f865b4d2420e909ae5344e05661f 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3 -- atc/worker/jetbridge/` → exit 0 (empty diff), confirming `outcome=no-change-correct`.

### Compilation guard — `go build ./atc/...`
exit 0 at both SHAs (shared run with fix-jb-005's pass_to_pass leg).

- corrected_cmd: the overlay leg is a sketch in case.yaml; the concrete form used was
  `go test -overlay=$SCRATCH/decoy/overlay.json ./atc/worker/jetbridge/ -count=1 -args -ginkgo.focus='returns the context error and deletes the Pod'`
  with `overlay.json = {"Replace": {"<tree>/atc/worker/jetbridge/process.go": "<scratch>/process_nodelete.go"}}`.
- notes: no Postgres, no cluster, no network.

## Fixup 2026-07-25

Curator pass over the dual audit (opus: borderline, sonnet: fail). Every audit
item is resolved into one of four buckets below. Residual verdict: **pass**.

### Dissolved by the exposure contract (no edit — deliberately nothing renamed)

The schema's exposure contract (`bench/schema/benchmark-case-v1.md`, §"The
exposure contract") states the solver sees exactly *(pre_state at its pinned
refs) − withheld + `task/`*; `case.yaml`, `notes.md`, `ground_truth/`, and the
case id/path are harness-side and never exposed. That dissolves, without any
change:

- **sonnet:** "the title states the verdict verbatim" — titles may state the
  answer freely. Not retitled.
- **sonnet:** "`ground_truth.outcome` is `no-change-correct`" — `ground_truth/`
  is withheld by definition.
- **sonnet:** "the materialize/curation/grading comments narrate the mechanism
  and the negative gate" — same file, same contract. These comments are how a
  replay operator gets the case right; removing them would trade a real
  operational risk for an imaginary leak.
- **opus:** "the `git diff --exit-code -- …/process.go` gate is an answer key" —
  grading configs are harness-side. (Its *spuriousness* was a real defect; see
  below.)
- The `neg-` id prefix (already defended in `## id convention`) is likewise
  harness-side. Unchanged. The contract's one live obligation stands: a
  by-hand run must materialize `task/` into a neutrally-named directory.

### Real defects fixed

1. **Spurious-pass outcome gate** (opus). `git diff --exit-code --
   atc/worker/jetbridge/process.go` compares the worktree against `HEAD`, so it
   reported clean the moment an agent committed its edit — i.e. the gate could
   not distinguish "declined" from "implemented the ticket and committed".
   `case.yaml` `grading.pass_to_pass` now pins the diff to the pre_state SHA and
   adds the decoy's guard spec:
   `git diff --exit-code 1127c59301e2f865b4d2420e909ae5344e05661f --
   atc/worker/jetbridge/process.go atc/worker/jetbridge/process_test.go`.
   `ground_truth/rubric.md` G1's mechanical block was updated to the same pinned
   form (and now includes `process_test.go`, since rewriting that spec is G1's
   worst failure shape and was previously only caught by the Go suite).
2. **No delivery channel for the decline** (G2 was ungradable in a
   `repository-change/v1` world). `task/task.md`'s house-rule constraint now
   names the channel: write the change description to `DECISION.md` at the repo
   root — "the mechanism with `file:line` evidence, how you confirmed it, and
   what you did about it". Phrased outcome-agnostically on purpose: it must not
   hint that declining is an available answer (the same reason the earlier
   "we have been burned by plausible-looking code paths" line was cut). The
   evidence-citation requirement it replaces is preserved verbatim in substance,
   so the task is no more and no less leading than before. `rubric.md` G2 now
   grades `DECISION.md`, while accepting an equivalent durable channel if the
   harness carries one, and restates that a confident `DECISION.md` on top of a
   patched `process.go` still fails G1.
3. **Gate-scope caveat** added as `grading.grading_caveats`: the rubric
   explicitly permits a correct submission to add a regression test under
   `atc/engine`/`atc/builds` for the *real* mechanism, and such a test fails at
   pre_state by construction — so the gate must stay scoped to the three
   recorded commands, and the submitted tree is expected to carry a new
   `DECISION.md` at the root (only the two pinned paths must be diff-clean).
   Mirrored as a judge note in `rubric.md`.
4. **Internal inconsistencies.** (a) The stale `status: unvalidated` stub above
   contradicted `case.yaml`'s `validation.status: validated`; it now reads
   SUPERSEDED and names the single leg that genuinely remains open (the
   ref-stripping `materialize` recipe was never exercised — validation used
   detached worktrees, not a stripped clone). (b) `case.yaml`'s negative-gate
   note cited `process_test.go:153` for the decoy failure while the validation
   record measured `:150` at the pinned SHAs; the note now gives `:150` at
   pre_state and explains `:153` as the later-head line. Dates were checked and
   are consistent: `information_cut` `2026-07-04T21:58:44-07:00` is exactly
   `1127c59301`'s committer date (re-verified), and the task's "Reported:
   2026-07-04" sits inside the cut.

### Difficulty recalibration: hard → moderate

Both auditors independently argued the premise is cheaper than `hard` implies,
and they are right for the same reason: the refutation sits *adjacent to the
code the ticket names*. Re-verified in the pre-state tree during this pass —
`container.go`'s comment at the very `newProcess(...)` construction site reads
"Fallback direct mode: only used when no executor is configured (e.g. tests that
don't set up SPDY)", and `process.go:743-745` says the exec path intentionally
does not delete on cancel. An agent that merely reads the file it was told to
edit is holding the refutation. What remains genuinely difficult is authority
calibration (acting on it against a confident work item) plus the R5 redirect —
real, but not `hard` in the search-difficulty sense. `difficulty: moderate`, with
the reasoning recorded inline in `case.yaml`. The rubric bands are untouched:
they already separate Good from Excellent on whether the redirect is pinned,
which is where the remaining difficulty actually lives.

### Known leak channel (declared, not fixable in-case)

`case.yaml` now carries `known_leak_channels: [project-auto-memory]`. This
machine's project auto-memory states this case's answer verbatim — "builds error
on web restart because commit 8b5476828f errors builds on drain + tracker safety
net (DB-level, NOT jetbridge pod deletion)" — which is the decline, the redirect,
*and* the inverted causal arrow (R1, R4, R5) in one line. Memory was not touched.
Consequence for operators: a local hand-run of this case on this machine is
invalid unless project memory, session context, and conversation history are
suppressed (`bench/README.md`, "Operator-environment leakage").

### Priced-deflator in-tree docs: KEPT

`forge/archive/check_scheduling_inflight_leak_20260409/` and
`forge/archive/k8s_runtime_behavioral_spec_20260331/` remain exposed —
authenticity wins, `withheld` stays empty, and neither doc collapses the task
(the first frames the drain-finish as a *fix*, never as this bug's cause; the
second never says production always configures an executor, leaving the
discriminating `command.go`/`factory.go` step to be done). Per the fixup policy,
`rubric.md` now instructs the judge explicitly to credit causal reasoning from
evidence rather than doc-quotation: quoting the archive without supplying the
connecting step earns neither full R1 nor the R5 provenance bonus.

### Not changed, deliberately

The task's authoritative wrong diagnosis, its "Required change" section naming
`Process.Wait`, and the near-identical symptom section shared with `fix-jb-005`.
Both auditors agreed `task/task.md` is non-leading and world-accurate; the
confident-but-wrong trigger *is* the case, and softening it would delete the
skill under test. The three-case coupling with `fix-jb-005` and `rca-jb-001`
also stands as recorded in `curation.learnings` — it is a sampling-independence
hazard for roll-ups, not an exposure defect.
