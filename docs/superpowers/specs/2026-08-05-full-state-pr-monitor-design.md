# Full-State PR Monitor — design

**Date:** 2026-08-05
**Status:** Draft — awaiting review. Both previously-open decisions are resolved below.
**Supersedes (in part):** the action-derivation half of `2026-07-29-provider-native-pr-publish-design.md`.
**Prior art:** [PR interface cleanup audit](../plans/2026-08-05-pr-interface-cleanup-audit.md).

## Objective

Make the agentic PR loop reachable, cheap enough to run, and shaped so the *next* watched source type (a Jira ticket, a review queue) costs a resource plus a snapshot type rather than a second `agent/pullrequest`.

The governing idea: **the forge is the state store.** Today's design mirrors "what has been handled" into `agent_pr_bindings` via an opaque provider cursor, and that mirror is what generates the action-derivation machinery, the digest suppression, the acknowledgement dispatch and the config churn. Pass the agent the complete current state of the PR and it can decide what to do by reading it — the same way a human reviewer does.

## What was refuted along the way

Two designs were attacked before this one. Recording why they failed, because the constraints they exposed bound any future design.

**Refuted: "check emits facts, the server derives actions."** 23 fatal findings. Two are structural:

1. **`Observe` is a consuming delta.** `agent/pullrequest/github/observe.go:121-131` advances the cursor watermark past the batch it returns, in the same call. `observe_test.go:91-104` proves it: `Observe(c)` twice with an unchanged cursor is idempotent, but `Observe(first.Cursor)` can never return the first batch again. Any design where the cursor comes from the previously *emitted* version, rather than a server-acknowledged one, silently drops review batches forever.
2. **Concourse consumes a version at build *start*, regardless of outcome.** `AdoptInputsAndPipes` (`atc/db/build.go:1470-1540`) writes `build_resource_config_version_inputs` before the plan runs; `NextEveryVersion` (`atc/db/versions_db.go:276-355`) and `IsFirstOccurrence` (`:37-57`) have no status predicate; and the server only ever reads `status='succeeded'` builds (`atc/db/agent_workflow_resource_source_builds.go:123-149`). A failed `admit` build burns its version permanently.

Today's design survives (2) **only because of** the frozen acknowledged cursor: the next check re-derives the same unacknowledged batch and re-emits it as a new, higher-`check_order` version. **The pushed cursor is the retry mechanism, not merely a cursor.** That is the single most important thing to know before touching this subsystem.

**Also refuted:** freshness via `EnsureManualBuild` + `RequestPostDispatchChecks` — structurally closed to binding-scoped pipelines at five layers, and a manually-triggered build on a binding source pipeline is a cluster-wide poison pill that aborts the reconciler tick for every other instance (`agent/workflowrun/source_build_reconciler.go:252`, unbatched `ORDER BY source.pipeline_id`).

## Architecture

### 1. Full-state observation

`Observer.Observe` returns the complete current PR state rather than an unacknowledged delta. No provider cursor semantics.

**This is a deletion, not an addition.** `Observe` already fully paginates reviews and comments (`observe.go:252-278`, `:280-306`, `per_page=100`, Link-header following). The delta is computed client-side afterwards by `selectReview`/`afterWatermark` (`:308-341`) picking one review. Full state means deleting that selection, looping `normalizeReview` over every submitted review, hoisting thread grouping out so `normalizeThreads` runs once over all comments, and sorting the merged lists.

**It fixes a live bug.** `normalizeThreads` builds its root index only from the current review's comments (`:374-383`), so a reply in review B to a root comment from review A cannot resolve and `Observe` fails permanently with `"github review reply has no root"` (`:391`). GitHub assigns a reply's `pull_request_review_id` to the *submitting* review, so any threaded back-and-forth across two reviews bricks the monitor today. Enumerating all comments in one pass removes the class.

**Cost is unchanged** — the same three endpoints, already fully paginated.

### 2. The agent decides

The workflow passes full state to the agent. No `ActionFor`, no `action_kind`/`action_digest`, no trigger policy in the resource. `agent/pullrequest/triggers.go` (173 src / 207 test) is deleted.

`body.Trigger` survives as the response-authorization gate (`agent/snapshot/contracts/pull_request_response.go:114-149`) but is computed deterministically in `in` from full state — "there exists a human-authored thread with no subsequent agent reply" — rather than from a cursor delta. **The response must additionally name a batch that exists in the sealed observation**, which restores the safety property the positional `ReviewBatches[0]` assumption provided.

### 3. Self-caused skip guard

After publishing, the platform records a digest of the resulting state as self-caused — **but only if** the observed item set equals (items read at run start ∪ items published). If anything else changed, record nothing.

> **Invariant: the guard fails toward re-running, never toward skipping.** A false "not self-caused" costs one agent run. A false "self-caused" loses a human's comment, possibly permanently.

Three corrections the probe forced:

