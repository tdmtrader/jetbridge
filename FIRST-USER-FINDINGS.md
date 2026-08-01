# First-user findings — jetbridge node-level dogfood

Session started 2026-08-01. Goal: act as the platform's first real user, working
from the user guides ([docs/operations/reusable-node-definitions.md](docs/operations/reusable-node-definitions.md),
[docs/agentic/README.md](docs/agentic/README.md)), building and iterating on
reusable nodes (code review first), graded against the bench corpus.

Environment: live web at http://concourse.home (cicd ns), running commit
`844f9495a9` (== this branch's tip at session start). Corpus commit cited per
result below.

Legend: 🔴 pain point · 🟢 works well · 🔵 agent-quality pattern · ⚪ observation

## Summary

The platform's node model is right and the loop genuinely closes: typed inputs
→ immutable node version → hermetic agent pod → contract-validated record →
sealed snapshot → out-of-band grading. Two nodes (`code-review`,
`log-diagnosis`) were authored, released, run against real corpus cases, and
graded, and one of them produced a root-cause diagnosis that beats the human
baseline on provenance.

Getting there took **six** fixes. Four are in this branch or already deployed;
two remain open and block other users:

| # | blocker | state |
|---|---|---|
| F5 | fly wrote dir tar headers the server rejects — no nested directory could be uploaded | fixed here |
| F20 | output-builder MCP never implemented `initialize` → agents had **no** working way to write output | fixed here, **needs agent-runner image rebuild** |
| F25 | reviewgrade scored sealed records as 0% silently | fixed here |
| F16 | hermetic egress fail-closed, no preflight → 5-min silent death | fixed live (home-infra `8dc7550`) |
| **F7** | **deployed web image has no `git`, so no `repository/v1` can ever seal → every flagship seed is unrunnable on this deployment** | **OPEN** |
| **F12/F17** | **guide examples (`model: claude-sonnet`, `budget_slice_usd`) fail against the deployed runner** | **OPEN** |

The recurring theme across F6, F11, F16, F17, F20 and F25 is **silent or
distant failure**: nearly every mistake I made produced either a fixed opaque
string, a status with no reason, or a plausible-looking empty result. The
single highest-leverage platform investment is not a new capability — it is
making failures say what went wrong, where the user can see it.

The most valuable *agent-quality* results are F29 (write the record early —
this alone converted a total loss into a correct diagnosis), F30/F34/F35
(precision wording suppresses recall, and only a graded fixture reveals it),
and F26 (retrieval is far ahead of verification: the reviewer finds the right
code and misjudges what is wrong with it).

## Findings

### F1 🔴 The guide never says what bytes a typed snapshot must contain
`fly agent snapshots create --type X --from dir` is the documented entry point,
but neither guide documents the required content layout per type. You have to
read Go source to learn that `work-item/v1` means a `work-item.json`
(`WorkItemDocument`, schema_version exactly `"1.0.0"`, six required fields),
that `repository/v1` must be an actual **git repository** (the validator runs
git against `HEAD` — a bare exported tree fails), and that record-bearing types
need the full `record.json` envelope. A "content contract" section per type —
or `fly agent snapshots explain-type X` — would remove the single biggest
onboarding wall.

### F2 🔴 The shipped sample node's prompt contradicts the review/v1 contract
`bench/nodes/code-review/prompts/review.md` instructs severities
`observation|minor|major|critical`, but `contracts/review.go` accepts
`observation|low|medium|high|critical`, requires high/critical to be
`blocking: true`, forbids blocking observations, and ties `conclusion` to
blocking findings. An agent that follows the sample prompt to the letter
produces a record that fails sealing. The severity vocabulary and
blocking/conclusion coupling exist only in Go source, not in any guide.

### F3 ⚪ `repository-change/v1` appears impossible to create from the CLI
Its seal gate (`AdmitForSeal → verifyAgainstBase`) requires the base
`repository/v1` to be a *bound declared input* so lineage can be verified —
but `fly agent snapshots create` has no way to bind inputs. Reasonable design
(changes should be produced by nodes), but it means a user who has a diff in
hand cannot enter it as `repository-change/v1`; the practical fallback is
`opaque/v1`. Guide says nothing about this asymmetry. (Verified empirically
below.)

### F4 🟢 Node-level runs are genuinely first-class
`fly agent nodes run NAME VER --input port=ID` with no wrapper workflow is
exactly the right granularity for iterating on a single agent step. Import →
release → run took under a minute once inputs existed, and import correctly
allocated the next version (2) instead of clobbering.

### F5 🔴 BUG (fixed in this branch): fly's snapshot tar dialect was rejected by the server
`fly agent snapshots create` wrote directory tar headers with a trailing `/`
([agent_snapshots_tar.go](fly/commands/agent_snapshots_tar.go)), while the
server's `validateArchivePath` ([archive.go:935](agent/snapshot/archive.go:935))
rejects any trailing separator and its own canonical writer emits slashless
directory entries. Net effect: **creating a snapshot from any directory that
contains a subdirectory failed** with `400 invalid_archive`. Every prior proof
had used flat dirs, so this shipped unnoticed. Fixed client-side in this branch
(drop the `+= "/"`); test updated.

### F6 🔴 Error responses strip every actionable detail
The snapshots API deliberately maps all failures to fixed strings
(`writeSnapshotError` in [handler.go](agent/api/snapshots/handler.go) even has a
comment admitting a debugging session died on this). From the CLI you get
`snapshot archive is invalid` / `snapshot does not satisfy its declared type`
with no cause; the real reasons (`archive path ".claude/" has a trailing
separator`, `repository .git directory is required`, `exec: "git": executable
file not found`) only exist in the web pod's log. A first user without kubectl
cannot self-serve past *any* create mistake. Suggestion: validation failures
are the user's own input — echo the contract error verbatim (or behind a
`--debug` server capability), keep opaque messages for internal faults.

