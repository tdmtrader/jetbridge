# fix-jb-005 — curation record

## Provenance walk

Backed out of a merged fix commit on the `jetbridge` line of this repo.

| Role | SHA | Date (committer) | Subject |
|---|---|---|---|
| terminal artifact | `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3` | 2026-07-04T22:35:47-07:00 | `fix(engine): release in-flight job builds on drain instead of erroring them` |
| pre_state (parent) | `1127c59301e2f865b4d2420e909ae5344e05661f` | 2026-07-04T21:58:44-07:00 | `fix(ci): gate verify-upgrade behind a settle timer to survive ATC restart` |
| causing commit (in pre_state history) | `8b5476828f334d0ada0885d2bdbe59694e0e9a6b` | 2026-04-10T05:27:18-07:00 | `fix(check): prevent in-flight check tracking leak that permanently blocks automatic resource checks` |
| track doc (post-cut, NOT in pre_state) | `03ef35b235` | 2026-07-04T22:36:36-07:00 | `chore(forge): create build_survival_across_web_restart track; Phase 1 complete` |

Verification performed:

- `git rev-parse 7c59cbb^` → `1127c593…`. Parent relationship confirmed, not assumed.
- `git show --stat 7c59cbb` → exactly four files, `atc/builds/tracker.go` (+11/-3),
  `atc/builds/tracker_test.go` (+47/-6), `atc/engine/engine.go` (+8/-1),
  `atc/engine/engine_test.go` (+21/-4). No same-commit doc/CHANGELOG/version
  companions to strip — the commit is source + tests only.
- Commit message content matches the candidate's claim verbatim (drain +
  tracker safety net scoped to `Name() == db.CheckBuildName`, three swept-up
  resume paths, downstream pod-deletion cascade).
- The named causing commit `8b5476828f` resolves and is reachable from
  pre_state; its message and its archived track
  (`forge/archive/check_scheduling_inflight_leak_20260409/`) both describe
  adding exactly the two unscoped mechanisms this case removes. The pre-state
  source was read directly (`git show 1127c593:atc/builds/tracker.go`,
  `…:atc/engine/engine.go`) and contains the unscoped `else if build.IsRunning()`
  branch and the unconditional `b.finish(…, "build released during drain", …)`
  in the `<-b.release` case. Pre-state is coherent.
- Durability: `git show jetbridge:atc/engine/engine.go` and
  `…:atc/builds/tracker.go` today still carry the scoped predicate — the fix was
  never reverted or superseded (the only later touches to those files are
  `265ddf9e7b` and `c0e52a562b`, which add an orthogonal `panicked` flag /
  agent-step work and leave the drain scoping intact). The case is not grading a
  decision that was later reversed.
- Self-hosted-corpus caveat: `git ls-tree 1127c593 -- bench/` is empty. The
  pre_state predates `bench/` by three weeks, so the corpus and its answers are
  unreachable from the exposure manifest.

## Task derivation

