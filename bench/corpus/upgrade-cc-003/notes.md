# upgrade-cc-003 — curation record

## Provenance walk

Backed out of a merged upstream pull request. Every SHA below was resolved in
this repository (the jetbridge fork carries upstream `master` history).

```
3bddb5e0e2e06d72ae722678af45f472b6c61504   TERMINAL
    "Merge pull request #9059 from concourse/bump-dex"   2025-01-28T12:17:39-05:00
    parents: cd71788dc4 (first) 52c7742d09 (second)
  |
  +-- cd71788dc4e7338197f9679f13e9ac221d2bd088   merge-base / first parent
  |       "Merge branch 'master' of github.com:concourse/concourse into HEAD"
  |       2025-01-23T21:45:34+00:00
  |
  '-- 52c7742d09208e28813eb8365956581362a4c4fa   PR head  ->  POST_STATE
          "use uint16 because of garden update"           2025-01-24T17:03:15-05:00
        |
        f25fca2ebc728cd1b870c2fbd53d432474437b03
          "switch to concourse/houdini and pass slog into dex"  2025-01-24T11:37:10-05:00
        |
        4fe7b9a8b789ba47e240d9038e9dfb977c98c03a   ->  PRE_STATE
          "bump dependencies"                            2025-01-24T10:45:46-05:00
        |
        cd71788dc4  (merge-base)
```

All three commits are by Taylor Silva, 2025-01-24. The branch is linear.

**The merge adds nothing.** Verified: `git diff cd71788dc4 3bddb5e0e2` is byte-identical
to `git diff cd71788dc4 52c7742d09`. So the PR head is a faithful post-state and
`ground_truth/reference.diff` is `git diff 4fe7b9a8b7 52c7742d09` — 11 files,
+23/−26, no unrelated churn to exclude.

**Pre-state choice.** `4fe7b9a8b7` (the PR's own first commit) rather than the
merge-base. `4fe7b9a8b7` touches only `go.mod` (146 lines) and `go.sum` (330
lines) and no Go source, so it is exactly "bump landed, code not yet repaired" —
a coherent, self-consistent tree that happens not to compile. Pinning at
`cd71788dc4` instead would have made the task "reproduce a 140-module bump",
whose ground truth is a lockfile and whose diff is unbounded. The mining
candidate flagged this and it is the right call; it is recorded as curation
learning (1).

**Coherence check on the pre-state.** `go.mod` at `4fe7b9a8b7` still requires
`github.com/vito/houdini v1.1.3` and `github.com/concourse/dex v1.9.0` — i.e.
the tree really is in the intermediate state where a bumped dex sits next to an
unbumped in-tree adapter, and an unmoved houdini sits next to a bumped garden.
No stray fix is already present.

## What the case actually contains (verified, not inherited)

The candidate said "three breakages". It is four API changes across three
subsystems, and the candidate's file attribution was off by one commit (it
placed `atc/hijack_payload.go` and `atc/runtime/types.go` in `52c7742d09`; they
are in `f25fca2e`). Corrected in `ground_truth/answer.md`. Verified directly:

| # | Upstream change | Evidence |
|---|---|---|
| A | `garden.WindowSize{Columns,Rows}` `int` → `uint16` | diffed `code.cloudfoundry.org/garden@v0.0.0-20231010181202` vs `@v0.0.0-20250122021912` in GOMODCACHE — `container.go:178` is the *only* difference in the two files that matter |
| B | `dex` `server.Config.Logger` `log.Logger` → `*slog.Logger`; `storage/sql.Postgres.Open(log.Logger)` → `Open(*slog.Logger)` | read `concourse/dex@v1.8.0` vs `@v1.9.0` in GOMODCACHE (`server/server.go:104` vs `:119`; `storage/sql/config.go:87` vs `:85`) |
| C | `dex` `storage.Storage` create methods gained a leading `ctx` | `concourse/dex@v1.9.0/storage/storage.go:81,84,86` vs `@v1.8.0:80,83` |
| D | `vito/houdini v1.1.3` no longer compiles against the new garden | built it — see #validation |

Also confirmed that `lager/v3` v3.23.0 (part of the same refresh, up from
v3.18.0) ships `func NewHandler(l Logger) slog.Handler` at `handler.go:22`,
which is the affordance upstream reached for. That is what makes B a "did you
find the library's own migration helper" criterion rather than a "can you write
a slog.Handler" one.

One claim in the candidate did **not** survive: `skymarshal/dexserver/dexserver_test.go`
is listed as a modified test file implying a compile break. It is not — the
change is `lagertest.NewTestLogger` → `lager.NewLogger`, and `*lagertest.TestLogger`
still satisfies `lager.Logger`. It is log-noise reduction. The rubric explicitly
does not require it.