### F7 🔴 P0 PLATFORM GAP: the deployed web image has no `git`, so the whole
repository-typed family cannot seal
`repository/v1` and `repository-change/v1` validators exec `git` inside ATC
([repository.go:568](agent/snapshot/contracts/repository.go)); the live
`registry.home/jetbridge:latest` web container has no git anywhere on PATH
(verified in-pod). So on the current production image: no repository snapshot
can be created, and by extension the flagship seeds (`code-review-v3`,
`small-fix-v3`, `version-upgrade-v3`, `merge-delivery-v3`) cannot run — their
signatures all carry `repository/v1`. The Dockerfile needs git (and the seal
path needs a CI test that runs on the shipped image, not the dev host where
git happens to exist).

### F8 ⚪ `repository/v1` (git-repo-required) vs leak-safe materialization
The corpus deliberately materializes pre-state with `git archive` (no `.git`,
so no descendant refs to leak), but the `repository/v1` contract *requires* a
git repo with `HEAD`. A synthetic single-commit repo bridges the two — but then
`head_sha` is a synthetic SHA, which will matter the moment something
cross-checks it (e.g. repository-change `base_sha` must equal base repo HEAD).
Bench-harness materialization needs a documented recipe here.

### F9 ⚪ Node parameters become env vars — sample prompt implied interpolation
`CompiledNodeDefinition.Instantiate` puts parameters into the agent step's env.
The original sample prompt said `${MINIMUM_SEVERITY}` as if the platform
interpolated prompt text; it doesn't. Prompts must say "read the
MINIMUM_SEVERITY environment variable". Guide doesn't state the mechanism.

### F11 🔴 A failed node run hides its reason three hops away
`nodes runs` says `failed`. `nodes show-run --json` adds hashes and IDs but no
error. The actual reason only surfaces via `fly watch -b <planned_build_id>` —
a field the guide never mentions doubles as the only debugging handle, and it
drops you into classic-Concourse build-land. `show-run` should carry the
terminal error line (here it would have been one string).

### F12 🔴 Version skew: shipped node samples + current runner vs deployed agent-runner image
First real agent dispatch failed with `error: unknown option '--max-budget-usd'`
— the runner ([runner.go:496](agent/runner/runner.go:496)) passes the flag for
any positive `budget_slice_usd`, but the deployed `registry.home/agent-runner`
image's Claude CLI predates it. So the guide's own examples (which all carry
`budget_slice_usd`) fail on this deployment. This is the third
runner-image-staleness incident class in this project's history (cf. runbook
A0-1). The runner should probe CLI capabilities (or the image build should be
version-locked to the runner code that will exec into it).

### F13 🔴 The live view of a working agent is still raw JSON events
`fly watch -b <build>` (and the build page) streams the Claude session as raw
single-line JSON blobs (`{"type":"system","subtype":"init",...}`). This is the
same P0 called out by UX audits №4 and №5, unchanged at the node-run surface.
For node iteration what's missing most is a human view of: current turn count,
last tool call, and tokens/cost so far.