- **The digest must be derived server-side** at acknowledge time from the run-start observation plus `MonitorRunEvidence.Publications`. Nothing observes after publishing today, and a workflow-supplied digest would let a buggy agent silence its own future checks.
- **Attribution goes through the operation marker in the comment body** (`agent/pullrequest/github/mutate.go:953-970`), not the author field. The platform cannot identify its own comments by author: there is no `GET /user`, no configured bot identity in the resource `Source` (`resource/protocol.go:80-91`), and the token is opaque (`httpclient.go:11-13`).
- **`GET /repos/{r}/issues/{n}/comments` must be added to the observer.** The platform *writes* PR conversation comments (`mutate.go:453`) but never reads them, so a human replying in the conversation is invisible — the set-equality test would hold and the guard would wrongly record a skip. That directly violates the invariant above.

Store as `self_caused_digests JSONB` on the instance row, bounded, retaining 6–8 (two runs' worth of publication prefixes) with a hard cap of 16. Clear on any non-acknowledge revision — `MarkAttention`, operator resume, observation request — so an operator asking for a run gets one.

### 4. Retry without a new version

Because a failed `admit` build burns its Concourse version, retry is server-side: the captured `pull-request/v1` snapshot is durable, so a failed or freshness-due run is re-launched from the **same snapshot**, needing no new build or version. A second run from one admission is not structurally forbidden — the generic retry path already requires it.

This also removes the need for `binding_revision` in the version, and removes the freshness-via-manual-build path entirely.

### 5. Digest scope — **revised against measurement**

| Field | In digest? | Why |
|---|---|---|
| Threads, comments, review states | **yes** | the signal the feature exists for; over the activity-ordered window (RESOLVED-1) |
| Head SHA | **yes** | a push is a real change |
| Target SHA | **no** — *revised* | see below |
| Mergeability | **no** — *revised* | see below |
| CI check runs | no | JetBridge is the CI |

Two revisions against the earlier answer ("threads, new commits, mergeability"), both forced by measurement:

**Target SHA is the cost bomb.** Measured base-branch rate on this repo is mean ~44 commits/day (p90 ~115, max 173 on 2026-07-11). At a 5-minute poll that mints ~30–70 versions/day **per watched PR regardless of whether the PR is active** — 40 idle PRs would generate ~1,400 `admit` builds/day for no semantic reason. Base movement almost never requires agent action: if the merge now conflicts, that surfaces independently; if it needs re-testing, that is a CI decision JetBridge owns. Excluded from the digest; base-movement re-evaluation is driven by the existing freshness interval instead.

**Mergeability re-admits CI through the back door.** `normalizeMergeability` (`observe.go:242-245`) maps GitHub's `mergeable_state` `blocked`/`unstable`/`draft` to `PolicyBlocked` — and `mergeable_state` is a function of commit statuses, which **the platform itself POSTs** (`mutate.go:200-203`). So every JetBridge build flips `clean → unstable → clean`, i.e. `Mergeable ⇄ PolicyBlocked`, creating a self-triggering loop. "CI check runs are excluded" is false while mergeability is in the digest. Sticky-Unknown does not save it: those transitions are not `Unknown`, so stickiness never touches them, and where it does apply it asserts a stale `Mergeable` the agent then acts on. Excluded; conflict is surfaced to the agent as observed state, not as a trigger.

## Cost model

Measured per `admit` build, because `in` performs two full, unshared, unshallow, unfiltered clones (`resource/in.go:110-159`; shallow rejected at `dependencies.go:99-109`, alternates rejected at `:114`), then `git fsck --full --strict --no-reflogs` per checkout, inside `repository/v1` resealing (`agent/snapshot/contracts/repository.go:146`):

**352 MiB fetched, 514 MiB on disk, ~21s CPU (5.7s of it `fsck`), 25–45s wall.**

| | builds/day @ 50 PRs | git egress | volume churn | agent cost/day |
|---|---|---|---|---|
| Digest as first proposed | ~3,000 | ~1.0 TiB | ~1.5 TiB | ~$1,560 |
| Digest as revised above | ~460 | ~158 GiB | ~231 GiB | ~$235 |

Agent cost uses the measured $15.64/agent-hour. **The first row is disqualifying** — and worker disk / volume GC breaks before anything else, in a deployment whose registry PVC has already filled to 241 GB once.

Three further cost fixes are prerequisites, not nice-to-haves:

1. **Fix `in` materialization**: one fetch into a shared object store with two worktrees, or `--filter=blob:none`; drop `fsck --full --strict --no-reflogs` (`agent/snapshot/contracts/repository.go:146`) from the per-build path, or scope it to first materialization only.
2. **Delete the `in.go:88-95` exact-version equality gate.** It errors `"selected version is stale"` whenever the PR moved between `check` and `get` — and burns the version anyway. It is a live defect incompatible with any queued execution model, and `Serial: true` guarantees a queue (`EnsurePendingBuildExists` collapses only the *pending row*; `version: every` still forces one build per version).
3. **Add ETag/`If-None-Match`** to `github/client.go:56-80`, so 288 polls/day/PR mostly cost 304s against the 5,000/hr ceiling.

## Structural changes retained from the earlier sections

- **Generic source instances.** `agent_pr_bindings` → `agent_workflow_source_instances` (~18 columns, not 45); `pr_binding_id` → nullable `source_instance_id`. One store surface, no binding-scoped twins (~370 LOC), no `pr_binding_id` predicates in ten executing queries.
- **One pipeline per PR**, preserving per-PR isolation and independent pause/terminate.
- **`resource_sources:` canonical** — `pr-monitor-v3` becomes its first production user.

Three carried blockers must be handled in the plan: the per-instance config hash must be produced by the *same* render function the generic path recomputes; `origin_kind='pr-monitor'` is hard-coded in five SQL statements; and `BindPRMonitorAuthority` must remain reachable or the seed's typed sentinels strand (it fails closed, but every run fails).

## Resolved decisions (were OPEN)

**RESOLVED-1 — observation window.** A *thread* is one root review comment plus its reply chain, or a synthetic `review-<id>-body` thread per review with body text (`observe.go:365`). Full state on a long-lived PR therefore accumulates without limit, against `maxThreads = 512` (`types.go:24`) and `maxPullRequestThreads = 512` (`contracts/pull_request.go:108`) — which **reject rather than truncate**, and `observe.go:140-142` turns rejection into a permanent `Observe` failure.

**Decision: an activity-ordered window, not a raised bound.** Sort threads by last activity and emit the most recent N (default 150), with an explicit truncation marker in the record so the agent knows its view is partial. Activity-ordering is the safety property: any thread receiving a new comment sorts into the window by construction, so a human replying on an ancient thread is always visible. At N = 150 neither bound binds and no schema revision is needed.

Note the deciding argument is *not* the schema. Raising the bound is cheap — `record_schema.go` models revisions as `current` + `superseded[]`, append-only, and `pull-request/v1` has already gone rev2 → rev3; the data-loss hazard is rewriting a frozen descriptor in place, not adding a revision. The window wins because **the agent is the consumer**: handing an LLM every thread a PR ever had costs tokens and degrades its judgement.

The skip guard's set-equality is defined over the window. Because the window is activity-ordered, an item that changed is always inside it, so the comparison cannot silently exclude a human change.

**RESOLVED-2 — thread resolution stays invisible; no GraphQL.** Resolution state is unavailable from the REST endpoints used (`observe.go:253`, `:281`). This is a blind spot, not a fail-toward-skipping bug: resolution is not an item in the set-equality comparison, so it cannot make that comparison falsely true. The one consequence is that the "human thread with no agent reply" trigger still fires for a thread a human resolved to mean *never mind*, so the agent replies once to withdrawn feedback and the skip guard then settles it.

Human-triggered merge needs nothing extra: PR state `active → completed` comes from `GET /pulls/{n}` and is already carried in the observation.

## Testing

- **Unit/integration:** full-state `Observe` against the existing fixtures, extended with a cross-review-reply case (currently a hard failure), a window-truncation case asserting the truncation marker, and a case proving a new comment on an old thread sorts *into* the window.
- **Server path:** fake-forge integration test driving promotion → admit build → capture → admission → run → acknowledge → next check skips, as the CI gate.
- **Live proof (acceptance):** deploy to theborg, drive one PR end to end on a throwaway repo — agent opens it, human comments, monitor observes, workflow revises and replies, instance acknowledges, next check does *not* re-run. This is the only evidence that credentials, Helm, image pinning and the real GitHub API work; `PR_PUBLISH_LIVE_PROOF.md` still reads "Current result: not run."

The `len(ReviewBatches) == 1` consumers (`revision_executor.go:592`, `:693`, `:700`, `:705`; `monitor_run_inspector.go:310`, `:503-504`) must be rewritten to select by `response.BatchID` and re-tested — mechanical, but it moves batch authority to the agent's named batch, which is why the "must exist in the sealed observation" check in §2 is load-bearing.

## Sequencing

1. Fix `in` materialization cost + delete the equality gate + ETags. *(prerequisite; independently valuable)*
2. Full-state `Observe` + rewrite the `len==1` consumers. *(fixes the cross-review bug)*
3. Generic source instances + collapse store twins.
4. Skip guard, server-derived, with the issue-comments read.
5. Server-side re-launch; delete `triggers.go` and the freshness-via-manual-build path.
6. `resource_sources:` declaration + mixed inputs; delete `LaunchMonitor`.
7. Close the four authority-spine gaps; remove the boot gate; live proof.

The boot gate (`incompletePRAuthoritySpineError`) stays on until step 7 and comes off immediately before the live proof, not one commit earlier.

## Related defect, tracked separately

`LaunchMonitor` sets `ResourceSourceAdmissionID` for monitor runs that declare no `resource_sources`, so the *first* launch (`created=true`) fails in `mergeStoredSourceInputs` → `ResourceSourcePipelineTargetFor`. Latent, not live — `LaunchMonitor` has no production caller — but it would fire the moment the feature is enabled. The only monitor binder test covers `created=false` and short-circuits. Fix independently with a `created=true` regression test.
