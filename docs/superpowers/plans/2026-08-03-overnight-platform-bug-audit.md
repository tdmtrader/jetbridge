# Overnight platform bug audit and remediation — 2026-08-03

Scope: reproduced defects only — behavior contradicting the code's evident intent, a
cross-layer contract, or something that concretely blocks using the platform. Feature
gaps, shortcomings, and cleanup were out of scope by instruction.

Base: `jetbridge` at `5240e3341a`. All work is committed locally and **not pushed**,
because a push starts the self-build/release chain unattended.

## Method

Fourteen independent finders swept the platform, weighted toward the 97 commits that
have never been deployed, plus a strictly read-only inspection of the live theborg
`cicd` deployment. Every blocker/major candidate then faced two adversarial verifiers
with opposing jobs — one trying to refute the code-level mechanism, one trying to refute
that it matters in real use. A finding survived only if both failed to refute it.

48 raw findings → 45 after dedup → 21 verified → **18 confirmed, 3 refuted**. The 20
minor findings were handed to fixers as verify-first claims; all but one held.

Three findings were refuted and deliberately not acted on: the web-image writeback YAML
claim (the real defect there was different and is fixed below), the forge-PR image claim
(the publisher is fail-closed and the path unreachable), and ticket current-run join
scoping (unreachable while every agentic API hard-gates to team `main` — noted as a
latent multi-team landmine, not a present bug).

## What was fixed

25 commits. Every fix carries a regression test, and each fixer mutation-checked its own
tests — reverting the fix and confirming the new test fails with the production symptom.

### Blockers

**The managed output builder was unreachable by the only client that talks to it.**
Its MCP `initialize` demanded protocol version `2024-11-05` and errored on anything
else; the pinned Claude CLI sends `2025-11-25` and would have accepted `2024-11-05` in
reply, but an error makes its client drop the server outright. Compounding it,
`DisallowUnknownFields` was applied to the JSON-RPC params *envelope*, so every real
tool call died on the standard `_meta.progressToken`. The failure was invisible: the
runner's preflight spoke the server's private dialect, passed, and advertised three
tools the agent could never call — the step burned its budget and sealed nothing. Fixed
by negotiating rather than demanding, tolerating the envelope while keeping strictness
on tool arguments, and making the preflight send what the real client sends.

**`capture-resource` could not succeed for any snapshot type** (previously documented as
item 5b). The server generates a capture pipeline whose seal task has an untyped
`source` input feeding a typed output, then rejects it with `every declared task input
must be typed`. A capture task exists precisely to turn untyped bytes into the first
typed snapshot, so its input can never be typed. Fixed with a named, tightly-shaped
exemption for the input direction only — the general rule stays absolute, and a plan
claiming the capture function ID without the exact rendered shape is rejected outright.
The regression test is the missing seam: it renders the real capture config, runs it
through the real planner, and drives the real step to worker selection.

### Truth and visibility

Several fixes converge on one theme — the platform was telling operators things that
were not true.

- A durably **failed run rendered green**: the UI let a fresher `execution_status`
  column override the durable status in the terminal direction. Now worst-truth-wins.
- The **attention lens hid terminal failed runs** whose projection had no
  attention-worthy row, and **await/publish nodes that died before writing their own row
  froze as `pending`** — hiding the node that killed the run. Both fixed, plus the
  freeze/wait-cancel ordering that pinned aborted runs in the lens forever.
- **Every web-side Prometheus metric was absent.** The ServiceMonitor scraped a port
  serving nothing, and the root cause ran deeper: the chart never rendered the
  Prometheus bind flags, so the emitter was never started at all. Wired end to end
  behind a new opt-in, and four of eight alerts that selected nonexistent series were
  rewritten against real ones.
- **`fly intercept` never worked on K8s job steps** — and worse, hijacking a completed
  pod *deleted* it, destroying the exit-status annotation. Fixed both halves.
- One **ERROR line per step** on the ordinary happy path (attach-before-create) trained
  operators to ignore errors; now debug.

### Data and lifecycle

- **Completed pods were deleted immediately**, destroying the annotation that is the
  only mechanism letting a restarted web resume a plan instead of re-running the step.
  Pods now survive until their build stops running, read directly from started builds
  and failing closed. Operator impact: pause-pod count rises during builds, reclaimed
  within one reaper tick after the build finishes; check pods keep the fast path.
- **Migration 1773106158's backfill orphaned every earlier run of a ticket**, adopting
  only the single run its dropped column named. Fixed with a second backfill keyed on
  origin, plus an index that the prefix search can actually use. Both new migrations
  were amended in place rather than stacked, having confirmed neither has been applied
  durably anywhere — that decision is safe now and will not be later.
- **Nested retry closures collided on the occurrence primary key** and were silently
  eaten by `ON CONFLICT DO NOTHING`; the freeze now rejects self-colliding batches.
- **Every reusable-node run failed the freeze**, logging an error on a normal path,
  because the freezer only knew how to read workflow definitions.