### F14 ⚪ The `output-builder` MCP server reports `status: "failed"` at agent
session init
Seen in the session-init event of the first agent dispatch that reached
Claude. Open question at time of writing whether it's cosmetic or a real
capability loss; either way a platform-owned sidecar reporting `failed` at
init should fail the run loudly or not be configured at all, not scroll past
inside a JSON blob.

### F16 🔴 Fail-closed hermetic egress + no preflight = every agent node dies
a 5-minute silent death
The chart defaults `networkPolicy.hermeticEgressTo: []` (documented,
reasonable). But on a deployment where nobody set it, the *first* agent
dispatch hangs 5 minutes and fails with `"Request timed out"` — visible only
deep in the JSON event stream; `show-run` just says `failed`, zero cost, zero
tokens. Nothing at import, release, run-create, or even pod-start time warns
that the model endpoint is unreachable by policy. The web node could preflight
this (it knows both the flag state and that the plan contains `agent:`) or the
runner could fail fast on the first connect error instead of a full timeout.
Fixed on this deployment via home-infra `8dc7550` (DNS + non-RFC1918 HTTPS).

### F15 🟢 `nodes show --json` compiled view is excellent
Full parameter list, expanded function with `hermetic: true`, per-port
`input_types`, and the leaf plan — exactly what you need to confirm what the
compiler actually captured from your source directory.

### F17 🔴 The guide's `model: claude-sonnet` is not a real model, and nothing
validates the model string before dispatch
With egress open, the next run failed instantly: `API Error: 404 model:
claude-sonnet`. The model value is passed through unvalidated at import,
release, and run-create; you spend a pod launch + budget round-trip to learn
the model name is wrong, and the error again lives only in the JSON stream.
Both guides use `claude-sonnet` in their examples. CLI alias `sonnet` works.
At minimum the docs need real values; better, the platform should validate
model identity at import (it freezes the model into the node — a frozen
invalid value is permanent).

### F18 🟢 The false-`compatible` guard works
I accidentally released the wrong version (a structurally different node) with
`--compatibility=compatible` and the server refused with 422, exactly as the
guide promises ("The server rejects a false compatible declaration"). The
correct successor then released cleanly. Papercut: the error names neither the
predecessor version it compared against nor the offending port, which made a
user error look like a platform bug for ten minutes.

### F19 ⚪ Version numbers are shared, racing state — scripts must parse
import output
An unrelated actor imported the guide's example node under the same name
(`code-review`) between my imports, so "my next version" was 5, not 4, and my
scripted `release 4` hit their node. Any automation must use the version
echoed by `import`, never a predicted one. (A `--json` output for `import`
would help; today it's a prose line.)

**Corrected while fixing this (2026-08-01):** the sequence is **per node
name**, not team-global — `atc/db/agent_nodes_factory.go:106-110` allocates
`MAX(version)+1` scoped to the name, under a per-name advisory lock. And
predicting N+1 is wrong even with no concurrency at all, because import is
**content-hash idempotent**: re-importing unchanged content returns the
existing version without bumping. Fixed by adding `--json` to `import` and
`release`; the release response needed a wrapper because `workflow.NodeRelease`
carries no name or version of its own.

### F20 🔴 BUG (fixed in this branch): the output-builder MCP server never
implemented `initialize`, so no MCP client could ever connect
The managed output-builder sidecar is the platform's chosen mechanism for
record authoring on the runtime-image path (env-var authority is deliberately
NOT provided there). But its `/mcp` adapter
([adapters.go](agent/outputbuilder/adapters.go)) only handled
`tools/list`/`ping`/`tools/call` — the mandatory `initialize` handshake
returned `method not found`, so the Claude CLI registered the server as
`status: "failed"` on every single agent run. The agents were left with no
working mechanism at all: MCP failed, env vars absent by design, and the
runner's fallback prompt block suppressed (it only renders when the builder is
off). Verified live by curling the sidecar from inside a running pod
(`tools/list` worked; `initialize` → `-32601`). Fixed in this branch with a
handshake + notifications handling and a pinned test; needs an agent-runner
image rebuild to reach the cluster. Interim: node prompts drive the builder
over plain HTTP with curl, which always worked.
Lesson for the platform: the MCP adapter was clearly only ever tested with
direct `tools/list` POSTs, never a real MCP client handshake — a smoke test
that connects with the actual claude CLI would have caught this and F12.