## Leakage analysis

**Withheld: the commit messages.** Both fix commits name the answer in their
subject lines — `"use uint16 because of garden update"` and `"switch to
concourse/houdini and pass slog into dex"` — and `f25fca2e`'s body explains the
lager→slog bridge and the fork rationale in prose. None of it is exposed. The
work item is synthesised from scratch.

**Withheld: descendant reachability.** `4fe7b9a8b7` is reachable from
`upstream/master`, so a materialization that keeps refs or a reflog puts the two
fix commits (and the merge) one `git log --all` away. `case.yaml` carries the
`git archive | tar -x` materialization instruction for this reason; it is
load-bearing, not decorative.

**Scrubbed from the task:** any statement of *which* subsystems break, *how
many* failures there are, or *where* they are. In particular the task does not
say "propagate the type change rather than casting at the call site" — that
sentence was drafted and then deliberately cut, because it converts the case's
single best discriminator (does the agent reason about a wire struct that both
`fly` and the ATC compile against, unprompted?) into instruction-following. The
consequence is that a locally-cast solution is scored as a *lesser* answer, not
a wrong one — `rubric.md`§A.4 says so explicitly. This is the honest trade and
it is the main thing a reviewer of this case should push back on if they
disagree.

**Retained in the task, deliberately:**
- The list of notable bumped modules (garden, lager, dex, k8s, containerd, otel,
  x/*). This is readable from `git show HEAD -- go.mod` at pre-state in one
  command, so withholding it buys nothing; it is padded with modules that do
  *not* break so it is not a pointer.
- "no pin-back / no `replace` / no deleting tests / `go mod tidy` clean". These
  are boilerplate upgrade hygiene and they exist in real upgrade tickets. They
  do close the escape hatches on the houdini blocker, which is a mild nudge —
  but without them the case has no floor at all, because pinning `garden` back
  makes every symptom vanish and would score as a green build.
- No compiler output is pasted into the task. The agent can produce it in one
  command; pasting an excerpt would have implied a complete list and pointed
  straight at `toGardenTTYSpec`.
- The "one binary, one module" note lists `web` / `worker` / `fly`, which is the
  complete set of the binary's commands and therefore not a selective pointer.
  An earlier draft glossed `web` as "ATC + the `skymarshal` auth service"; that
  gloss was cut, because naming `skymarshal` *is* selective — it is one of the
  three broken subsystems and nothing else in the task mentions it.

**Anti-leakage present at pre-state (kept):** `skymarshal/logger/logger.go`, the
logrus→lager bridge, is still in the tree and still compiles. It biases toward
"repair the existing adapter" and away from `lager.NewHandler`. Faithful to what
the repository looked like; it stays. `withheld: []`.

**Memorization: high, and this one is not marginal.** Public repo, PR #9059,
merged 2025-01-28, roughly sixteen months before the assistant cutoff. A model
may have `concourse/houdini v1.2.0` and the `uint16` change memorised outright.
Per `bench/README.md` this case cannot anchor an efficacy claim alone. It is
still worth having: it is one of the few candidates where the required move
(swap a dependency for a fork) is unreachable by local reasoning, so it probes a
distinct capability, and a memorisation-driven pass is at least detectable —
an agent that goes straight to `github.com/concourse/houdini` without ever
building `vito/houdini` and seeing it fail is recalling, not reasoning. Judges
should note that in the transcript.

## Validation

**Status: partial. `go build ./...` at pre_state and post_state was NOT run.**
The curation machine ran out of disk part-way through the first full build:

```
/dev/disk3s5  228Gi  190Gi  1.5Gi  100%  /System/Volumes/Data
unzip .../google.golang.org/api/@v/v0.218.0.zip: ... no space left on device
```

GOMODCACHE was already 5.6 GB and GOCACHE 16 GB, and other sessions were
building concurrently. Neither cache was cleared — that is the user's data and
other work was in flight. Free space fell to ~475 MB during the focused runs
below, at which point building stopped.

What **was** verified, on `go1.25.6` (satisfies `toolchain go1.23.4`), with
pre- and post-state trees materialized via `git archive`:

1. **Subsystem A fail-to-pass — CONFIRMED.**
   `go vet ./atc/worker/gardenruntime/gclient/`
   - at `4fe7b9a8b7`: `vet: atc/worker/gardenruntime/gclient/retryable_garden_connection_test.go:382:47: cannot use 345678 (untyped int constant) as uint16 value in struct literal (overflows)`
   - at `52c7742d09`: clean, exit 0.
2. **Subsystem B fail-to-pass — CONFIRMED.**
   `go vet ./skymarshal/storage/`
   - at `4fe7b9a8b7`: `vet: skymarshal/storage/storage.go:41:20: cannot use logger.New(log) (value of type *logrus.Logger) as *slog.Logger value in argument to store.Open`
   - at `52c7742d09`: clean, exit 0.
   (Both packages were vetted in the same invocation; both are Postgres-free to
   type-check.)
3. **Subsystem D — CONFIRMED as a real, non-local break.** Against the pre-state
   module graph (`go.mod`/`go.sum` from `4fe7b9a8b7` alone, no source needed):
   ```
   # github.com/vito/houdini/process
   .../vito/houdini@v1.1.3/process/spawn.go:37:20: cannot use ttySpec.WindowSize.Columns (variable of type uint16) as int value in assignment
   .../vito/houdini@v1.1.3/process/spawn.go:38:17: cannot use ttySpec.WindowSize.Rows (variable of type uint16) as int value in assignment
   .../vito/houdini@v1.1.3/process/spawn.go:98:49: cannot use size.Columns (variable of type uint16) as int value in argument to ptyutil.SetWinSize
   .../vito/houdini@v1.1.3/process/spawn.go:98:63: cannot use size.Rows (variable of type uint16) as int value in argument to ptyutil.SetWinSize
   ```
   and against the post-state graph, `go build github.com/concourse/houdini
   github.com/concourse/houdini/process` exits 0. This was the single most
   valuable check in the whole walk: it upgraded the houdini change from
   "opportunistic fork, per the commit message" to "the build cannot go green
   without it".
4. **Subsystem C — by inspection only.** `concourse/dex@v1.9.0/storage/storage.go`
   requires `ctx` on `CreateClient`/`CreatePassword`/`CreateConnector`; the three
   call sites in `dexserver.go` pass none. Compiling `./skymarshal/dexserver/`
   pulls `dex/server` → `google.golang.org/api` and was not attempted (disk).
5. **Subsystem A production site — by inspection only.**
   `atc/worker/gardenruntime/container.go:125-127` assigns `int` into a `uint16`
   field. Certain, but the package was not compiled (pulls `atc/db`).

**What a validator must still do**, in this order:
- Free/allocate disk first: ~6 GB GOMODCACHE plus several GB GOCACHE, on top of
  whatever is already there. This is the binding constraint, not time.
- `go build ./...` at `4fe7b9a8b7` → must fail, and the failure list should be
  captured verbatim into this file (it is the exact input an agent will see).
- `go build ./...` at `52c7742d09` → must succeed. If it does not, the case is
  wrong about the reference change being complete and should be re-derived
  against the merge commit `3bddb5e0e2`.
- `go vet ./...` at both refs.
- Flip `validation.status` to `validated` only when the full-build pair is
  green/red as expected.

## Open questions

- **Is `rubric: judge` the right primary?** The case has a genuine mechanical
  gate (`go build ./...`), and a mechanical-only grade would be cheap and
  reproducible. It was rejected because the shortcut solution passes it —
  see curation learning (3). If pilot runs show every agent either fails the
  build entirely or produces the upstream answer, the judge layer is dead weight
  and the case should be simplified to `mechanical`.
- **Does the houdini criterion need network?** Yes, unavoidably — the module
  proxy is needed to build at all. But discovering `github.com/concourse/houdini`
  specifically may need more than the proxy (a registry search, or the guess
  that the org forked it). If the harness runs with a pre-warmed, sealed module
  cache, D becomes unsolvable and the case degrades to a two-subsystem case.
  The harness's network posture should be recorded alongside any result.
- **`atc/api/containerserver/hijack.go` is a silent participant.** It copies
  `atc.HijackWindowSize` into `runtime.WindowSize` field-by-field and needs no
  change *only because* both structs move together. It is the cheapest available
  probe for a half-done migration (rubric A.6) but nothing in the graded command
  set exercises it beyond type-checking. A validator with a working full build
  should confirm that a deliberately half-migrated tree does in fact fail
  `go vet ./atc/...` there.
- **Should a companion case pin at `cd71788dc4` and grade the bump itself?**
  Probably not with a diff rubric, but "given a red CI after a refresh, produce
  the *diagnosis*" would be a legitimate log-diagnosis-shaped sibling reusing
  the same terminal artifact. Noted, not built.

## Validation

- date: 2026-07-25
- validator: mechanical
- worktrees: NOT created — see below
- outcome: **environment-blocked (disk)**

Every command in this case (`go vet ./atc/worker/gardenruntime/gclient/`, `go vet ./skymarshal/storage/`, `go build ./...`, `go build ./worker/workercmd/`, `go vet ./atc/... ./fly/... ./skymarshal/... ./worker/...`) requires compiling a large dependency graph. The validation host was already at 100% capacity and hit `no space left on device` on strictly smaller workloads earlier in the same session:
```
# github.com/aws/aws-sdk-go/service/ssm
compile: writing output: write $WORK/b684/_pkg_.a: no space left on device
.../darwin_arm64/link: mapping output file failed: no space left on device
```
Free space at the time of this entry: ~227Mi on /System/Volumes/Data. Attempting this case's `go build ./...` (which additionally pulls the containerd graph for the houdini gate, ~6GB GOMODCACHE per the case notes) was therefore not attempted rather than reported as a false failure.

This mirrors the case's own recorded status ("UNVERIFIED - curation machine ran out of disk mid-build"): the disk requirement is a real, reproducible property of this case, not a one-off.

### Environment requirement
- **~15 GB free disk** (≈6 GB GOMODCACHE incl. containerd + several GB GOCACHE, at two SHAs)
- network or a pre-warmed module cache for `github.com/concourse/houdini` (post) and `github.com/vito/houdini@v1.1.3` (pre)
- no PostgreSQL, no cluster
- when it is run, capture the pre-state failure list verbatim into notes.md — it is the exact input an agent sees

- corrected_cmd: none proposed; commands were not executed.

## Fixup 2026-07-25

Curator pass over the dual-audit findings (opus: borderline, sonnet: fail).
Every audit item resolved; residual verdict **pass**.

### Dissolved by the exposure contract (no action, nothing renamed or redacted)

Both auditors' leakage findings target `case.yaml` itself:

- sonnet's entire FAIL — case.yaml "states the two fix commits' subjects verbatim
  and calls them 'the answer key', quotes compiler errors naming both target
  types, names the fork-swap technique, and discloses the rubric's
  cast-vs-propagate discriminator";
- opus's first curator action — the same two items.

Per `bench/schema/benchmark-case-v1.md` §"The exposure contract", the solver sees
exactly `pre_state − withheld + task/`. `case.yaml`, `notes.md`, `ground_truth/`
and the case id/path are harness-side and are never exposed, so titles, ids and
grading configs may state the answer freely. Nothing was softened: the verbatim
commit subjects and compiler output in `case.yaml` are what make this case
auditable and re-derivable, and removing them would cost real curation value for
zero exposure benefit. The same contract covers the title, which names all three
subsystems. Both auditors independently confirmed the actually-exposed surface
(`task/task.md` + the pre_state tree) is clean — that is the finding that
mattered, and it stands.

The one hand-run caveat the contract attaches applies here: a by-hand runner must
materialize `task/` into a neutrally-named directory and must materialize the
repo detached, per `pre_state.materialize`.

### Real defects fixed

1. **Graded escalation with no delivery channel** (opus's second finding — the
   only substantive solver-side item in either audit). `rubric.md` D.4 awards
   ~half of subsystem D for correctly declaring the houdini break a blocker, but
   `task/task.md` never said a blocker report was an acceptable deliverable or
   where to put it. Fixed in `task/task.md`: new **"How to report"** section
   asking for an `UPGRADE-REPORT.md` at the repo root with (a) what changed and
   which upstream API change forced it, (b) anything that could not be done
   within the constraints. Deliberately generic — it names no subsystem, no
   count, and no dependency, and reads as ordinary upgrade-ticket boilerplate, so
   it does not narrow the search or hint that a blocker exists. `rubric.md` D.4
   now names that channel while instructing the judge to accept any unmistakable
   equivalent (final response, `BLOCKERS.md`, commit message) and grade the
   diagnosis rather than the filename. `rubric.md` P.3 exempts the report from
   the scope-creep comparison, since `reference.diff` predates the instruction
   and has no counterpart.
2. **`fail_to_pass` houdini gate vs. the escalation path.** `go build
   ./worker/workercmd/` is red on a legitimate escalation run, so the mechanical
   set alone would zero an answer the rubric pays partial credit for. The
   flexibility now lives in `rubric.md` ("Gate 1 is not a kill switch": a red
   build is an automatic zero only when it is *silent*, and a failure confined to
   the houdini backend with an explicit blocker report scores per D.4), and
   `case.yaml` carries the matching GRADING CAVEAT comment above `fail_to_pass`.
3. **Both `pass_to_pass` entries failed at pre_state.** `go vet ./atc/...
   ./fly/... ./skymarshal/... ./worker/...` is a strict superset of the two
   *verified* pre_state failures (`./atc/worker/gardenruntime/gclient/`,
   `./skymarshal/storage/`), and `go test ./atc/worker/gardenruntime/...` cannot
   compile at pre_state for the same reason. `pass_to_pass` means green **before
   and after**; both were end-state gates wearing the wrong label, and any
   harness running its regression guard at pre_state would have recorded a
   spurious failure. Both moved into `fail_to_pass` (where they are correct and
   useful — the broad vet is the half-done-migration probe for rubric A.6).
   `pass_to_pass` is now `go test ./fly/commands/... -count=1`, chosen because it
   is green at both refs: `git grep -l "cloudfoundry.org/garden" 4fe7b9a8b7 --
   fly/` and the same for `concourse/dex` are both empty, and the root `atc`
   package does not import garden either, so the narrowing does not reach `fly/`
   at pre_state. It is still a real guard for subsystem A —
   `fly/commands/internal/hijacker/hijacker_test.go` exercises the
   `atc.HijackWindowSize` producers the propagation touches. Status honestly
   recorded as `unverified` (not executed; the disk constraint below is
   unchanged).
4. **Constraint/rubric tension in subsystem B.** The task's "do not take up new
   capabilities the upgraded libraries now offer" can be read as discouraging
   `lager.NewHandler`, which is exactly the B.3 full-credit answer. The task text
   was left alone (it is authentic upgrade-hygiene wording, and clarifying it
   would point at the affordance); instead `rubric.md` B.4 now says not to deduct
   further when an agent found the helper and explicitly declined it on that
   reading.

### Difficulty

Unchanged: **hard**. Neither auditor contested it. Four upstream API changes
across three subsystems that share no code, one of them unrepairable inside this
repository — the floor is "notice the fourth failure is in a dependency's own
source and act correctly on that", not "read a compiler error". Memorization can
make an individual *run* look easy; that is a property of the result, now
controlled for in the rubric (P.2a) rather than a reason to relabel the task.

### Known leak channels

Added `known_leak_channels: [upstream-git-history]`.

- **Not** `project-auto-memory`: this machine's memory files are about
  jetbridge's K8s runtime and say nothing about garden, dex, slog or houdini, so
  this case's answer is not in that channel.
- The live channel is the host's own git objects and the public remote:
  `4fe7b9a8b7` is reachable from `upstream/master`, so a solver with refs, a
  reflog, or network access to `github.com/concourse/concourse` is one
  `git log --all` from two commit subjects that state the entire answer. This was
  already described under #leakage-analysis and mitigated by the mandatory
  detached `git archive` materialization; declaring it as a channel makes the
  requirement machine-visible to a replay harness rather than a prose footnote.
- The related-but-distinct `memorization_risk: high` is unchanged and still means
  this case cannot anchor an efficacy claim on its own. `rubric.md` P.2a now
  makes the judge write down which one it saw: evidence-grounded reasoning (built
  the tree, read `GOMODCACHE`, reproduced the failure inside `vito/houdini`)
  versus an asserted answer that arrives without the observation that motivates
  it.

### Not changed, on purpose

- `withheld: []` stands. Nothing at pre_state describes any of these fixes, and
  `skymarshal/logger/logger.go` biases *away* from the answer. No in-tree design
  doc is involved, so the priced-deflator question does not arise here; the
  judge-credits-reasoning-not-quotation discipline was added anyway (P.2a),
  because the memorization channel raises the same failure mode.
- `information_cut` (`2025-01-24T10:45:46-05:00`) is exactly the pre_state
  committer date and no exposed file carries a conflicting date — `task/task.md`
  states no dates at all. Checked; consistent; no edit.
- The task still does not say "propagate the type change rather than casting",
  which keeps the case's best discriminator a real one.
- `validation.status: partial`. The disk-blocked full build is untouched by this
  pass and remains the top item for a validator.

### Files touched

- `task/task.md` — added "How to report".
- `ground_truth/rubric.md` — "Gate 1 is not a kill switch"; B.4 clause; D.4
  delivery channel; new P.2a memorization control; P.3 report exemption.
- `case.yaml` — header comment replaced (BORDERLINE → resolution record);
  GRADING CAVEAT on the escalation path; `pass_to_pass`/`fail_to_pass`
  reclassification; difficulty rationale; `known_leak_channels`; curation
  learnings (4) and (5); `leakage_audit` curator-fixup entry.
- `notes.md` — this section.