The real pre-investigation trigger was operational: the release pipeline could
not upgrade itself, because `self-upgrade` restarts `concourse-web` and every
in-flight build errored. Two commits already in pre_state are the symptomatic
bandages and were used as evidence of the symptom's reality, not as hints:
`c2a7d3bfcb` (`attempts: 2` on flaky single-node build/test jobs) and the
pre_state commit itself, `1127c59301` (verify-upgrade settle timer — its body
says the build "was errored when the old web pod terminated (not a step failure,
so attempts couldn't save it)"). `task.md` restates that symptom, the
architectural intent (pods have no OwnerReferences; annotations exist for
recovery; upstream releases builds on drain), and the invariant the curator
asked for: **a build in flight when a web drains must still be in flight
afterwards.**

`task.md` is derived from the "Overview (the WHY)" and "Requirements (the WHAT)"
halves of the post-cut track spec
(`forge/tracks/build_survival_across_web_restart_20260704/spec.md`, added by
`03ef35b235`), which read as a genuine work item. Everything from that spec's
"Root cause (verified 2026-07-04)" and "Technical approach (the HOW)" sections
was excluded.

## Leakage analysis

Scrubbed / withheld from `task.md` (all of it present in the real post-cut spec
or the commit body):

- **File and function names.** `atc/engine/engine.go`, `atc/builds/tracker.go`,
  `engineBuild.Run`, `<-b.release`, `Tracker.trackBuild`, and the line numbers
  the spec cites. None appear in the task.
- **The causing commit.** `8b5476828f` is not named, nor is the archived track
  `forge/archive/check_scheduling_inflight_leak_20260409/` that documents it.
  Instead the task carries the behavioral constraint "do not regress anything
  that was previously fixed here … the commit history and the archived track
  notes in the tree are the record". Finding *which* earlier fix is at issue is
  part of what the case tests. Both the commit and the archive are legitimately
  present at pre_state and reachable by an agent that goes looking — that is the
  intended discovery path, not a leak.
- **The predicate.** `db.CheckBuildName`, and the whole check-build-vs-job-build
  distinction, are never mentioned. The task's requirement list is deliberately
  phrased as outcomes (survive drain, leave other webs' builds alone, retry
  transient errors, keep aborts and panics) rather than as the spec's
  requirement 2 ("in-memory check builds must still be finalized so
  `inFlightChecks` cleanup runs"), which would have handed over the key insight.
- **`inFlightChecks` / `onFinishBuild` / `checkFactory`** are not named.
- **The downstream diagnosis.** The `failed-to-get-pod` log line is included as
  an *observation* with an explicit "confirm rather than assume" hedge; the
  spec's conclusion (errored build → container GC → reaper deletes pods) is
  withheld and is scored as rubric item 8.

Checks run against the pre_state tree:

- `git grep -iE 'drain.*errored|errored.*drain|finalizing-orphaned|build released during drain|survive.*web restart' 1127c593 -- '*.md'` → no hits.
- `git ls-tree -r --name-only 1127c593 | grep -iE 'build_survival'` → no hits;
  the track directory first appears in `03ef35b235`, one minute *after* the fix,
  so it is outside the cut in both content and time.
- `git grep -l 'CheckBuildName' 1127c593 -- '*.md'` → one hit,
  `forge/archive/scheduler_behavioral_spec_20260331/spec.md`, which documents
  scheduler metric behavior for check vs non-check builds. Read: it says nothing
  about drain, orphaning, or `IsRunning`. Not a leak; if anything it is fair
  ambient evidence that the codebase already distinguishes the two build kinds
  (as does `trackBuild` itself, in its metrics branch).
- `git grep -l -iE 'in-flight check|inFlightChecks' 1127c593 -- '*.md'` → the
  prior track's archive plus two older check-pod tracks. All read; all describe
  the *cause* (why the unscoped finish exists) and none anticipate the
  job-build regression or propose scoping. Deliberately left exposed.
- **Tests in the snapshot:** the grading assertions do not exist at pre_state.
  `TestTrackFinalizesOrphanedBuild` exists but asserts the *old* behavior; the
  fix renames and splits it. No withheld path is needed for the snapshot —
  `withheld: []` — because the leak-bearing files (`ground_truth/`) are never
  exposed and the post-state test files are applied only by the grader.

`withheld` is empty by design: nothing present at pre_state gives the answer
away.

Memorization risk: **none**. Both the fork-local defect (`8b5476828f`) and this
fix are private post-cutoff history of this repo; the surrounding upstream code
is public but the drain-finish behavior being fixed here exists only in the fork
(upstream simply logs `releasing` and returns).

## Mechanical gradability

Both grading packages are PostgreSQL-free — verified, not assumed:
`PGHOST=127.0.0.1 PGPORT=1 PGDATABASE=nope go test ./atc/builds/ ./atc/engine/`
passes at HEAD, so nothing in them opens a database. They run on
`atc/db/dbfakes` + `atc/builds/buildsfakes` counterfeiter fakes only. This
contradicts the curation hand-off's expectation that PostgreSQL is required; the
empirical result is recorded in `case.yaml` under `grading.environment`. Note
that the repo's `make test-unit` *as a whole* does need PostgreSQL — it is the
wrong gate for this case. `atc/scheduler/algorithm` also needs PostgreSQL, hence
the narrow `./atc/scheduler/` in `pass_to_pass`.

Why the transition is real: at pre_state the counterfeiter `FakeBuild` defaults
`Name()` to `""`, so the drain path and the tracker safety net both fire and the
post-state assertions (`FinishCallCount() == 0` for a job build) fail. With the
source half of the reference change applied, `"" != db.CheckBuildName` and
`"42" != db.CheckBuildName`, so job builds are left running and the check-build
contexts (which set `NameReturns(db.CheckBuildName)`) still finish. Both
post-state test files compile against pre-state source — they add no new imports
(`db`, `time`, `context` are already imported in both files).

Focused invocations, both confirmed to work in this repo:

```
go test ./atc/engine/ -run TestEngine -args -ginkgo.focus='when the build is released'
go test ./atc/builds/ -run TestTracker -testify.m 'TestTrackDoesNotFinalizeReleasedJobBuild|TestTrackFinalizesOrphanedCheckBuild'
```

## Difficulty / quality

`difficulty: hard`. Mechanical proxies say otherwise on their own — 2 source
files, 19 changed source lines, one added conjunct per site — but the subtlety
dominates: the obvious reading of the symptom leads to deleting the two
mechanisms, which passes nothing (rubric item 3) and reintroduces a production
outage. The agent has to reconstruct a three-month-old fix's rationale from
history and then notice that the map it protects only ever holds one kind of
build.

`quality: 5`. Vivid fix-free symptom; small, reviewable, hermetic diff; a
genuine correctness trap; tests that discriminate the two behaviors explicitly;
no database, no cluster, no network.

## Open questions

1. **Is rubric item 3 reachable purely mechanically?** Mostly. A submission that
   deletes both mechanisms outright fails `TestTrackFinalizesOrphanedCheckBuild`
   and the check-build engine context, so the tests do catch the naive fix. But
   a submission could satisfy the tests by scoping on something incidental
   (e.g. `build.ID() == 0`, true for in-memory check builds) and still be wrong
   for DB-backed check builds. The judge rubric is the backstop; consider
   promoting that to an explicit adversarial variant later.
2. **Should the task include the release-pipeline failure logs as a second
   exposed input?** It would make this a more realistic log-diagnosis→fix hybrid,
   but the raw logs for the 2026-07-04 runs were not captured in-tree and
   reconstructing them would be synthetic. Left out.
3. **Sibling case candidate.** Phase 2 of the same track (in-pod resumable task
   exec via a POSIX-sh supervisor) is a much larger, separately-mergeable change
   in `atc/worker/jetbridge`. It is explicitly out of scope here (rubric item
   12) and would make a good independent case if a clean terminal artifact
   exists for it.
4. **Task wording risk.** `task.md` requirement 3 names "transient / retryable
   step errors" as a behavior to restore. That is taken from the real spec's
   requirement 4, but it does narrow the search a little (the retry path lives
   in the same `select` as the drain path). Judged acceptable — it is a symptom
   the operators genuinely reported — but a leakage auditor should weigh it.

## Validation

_Stub — to be filled by the validation stage._

- [ ] Materialize pre_state at `1127c59301e2f865b4d2420e909ae5344e05661f`.
- [ ] Overlay post-state `atc/engine/engine_test.go` and
      `atc/builds/tracker_test.go`; run `go test ./atc/engine/ ./atc/builds/` →
      expect FAIL (`TestTrackDoesNotFinalizeReleasedJobBuild` and the job-build
      drain spec).
- [ ] Apply the source half of `ground_truth/reference.diff`; re-run → expect PASS.
- [ ] Run `pass_to_pass` commands at both ends → expect PASS both times.
- [ ] Record `validation.status` in `case.yaml`.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `1127c59301e2f865b4d2420e909ae5344e05661f`, post `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`
- outcome: **validated** (all four legs), with one clarification recorded below
- overlay used for every fail_to_pass leg:
  `git show 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3:atc/engine/engine_test.go > atc/engine/engine_test.go`
  `git show 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3:atc/builds/tracker_test.go > atc/builds/tracker_test.go`

### PRIMARY fail_to_pass — `go test ./atc/engine/ ./atc/builds/`
PRE (FAIL, exit 1; run with `PGHOST=127.0.0.1 PGPORT=1` to prove no DB is needed):
```
Summarizing 1 Failure:
  [FAIL] Engine Build Run ... when the build is released when this is a job build
         [It] does not finish the build, leaving it running for the next web to resume
Ran 233 of 233 Specs in 1.019 seconds
FAIL! -- 232 Passed | 1 Failed
--- FAIL: TestTracker/TestTrackDoesNotFinalizeReleasedJobBuild (0.10s)
FAIL	github.com/concourse/concourse/atc/builds	0.792s
```
POST (PASS, exit 0):
```
ok  	github.com/concourse/concourse/atc/engine	1.390s
ok  	github.com/concourse/concourse/atc/builds	1.205s
```

### Focused Ginkgo variant — `go test ./atc/engine/ -run TestEngine -args -ginkgo.focus='when the build is released'`
PRE (FAIL, exit 1): `Ran 2 of 233 Specs ... FAIL! -- 1 Passed | 1 Failed | 231 Skipped`
POST (PASS, exit 0): `ok  github.com/concourse/concourse/atc/engine  0.381s`

### Focused testify variant — `go test ./atc/builds/ -run TestTracker -testify.m 'TestTrackDoesNotFinalizeReleasedJobBuild|TestTrackFinalizesOrphanedCheckBuild'`
PRE (FAIL, exit 1): `--- FAIL: TestTracker/TestTrackDoesNotFinalizeReleasedJobBuild (0.10s)`
POST (PASS, exit 0): `ok  github.com/concourse/concourse/atc/builds  0.351s`

**Clarification (important):** both focused variants ALSO require the overlay; the case.yaml note only attaches it to the primary leg. Verified false-pass trap at pre WITHOUT the overlay:
```
$ go test ./atc/engine/ -run TestEngine -args -ginkgo.focus='when the build is released'
ok  	github.com/concourse/concourse/atc/engine	0.384s      <-- green, wrong signal
$ go test ./atc/builds/ -run TestTracker -testify.m 'TestTrack...'
ok  	github.com/concourse/concourse/atc/builds	0.253s      <-- green, wrong signal
```
The context string `when the build is released` already exists at pre_sha (the added spec is the `when this is a job build` child), and neither testify method name exists at pre, so an unfiltered focus silently matches the old specs / no specs.

### pass_to_pass — `go build ./atc/... && go test ./atc/exec/ ./atc/scheduler/`
PRE exit 0 (`atc/exec 1.163s`, `atc/scheduler 0.899s`); POST exit 0 (`1.178s` / `0.917s`).

- corrected_cmd: none for the command text itself; the setup for the two focused legs must include the same two-file overlay as the primary leg.
- notes: no Postgres (verified with PGHOST=127.0.0.1 PGPORT=1).

## Fixup 2026-07-25

Resolution of every open audit item from `leakage_audit` (opus: borderline;
sonnet: fail). Four buckets: dissolved-by-contract, real defect, difficulty
recalibration, known leak channel.

### Dissolved by contract (no action)

- **`case.yaml` title, id, `grading.*` notes and `ground_truth/` state the
  answer.** Harness-side per the exposure contract in
  `schema/benchmark-case-v1.md`: the solver sees only pre_state − withheld +
  `task/`. Nothing renamed, nothing retitled. The only live obligation is the
  hand-run rule (materialize `task/` into a neutrally-named directory), which is
  already the schema's.
- **`withheld_test_paths` naming the two test files.** Same reason — that list
  is grader configuration, never exposed.

### Real defects fixed

1. **Leading text in `task/task.md` (opus).** Requirements 1/2/3 and the first
   constraint described the mechanisms the diff touches, and the constraint
   *instructed* the solver to read commit history and archived track notes,
   converting the case's discriminator (investigate before you change) into a
   checklist item. Edits, all preserving the authentic trigger:
   - req 1: "a later tracker cycle — very likely on a *different* web process"
     → "a later web process" (drops the tracker as the named mechanism).
   - req 2: "A web that finds a started build it cannot take ownership of must
     leave that build alone" → an overlap requirement stated as an outcome
     (no `AcquireTrackingLock` shape).
   - req 3: "Transient / retryable step errors … retried on a later tracker
     cycle" → "momentary infrastructure blips … should stop being terminal too"
     (drops both `exec.Retriable` framing and the tracker).
   - req 5: panic-path wording generalised to "if the web genuinely crashes …
     must still end up errored" (still gradable for rubric must-have 4).
   - "Why this is wrong" bullet 3: dropped "the next ATC's build tracker
     re-attaches to it".
   - constraint 1: kept the non-regression warning (without it the trap is
     unfair), removed "The commit history and the archived track notes in the
     tree are the record … read them before you change behavior".
   The symptom, the invariant, the mitigations-are-not-fixes framing and the
   scope boundary are untouched, so the work item is still the real one.

2. **Grading overlay clobbers the tests the task asks for.** `task.md` requires
   "Add tests that fail before your change", and `fail_to_pass` overwrites
   `atc/engine/engine_test.go` + `atc/builds/tracker_test.go` — precisely where
   a solver would put them. Added `grading.caveats[overlay-clobbers-agent-tests]`
   fixing runner order (run the submission's suite → snapshot its two test files
   for the judge → only then overlay), and rubric must-have 5 now says to score
   the agent's tests from that pre-overlay copy.

3. **Spurious-pass gate.** The two focused invocations pass green at pre_state
   without the overlay (validation log recorded this; the `case.yaml` note
   attached the overlay only to the primary leg). The note now states the
   overlay is mandatory for all three legs and explains the false-green
   mechanism.

4. **`fail_to_pass` pins the reference shape where the task leaves it open.**
   The task says only "don't regress the earlier fix"; the overlay's check-build
   assertions additionally require that the fix take the form of *scoped
   `Finish()` calls*. Flexibility moved into `ground_truth/rubric.md` must-have
   3 (any in-scope mechanism that guarantees the `inFlightChecks` cleanup
   qualifies) and recorded as `grading.caveats[overlay-assumes-reference-shape]`:
   a submission failing only the check-build assertions is judge-adjudicated,
   never auto-failed; any job-build assertion failure is a real fail. Added
   `grading.caveats[overlay-compile-coupling]` for the related case where the
   post-state tests no longer compile against a restructured submission.

5. **Manifest consistency.** Re-checked: `information_cut`
   (2026-07-04T21:58:44-07:00) equals the pre_state commit timestamp, matches
   the provenance table, and `task.md`'s "Reported: 2026-07-04" sits inside it.
   No change needed.

### Priced deflator — KEPT (both auditors' central finding)

`forge/archive/build_tracker_behavioral_spec_20260331/spec.md` (EX-12: drain
MUST return from `Run()` without calling `build.Finish()`; EX-10/EX-11: the
job-vs-check asymmetry on the retriable path) and
`forge/archive/check_scheduling_inflight_leak_20260409/` (names both code sites,
the `inFlightChecks` rationale, and that DB-backed check builds are unaffected)
are authentic pre-cut history and stay exposed — this repo's engineers would
find them, and withholding them would grade a fictional codebase. Priced, not
withheld:

- neither doc collapses the case: EX-12 is **stale** with respect to
  `8b5476828f`, and a solver that follows it literally deletes the drain
  `finish()` and reintroduces the production leak (rubric must-have 3 fail);
- the two docs sit among 130 archived tracks at pre_state, so reaching them is a
  targeted search (blame/`log -S` on the two call sites), not a handout;
- `ground_truth/rubric.md` gained a "Judge guidance — evidence, not quotation"
  section: no penalty for citing the archives, but items 6/7/8 require
  demonstrated causal reasoning, and treating an archived spec as authoritative
  over a later deliberate code change is itself the failure mode this case
  probes.
- `withheld` stays `[]`; the deflator is documented inline in `case.yaml` above
  the `difficulty` field.

### Difficulty recalibration

`hard` → **`moderate`** (opus argued hard→medium; sonnet did not contest
difficulty). Rationale recorded inline in `case.yaml`: the in-tree archive
record supplies a large share of the causal chain to an agent that goes looking,
which is exactly the behavior the case rewards — so the ceiling is lower than
`hard` implies. It is not `trivial`: the naive delete-both fix still fails
mechanically, and the scoping insight (`inFlightChecks` only ever holds
in-memory check builds) is not stated anywhere at pre_state.

### Known leak channel

The dev machine's project auto-memory names this case's root cause verbatim
("builds error on web restart because commit 8b5476828f errors builds on drain +
tracker safety net … fix track build_survival_across_web_restart_20260704"), so
`known_leak_channels: [project-auto-memory]` is declared; memory is untouched,
and a local hand-run of this case is invalid unless project memory, session
context and conversation history are suppressed.

### Files edited

- `task/task.md` — five softenings above.
- `ground_truth/rubric.md` — judge-guidance section; must-have 3 broadened +
  grading caveat; must-have 5 scored from the pre-overlay copy; item 6 reworded
  to match the new constraint text.
- `case.yaml` — header comment, `fail_to_pass` note, new `grading.caveats`,
  deflator comment, `difficulty: moderate`, `known_leak_channels`,
  `leakage_audit` curator-fixup entry, three new `curation.learnings` lessons.
- `notes.md` — this section.

Residual verdict: **borderline**. Nothing exposed states the answer outright and
every grading defect is fixed, but the kept deflator is materially helpful and
the two prior auditors disagreed — so the case is usable and should not anchor a
headline efficacy claim on its own.
