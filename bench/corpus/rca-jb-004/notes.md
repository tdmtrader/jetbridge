# Curation record — rca-jb-004

## Provenance walk

Backed out of the terminal commit
`fc36f66737469bf63c8aa85f2e1c7edf5837a06d` (2026-04-23T07:08:34-07:00,
"fix(jetbridge): stop poisoning ArtifactLocator with daemon pod IPs"). Every SHA
below was resolved and read during extraction.

**The work, in the order it happened** (all 2026-04-23, one straight line on
`jetbridge`):

| SHA | Time | What |
|---|---|---|
| `3b00cbb7b8` | 04-18 12:40 | **pre_state.** "docs(forge): record Phase 6 checkpoint in plan.md" — a docs-only commit closing out an unrelated track. |
| `12b0743df1` | 07:00:35 | `test(jetbridge)` — reproduce the poisoning via downstream `ArtifactFromVolume`. **Direct child of pre_state** (`git rev-parse 12b0743df1^` → `3b00cbb7b8`). |
| `1389a73877` | 07:01:39 | `test(jetbridge)` — assert `FindDaemonResourceCache` writes nothing to the locator. |
| `ec489efaa8` | 07:03:09 | `test(jetbridge)` — IP-shaped input to `NodeIPResolver` must short-circuit; also declares the `ErrNodeNameIsIP` sentinel. |
| `febae10233` | 07:0x | `refactor` — plumb `ctx` through `StorageBackend.WrapVolumeForLookup`. |
| `638c2a8b00` | 07:0x | `feat` — add `isResourceCacheKey` predicate. |
| `b19a0fb2bc` | 07:06:59 | `fix` — probe daemons on `WrapVolumeForLookup` for `rc-*` keys. Its own message: "This is the load-bearing half". |
| `04eade3c88` | 07:07:36 | `fix` — reject IP-shaped inputs in `NodeIPResolver.Resolve`. |
| `fc36f66737` | 07:08:34 | **terminal** — delete the poisoning `Record` call. |
| `cf0b7fa894` | 07:1x | `test` — cover the probe-on-lookup paths exhaustively. |
| `efc827aec0` | 07:12:07 | `docs(forge)` — the `ArtifactLocator.Record` caller audit. |
| `8574f59bea` | 07:24:41 | `chore(forge)` — scaffold the track spec/plan **retrospectively**, after the fix. |