### F21 🔵 Node prompts must not hardcode the output mechanism
The runner *prepends* its own mechanism instructions to the node prompt (an
"use the output builder tools" block when the builder is on; a "Sealed record
authority" values block when it's off). My first prompt hardcoded the env-var
mechanism; on the builder path those vars don't exist, and the agent
faithfully wrote empty strings into `record.json` ("invalid type reference"),
or wrote nothing. The right split: node prompt owns the *semantics* (what a
good review/diagnosis is, contract vocabulary, sorted ids), platform blocks
own the *mechanics*. Prompts should defer: "use the platform-provided output
mechanism described above this prompt".

### F22 ⚪ No `fly agent nodes cancel` — aborting means dropping to builds
To kill the two doomed in-flight runs I had to fish `planned_build_id` out of
`show-run --json` and use classic `fly abort-build`. Worked (status went
`aborted`→ actually recorded `failed`), but the node surface should own
cancellation the way it owns dispatch.

### F23 🔵 40 turns is not enough for a 4.5k-file review; and agents don't
self-budget turns
Both first real runs burned all 40 turns exploring and never wrote output
(`error_max_turns`, ~$0.6 each). The review agent spent turns on a thorough
authorization pass — good instincts, no output to show for it. Nodes now run
`max_turns: 80` and the prompts say "reserve your final turns for writing the
output". A platform-side improvement worth considering: warn the agent (inject
a user turn) when N turns remain, the way the CLI warns about context.

### F24 🟢 FIRST SEALED OUTPUT — the loop closes
`code-review@7` on corpus case **review-jb-004** succeeded end to end:
snapshot 8, `review/v1`, sealed, downloadable, and it round-tripped through
`reviewgrade`. The whole path works — typed inputs → node version → hermetic
agent pod → validated record → sealed snapshot → out-of-band grading.
(Corpus commit: this branch's tree; web image `844f9495a9`; node hash
`6f04416490cf`.)

### F25 🔴 BUG (fixed in this branch): reviewgrade silently scored a sealed
record as 0%
`cmd/reviewgrade` unmarshalled `record.json` straight into a review *body*.
A sealed record nests the body under `body`, so `encoding/json` happily
produced an empty Review — no error — and the tool reported
`candidate recall 0/2`. A correct review looked like a total miss. Fixed
(envelope-aware decode + refuse an empty result); this is the harness's own
version of the platform's silent-failure theme.

### F26 🔵 GRADED RESULT — location without mechanism (the important one)
After the decode fix: **1/2 location candidates, 0/2 confirmed on mechanism.**
- Oracle F1 (caller-writable `pipeline_run_id` → any team's pipeline gets
  archived) — the agent anchored *exactly* the right function
  (`atc/db/pipeline_run_factory.go:485-498` vs oracle `482-498`) but reported
  a completely different defect: a `?`-vs-`$N` placeholder bug it rated
  `critical`. That claim is **false** — squirrel's `sq.Dollar` rewrites `?`
  across the whole query at `ToSql`. The agent asserted a library's behavior
  without reading it.
- Oracle F2 (cost chip stale across build switch, cause in a file the diff
  never touches) — missed entirely.