- **`await_snapshot` discarded the answer it just waited for** when the wait expired —
  a resolved answer or `on_timeout: default` was lost exactly at the deadline.
- **Agent outputs leaked across a checkpoint replay** inside `retry:`/`across:` scopes.
- **Peer artifact fetches were bounded by a 3-minute whole-request timeout** that
  severed large streams mid-body, deterministically, with all retries restarting from
  zero under the same clock. Replaced with a stall guard.

### Deployment and release

- **Broker workspace-capture endpoints 404'd** — implemented in the handler and auth
  lists but never registered as routes, so every `request_review` child execution
  failed. The full route-consistency checklist is now enforced by an exact-set test, and
  a duplicate-route uniqueness test was added (duplicates silently shadow rather than
  panicking, contrary to the received wisdom).
- **Artifact-daemon TLS CA and resolve key were re-minted on every ArgoCD sync**,
  because `lookup` returns nothing under `helm template`. Documented honestly, with the
  false "persisted across upgrades" claim corrected and a loud install-time warning.
- **The broker image was published only to a cluster-internal address the kubelet
  cannot pull** — the same defect class as the agent-runner incident on 2026-08-02, now
  fixed the same way.
- **Three runtime test images shipped without `git`**, so no K8s tier could exercise
  `repository/v1` validation or the direct-Git publisher — exactly the blind spot that
  let the original missing-git defect ship. The parity guard now reads every copy.
- **Record-body and archive validation failures were opaque**, returning bare
  `validation_failed` for precisely the rules the node-authoring guide tells authors to
  satisfy. Nineteen closed-enum reasons added.

## Live cluster findings (read-only)

Inspection of `cicd` on theborg found an active, ongoing failure that no code change
here addresses:

**All 15 stored agent snapshots are unreadable.** `hangar-fake-gcs` was OOMKilled at its
1Gi limit on 2026-08-02 20:51 PT; since then every snapshot fails exact metadata
revalidation, and the repair pass reports 15 scanned / 15 failed every ten minutes,
indefinitely. The startup pass before the OOM was clean. Two uploads died mid-write
during the kill window. Because `fake-gcs-server` keeps metadata in memory and grows
monotonically, this recurs roughly every two to three days.

Also observed: a stale `concourse` namespace with a pod that has been `ErrImageNeverPull`
for 16 days (108k kubelet warnings) and two PVCs pending for 112 days.

## Decisions left for you

1. **Hangar storage.** An in-memory GCS emulator is the production object store, and its
   OOM is what corrupted the live snapshots. Options range from persisting its backing
   store to replacing it with MinIO or real GCS. Related: should the repair pass
   *quarantine* permanently-corrupt objects rather than erroring forever? That is
   data-destructive, so I did not do it. The 15 corrupt snapshots also need explicit
   cleanup or re-seal — likewise destructive, likewise left alone.

2. **The chart's metrics opt-in changes upgrade behavior.** `serviceMonitor.web.enabled`
   defaults true and now *hard-fails* rendering unless `web.metrics.enabled` is set.
   That is deliberate — it makes the combination that never worked impossible to ship
   silently — but home-infra values need one of the two set before the next chart
   upgrade, or the sync will fail.

3. **The `set-self` config/source skew** (from the earlier follow-ups doc) is still open.
   My preference is stamping the source commit into the pipeline config and asserting it
   in the writeback task, so skew fails closed rather than depending on which half
   happens to be stricter — but I am about 70% on that versus giving writeback jobs an
   ungated `repo`, and the blast radius is the whole release chain.

4. **Archive entry-name disclosure** was excluded from the validation-disclosure work.
   Including the offending path would genuinely help authors, but it is caller-submitted
   and bounded only by a 4096-byte limit, so it is a deliberate widening of the response
   contract rather than a free addition.

5. **The Ubuntu `git` pin** (`1:2.34.1-1ubuntu1.17`) will eventually leave the jammy
   archive and fail four image builds at once. The two Debian-based e2e images take
   unpinned `git` for the same reason. Worth a deliberate pinning policy.

6. **The stale `concourse` namespace** is one `kubectl delete ns` away from silence, but
   it is a live mutation on your cluster, so it is yours to make.

## Verification

`go build ./...`, `go vet ./...`, and `helm lint deploy/chart` are clean on the merged
head. Every fixer ran its own focused suites green, including the full migration suite
(228 specs) and focused DB specs for the projection work. The frontend bundle was
regenerated for the UI changes.

`make test-unit` on the merged head finished with one failure, the same checkpoint expiry
spec that failed on the pre-existing baseline. It turned out to be a defect in the test
rather than flakiness: it compared `upload_expires_at` against `created_at` with a 1ms
tolerance, but the expiry is an hour past `clock_timestamp()` (which advances during the
transaction) while `created_at` defaults to `now()` (fixed at transaction start), so it
only ever held on an idle machine. The assertion now states the invariant it means —
mutation-verified by widening the TTL to two hours and watching it fail. The other
baseline failure, a `cmd/concourse` spec with a one-second timeout, did not recur and
passes on rerun; it is timing-fragile under load and worth tightening independently.