The regressing commit named by the terminal artifact,
`864eba169ff9e01a0ebbc4b92a34dbd1277be663` (2026-04-01), was read in full. It
does exactly what the fix commit says: it changed `FindResourceCache` to return
a daemon pod IP, renamed the receiving local `nodeName` → `daemonIP`, left the
`Record` call untouched, and added a comment rationalizing the substitution
("The daemon IP is used as a placeholder — on single-node clusters this is
fine"). Confirmed by reading the diff, not by trusting the message.

**Independent finding, not in any commit message.** The fix commit blames
`864eba169f` alone. That is incomplete. At `864eba169f` the poisoned entry was
unreachable as a hard failure: `FindDaemonResourceCache` returned a
`NewStubVolume`, and the only consumer of a locator entry keyed `rc-*` was
`preferredInputNode` (`storage_daemonset.go:361`), which feeds a
`preferredDuringScheduling` affinity term — a soft preference for a nonexistent
node, which K8s ignores. The read path that turns the poison into a build
failure is `Worker.ArtifactFromVolume`, added by `dc5c936532` on **2026-04-18**
(`git log -S "func (w *Worker) ArtifactFromVolume"` → single result), three
commits before the pre_state. Enumerated every locator reader at pre_state to
confirm no other path could have produced a hard failure earlier:
`reaper.go:198`, `storage_daemonset.go:124` (reads `HostDir` only, which was
recorded correctly), `:371`, `:493`, `worker.go:320`. This is what makes the
operator's "it started on the 18th" true, and it is why the rubric scores
naming both commits above the human answer of record.

### Why this pre_state

Two candidates were on the table. The mining pass offered `04eade3c88` (parent
of the terminal commit) and, as a cleaner alternative, `3b00cbb7b8`.
`04eade3c88` is unusable: at that ref all three reproduction tests are present
and green-pending, the `ErrNodeNameIsIP` guard is in, and the re-probe path is
in — the remaining work is deleting one line that two failing specs point
straight at. Took `3b00cbb7b8`, which precedes the entire investigation. It is
the *direct parent* of the first investigation commit, so no fix-chain content
has to be surgically withheld: it simply does not exist yet.

Consequence recorded in `case.yaml`: the reference change is the whole
five-commit chain, not just the terminal commit, and the rubric had to be
written against the chain root. See "the obvious fix is insufficient" below.

## The evidence bundle

No build log, CI artifact, or web log from the incident survives — nothing was
archived. `task/evidence/` is therefore **reconstructed**, and the standard for
inclusion was: every load-bearing string must be traceable to something that
exists at or before the cut, and everything else must be labelled here.

Grounded in a real artifact:

- `resolve node IP for 100.68.228.107: get node 100.68.228.107: nodes "100.68.228.107" not found`
  — verbatim from the track spec (`8574f59bea`), which is where the real
  production IP is recorded. Independently confirmed to be exactly what the
  pre_state code emits: `volume_daemonset.go:297` wraps
  `node_ip_resolver.go:50-52`, and the extractor's own pre_state test run produced
  the identical shape with `127.0.0.1` substituted.
- `INFO: found existing resource cache` — verbatim, `atc/exec/get_step.go:382`
  at pre_state.
- `/var/concourse/artifacts` — the chart default,
  `deploy/chart/values.yaml:470` at pre_state.
- `HEAD /resource-caches/<key>` returning 200 — exactly what
  `DaemonClient.ProbeResourceCache` does (`daemon_client.go:145`).
- `GET /artifacts/steps/rc-<id>` returning a tar — the peer-fetch path,
  `cmd/artifact-daemon/peers.go:184` and `behavioral_test.go:49`.
- The `file:`-based task-config read being the first thing to touch the artifact
  — `atc/exec/task_config_source.go:81`, which returns the `StreamOut` error
  unwrapped.
- Single-node cluster named `theborg` — the real deployment.

Reconstructed (plausible, not sourced): build numbers, pipeline and job names,
the pod CIDR `100.68.228.0/20` and the individual pod IPs other than
`100.68.228.107`, the 11-of-34 correlation counts, the dates in the timeline
table, and the `kubectl auth can-i` check.

Verified against the code rather than invented — each of these is a claim the
pre_state source actually implies, so the bundle is not lying to the agent:

- Only cache-**hit** builds fail. `FindDaemonResourceCache` is the sole writer
  and only writes on a hit.
- Restarting `web` does **not** help. The write and the read happen in the same
  build, one step apart, so the ephemeral locator has nothing stale to clear.
  (An earlier draft of `task.md` claimed "the first build after a web restart is
  fine" — that was wrong, caught by re-reading the call order, and removed.
  Fabricated clues are worse than missing ones: this one would have pointed the
  agent at `NodeIPResolver`'s 5-minute TTL cache and the judge would have had no
  way to know the case had misled it.)
- Restarting the daemon changes the IP in the message immediately. The value is
  re-derived from a live probe on each hit.
- Wiping `steps/rc-*` buys exactly one green build.

## Leakage analysis

Checked and clean at pre_state:

- `git grep -iE "poison|ErrNodeNameIsIP|isResourceCacheKey"` over the whole tree
  at `3b00cbb7b8` → one unrelated hit (a security spec using "poisoned data"
  about a threat model). No fix vocabulary anywhere.
- `forge/tracks/fix_cache_locator_pod_ip_poisoning_20260423/` does not exist at
  pre_state (created `8574f59bea`, five days later).
- The one *active* track at pre_state
  (`route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`)
  was read end to end. It is about a different failure
  (`exec stream: pods "…" not found`) and contains no mention of node IPs, the
  locator's `NodeName` field, or `FindDaemonResourceCache`. Deliberately left
  EXPOSED: it is genuine context an engineer would have had, and its presence is
  what makes "the artifact-read routing went out on the 18th" checkable.
- `bench/` does not exist at pre_state (2026-04-18 vs. corpus start 2026-07-25),
  so the self-hosted-corpus caveat is satisfied.
- Pre-cut **commit messages** were grepped too, since the preferred
  materialization exposes them: `git log --format="%h %s" 3b00cbb7b8 | grep -iE
  "poison|node ip|nodeipresolver|locator"` returns nine commits, all of them
  ordinary history of the locator's and the cache's evolution (the nearest,
  `5baf907e30 "remove stale locator fast path from FindDaemonResourceCache"`,
  is about a different fast path and does not describe this defect). Rich
  context for the archaeology the case asks for; not a leak.

Handled by materialization, not by withholding:

- The fix chain is a straight-line **descendant** of the pre_state ref and is
  reachable from `jetbridge`, `main`, and ~20 branch tips. Eight of its nine
  commit messages restate the answer; `fc36f66737`'s message is a complete
  answer key including the regressing SHA. This is the single biggest leakage
  risk in the case, and a plain clone or worktree fails it outright.

  The materialization in `case.yaml` is an **ancestors-only fetch**: `git init`
  into an empty directory, `git fetch --no-tags origin <pre_state sha>`,
  `git checkout --detach FETCH_HEAD`. Fetching by SHA brings that commit's
  ancestors and nothing else. Verified in a scratch directory: `git for-each-ref`
  is empty, `git branch -a` shows only the detached HEAD, `git rev-list --count
  HEAD` is 28474, `git cat-file -e fc36f66737…` **fails** (object absent), and
  the two archaeology queries the rubric cares about both work —
  `git log -S "artifactLocator.Record(cacheKey, daemonIP" -- …/worker.go`
  returns `864eba169f` and
  `git log -S "func (w *Worker) ArtifactFromVolume" -- …/worker.go`
  returns `dc5c936532`.

  This is preferred over `git archive | tar -x` (the form rca-jb-002 uses)
  because history *before* the cut is inside the information cut by definition,
  and rubric item A-4 asks the agent to date the regression. Withholding
  ancestors would make the case measure something it does not intend to.

Deliberately withheld from the evidence bundle (difficulty levers, recorded so
they are visible to a reader rather than silently applied):

1. **The web pod's info-level lager output.** In the real incident this would
   have contained `find-daemon-resource-cache.probing-daemons` — naming the
   buggy function — and
   `probe-resource-cache.cache-found {"daemon_ip":"100.68.228.107","key":"rc-3178"}`
   — printing the exact value, from the exact call, one line before it is
   misfiled. Together that is the diagnosis rather than evidence for it. Kept
   instead: the operator-level observation that establishes the same *fact*
   (`kubectl get pods -o wide` showing `100.68.228.107` is the artifact-daemon
   pod) while leaving the whole inference intact. The bundle's §6 explains the
   absence in-fiction ("nothing useful out of `--log-level=debug`"), which is
   half true — the daemon side genuinely has nothing, because the failing
   request never leaves the ATC.
2. **The 2026-04-01 anchor, scrubbed of implementation vocabulary.** The
   evidence timeline dates "resource-cache reuse started actually working" to
   ~1 April. An earlier draft described it as the switch "to the hostPath/symlink
   scheme" — which is `864eba169f`'s own title, and `git log --grep symlink`
   would have handed rubric item A-4 over without any diagnosis at all. Rewritten
   to what an operator observes (hits started happening) rather than what an
   engineer changed. Dating it is legitimate and useful: it keeps the archaeology
   findable — the agent still has to locate the bad write and blame it — rather
   than given.

Deliberately kept even though it reads like a leak: the task says the trouble
started with "the artifact-read routing work … on the 18th". That is true and
it is what the operator would say, but it points at `dc5c936532` — the
*activating* change, not the defect. It is a designed misdirection toward
"revert the routing work", which the rubric disqualifies (D-5). Priced in the
rubric rather than excised, per the `rca-jb-002` lesson.

## The obvious fix is insufficient — verified

The terminal commit reads as a one-line deletion, and a rubric written from it
alone would have been wrong. Deleting only the `Record` call at pre_state
leaves the headline regression spec **red**, with a different error:

```
DaemonSetVolume.StreamOut: no source node known (key=rc-42)
```

because the compensating read-side re-probe landed one commit earlier
(`b19a0fb2bc`) and does not exist at pre_state. Measured, not reasoned — see
Validation step 4. Rubric item B-2 exists because of this.

## Open questions

- **Is `NewDaemonSetVolumeFromIP` + `GET /artifacts/rc-{id}` actually correct in
  production?** The volume built from a probe hit fetches
  `/artifacts/<key>` with `key = "rc-42"`, but the registered symlink lives at
  `steps/rc-42`, and the daemon's `handleGetArtifact` trims to a path under the
  storage root. The reproduction test's `httptest` daemon answers any path
  containing `/artifacts/`, so it does not pin this. Possibly a second latent
  bug in the same area, possibly handled by the daemon's registry/alias lookup.
  Out of scope for this case (both pre and post behave the same way) but worth a
  look; if it is a real bug it would be a good candidate for its own case.
- **Difficulty calibration.** The bundle hands over "the IP is the daemon pod's"
  and "the value tracks the live pod". That is the honest operator finding, but
  it is a large fraction of insight A-1. If measured results cluster at the top,
  the dial to turn is §2 of `cluster-observations.md` — cut the
  `kubectl get pods -o wide` output and leave only "it is not a node", which
  moves the case toward `hard`. Do **not** turn the dial by adding the web logs;
  that only moves it toward trivial.
- **Scoring split.** The judge checklist weights diagnosis 60 / change 40. Not
  validated against real runs; if agents routinely produce a correct diagnosis
  and a wrong-shaped fix (or vice versa) the weights are the first thing to
  revisit.

## Extractor pre-check (superseded by the validation record below)

Written while `validation.status` was still `unvalidated` — the extraction
protocol gives that field to the validation stage, which has since run and set
it to `validated` (see "## Validation" at the end of this file; that heading is
what `case.yaml#validation.notes` points at). What follows is the extractor's
own pre-check, run so the case would not be sealed on an unverified claim.

Environment: macOS 24.6.0, Go from the repo's toolchain, no Postgres, no Docker,
no network beyond the module cache. Trees materialized with
`git archive <sha> | tar -x -C <dir>` into a private scratch directory.

1. **pre_state builds and is green.**
   `go test ./atc/worker/jetbridge/ -count=1` at `3b00cbb7b8` →
   `ok … 17.636s`. (Caveat for whoever repeats this: the first attempt was run
   in a shared scratch directory that already held files from another
   extraction; the mixed tree produced a bogus `agent/schema` resolution error.
   Use a private directory.)
2. **fail_to_pass is red at pre_state, for the right reason.** Copied
   `ground_truth/withheld_tests/atc/worker/jetbridge/worker_test.go` over the
   pre_state file and re-ran: `Ran 319 of 319 Specs … 317 Passed | 2 Failed`.
   The two failures are exactly the intended specs, and the first fails with
   ```
   resolve node IP for 127.0.0.1: get node 127.0.0.1: nodes "127.0.0.1" not found
   ```
   — the operator's error verbatim, modulo the httptest address. It compiles
   without touching any other file, confirming the "restore one file" grading
   recipe.
3. **Green at the terminal commit.** `go test ./atc/worker/jetbridge/ -count=1`
   at `fc36f66737` (whose tree already contains both withheld test files) →
   `ok … 17.665s`.
4. **Deletion-only is not sufficient.** Removed just the four-line `Record`
   block from `worker.go` on the pre_state tree (withheld spec restored) →
   `318 Passed | 1 Failed`; the locator spec goes green, the downstream-read
   spec stays red with `DaemonSetVolume.StreamOut: no source node known
   (key=rc-42)`.

Not yet done, left for the validation stage:

- Running `node_ip_resolver_test.go` restoration — expected to be a compile
  error at pre_state; asserted from source inspection
  (`ErrNodeNameIsIP` is added by `04eade3c88`), not executed.
- A leakage audit by two independent models (`leakage_audit: []`). Point them at
  §"Leakage analysis" and ask specifically whether task item 2 ("say when it
  became wrong") plus the evidence timeline's 1-April row together give away
  provenance. **Done** — opus (pass) and sonnet (borderline) are recorded in
  `case.yaml#leakage_audit`; neither found the 1-April row to be a giveaway.
  Their flags were resolved by the fixup pass below.
- Any check that the reconstructed evidence bundle reads as authentic to a
  fresh reader. It was written by the same agent that knows the answer, which is
  the usual way a bundle acquires a tell.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `3b00cbb7b8c0ec0e41b94d7f1215f4e66e9d47dd`, post `fc36f66737469bf63c8aa85f2e1c7edf5837a06d`
- outcome: **validated** (both runnable legs; the third entry is confirmed DO-NOT-RUN by static check)

### fail_to_pass
`cp <case>/ground_truth/withheld_tests/atc/worker/jetbridge/worker_test.go atc/worker/jetbridge/worker_test.go && go test ./atc/worker/jetbridge/ -count=1`

PRE (FAIL, exit 1) — exactly the two recorded specs, with the verbatim operator error:
```
resolve node IP for 127.0.0.1: get node 127.0.0.1: nodes "127.0.0.1" not found
Summarizing 2 Failures:
  [FAIL] Worker FindDaemonResourceCache when a downstream step wraps the returned volume via ArtifactFromVolume
         [It] StreamOut on the wrapped artifact succeeds without resolving the daemon IP as a node name
  [FAIL] Worker FindDaemonResourceCache when a probe hit occurs
         [It] writes nothing to the ArtifactLocator for the cache key
Ran 319 of 319 Specs in 4.142 seconds
FAIL! -- 317 Passed | 2 Failed
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/atc/worker/jetbridge  17.664s`

### pass_to_pass (no overlay)
`go test ./atc/worker/jetbridge/ -count=1` — PRE `ok ... 17.675s`, POST `ok ... 17.687s`.

### `node_ip_resolver_test.go` overlay — confirmed DO-NOT-RUN
Static check rather than execution, as instructed:
- the withheld file references `ErrNodeNameIsIP` (lines 70-71)
- at pre_sha, `grep -rl ErrNodeNameIsIP --include='*.go' atc/worker/jetbridge/` -> **0 files**
- at post_sha it exists in `atc/worker/jetbridge/node_ip_resolver.go`
So dropping that file on a pre-state tree is a package-wide compile error that would also take the real fail_to_pass down. The warning in case.yaml is accurate; leave it unused for grading.

- corrected_cmd: none; `bench/corpus/...` must be resolved from the corpus checkout (absolute path), not from inside the materialized worktree.
- notes: no Postgres, no Docker, no cluster; ~18s per leg.

## Fixup 2026-07-25

Curator-fixup pass over the two-model leakage audit. Every audit item is
resolved below into dissolved / fixed / recalibrated / known-leak. No exposed
evidence was weakened, no ids or titles were renamed, and the pre_state pin,
the reference change and the ground-truth answer are untouched.

### Dissolved by the exposure contract (no action)

1. **opus: "retitle case.yaml — its title is a one-line answer key."** and
   **sonnet: "case.yaml's title states the mechanism."** `case.yaml` is
   harness-side: the solver sees `pre_state` minus `withheld` plus `task/`, and
   nothing else in the case directory. Titles, ids, paths and grading configs
   may state the answer freely (schema §"The exposure contract"). Not renamed,
   not retitled. The only real obligation the contract creates is on the
   *runner* — a hand-run must materialize `task/` into a neutrally-named
   directory — and that is a harness property, not a case defect. The stale
   `# BORDERLINE: needs human spot-check` banner at the top of `case.yaml` was
   replaced with a note recording this dissolution.
2. **opus: "enforce the fetch-by-SHA detached materialization."** Already
   enforced: `pre_state.repository.materialize` carries the ancestors-only
   recipe, the fallback, and the consequence for rubric item A-4. No change
   needed.

### Real defects, fixed

3. **Grading overlay clobbered the deliverable it grades.** `task.md` item 3
   asks for a regression test in `atc/worker/jetbridge`; the natural file is
   `worker_test.go`, which is exactly the path `fail_to_pass[0].restore`
   overwrites. Rubric B-3 would then be scored against the humans' test, or
   against nothing. Added `grading.overlay_protocol` to `case.yaml` (snapshot
   the agent's tree and its test diff; apply the restore only to a copy; judge
   B-3 from the pre-overlay tree; any file in the package is acceptable) and a
   matching paragraph in `ground_truth/rubric.md#B-3`.
4. **The mechanical leg pinned the humans' solution shape.** `focus_specs[1]`
   ("writes nothing to the ArtifactLocator") encodes B-1's shape, and the
   withheld file compiles against pre_state signatures while the humans' own
   change altered one (`WrapVolumeForLookup` gained a `ctx`) — so a solution
   matching one of the three shapes rubric B-2 explicitly accepts could be red
   or non-compiling on the overlay. Added `grading.caveat`: that outcome is
   "overlay incompatible", resolved by the judge, never scored as a mechanical
   failure; and a green overlay corroborates the change half only. Mirrored in
   the rubric's new "How to read the agent's output" preamble.
5. **`information_cut` contradicted the exposed task's own dates.** T was the
   pre_state *commit* timestamp (2026-04-18T12:40:03-07:00), but the exposed
   evidence carries observations dated 19–23 April and a report dated 23 April,
   i.e. content later than the declared cut. Reframing the task to fit
   18 April was not available: the 18 April deploy is load-bearing (it is the
   activating change and the designed misdirection), so shifting the dates
   would falsify the incident. Moved T to the instant the work began —
   `2026-04-23T07:00:35-07:00`, the timestamp of the first investigation commit
   `12b0743df1`, which is itself not exposed. Verified this exposes no extra
   repository content: `git rev-parse 12b0743df1^` is the pre_state ref, so
   `3b00cbb7b8` is the branch head across the whole 18–23 April window and the
   `pre_state` pin is unchanged.
6. **No delivery channel for the diagnosis.** The case is judge-graded on a
   written diagnosis, but `task.md` never said where to put it (rca-jb-001 says
   `RCA.md` at the repo root; rca-jb-003 and rca-jb-005 at least have a
   `## Deliverable` section). Added a `## Deliverable` section naming `RCA.md`
   at the repository root for items 1/2/4 plus the change and test for item 3,
   and told the judge, in the rubric preamble, to grade the diagnosis wherever
   it was actually delivered and to log a missed channel as a process note
   rather than a Part A deduction.
7. **One leading clause in the constraints.** "…producer-recorded step outputs,
   the reaper's cleanup, and input scheduling affinity **all read the same
   bookkeeping** and are behaving correctly" handed over, for free, that a
   shared bookkeeping store exists and that it holds node names — a meaningful
   slice of rubric A-1's second half. Softened to "are all behaving correctly
   and must keep behaving correctly", which preserves the authentic
   don't-regress constraint (and the D-4 guard) without the architectural hint.
   Kept deliberately, after review: task item 1's "what value is wrong, who put
   it there" — it is the demand that makes rubric A-1's zero-credit rule for
   stopping at `NodeIPResolver` fair, and removing it would let a
   symptom-level answer be scored as unfairly failed.
8. **Stale internal cross-reference in these notes.** The first `## Validation`
   heading asserted `validation.status: unvalidated`, which the validation stage
   has since superseded, and it duplicated the anchor
   `case.yaml#validation.notes` points at. Renamed to "## Extractor pre-check"
   with the supersession stated; the surviving `## Validation` heading is the
   real record. Marked the "leakage audit by two models" open item as done.

### Priced deflator, kept

9. **sonnet: the same-day forge track
   `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
   is a convenient dated paper trail for the activating change.** True, and
   kept: it is authentic history written before the incident (opus asked for
   exactly this to be recorded — it is real work, not a plant), and an engineer
   at T had that directory in front of them. Authenticity wins; the deflator is
   priced into grading instead. `rubric.md` gained a preamble bullet requiring
   the judge to credit causal reasoning from the evidence and the code over
   quotation of in-tree documents, and A-4 now says explicitly that naming the
   18 April date is nearly free (the evidence timeline states it too) and that
   the higher band is earned by the causal claim — the routing change made an
   existing bad value reachable, it did not create it.

### Difficulty

10. No recalibration. Neither auditor argued the effective difficulty differs
    from `moderate`, and the fixup changed no exposed evidence: item 7 removed a
    hint (marginally harder), items 6 and the rubric preamble only clarify
    grading. The recorded dial (cut §2 of `cluster-observations.md` to reach
    `hard`) is unused and stays an open question. Left at `moderate`, with the
    reasoning noted inline in `case.yaml`.

### Known leak channel

11. **`known_leak_channels: [project-auto-memory]` declared.** Not an auditor
    flag — found by the curator grepping this machine's project memory. It is
    **partial**: `project_artifact_architecture.md` states that step-produced
    artifacts are wrapped by `Worker.ArtifactFromVolume` so downstream
    `StreamOut` dispatches to `*DaemonSetVolume.StreamOut` (part of rubric A-2's
    chain) and dates that routing work to 2026-04-18 under the track named above
    (rubric A-4's activating change). Nothing in memory mentions
    `ArtifactLocator`, the node-name field, the daemon-IP write, `864eba169f`,
    or the error string — the load-bearing A-1 insight is not leaked. Per the
    README, a local hand-run on this machine is invalid for A-2/A-4 unless
    project memory, session context and conversation history are suppressed.
    Memory itself was not modified.

### Residual

`pass`. Every audit item is dissolved, fixed, or declared. Files touched:
`case.yaml`, `task/task.md`, `ground_truth/rubric.md`, `notes.md`. Nothing in
`ground_truth/answer.md`, `ground_truth/reference.diff`,
`ground_truth/test.diff`, `ground_truth/withheld_tests/` or `task/evidence/`
was changed, so the validation record above still holds as run.