- Its other finding (`agent-metrics` endpoint leaking other tickets' spend)
  is unmatched and plausible; it needs a human judge, which is exactly what
  the case's rubric says to do.
This is the single most useful signal of the session: **the agent's retrieval
was excellent and its verification was not.** It found the right code twice
over and then reasoned about the wrong property. Prompt-level fix applied to
the node: "before you report a finding, try to disprove it — if it depends on
what a library does with its input, go read that library," plus explicit
severity calibration. Location-matching graders will happily score this as a
hit, which is why `reviewgrade`'s "candidate, not scored finding" rule is
right and must not be relaxed.

### F27 🔵 Exploration expands to fill the turn budget — write the output early
`log-diagnosis@6` on rca-jb-004 spent **all 80 turns** and made **zero**
output attempts (`error_max_turns`, $2.10). Its last words were "Let me verify
that fetchPodNodeName returns the node name and not a pod IP" — it was
*one step from the answer* and had written nothing. The review node survived
the same budget only because reviews converge faster.
The fix that generalizes to any agent node: **instruct a provisional write
early, then refine.** Record writing is idempotent (last successful write
wins), so "write your best current answer now, then keep investigating and
rewrite" converts a total loss into a graded result. Both node prompts now say
this; both are re-running to test it.
A platform-side version of the same idea would be stronger than any prompt:
inject a turn-budget warning near exhaustion, or auto-invoke a "write what you
have" turn before the cap.

### F28 ⚪ Cost visibility exists but only in the raw stream
`total_cost_usd` per run is right there in the `result` event ($0.56, $0.67,
$2.10 for the runs above) but `nodes show-run --json` reports no cost at all.
For iterating on a node, cost per version is exactly the number you want next
to the outcome.

### F29 🔵 CONFIRMED: "write early, then refine" turns a total loss into a
correct answer
Re-ran both nodes with only the prompt changed (write a provisional record
immediately, refine it after). `log-diagnosis@8` on rca-jb-004 went from
*80 turns, zero output, errored* to **succeeded with the correct root cause**:
rank-1 hypothesis at 0.98 confidence naming
`atc/worker/jetbridge/worker.go:364-367` recording an artifact-daemon **pod
IP** into a field that contractually holds a Kubernetes **Node name** — which
is verbatim the withheld answer's "This is the defect", with the right
supporting anchor in `artifact_locator.go:30-34`, plus honest log anchors and
three prioritized actions. One sentence of prompt discipline was the whole
difference between a $2 loss and a correct diagnosis.
**Take-away for node authors:** any node whose agent explores before it
concludes should mandate an early provisional write. Idempotent record
writing makes this free.

### F30 🔵 The same prompt change that fixed one node BROKE the other
The identical iteration (write-early + "try to disprove your finding before
reporting it" + severity calibration) applied to code-review produced
`conclusion: accept` with **zero findings** on a case where two real majors
exist — worse than the previous version, which at least found the right code.
The summary is a confident, well-written account of four review angles that
"came back clean", including the very endpoint the earlier version had
flagged.
The lesson is sharper than "prompts are finicky": my precision instruction
had no floor, so *disprove before reporting* collapsed into *do not report
what you could not prove*, and an agent that cannot fully confirm a suspicion
in its turn budget reports nothing. Rebalanced to: verification decides a
finding's **severity and wording, never whether it is reported**; an
unresolved suspicion must still be reported at the severity the evidence
supports; never conclude `accept` merely because nothing reached certainty.
**Take-away for the platform:** node iteration needs A/B, not sequencing —
this regression was only visible because the same fixture was re-run and
graded. `agent/experiment` cannot target a node today
(`TargetKind` is workflow|function), so every node author has to hand-roll
what the experiment subsystem already does. Adding `TargetNode` would make
this loop first-class.

### F32 🔵 Scored against the judge rubric, the diagnosis beats the human
baseline on provenance
Hand-scoring `log-diagnosis@8`'s record against
`rca-jb-004/ground_truth/rubric.md` Part A (the diagnosis half, 60%):
- **A-1 type confusion (25%) — full.** Names both halves the rubric requires
  (a pod IP, in a field that holds a Node name) *and* the write site
  `artifactLocator.Record(cacheKey, daemonIP, cacheKey)`. The rubric scores
  zero for answers that stop at `NodeIPResolver.Resolve` — this one explicitly
  distinguishes where the error is printed from where the bad value is made.
- **A-2 read-back seam (15%) — full, with the bonus.** Traces
  `WrapVolumeForLookup` → `DaemonSetVolume.sourceNode` → `Resolve`, and its
  recommended fix cites `NewDaemonSetVolumeFromIP` as the branch that exists
  precisely to skip the resolver — the rubric's listed bonus.
- **A-4 provenance (10%) — strong.** Separates the change that *introduced*
  the bad write from the 18 April change that made it *reachable*, which the
  rubric says "exceeds the human baseline". (Its introduction date is a week
  off the human answer of record; the rubric explicitly does not require SHA
  archaeology when the change is described by content.)
- **A-3 observed pattern (10%) — the gap.** It never explains why only
  cache-*hit* builds fail or why wiping the daemon directory buys exactly one
  good build. It offered a single hypothesis and no counterevidence.
Roughly 50-52 of 60 on the diagnosis half, with a clean, specific miss to aim
the next prompt iteration at: require the record to account for the *observed
pattern*, not only the mechanism.

### F33 ⚪ A node is a signature subset of a corpus case, and nothing says so
rca-jb-004's signature declares **two** outputs (`diagnosis/v1` *and*
`repository-change/v1`); my node produces only the diagnosis, so the case's
Part B is simply unscored. That is a legitimate way to work — start with one
output, add the second later — but nothing in the platform or the harness
records that a run covered a *subset* of the case. A result that says
"52/60 on Part A" and a result that says "52/100 overall" are very different
claims, and today only a human's memory distinguishes them.

### F34 🔵 Adversarial-precision wording suppresses recall, and the effect is
stable across versions
The rebalanced prompt (v10: "disproving means checking, not dropping", explicit
"never conclude `accept` merely because nothing reached certainty") produced
**the same `accept`, zero findings** result as v8. Two consecutive versions,
same fixture. Worse, v10's summary asserts the change "correctly leverages the
1:1 ticket↔template relationship (`agent-ticket-<id>` naming)" — treating a
naming convention as an authorization guarantee, which is exactly the reasoning
the case is built to punish.
So the floor clause did not undo the damage: once a prompt frames the reviewer's
job as *surviving scrutiny*, the cheapest way to satisfy it is to find nothing.
v11 drops the disprove framing entirely and re-expresses the same intent as
**severity carries confidence** — report the concern, let the rating encode how
well you verified it — plus "treat a naming convention, a comment, or a test as
a claim to verify, never as proof". Running now.
**Generalizable:** precision and recall instructions are not independent knobs
in a prompt. Language that rewards caution costs recall asymmetrically, and you
cannot see it without a fixture whose answer you already know. This is the
strongest argument in the session for `TargetNode` in `agent/experiment`: every
node author will make this exact mistake, and only a graded A/B reveals it.

### F35 🔵 RESULT of the controlled iteration: recall came back, mechanism
accuracy did not
Dropping the disprove framing (v11: *severity carries confidence*, plus "treat
a naming convention, a comment, or a test as a claim to verify") restored
reporting immediately — `changes-required`, 2 findings, back to 1/2 location
recall. So the suppression was caused by the framing, and it is reversible.
But the mechanism problem survived every variant:

| ver | prompt change | conclusion | location recall | mechanism confirmed |
|---|---|---|---|---|
| v7 | baseline (mechanism-agnostic output) | changes-required | 1/2 | 0/2 |
| v8 | + write-early, + disprove, + severity | accept (0 findings) | 0/2 | 0/2 |
| v10 | + "disproving means checking not dropping" | accept (0 findings) | 0/2 | 0/2 |
| v11 | disprove replaced by severity-carries-confidence | changes-required | 1/2 | 0/2 |

Four runs, three different *wrong* mechanisms proposed at the right code:
a nonexistent squirrel placeholder bug (v7), then in v11 an `instance_vars IS
NULL` mismatch — again inside `TemplatesForTerminalTickets`, again not the
caller-writable-id defect the oracle names. v11 also spent a finding on
**precision trap N4** (client-side float summing of costs), which the oracle
pre-registers as "nit-at-most"; it rated it `medium`, so severity calibration
partly worked even as the trap landed.
**Conclusion I'd hand to the platform team:** for this node the bottleneck is
not prompt wording, and no further prompt tuning is warranted without changing
what the agent can *do*. Both required findings need the agent to leave the
diff and read a *write path* (`agent/api/tickets` for F1) or a file the diff
never touches (`Header.elm` for F2). Candidate next moves, in order: give the
review node a capability sidecar with code-intelligence (find-references /
call-hierarchy) instead of grep; or decompose into two nodes — an
`impacted-surface` node that enumerates writers/callers of everything the diff
touches, feeding a review node that must anchor into that surface. That is a
node-composition experiment, which is exactly the kind of thing this
node-first exercise was supposed to surface.

### F31 ⚪ Two nodes, two very different failure profiles
Worth recording as the reason to build more than one node before generalizing:
the diagnosis node's problem was **stopping** (it explored forever and never
committed), the review node's problem was **judgment** (it committed
confidently to a wrong mechanism, then over-corrected into silence). Diagnosis
work has one answer and rewards depth; review work has an open-ended finding
set and rewards calibration. A single "agent node" prompt template will not
serve both — but the *contract* scaffolding (envelope, anchors, sorted ids,
write-early) is identical and should be platform-provided, not re-authored
per node.

### F10 ⚪ Release lineage only counts *released* versions
Node v1 existed (imported by an earlier session) but was never released;
releasing v2 as `--compatibility=compatible` succeeded even though v2's port
types differ incompatibly from v1's. Correct per the doc ("first release
establishes the lineage") but worth knowing: imported-but-unreleased versions
are invisible to compatibility checking.
