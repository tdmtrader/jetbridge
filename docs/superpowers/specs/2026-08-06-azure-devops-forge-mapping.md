# Azure DevOps as the second forge

Generated 2026-08-06 by five surveys of the Azure DevOps REST API plus ten
adversarial verification passes -- one documentation-literal and one
practice-reality per area (16 agents, ~3.0M tokens).

Question: how does the disposition-triggered review loop, and the borrowed
change/revision model, map onto Azure DevOps?

Where a verification refuted or downgraded a survey claim, the verification wins
and is marked [REFUTED] / [OVERSTATED] inline. Section 7 lists what neither pass
could settle, with the experiment for each.

---

# Azure DevOps as the second forge for disposition-triggered review

*Synthesis of five API surveys and ten adversarial verification passes. Where a verification refuted or downgraded a survey claim, the verification wins and is marked **[REFUTED]** / **[OVERSTATED]** inline. Unless stated otherwise every REST route is GA at `api-version=7.1` and carries an `azure-devops-server-rest-7.1` moniker (on-prem parity).*

---

## 1. THE HEADLINE

Azure DevOps and GitHub fail in exactly opposite directions, and Azure's failure is the cheaper one to repair. The single consequential structural fact is this: **Azure DevOps has a first-class revision noun and no review noun; GitHub has a review noun and no revision noun.** An Azure `GitPullRequestIteration` is a numbered, individually addressable object that names its own `sourceRefCommit`, `targetRefCommit` and `commonRefCommit` — head, target, merge-base — which is precisely the (base, head) triple ghstack has to synthesize on GitHub out of three parallel refs, and precisely the ordered series of revisions the Gerrit/jj borrowing was meant to supply. That half of the design should not be built on Azure; it already exists server-side. What does not exist is the trigger unit: there is no submitted-review object, no `submitted_at`, no draft/pending batching, no way to bind N comments to one verdict. The disposition survives as a five-valued reviewer vote (`10/5/0/-5/-10`), but the *envelope* must be synthesized by JetBridge, and the comment-only disposition has no completion signal at all. Net: **Azure DevOps is the better host**, because building a review envelope out of a vote edge plus a time window is a bounded, well-understood piece of work, whereas building a durable revision series on top of a mutable branch ref is the thing that forced the Gerrit borrowing in the first place. The second-order argument reinforces it — the live-proven GitHub self-trigger bug (the agent's reply filed under a new review object with non-null `submitted_at`) is structurally impossible here, because votes and comments are different resources on different endpoints with different OAuth scopes and different event ids. But **[OVERSTATED]** on the surveys' stronger phrasing: this is not "immunity by construction with no author filtering." Verification pulled the `git.pullrequest.updated` payload from the docs source and it contains **no actor at all** — `createdBy` is the PR author, there is no `updatedBy`, no `pushedBy`, no iteration, no delta, and no `notificationType`. You still filter by actor; you just get the actor from a follow-up read instead of from the payload.

---

## 2. THE MAPPING TABLE

| Design piece | Azure DevOps native | JetBridge must build |
|---|---|---|
| **Change identity** | `pullRequestId` (stable across force-push, rebase, retarget) and `artifactId` (`vstfs:///Git/PullRequestId/{projectId}/{repositoryId}/{pullRequestId}`). Plus a native external key-value bag GitHub has no equivalent of: `PATCH .../pullRequests/{id}/properties` (`application/json-patch+json`), whose own docs say it exists so "third party services can store additional information on the pull request without maintaining their own storage." | The jj-style agent-owned change-id that outlives one PR. **Do not attempt a commit header** — no REST write path (`Pushes - Create` composes commits from `comment`+`changes` only) and, fatally, **no REST read path**: `GitCommitRef` never exposes raw commit object text, so a header could never be read back without a full clone. Store the change-id as a **string** in the PR property bag (Microsoft's own sample sends `"value": 8` and returns `{"$type":"System.String","$value":"8"}` — **[REFUTED]** the type table's Int32-preservation claim; `System.DateTime` *is* preserved but truncated to ms), and mirror it as a `Change-Id:` message trailer for human/CLI legibility. |
| **Revision / base naming** | `GitPullRequestIteration`: `id` (1..N), `sourceRefCommit`, `targetRefCommit`, `commonRefCommit` ("The first common Git commit of the source and target refs"), `push`, `commits`, `changeList`, `reason`, `oldTargetRefName`/`newTargetRefName`. Iteration 1 = source head at PR creation; subsequent iterations from pushes (documented verbatim on `pull-request-iteration-changes/get`). Revision-to-revision comparison via `$compareTo`. | Nothing structural — adopt `iterationId` as the revision ordinal. But guard four things: (a) `hasMoreCommits` means `commits` is truncated — page the iteration-commits endpoint; (b) `supportsIterations` can be **false**: a PR with >100,000 modified files "won't support iterations… any attempt to create a status for a non-existent iteration will return an error" (`/repos/git/pull-request-status`, applies to Services *and* Server); (c) **[OVERSTATED]** "immutable" is nowhere documented and the object carries a mutable `updatedDate`; (d) **[OVERSTATED]** that force-push/rebase/retarget *mint* iterations — every `IterationReason` value (`push, forcePush, create, rebase, unknown, retarget, resolveConflicts`) has an **empty description** in every api-version; the .NET page shows `[Flags]` with `Push=0` (so `push` is the zero/default value and can never appear in a combination) and omits `resolveConflicts` entirely. Model `reason` as a flag set, fail open on unknown strings. |
| **Review-completed trigger** | **Does not exist.** No review resource, no id, no `submitted_at`, no body, no batching. Nearest signals: a reviewer vote transition, and a subscription filtered to `notificationType=ReviewerVoteNotification`. | The entire envelope. See §4 for the concrete design. |
| **Three dispositions** | Five, on `IdentityRefWithVote.vote` (int16): `10` approved, `5` approved with suggestions, `0` no vote, `-5` waiting for author, `-10` rejected. Richer than GitHub's three. | Collapse policy (a decision, not a mechanism). `10`→approved; `-5`/`-10`→changes-requested (they differ in whether they block); `5` is a genuine **fourth** disposition — it satisfies the approval policy *and* carries work — route it explicitly to "merge **and** answer." `0` is both initial state and reset. Also handle `isReapprove` ("this approve vote should still be handled even though vote didn't change") — do **not** dedupe on `(reviewerId, voteValue)`. |
| **Comment threading** | Threads are first-class; comments are sub-resources with `parentCommentId` (roots = `0`). Documented cap: "up to 500 comments can be created per thread." Anchors via `threadContext{filePath, leftFileStart/End, rightFileStart/End}` with `CommentPosition{line, offset}` — character-level spans, which GitHub cannot express. | Address comments by **`(threadId, commentId)`**. **[REFUTED]** the doc's "IDs start at 1 and are unique to a pull request": Microsoft's own List sample shows comment `id: 1` in all eight threads. Omit `author` on create or you get "An author of a comment cannot be updated" (n=1, `azure-devops-node-api#173`, but the field is listed in the documented request body, so the docs invite the bug). Never write a partial span: `azure-devops-mcp#793` (Bug, fixed client-side in v2.4.0) shows the REST API accepting `rightFileStart` with no `rightFileEnd` and returning 200, after which the ADO web UI throws `TypeError: can't access property "line"` and the whole PR-details region breaks — **the service still accepts the corrupting payload.** Also pin `CommentPosition.offset`'s base empirically: 7.1 says "Starts at 0", 4.1 and 7.2 say "Starts at 1", and MS's own sample (offset 1→13) reads 1-based. |
| **Thread resolution state** | `CommentThreadStatus`: `unknown, active, fixed, wontFix, closed, byDesign, pending` — richer than GitHub's resolved/unresolved (which is GraphQL-only). Set via `PATCH .../threads/{threadId}`. First-class **Check for comment resolution** branch policy can make it blocking. | REST/UI vocabulary translation: UI shows only five options (Active, Pending, Resolved, Won't fix, Closed); UI **"Resolved" == REST `fixed`**, and **`byDesign` has no UI dropdown entry at all**. Prefer `fixed`/`wontFix`. Also: threads with no status omit the key entirely (system threads) — absent ≠ `unknown`. |
| **Anchor survival across revisions** | `pullRequestThreadContext.{iterationContext, changeTrackingId, trackingCriteria}` + server re-projection via `GET .../threads?$iteration={n}&$baseIteration={m}`. `changeTrackingId` is a declared per-**file** identity from the iteration's change list, required on write for iteration-supporting PRs. `trackingCriteria.origFilePath` follows renames. | **[OVERSTATED]** — three corrections that change the design. (1) `trackingCriteria` is **absent from a default listing**: a measured live spike (`jinyeow/ado-pr.nvim` ADR-0002, PR !21121, 21 threads / 11 iterations) found 0 of 21 threads carried it without the params, 3 of 21 with. (2) **Side is a function of the query window, not a property of the thread** — the same threads returned `rightFileStart` populated in a plain list and `leftFileStart` populated under `$iteration=2&$baseIteration=1`. Any sealed position is meaningless unless the `(iteration, baseIteration)` window is sealed with it, and code must read side from whichever field is populated. (3) Tracking is officially **best-effort and known-broken**: Developer Community 700623, four reporters, Microsoft resolution by Pablo Núñez — "We try a best-effort approach to match lines from past iterations with new iterations which doesn't pretend to be infallible… We're not able to prioritize this issue" — Closed / Lower Priority. So Azure does the same heuristic forwarding the design set out to replace; it just does it server-side and silently. **Seal at ingest, never re-derive.** |
| **Declarative resolves** | **Does not exist.** No field on an iteration, push, or commit names the threads a revision resolves. Nothing links a thread to a resolving revision (`lastUpdatedDate` is the only temporal correlate and is touched by unrelated edits). | Build it — but in native slots rather than sidecar storage or prose: the per-thread `properties` bag (writable, appears in both Create and Update request bodies) stamped with e.g. `jetbridge.resolvedByRevision`, plus the thread status as the *kind* of resolution. Caveat: **no Microsoft sample anywhere shows an integration writing thread properties**, so the accepted request shape (raw scalar vs `{$type,$value}` envelope) and any size ceiling are undetermined — spike it. |
| **Self-trigger suppression** | Genuinely better at the *resource* level: a comment write touches no vote and fires only `ms.vss-code.git-pullrequest-comment-event`; a vote fires `git.pullrequest.updated` with the `ReviewerVoteNotification` subscription filter. The comment event payload **does** carry `resource.comment.author.{id,uniqueName}`. | **[REFUTED]** payload-based suppression on the vote path. `git.pullrequest.updated`'s resource is: repository, pullRequestId, status, createdBy, creationDate, closedDate, title, description, source/targetRefName, mergeStatus, mergeId, lastMerge*Commit, reviewers[], commits, url. **No `notificationType`** (it is a subscription filter, never echoed), no iteration, no `pushedBy`, no `updatedBy`. The only actor text is localized prose in `message.text` — do not parse it. Actor must come from a follow-up read of the `VoteUpdate` / `RefUpdate` system thread. Residual vectors: the agent's own push; `isReapprove`; and **branch-policy vote resets caused by the agent's own push** (undocumented whether they emit a vote notification — see §7). One more open hole: **unverified whether system threads themselves fire the comment event** — if they do, the author is `Project Collection Service Accounts`, which passes an author-id filter. Suppress on `commentType == "system"` as well as on author id. |
| **CI / status gating** | Materially better than GitHub: `GitPullRequestStatus.iterationId` ("ID of the iteration to associate status with. Minimum value is 1") — status pinned to a *revision*, which GitHub cannot express because it has no revision. Posting again **appends**; only the latest per unique `context{genre,name}` is shown. `GitStatusState`: `notSet, pending, succeeded, failed, error, notApplicable`. Blocking **Status checks** branch policy with Required/Optional. | Discover the Status policy type GUID at runtime via `GET _apis/policy/types` and template `settings` from a hand-configured instance — the GUID is documented for seven other policy types and **not this one**, and `settings` is a bare untyped `JObject`. Set **Reset conditions explicitly** (docs contradict themselves on the default: `available-pr-status-checks` says push resets to pending, `pull-request-status` tells you to turn reset on). **[OVERSTATED]** the recommendation to pin Authorized identity + iteration: `available-pr-status-checks` warns "Changing the authorized identity or requiring an iteration ID prevents status checks from posting correctly." That page is Services-only and `ai-assisted` — treat as a hazard flag, not gospel, but do not ship those knobs unverified. Use `notApplicable` as the clean "not agent-managed" exemption. |
| **Merge / land** | `PATCH` the PR with `status: "completed"` + `completionOptions`. `GitPullRequestMergeStrategy`: `noFastForward` (default), `squash`, `rebase`, `rebaseMerge` (UI: "Semi-linear merge" — no GitHub equivalent). **Server-side rebase at completion** means the agent often need not rebase-and-force-push at all, avoiding a spurious iteration and a vote reset. `mergeCommitMessage` lets you dictate the trailer. `autoCompleteSetBy` + `autoCompleteIgnoreConfigIds` (optional policies only; "Auto-complete always waits for required policies"). `mergeStatus`/`mergeFailureType` for diagnosis. | Land-retry loop (no merge queue — §4). Never send `lastMergeSourceCommit` on completion: the update doc enumerates exactly what is patchable (Status, Title, Description, CompletionOptions, MergeOptions, AutoCompleteSetBy.Id, TargetRefName when retargeting is enabled) and warns other properties "will either cause the server to throw an `InvalidArgumentValueException`, or to silently ignore the update" — **[OVERSTATED]** in the survey. Always re-read and assert `status == completed`; `vsts-rest-api-specs#134` reports `autoCompleteSetBy` being echoed but not applied, unresolved. Always set `mergeStrategy` explicitly (`squashMerge` is deprecated) and always set `mergeCommitMessage` (the default squash message is **undocumented**). |
| **Event transport** | Service Hooks: `git.push`, `git.pullrequest.created/updated/merged`, `ms.vss-code.git-pullrequest-comment-event`, plus five `git.repo.*` events (**[REFUTED]** the "that is all six" framing). Three consumers, all supporting all events: `webHooks`, `azureServiceBus`, `azureStorageQueue`. Per-subscription `resourceVersion` **payload pinning** — GitHub has nothing like it. `POST /_apis/hooks/notificationsquery` returns the full original event body. | **Poll `GET .../threads` as the source of truth; treat hooks as a latency optimization.** Forced by four independent findings: no actor in the vote payload; no documented ordering in either direction; no HMAC signature (only HTTP Basic and custom headers, and the docs say header values are "viewable by anyone who has access to the service hook subscription"); and — decisive — **[REFUTED]** the "notificationsquery is the recovery path": `troubleshoot` states verbatim "When a subscription is on probation, any new events are lost." Events dropped during a probation window (up to ~36h after any non-408/502/503/504 error) never become notifications and are unrecoverable. Also: `notificationType` is not in the payload, so you need one subscription per type with a distinct receiver URL (or a distinct `httpHeaders` value); and REST-set `publisherInputs` can be invisible/uneditable in the Service Hooks web UI (documented precedent for the work-item publisher's `workItemType` filter) — never let anyone re-save your subscription in the UI. Monitor `SubscriptionStatus` for `onProbation` / `disabledBySystem` / `disabledByInactiveIdentity`. |
| **Durable cursor** | Every state change is materialized as a durable system thread with a typed property bag: `CodeReviewThreadType` ∈ `{VoteUpdate, RefUpdate, ReviewersUpdate, MergeAttempt}` carrying `CodeReviewVoteResult`/`CodeReviewVotedByTfId`, `CodeReviewRefNewHeadCommit`/`CodeReviewRefUpdatedByTfId`, source/target/merge commits. One GET yields an ordered, timestamped, **actor-attributed** history of every vote and every source-branch advance. GitHub has no single equivalent read. | Everything about making it a cursor. **No `since`, no `$filter`, no `$top`/`$skip`, no continuation token, no `$expand`, no documented ETag** on `GET .../threads` — the only query params are `$iteration` and `$baseIteration`. High-water-mark on `publishedDate` client-side (**[OVERSTATED]**: thread `id` monotonicity is a single-sample observation, not documented). Parse defensively — the whole `CodeReview*` vocabulary appears **only in a sample payload**, with no schema and no enumerated value set; GUID formatting is inconsistent across thread types (`CodeReviewVotedByTfId` dashed, `CodeReviewReviewersUpdatedAddedTfId` undashed 32-hex) and key casing is irregular (`...UpdatedByDisplayname`). Also **[OVERSTATED]**: "append-only/immutable" is not documented — threads carry `lastUpdatedDate` and `isDeleted`, and a PATCH exists. And system threads dominate: 16 of 21 in the measured live PR. |

---

## 3. WHERE AZURE DEVOPS IS BETTER THAN GITHUB

**Iterations are a real revision noun, and they delete three pieces of the design.**
`GET .../pullRequests/{id}/iterations?api-version=7.1` returns an ordered series where each element is a self-describing `(sourceRefCommit, targetRefCommit, commonRefCommit)` triple. What that deletes:

1. **The ghstack base/head/orig ref triple** — entirely. GitHub needs it because a PR is a mutable branch ref plus a mutable head SHA with no revision object; Azure records the head, the target, and the merge base per revision, server-side, with a stable small-integer ordinal.
2. **Revision-to-revision diffing.** `GET .../iterations/{n}/changes?$compareTo={m}` gives "revision n vs revision m" (`$compareTo=0` = vs merge base), paged with server-supplied `nextTop`/`nextSkip`.
3. **Base-movement tracking.** A target-branch change is its own iteration with `reason=retarget` carrying `oldTargetRefName` and `newTargetRefName` — both endpoints named, in the ordered series. GitHub mutates `base.ref` in place and leaves a timeline event with no diff boundary. *(Verification note: that retargeting **creates** an iteration is inferred from two conditional field descriptions, not stated. And retarget-by-API is feature-gated: the update doc says `TargetRefName` is patchable "when the PR retargeting feature is enabled.")*

**Revision-scoped CI status.** `GitPullRequestStatus.iterationId` — "Posting status to a specific iteration of a PR guarantees that status applies only to the code that was evaluated and none of the future updates." GitHub commit statuses attach to a SHA and a PR has no revision to hang them off.

**Approval bound to a revision, enforced by the forge.** The minimum-reviewers policy exposes independent booleans `requireVoteOnEachIteration` (UI: "Require at least one approval on every iteration", Azure DevOps Server 2022.1+), `requireVoteOnLastIteration`, `resetOnSourcePush` (approvals only), `resetRejectionsOnSourcePush` (all votes), and `blockLastPusherVote`. **[OVERSTATED]** in the surveys as "four alternatives" — they are four independent settings and must be handled as combinations. Still, "approved means approved against *this* revision" is a forge-enforceable invariant here and is not on GitHub.

**A five-valued disposition.** `5 = approved with suggestions` — satisfies the approval policy *and* carries work. GitHub cannot express it.

**A native external property bag on the PR.** `PATCH .../pullRequests/{id}/properties` exists specifically for third-party state. On GitHub the change-id and revision ledger get smuggled into the PR body or a hidden comment where a human can edit or delete them.

**Real compare-and-swap on refs.** `POST .../refs` takes `oldObjectId` and returns `staleOldObjectId` ("The most likely scenario is that the caller lost a race to update the ref"). GitHub's `PATCH /git/refs` has no expected-old-SHA. *(Trap: a lost race still returns **HTTP 200** with per-element `success: false` — check the body, not the status. And the batch is not transactional; the enum includes `unprocessed`.)*

**Deleted branches never expire.** "There's no retention policy on deleted branches. You can restore a deleted Git branch at any time, regardless of when it was deleted," plus a permanent per-ref push ledger (`GET .../pushes?searchCriteria.refName=…&searchCriteria.includeRefUpdates=true`) recording every `oldObjectId`/`newObjectId`. **[OVERSTATED]** the conclusion "so drop `orig` refs": the ledger preserves the *SHAs*, but a 2017 Microsoft change ("we rolled out commit reachability bitmap indexes… Cloning will no longer download unreachable objects") means the *objects* are not in any clone, and fetch-by-unadvertised-SHA is not documented as supported. Safe for audit, **not** for checkout — to materialize an old revision you must either read blobs through the Items API at that commit (`versionDescriptor.versionType=commit&includeContent=true`, confirmed working in the field) or re-create a ref first.

**Blocking comment-resolution policy.** The **Check for comment resolution** policy makes the agent's disposition work load-bearing on merge, and the `fixed`/`wontFix`/`byDesign` vocabulary is a machine-readable record of *how* each item was disposed of. No GitHub branch-protection equivalent.

**`hasMultipleMergeBases`.** A first-class PR field flagging a hazard GitHub does not surface: with multiple merge bases, "a malicious user could abuse the UI algorithm to commit malicious changes that aren't present in the PR" and "changes proposed in the PR [that] are already in the target branch… might not trigger branch policies that are mapped to folder changes." Gate sealing on it.

---

## 4. WHERE AZURE DEVOPS IS WORSE

### 4.1 The trigger unit does not exist

Plainly: **there is no atomic submitted review object.** No `reviews` collection, no review id, no `submitted_at`, no review body, no draft/pending staging, no "Submit review" action in the API *or* the web UI — every comment is published on its own button press and the vote is an independent dropdown. Nothing joins them: no `reviewId` on a thread, no thread list on a vote, different resources, different scopes (`vso.threads_full` vs `vso.code_write`), different event ids.

**What the trigger must become instead:**

> **Primary trigger = a vote transition observed in the `VoteUpdate` system-thread log. Envelope = a debounce window closed by that transition.**

Concretely, per PR:

1. Poll `GET .../pullRequests/{id}/threads?api-version=7.1` on an adaptive interval. This one call yields every `VoteUpdate` (voter GUID, vote value as a *string*, `publishedDate`), every `RefUpdate` (new head SHA, pusher GUID), every `MergeAttempt`, and every user thread with its comments and `publishedDate`/`lastContentUpdatedDate`.
2. On a `VoteUpdate` for reviewer R at time T, where R ≠ the agent identity: seal a `review/v1` record containing R's vote plus every `commentType == "text"` comment authored by R with `publishedDate` in `(T_prev_vote_by_R, T]`, with anchors resolved once and frozen.
3. Bind the disposition to a revision by taking the greatest iteration whose `createdDate` precedes T. This is a **heuristic and it races** with a push landing between comment and vote — the API exposes no vote↔iteration association even though branch policies prove the server tracks one. Mitigate by turning on `requireVoteOnEachIteration`, which makes a stale approval unable to survive a push at all.
4. Service hooks (`git.pullrequest.updated` filtered to `ReviewerVoteNotification`, on its own receiver URL) are a wake-up nudge that carries `{repositoryId, pullRequestId}` and nothing else authoritative.

**Failure modes you are accepting, stated up front:**

- **Comment-only has no completion signal.** A reviewer who writes eight comments and never votes produces no trigger. There is no "I'm done" marker anywhere in the model. You must invent the boundary: a quiet-period debounce (e.g. 90s since that author's last comment on this PR), an explicit convention (reviewer sets vote `0` / "Reset feedback" to close a pass), or a `@jetbridge go` marker comment. This is a first-class product decision, not an implementation detail — it is the one place the model genuinely does not port.
- **Vote-then-comment ordering** produces an envelope missing the trailing comments. Mitigate with a short trailing grace period after the vote before sealing.
- **The agent's own push may fire a vote event.** Under `resetOnSourcePush` / `resetRejectionsOnSourcePush`, the agent's push zeroes votes. Whether that emits `ReviewerVoteNotification` or writes a `VoteUpdate` thread is **undocumented in both directions** and is the highest-priority live test (§7). Defensive rule until settled: drop any vote transition to `0` arriving within a short window after a `RefUpdate` attributable to the agent.
- **`resetOnSourcePush` is too coarse for an agent loop.** Microsoft Q&A 1688029 (unanswered on the merits) reports approvals reset by *empty* commits and by merges from the target branch that change nothing. Since "approved → rebase if needed → merge" involves a source-branch push, the agent's own landing step wipes the approval it is acting on. **Prefer server-side rebase at completion** (`mergeStrategy: rebase | rebaseMerge`) so no push happens at all.
- **You cannot clear a stale vote programmatically as a bot.** **[REFUTED]** the survey's "PATCH `.../reviewers` lets the agent reset stale reviewers." `azure-devops-node-api#611` reports a 200 with no state change; the maintainer's working repro resets **the caller's own** vote and says "PAT owner… can reset their own vote." No evidence a bot may reset a human's vote, and the docs state no permission requirement for the route. Do not build control flow on it.

### 4.2 Everything downstream of the trigger

- **No merge queue, no merge train, no Gerrit submit strategies.** Auto-complete is per-PR deferred completion against the *current* target — it does not serialize competing PRs and does not speculatively test a batch. On `approved`, the agent must itself detect target movement, re-verify, and land, and two agents can land onto a target neither tested against. Build a land-retry loop with bounded attempts. *(New environmental fact, sprint 270: a repo/project setting "Set PRs to auto-complete on creation by default" now exists — an agent-created PR in such a repo is armed to merge the instant policies pass, before any human disposition exists. Detect and account for it.)*
- **No textual diff or patch media type anywhere.** Iteration changes and the Diffs API both return *file-level change lists* — path, blob `objectId`/`originalObjectId`, `changeType` — with no hunks and no line content (confirmed against a captured live response in `azure-devops-mcp#1237`, where `changeType` also came back as the **integer 1** rather than the documented string). Fetch blobs and diff locally, or clone.
- **Event transport is weaker in four ways at once:** no actor in the PR-update payload, no delta, no documented ordering, no HMAC. Plus silent event loss during probation.
- **Rate limits are uncomputable and silent** (Services only): TSTU-metered, and throttling arrives first as *latency on HTTP 200 responses* with a `Retry-After` header, "up to 30 seconds," "indefinitely" if consumption stays high. A client that inspects only status codes sees no error at all. `X-RateLimit-Cost`/`-Remaining` exist but the docs hedge them as "if available"/"if present." Build an adaptive poller with a fallback for absent headers.
- **The Go SDK is a liability.** `github.com/microsoft/azure-devops-go-api/azuredevops/v7` last published **v7.1.0 on 2023-04-17**; last API-affecting commit 2024-10-11; 2026 activity is CodeQL/CI only. Its generated git client hardcodes `7.1-preview.1` (114 sites) and `7.1-preview.2` (5), with **zero** uses of GA `7.1` — against a documented policy that a preview version "is deprecated and can be deactivated after 12 weeks" and then "requests that specify a -preview version get rejected." All three community clients are abandoned (2019/2021).
- **Commit-level comments are a silent dead end.** Comments left on a commit from the Commits tab do not appear in the PR, produce no thread, no notification, no event ("Basically it is lost" — Microsoft Q&A 1602132, accepted answer confirms).
- **On-prem loses a lot.** Azure DevOps Server has **no Entra ID and no OAuth of any kind** ("For on-premises scenarios, use client libraries, Windows authentication, or personal access tokens"), no service principals or managed identities, no `az` CLI, no documented TSTU limits, and (inferred) no outbound Azure connectivity for the Service Bus / Storage Queue consumers in many installs. All `dev.azure.com/{organization}` URLs become `{instance}/{collection}`. Meanwhile on Services, **global PATs are being decommissioned — "December 1, 2026, all existing global PATs will be fully decommissioned and stop working"** — so the on-prem/PAT fallback must use org-scoped tokens and has a deadline.

---

## 5. THE PROVIDER-NEUTRAL INTERFACE

**Opinion up front:** the core owns the *state machine* and the *sealed record*; the adapter owns *wire shapes* and *identity*. The interface is **poll-only with an opaque cursor**. Webhooks do not appear in it at all — they are an adapter-internal latency optimization that can only ever mean "call `PollReviewEvents` sooner." Anything else leaks GitHub's delivery semantics into a forge whose delivery semantics are materially different.

```go
package forge

// ---------- identity ----------

type ActorID string                      // opaque, forge-scoped, normalized by the adapter
type ChangeKey struct{ Repo RepoID; Change string } // Azure: pullRequestId; GitHub: PR number
type ChangeID string                     // JetBridge-owned, stable across PRs. Core mints it.
type RevisionID string                   // opaque ordinal. Azure: "7". GitHub: synthesized.

// ---------- revisions ----------

type Revision struct {
    ID          RevisionID
    Ordinal     int
    Head        string    // commit OID
    Target      string    // commit OID (target ref head as of this revision)
    MergeBase   string    // commit OID
    CreatedAt   time.Time
    Author      ActorID
    Kind        RevisionKind // Created|Pushed|ForcePushed|Rebased|Retargeted|Unknown (flag set)
    BaseSuspect bool         // Azure: hasMultipleMergeBases; GitHub: always false
    Truncated   bool         // Azure: hasMoreCommits
    Raw         json.RawMessage // adapter-owned; sealed verbatim into review/v1
}

// ---------- review ----------

type Disposition int
const (
    DispositionUnknown Disposition = iota
    DispositionApproved                 // ADO 10; GH APPROVED
    DispositionApprovedWithComments     // ADO 5; GH: never produced
    DispositionCommentOnly              // ADO 0-with-comments; GH COMMENTED
    DispositionChangesRequested         // ADO -5/-10; GH CHANGES_REQUESTED
)

type ReviewEvent struct {
    EnvelopeID   string        // adapter-minted, stable, dedupe key
    Author       ActorID
    Disposition  Disposition
    RawVerdict   string        // "-5", "CHANGES_REQUESTED" — never routed on, always sealed
    Blocking     bool          // ADO -10 vs -5; GH always true for changes-requested
    AgainstRev   RevisionID
    RevCertainty Certainty     // Exact | Inferred  <-- see leak #3
    ClosedAt     time.Time
    ClosedBy     EnvelopeClose // Verdict | QuietPeriod | Marker
    Comments     []Comment
}

type Comment struct {
    ID       CommentID   // Azure: {threadId, commentId}; GitHub: comment id
    ThreadID ThreadID
    Author   ActorID
    Body     string
    Anchor   Anchor
    Status   ThreadStatus // Active|Fixed|WontFix|ByDesign|Closed|Pending|Unknown
    IsSystem bool
}

type Anchor struct {
    Path      string
    Range     LineRange   // display only. NEVER used to re-derive position.
    BlobOID   string      // immutable pin. Azure: item.objectId at the anchoring iteration
    CommitOID string      // immutable pin
    Rev       RevisionID
    Window    json.RawMessage // adapter-owned coordinate system; sealed verbatim
    Forwarded bool        // server moved it (Azure trackingCriteria); advisory only
}

// ---------- the interface ----------

type Forge interface {
    Capabilities() Capabilities
    Self(ctx context.Context) (ActorID, error)

    GetChange(ctx context.Context, k ChangeKey) (Change, error)
    ListRevisions(ctx context.Context, k ChangeKey) ([]Revision, error)
    MaterializeRevision(ctx context.Context, k ChangeKey, r RevisionID, dst string) error

    PollReviewEvents(ctx context.Context, k ChangeKey, since Cursor) ([]ReviewEvent, Cursor, error)

    PostReplies(ctx context.Context, k ChangeKey, replies []Reply) error
    DeclareResolutions(ctx context.Context, k ChangeKey, r RevisionID, res []Resolution) error

    PublishRevision(ctx context.Context, k ChangeKey, req PublishRequest) (Revision, error)
    SetGate(ctx context.Context, k ChangeKey, r RevisionID, g Gate) error
    Land(ctx context.Context, k ChangeKey, req LandRequest) (LandResult, error)

    BindChangeID(ctx context.Context, k ChangeKey, id ChangeID) error
    LookupChangeID(ctx context.Context, k ChangeKey) (ChangeID, bool, error)
}

type Capabilities struct {
    AtomicReviewEnvelope     bool // GH true, ADO false
    CommentOnlyCompletion    bool // GH true, ADO false
    NativeRevisions          bool // ADO true, GH false
    RevisionScopedGate       bool // ADO true, GH false
    RevisionBoundVerdict     bool // ADO true (policy-dependent), GH approximate
    MergeQueue               bool // GH true, ADO false
    ServerSideRebaseOnLand   bool // ADO true, GH partial
    RichResolutionVocabulary bool // ADO true, GH false
    ExternalPropertyStore    bool // ADO true, GH false
    ProgrammaticVerdictReset bool // ADO false (unproven), GH partial
}
```

**Core vs adapter, opinionated:**

| Core | Adapter |
|---|---|
| Change-id minting and lifecycle | Where the change-id is persisted (ADO: PR property bag; GH: a marker in the body) |
| Disposition **routing** and the collapse policy | Disposition **extraction** and the raw verdict string |
| Envelope debounce policy and quiet-period timing | Nothing — the core sets the window, the adapter reports timestamps |
| Sealing `review/v1`, digesting, immutability | Producing `Raw`/`Window` blobs to be sealed verbatim |
| Self-suppression *policy* (drop events where `Author == Self()`) | Resolving the actor — from the payload on GitHub, from `VoteUpdate`/`RefUpdate` system threads on ADO |
| Land-retry loop, rebase decision, backoff | The single land call and its failure taxonomy |
| Cursor persistence | Cursor *shape* (opaque bytes) |
| Rate-limit budget policy | Reading `X-RateLimit-*` / GitHub's headers and reporting pressure |

**Every place the two forges force a leak, and the ruling:**

1. **Revision identity.** ADO has one, GitHub does not. → **Normalize.** `RevisionID` is opaque; GitHub's adapter synthesizes revisions from ghstack-style ref triples it publishes itself. Expose `NativeRevisions` only so the core can *skip publishing* those refs on ADO, never so it branches on semantics.
2. **The review envelope.** → **Capability flag `AtomicReviewEnvelope`.** The core runs the same state machine either way; when false it also runs the debounce timer and populates `ClosedBy`. Do not fake a GitHub review object on ADO — that is precisely what the 5,400-line integration probably did.
3. **Verdict↔revision binding.** ADO cannot tell you which iteration a vote targeted; GitHub's review carries `commit_id`. → **Expose as data, not a flag:** `RevCertainty` on every event. The core refuses to auto-merge on `Inferred` unless `requireVoteOnEachIteration` is confirmed on the branch (which the adapter reports via `RevisionBoundVerdict`).
4. **Comment-only completion.** → **Capability flag `CommentOnlyCompletion`.** When false, the core's debounce is the only boundary and it must say so in the sealed record.
5. **Anchor coordinate systems.** ADO's position depends on the query window and the side flips; GitHub nulls `position`. → **Refuse to model a portable line anchor.** `Anchor.Range` is display-only. Authority is `(BlobOID, CommitOID, Rev, Window)`, sealed at ingest, never re-derived. `Forwarded` is advisory — and on ADO you only ever see it if you asked with `$iteration`/`$baseIteration`.
6. **Resolution vocabulary.** ADO has seven statuses in REST (five in the UI, `byDesign` in neither dropdown); GitHub has a GraphQL boolean. → **Normalize to a common enum, capability-flag the richness.** Adapters map lossily and record the raw value. Prefer `Fixed`/`WontFix` on ADO so humans can see and revert them.
7. **Character-level spans.** ADO supports them and validates nothing; GitHub cannot express them. → **Refuse to model.** Line ranges only. The upside is not worth `azure-devops-mcp#793`.
8. **Merge queue.** → **Capability flag `MergeQueue`.** When false the core owns the retry loop. Do not emulate a queue.
9. **Programmatic verdict reset.** ADO's is unproven for a bot on another user's vote. → **Capability flag, default false.** Never make correctness depend on it.
10. **Multi-ref atomicity.** ADO has no `--atomic` push and its batch `POST /refs` returns per-element results; GitHub is equally non-atomic. → **Refuse to model.** `PublishRevision` is a single logical operation whose adapter may leave a torn state; the core must reconcile on every restart from `ListRevisions`.
11. **Event delivery.** → **Refuse to model.** Interface is poll + cursor. Webhooks live entirely inside the adapter.
12. **On-prem vs cloud.** Different auth, different host shape, no CLI, no documented throttling. → **Adapter-internal**, exposed only via a `RateLimitPressure` hint. Two auth strategies (Entra service principal on Services; org-scoped PAT on Server) behind one credential provider. The bot's identity GUID is **runtime configuration resolved per deployment**, never a compile-time notion.

---

## 6. THE MINIMAL AZURE DEVOPS ADAPTER

**What it must implement — about a dozen routes, all pinned at `api-version=7.1`:**

- `GET .../pullrequests/{id}` (change, `hasMultipleMergeBases`, `supportsIterations`, `mergeStatus`, reviewers)
- `GET/PATCH .../pullRequests/{id}/properties` (change-id + revision ledger, strings only)
- `GET .../pullRequests/{id}/iterations` and `/{n}` (revision series)
- `GET .../pullRequests/{id}/iterations/{n}/changes?$compareTo=` (change list + `changeTrackingId` + blob OIDs)
- `GET .../pullRequests/{id}/threads` (the cursor: system-thread ledger + user threads) — **twice** per seal: once bare for authored positions, once with `$iteration`/`$baseIteration` for the current window
- `POST .../threads`, `POST .../threads/{id}/comments`, `PATCH .../threads/{id}` (replies, resolutions, `properties` stamps)
- `GET .../items?versionDescriptor.versionType=commit&includeContent=true` (materialize old revision content when git can't reach it)
- `POST .../pullRequests/{id}/statuses` (gate, `iterationId`-scoped)
- `PATCH .../pullrequests/{id}` (land: `status` + `completionOptions` only)
- `GET .../_apis/git/policy/configurations` and `GET _apis/policy/types` (capability probing — never hardcode the Status policy GUID)
- `POST/GET .../refs` (retention refs and CAS)
- `POST /_apis/hooks/subscriptions` + `GET /_apis/hooks/subscriptions` (optional latency path + probation health)

Plus a thin HTTP core: auth (Entra client-credentials on Services, org-scoped PAT Basic-with-empty-username on Server), `api-version=7.1` pinning, `X-RateLimit-*`-aware adaptive backoff, response-body success checking (not status codes), and defensive `PropertiesCollection` parsing with GUID normalization.

**Size:** roughly **1,200–1,800 lines of non-test Go** across ~10 files, plus **~1,000–1,400 lines of tests** against recorded fixtures. Call it **≤3,000 lines total.** If it crosses 4,000, something in §5's "refuse to model" list has been modeled.

**Do not depend on `microsoft/azure-devops-go-api`.** Hand-roll the client; optionally vendor the SDK's MIT-licensed `git/models.go` structs (they are correct) and discard `client.go` (114 hardcoded `7.1-preview.1` call sites against a 12-week preview-deactivation policy).

**What the deleted 5,400-line integration was almost certainly doing that must not recur:**

1. **Wrapping the whole REST surface** — work items, builds, pipelines, projects, teams, identities — instead of the dozen routes the loop touches. This is what generated SDKs teach you to do and it is wrong here.
2. **Emulating a GitHub review object.** Faking a `Review` type on top of votes+threads, with a synthetic `submitted_at`, so the GitHub code path could be reused unchanged. That is the leak that must become a capability flag.
3. **Rebuilding comment forwarding.** Writing a line-tracking heuristic on top of `$iteration`/`$baseIteration` because the anchors looked unstable. The correct move is to seal once and never re-derive.
4. **A bespoke webhook receiver** with signature verification that Azure does not offer, delivery ordering assumptions Azure does not guarantee, and a replay path (`notificationsquery`) that cannot recover probation-window losses.
5. **A policy-configuration manager** that created branch policies with hardcoded type GUIDs and guessed `settings` field names (the Status policy GUID is undocumented; `settings` is an untyped `JObject`).
6. **An identity/permission subsystem** — Graph traversal, group expansion, license checks — where one runtime-resolved `Self()` GUID would do.
7. **A retry/backoff framework** modeled on 429s, when the actual Azure failure mode is a silent latency ramp on HTTP 200.
8. **Sidecar state storage** for change-ids and resolution declarations, when the PR property bag and the thread property bag exist for exactly that.

---

## 7. OPEN QUESTIONS AND THE EXPERIMENTS THAT SETTLE THEM

Both verification passes left these unproven. Each needs a scratch Azure DevOps project (Services) and, where noted, a Server instance.

1. **Does a policy-driven vote reset on push emit `ReviewerVoteNotification` and/or write a `VoteUpdate` system thread — and to whom is it attributed?** *Highest priority; it determines whether the agent self-triggers on every revision.* Enable `resetOnSourcePush`, have a human approve, push as the agent, then diff `GET .../threads` before/after and capture webhook deliveries on a subscription filtered to `ReviewerVoteNotification`. Record the actor GUID in any resulting `VoteUpdate`.
2. **Do system threads (`commentType: "system"`) fire `ms.vss-code.git-pullrequest-comment-event`?** If yes, votes leak back into the comment channel authored by `Project Collection Service Accounts`, defeating author-id suppression. Subscribe to the comment event, cast a vote, count deliveries.
3. **Does a status-only `PATCH .../threads/{id}` (no new comment) emit the comment event?** If yes, an agent resolving threads loops. Same rig; PATCH status only.
4. **Can the agent's service identity `PATCH` the status of a thread it did not author?** The REST reference states no constraint; the UI doc frames it as a PR-author action; and there is precedent for silent cross-identity no-ops (`azure-devops-node-api#611`). Assert the *response body*, not the 200.
5. **Can the agent's service identity set `autoCompleteSetBy` (to itself)?** `vsts-rest-api-specs#134` reports it echoed but not applied. PATCH, re-read, assert.
6. **What is the accepted write shape and size ceiling for the thread `properties` bag?** No Microsoft sample shows an integration writing it. Try a raw scalar and a `{$type,$value}` envelope; probe with 1 KB / 16 KB / 256 KB values.
7. **What does `threadContext` contain when the anchored line is deleted in the requested iteration — and does the thread still appear?** Anchor a comment, delete the line, re-list with `$iteration=N&$baseIteration=N-1`. This is the one undocumented behaviour that could silently corrupt a sealed record, and Microsoft has explicitly deprioritized the underlying bug.
8. **Are an old iteration's commits still `git fetch`-able after a force-push?** Push twice, force-push over iteration 2, then attempt `git fetch origin <iteration1.sourceRefCommit>` from a fresh clone, and separately `GET .../items?...versionType=commit&includeContent=true` at that SHA. Field evidence says REST serves it and git does not; confirm, and if git fails, adopt a JetBridge retention ref at seal time (`refs/heads/jetbridge/{change-id}/{n}`, since custom namespaces are unproven — see next).
9. **Can a custom ref namespace be created?** `POST .../refs` with `name: "refs/jetbridge/test"`, `oldObjectId` = 40 zeros; read `updateStatus`. Expect `invalidRefName` or a permission status. Determines whether retention refs must live under `refs/heads/`.
10. **Is the multi-element `POST /refs` batch transactional?** Post two updates where one is deliberately stale; check whether the other applied. The `unprocessed` enum value suggests not.
11. **What is the actual `resourceVersion` of `ms.vss-code.git-pullrequest-comment-event`?** The two verification passes disagree (`2.0` vs `1.0-preview.1`) and the surveys said `1.0`. Read it off the live subscription-creation response before pinning; an unpinned or wrongly-pinned version changes the sealed record's input shape.
12. **What HTTP headers actually arrive on a webhook delivery?** Entirely undocumented; `x-vss-subscriptionid` is community folklore with no primary source. Observe with a throwaway receiver, but design on distinct receiver URLs (contractual) rather than headers.
13. **What is the default squash commit message when `mergeCommitMessage` is omitted?** Complete a squash PR without it, read `lastMergeCommit.comment`. Then always set it explicitly anyway.
14. **Measure `X-RateLimit-Cost` for `GET .../threads` and `GET .../iterations`** to size the poll loop — and confirm whether the headers are present at all, since the docs hedge them as "if available."
15. **Is `notificationType` (set via REST `publisherInputs`) preserved if a subscription is opened and re-saved in the Service Hooks web UI?** Documented precedent says the UI does not display API-defined filters for the work-item publisher. If it silently drops them, the four-subscription design is one admin click from collapsing into an untyped firehose.

---

# APPENDIX: raw surveys and verification passes

```json
[
  {
    "key": "iterations",
    "survey": {
      "area": "Azure DevOps pull request iterations as a revision noun (and the surrounding review-loop primitives), mapped onto JetBridge's disposition-triggered review design",
      "findings": [
        {
          "capability": "A first-class, individually addressable REVISION noun on a pull request (\"iteration\")",
          "exists": "native",
          "detail": "Azure DevOps has a real revision noun. GitPullRequestIteration is a distinct REST resource with its own int32 id, scoped to the pull request, monotonically numbered from 1. It is listable and individually GETtable. Full documented field set: _links, author (IdentityRef), changeList (GitPullRequestChange[]), commits (GitCommitRef[]), commonRefCommit (GitCommitRef), createdDate, description, hasMoreCommits, id, newTargetRefName, oldTargetRefName, push (GitPushRef: pushId, pushedBy, date, url), reason (IterationReason), sourceRefCommit (GitCommitRef), targetRefCommit (GitCommitRef), updatedDate. The type doc says verbatim: 'Provides properties that describe a Git pull request iteration. Iterations are created as a result of creating and pushing updates to a pull request.' GitPullRequest additionally carries supportsIterations: 'If true, this pull request supports multiple iterations. Iteration support means individual pushes to the source branch of the pull request can be reviewed and comments left in one iteration will be tracked across future iterations.' Note supportsIterations is a per-PR boolean \u2014 'legacy' PRs exist without iteration support and the thread API calls this out, so a client must branch on it rather than assume iterations are always present. hasMoreCommits means the commits array is truncated; use the per-iteration commits endpoint for the full list.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?includeCommits={bool}&api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/pullrequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request-by-id?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "What creates a new iteration, and what iteration 1 is",
          "exists": "native",
          "detail": "Documented verbatim in the iterationId path-parameter description of Pull Request Iteration Changes - Get: 'ID of the pull request iteration. Iteration one is the head of the source branch at the time the pull request is created and subsequent iterations are created when there are pushes to the source branch. Allowed values are between 1 and the maximum iteration on this pull request.' So: PR creation makes iteration 1; each push to the source branch makes a new one. The IterationReason enum additionally proves that force-push, rebase, retarget (target-branch change) and conflict resolution also mint iterations. IMPORTANT ASYMMETRY: a plain advance of the TARGET branch does NOT appear to create an iteration \u2014 nothing in the docs says it does, and targetRefCommit is captured per iteration rather than tracked live. That means iteration N's targetRefCommit is the target head as of that iteration, and the PR's live merge preview (lastMergeTargetCommit / lastMergeCommit on GitPullRequest) can drift away from it without any new iteration. Do not treat 'no new iteration' as 'the merge base has not moved'.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/changes?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "IterationReason enum \u2014 values, and the [Flags] trap",
          "exists": "native",
          "detail": "REST 7.1 documents seven values with EMPTY descriptions for every one: push, forcePush, create, rebase, unknown, retarget, resolveConflicts. Microsoft never documents what any of them mean. The .NET client doc for Microsoft.TeamFoundation.SourceControl.WebApi.IterationReason is the only place the mechanics leak, and it is load-bearing: the enum is declared '[System.Flags]' with Push = 0, ForcePush = 1, Create = 2, Rebase = 4, Unknown = 8, Retarget = 16. Three consequences a parser must handle: (1) 'push' is the ZERO value, i.e. the default/absence-of-other-reasons case, not a positive assertion; (2) reasons COMBINE \u2014 a force-push that is also a rebase is legitimately ForcePush|Rebase (5), so `reason` must not be modelled as a closed single-valued enum, and the JSON is expected to be a comma-separated camelCase string in that case; (3) resolveConflicts does not appear in the .NET page at all (last updated 2018), so its numeric value (presumably 32) and its semantics are undocumented. Semantics I would treat as INFERRED, not documented: create = iteration 1 at PR creation; push = fast-forward advance of the source ref; forcePush = non-fast-forward source ref update; rebase = server-performed rebase (e.g. the UI 'rebase' action or a conflict-resolution rebase); retarget = targetRefName changed, paired with oldTargetRefName/newTargetRefName; resolveConflicts = commits written by the web conflict-resolution editor; unknown = the server could not classify it.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/dotnet/api/microsoft.teamfoundation.sourcecontrol.webapi.iterationreason"
          ]
        },
        {
          "capability": "Each revision names its own BASE unambiguously (the ghstack requirement)",
          "exists": "native",
          "detail": "This is the single best fit to JetBridge's design and it needs no construction. Every iteration carries three commit refs: sourceRefCommit ('The source Git commit of this iteration'), targetRefCommit ('The target Git commit of this iteration'), and commonRefCommit ('The first common Git commit of the source and target refs'). commonRefCommit IS the merge base: the Diffs API describes the same concept in the same words \u2014 'Find the closest common commit (the merge base) between base and target commits' \u2014 and returns it as `commonCommit`. So an iteration is a fully self-describing (base, head, merge-base) triple, exactly what ghstack has to synthesise out of base/head/orig ref triples on GitHub because GitHub has no revision object at all. CAVEAT: GitPullRequest exposes hasMultipleMergeBases, described only as 'Multiple mergebases warning'. In a criss-cross history there is more than one merge base, and commonRefCommit is then ONE of them, chosen by the server, not a canonical value. If JetBridge seals commonRefCommit into review/v1 it should also seal hasMultipleMergeBases and refuse to treat the base as canonical when that flag is set.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/diffs/commits?baseVersion={sha}&baseVersionType=commit&targetVersion={sha}&targetVersionType=commit&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/diffs/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request-by-id?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Are iterations IMMUTABLE and still addressable after a force-push?",
          "exists": "native",
          "detail": "Yes for the iteration record, and this is explicitly documented \u2014 not inferred. The Azure Repos review doc states verbatim of the Updates tab: 'The changesets are numbered and the most recent changeset appears at the top of the list. Each changeset shows the commits that were pushed in that push operation. A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset.' And, drawing the contrast explicitly: 'The commit history in the Commits tab is overwritten if the PR author force-pushes a different commit history, so the commits shown in the Commits tab might differ from the commits shown in the Updates tab.' That is the whole ballgame for JetBridge: the iteration series is an append-only log that a history rewrite cannot rewrite, whereas the PR's flat commit list is exactly as mutable as GitHub's. Iteration N remains individually GETtable by id, and its change list and commit list remain individually fetchable, after any number of subsequent force-pushes. An iteration also has no documented mutator \u2014 there is no PATCH/PUT/DELETE on the iterations route \u2014 so it is immutable by API surface as well as by observed behaviour. The only mutable-looking field is updatedDate, whose meaning is undocumented (INFERRED: it tracks derived state such as merge/diff computation, not the content of the revision).",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/changes?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/commits?top={n}&skip={n}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-commits/get-pull-request-iteration-commits?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Static per-iteration git refs (a Gerrit refs/changes/NN/CCCC/P analogue)",
          "exists": "absent",
          "detail": "There is none. Azure Repos publishes no per-iteration ref, and nothing in the REST surface returns a ref name for an iteration \u2014 only commit SHAs. The only PR-scoped ref namespace Microsoft documents for Azure Repos is the MERGE ref: the Azure Pipelines predefined-variables page states that for a 'Git repo pull request' Build.SourceBranch is 'refs/pull/1/merge'. That is a single, mutable, per-PR ref pointing at the current merge preview \u2014 it is not per-revision and it is recomputed on every push. Notably: (a) no refs/pull/{id}/head is documented anywhere for Azure Repos (GitHub has one; Azure Repos does not appear to), and (b) the Refs - List REST API's own 'all refs' sample returns only refs/heads/*, refs/remotes/* and refs/tags/* \u2014 refs/pull does not show up even in an unfiltered listing, so it behaves as a hidden namespace reachable over the git wire protocol but not enumerated by the Refs API. Practical consequence: JetBridge cannot get a Gerrit-style guarantee by fetching a per-revision ref. The revision identity lives in the Azure DevOps database, not in the git object graph.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?filter=heads/&$top={n}&continuationToken={t}&api-version=7.1",
            "git fetch origin refs/pull/{pullRequestId}/merge"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/pipelines/build/variables?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Are the git objects of a superseded iteration still reachable/fetchable after a force-push?",
          "exists": "partial",
          "detail": "UNRESOLVED IN THE DOCS \u2014 and this is the one place I would not let the design assume. What IS documented: the iteration metadata survives force-push, the old iteration's commit list stays queryable via REST (which returns commitId, parents, author, comment), and the web UI still renders the old diff. That strongly implies Azure Repos retains those objects server-side and does not prune them on the timescale of a review loop. What is NOT documented anywhere: any ref pinning them, any retention window, any GC/prune policy, or any guarantee that `git fetch <old-sha>` from a client will succeed once the branch no longer reaches it. The Azure Repos Git limits page documents repository size, push size and path length and says nothing about object retention; the repository-maintenance material only describes repacking for size, not unreachable-object retention. RECOMMENDATION: treat 'REST can still describe iteration N' as durable, and treat 'a build agent can still `git fetch` iteration N's tree' as best-effort. If JetBridge needs to materialise the exact tree of an old revision, it should either (a) read blobs through the Items API with versionType=commit while the objects last, or (b) push its own retention ref (e.g. refs/jetbridge/{change-id}/{n}) at the moment it seals each revision. Option (b) is the only way to get a Gerrit-grade guarantee, and it is cheap.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/commits?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/items?path={path}&versionDescriptor.version={sha}&versionDescriptor.versionType=commit&api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "uncertain",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-commits/get-pull-request-iteration-commits?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Diff iteration N against iteration M via the API",
          "exists": "native",
          "detail": "Fully supported, and it is the same mechanism the UI uses. GET .../iterations/{iterationId}/changes takes $compareTo, documented verbatim as: 'ID of the pull request iteration to compare against. The default value is zero which indicates the comparison is made against the common commit between the source and target branches.' So $compareTo=0 (or omitted) gives 'this revision vs its merge base' \u2014 the whole-PR diff at that revision \u2014 and $compareTo=M gives the incremental diff between two revisions. Paging is documented: '$top: Optional. The number of changes to retrieve. The default value is 100 and the maximum value is 2000.' '$skip: Optional. The number of changes to ignore.' The response is GitPullRequestIterationChanges { changeEntries: GitPullRequestChange[], nextSkip, nextTop } where 'nextSkip/nextTop: Value to specify as skip/top to get the next page of changes. This will be zero if there are no more changes.' Each changeEntry carries changeId, changeTrackingId, changeType (VersionControlChangeType: add/edit/delete/rename/sourceRename/targetRename/...), and item { objectId, originalObjectId, path }. The documented sample for $compareTo=1 shows exactly this: an entry with both objectId and originalObjectId and changeType 'edit'. Follow the paging with nextTop/nextSkip rather than incrementing $skip yourself.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/changes?$top={n}&$skip={n}&$compareTo={m}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Getting the actual textual diff / patch for a revision",
          "exists": "workaround-only",
          "detail": "Plainly worse than GitHub. The iteration-changes endpoint returns a CHANGE LIST \u2014 paths, blob objectIds, originalObjectIds, change types \u2014 not a unified diff, and there is no patch media type anywhere in the Azure DevOps Git REST API. GitHub gives you `Accept: application/vnd.github.v3.diff` (or .patch) on a PR and hands back a ready patch. On Azure DevOps you must reconstruct: for each changeEntry, fetch the blob at item.objectId and at item.originalObjectId (Blobs API) or the item at each side's commit (Items API with versionType=commit), then diff locally. The generic Diffs API (GET .../diffs/commits with baseVersion/targetVersion/baseVersionType/targetVersionType/diffCommonCommit \u2014 'If true, diff between common and target commits. If false, diff between base and target commits') has the same limitation: it returns GitCommitDiffs { changes, changeCounts, commonCommit, baseCommit, targetCommit, aheadCount, behindCount, allChangesIncluded } \u2014 again a change list, no patch text. For an agent that needs the diff as review input, the realistic path is not REST at all: clone/fetch and run git locally against the two iteration SHAs. The web UI additionally truncates: 'For performance reasons, the summary view doesn't show changes for a file that's larger than 0.5 MB' and 'For any single file that's larger than 5 MB, the diff view shows truncated file content.'",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/diffs/commits?baseVersion={x}&baseVersionType=commit&targetVersion={y}&targetVersionType=commit&diffCommonCommit={bool}&$top={n}&$skip={n}&api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/blobs/{sha1}?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/diffs/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Retarget (base-branch change) recorded as a revision event",
          "exists": "native",
          "detail": "Azure DevOps records a target-branch change as its own iteration with reason=retarget, and the iteration carries oldTargetRefName ('If the iteration reason is Retarget, this is the original target refName') and newTargetRefName ('If the iteration reason is Retarget, this is the refName of the new target'). This is a genuinely useful property for JetBridge: a rebase-onto-a-different-base is a first-class, ordered, attributed event in the revision series, with both endpoints named. GitHub, by contrast, mutates pull_request.base.ref in place and leaves only a timeline event \u2014 there is no revision boundary and no diff boundary. Combined with targetRefCommit being captured per iteration, this means an Azure DevOps iteration series records base movement as faithfully as it records head movement.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?api-version=7.1",
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "A submitted-REVIEW object (the trigger unit in JetBridge's design)",
          "exists": "absent",
          "detail": "Azure DevOps has NO review object. There is no equivalent of GitHub's GET /pulls/{n}/reviews, no review id, no submitted_at, no review body, no batching of inline comments into a review, and no draft/pending review state. The API surface is: comment THREADS (created and published immediately, one at a time) and reviewer VOTES (a scalar on IdentityRefWithVote). Nothing joins them. The word 'review' does appear in current UI copy \u2014 'Copilot always leaves a Comment review, so its feedback doesn't satisfy required-reviewer policies and doesn't block merging' \u2014 but that is GitHub-borrowed UI language over the same vote+threads model; there is no review resource behind it. This is the central structural difference JetBridge must absorb: the design's premise that 'the trigger is a completed human review, not an individual comment' has no native carrier on Azure DevOps. The trigger must be redefined as a vote transition (see next finding), and comment-only feedback has no completion signal at all.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "The three DISPOSITIONS \u2014 how they map onto votes",
          "exists": "native",
          "detail": "The disposition itself maps cleanly, better than GitHub's three-value enum in fact, because Azure DevOps has five levels. IdentityRefWithVote.vote is documented verbatim as: 'Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'. Mapping to JetBridge: 10 -> approved (rebase-if-needed and merge); 5 -> approved-with-suggestions, which is genuinely a fourth disposition Azure DevOps can express and GitHub cannot \u2014 non-blocking approval WITH comments to answer, i.e. 'merge, and also write responses'; -5 (wait for author) and -10 (reject) -> changes-requested; 0 -> no vote, which is both the initial state and the 'reset feedback' state. Votes are set with PUT .../reviewers/{reviewerId}. Two fields matter for correctness: hasDeclined ('Indicates if this reviewer has declined to review this pull request') and isReapprove ('Indicates if this approve vote should still be handled even though vote didn't change') \u2014 the latter means a PUT of the SAME vote value is a semantically meaningful event, so JetBridge cannot dedupe purely on 'vote value unchanged'. Also note votedFor: group/team reviewers cannot vote directly; a member's vote rolls up into the group vote and is listed under votedFor. If a branch policy adds a group as a required reviewer, the disposition you care about may be the rolled-up group vote, not the human's.",
          "endpoints": [
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "An append-only, pollable log of disposition transitions",
          "exists": "native",
          "detail": "Azure DevOps writes every vote change as a SYSTEM COMMENT THREAD, which gives you an ordered, timestamped, immutable audit log without webhooks. The documented sample response from Pull Request Threads - List shows a thread with commentType 'system', content 'Normal Paulk voted 10', and a properties bag containing CodeReviewThreadType='VoteUpdate', CodeReviewVoteResult='10', CodeReviewVotedByDisplayName, CodeReviewVotedByTfId. The same mechanism records the other lifecycle events with these documented CodeReviewThreadType values: 'MergeAttempt' (properties CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit), 'ReviewersUpdate', and 'RefUpdate' (properties CodeReviewRefName, CodeReviewRefNewHeadCommit, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount, CodeReviewRefUpdatedByTfId). This is materially better than GitHub for JetBridge's sealing requirement: you can reconstruct the entire disposition history of a PR \u2014 who voted what, when, in what order, interleaved with pushes and merge attempts \u2014 from ONE polled endpoint, with no reliance on webhook delivery. Caveat: this property vocabulary appears only in a sample payload, it is not a documented schema, so treat the exact key names as observed-not-contracted and parse defensively.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Knowing WHICH iteration a vote was cast against",
          "exists": "absent",
          "detail": "The API does not tell you. IdentityRefWithVote has no iteration field, and the git.pullrequest.updated service hook payload carries the reviewers array with vote values but no iteration id and no delta. Yet the SERVER clearly knows: the branch policy offers 'Require at least one approval on the last iteration' and 'Require at least one approval on every iteration', which cannot be evaluated without a vote-to-iteration association. So this is a capability the product has and the API does not expose. WORKAROUND (inferred, not documented): correlate timestamps \u2014 take the VoteUpdate system thread's publishedDate and find the greatest iteration whose createdDate precedes it. That is a heuristic and it races with a push landing between vote and event. This matters directly to JetBridge: 'approved' must mean 'approved against revision N', and on Azure DevOps you cannot read that off the wire, you have to derive it. If the design needs certainty, the reliable move is to seal the iteration list at the moment the vote event is observed and record 'latest iteration at time of vote', accepting the race, or to require the vote-reset branch policy (below) so that a stale approval cannot survive a push at all.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "Comment anchoring to a revision (the Radicle requirement)",
          "exists": "partial",
          "detail": "Azure DevOps implements roughly two thirds of what the design wants, natively, and tells you which third it did heuristically. Every PR thread can carry pullRequestThreadContext with three parts. (1) iterationContext { firstComparingIteration, secondComparingIteration } \u2014 'the iteration context being viewed when the thread was created', with the documented subtlety 'If this value is equal to SecondComparingIteration, then this version is the common commit between the source and target branches of the pull request.' So a comment records the exact diff it was written against. (2) changeTrackingId \u2014 'Used to track a comment across iterations. This value can be found by looking at the iteration's changes list. Must be set for pull requests with iteration support. Otherwise, it's not required for legacy pull requests.' This is a DECLARED anchor to a file's identity within an iteration's change list, not a line-number heuristic, and it is required on write. (3) trackingCriteria \u2014 'The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria', carrying origFilePath, origLeftFileStart/End, origRightFileStart/End, and the first/second comparing iterations it was tracked to ('Threads were tracked if this is greater than 0'). That means: the presence of trackingCriteria is an explicit, machine-readable 'this comment was FORWARDED, here is where it came from' signal. GitHub gives you a boolean `outdated` and a possibly-null position; Azure DevOps gives you the original anchor and the forwarding record. WHAT IS MISSING is the other half of the Radicle model: there is no way for the NEXT revision to DECLARE which comments it resolves.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?$iteration={n}&$baseIteration={m}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Re-projecting comment positions onto an arbitrary iteration pair",
          "exists": "native",
          "detail": "GET .../threads accepts two query parameters with verbatim descriptions: '$iteration: If specified, thread positions will be tracked using this iteration as the right side of the diff' and '$baseIteration: If specified, thread positions will be tracked using this iteration as the left side of the diff.' So the server will re-anchor every thread onto any (base, head) iteration pair you name, and mark which ones it had to forward via trackingCriteria. There is no GitHub equivalent \u2014 on GitHub a review comment's position is computed once against the diff hunk it was filed on, and once it goes stale you get position: null and are on your own. For JetBridge this is directly usable: when sealing review/v1 for revision N, fetch threads with $iteration=N&$baseIteration=N-1 (or 0-equivalent for whole-PR) so the sealed record contains positions expressed in the coordinate system of the revision the agent is about to act on.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?$iteration={n}&$baseIteration={m}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "The next revision DECLARING which comments it resolves",
          "exists": "absent",
          "detail": "Not available. Thread resolution on Azure DevOps is imperative and out-of-band: you PATCH a thread's status to one of the CommentThreadStatus values \u2014 unknown, active, fixed ('resolved as fixed'), wontFix, closed, byDesign, pending. There is no field on an iteration, a push, or a commit that says 'this revision resolves threads X, Y, Z', and no linkage from a commit to a thread. JetBridge must build this itself. The good news is that there are two native slots to build it in rather than inventing sidecar storage: (a) the per-thread `properties` PropertiesCollection, an arbitrary key-value bag that round-trips on create and update, so the agent can stamp e.g. jetbridge.resolvedByIteration=7 and jetbridge.resolvedByRevision=<change-id>/<n> onto each thread it claims to have fixed; and (b) the per-PR properties bag (see change-identity finding). The richer status enum is a bonus over GitHub, which only has resolved/unresolved and only via GraphQL \u2014 Azure DevOps can distinguish fixed from wontFix from byDesign in REST, which maps well onto an agent reporting HOW it disposed of each comment.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1",
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "THE SELF-TRIGGER PROBLEM \u2014 does the agent's own reply re-trigger the loop?",
          "exists": "native",
          "detail": "This is where Azure DevOps is structurally BETTER than GitHub, and it fixes the exact failure the team proved live. On GitHub the agent's reply to a review comment is filed under a NEW review object with non-null submitted_at, so a 'submitted review' trigger fires on the agent's own writing. On Azure DevOps that cannot happen, because comments and votes are DIFFERENT RESOURCES with different event channels: posting a comment or a reply appends a Comment to a thread (POST .../threads or POST .../threads/{id}/comments) and does not create, change or touch any reviewer vote; it fires only ms.vss-code.git-pullrequest-comment-event ('A pull request is commented on'). Vote changes fire a different event entirely, git.pullrequest.updated, whose notificationType filter has the documented value ReviewerVoteNotification ('The votes score changes'). So if JetBridge defines the disposition trigger as a vote transition, the agent's textual replies CANNOT re-trigger it \u2014 by construction, not by author-filtering heuristics. That is the single strongest argument for Azure DevOps as the forge for this loop. Residual vectors that still need author filtering: (1) the agent's own push mints an iteration and fires notificationType=PushNotification \u2014 filter on iteration.author.id / iteration.push.pushedBy.id; (2) if the agent is itself a reviewer and sets its own vote, that fires ReviewerVoteNotification \u2014 filter on reviewer id; (3) isReapprove means an unchanged-value vote PUT can still be delivered; (4) for the comment-only disposition, where the trigger necessarily IS a comment event, the agent's own replies do fire it \u2014 filter on comment.author.id, which the payload carries.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1",
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "A NEW self-trigger vector unique to Azure DevOps: policy-driven vote resets on push",
          "exists": "native",
          "detail": "Azure DevOps can be configured so that the AGENT'S OWN PUSH mutates reviewer votes, which then fires a vote-change event that the agent's own action caused. The 'Minimum number of reviewers' branch policy has a 'When new changes are pushed' group with these documented options: 'Require at least one approval on every iteration' (available in Azure DevOps Server 2022.1 and higher), 'Require at least one approval on the last iteration', 'Reset all approval votes (does not reset votes to reject or wait)' \u2014 'to remove all approval votes, but keep votes to reject or wait, whenever the source branch changes', and 'Reset all code reviewer votes' \u2014 'to remove all reviewer votes whenever the source branch changes, including votes to approve, reject, or wait'. The az CLI exposes this as --reset-on-source-push. Consequence for JetBridge: under either reset option, the agent pushing a revision causes votes to drop to 0, generating ReviewerVoteNotification events attributable to the agent's push, not to a human. The loop must correlate a vote transition to 0 arriving immediately after its own push and suppress it. Positively, though, 'Require at least one approval on every iteration' is a real gift: it means a stale approval cannot be inherited across a revision, which is precisely the invariant the design wants ('approved' must mean 'approved against THIS revision') and which no GitHub setting enforces per-revision. Related policies worth setting: 'Prohibit the most recent pusher from approving their own changes' \u2014 'Selecting this option means the most recent pusher's vote doesn't count' \u2014 which cleanly prevents the agent from self-approving, and 'Allow requestors to approve their own changes'.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops"
          ]
        },
        {
          "capability": "Event/trigger plumbing (service hooks) and payload fidelity",
          "exists": "partial",
          "detail": "Three relevant events, all publisher 'tfs'. (1) git.pullrequest.updated \u2014 'A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch', with a notificationType filter whose documented values are PushNotification ('The source branch is updated'), ReviewersUpdateNotification ('The reviewers change'), StatusUpdateNotification ('The status changes'), ReviewerVoteNotification ('The votes score changes'). Additional filters: repository (guid), pullrequestCreatedBy, pullrequestReviewersContains, branch. (2) ms.vss-code.git-pullrequest-comment-event \u2014 'A pull request is commented on'; filters repository, branch only. (3) git.pullrequest.merged \u2014 'A pull request merge is attempted', with mergeResult filter values Succeeded, Unsuccessful, Conflicts, Failure, RejectedByPolicy. PAYLOAD LIMITATIONS, which matter: git.pullrequest.updated carries the full PR resource including the reviewers array WITH vote values, but carries NO iteration id and NO delta \u2014 it does not tell you which reviewer changed or from what. You must diff against your own stored prior state, and you must resolve the iteration separately. The comment event does carry the comment object (id, parentCommentId, author, content, publishedDate, lastUpdatedDate, lastContentUpdatedDate, commentType) with the thread id reachable via _links.self, and it fires on EDITS as well as creations ('has edited a pull request comment'), so an edit of an old comment looks like new feedback unless you compare publishedDate against lastContentUpdatedDate. Given all this, I would drive JetBridge primarily off polling the threads endpoint (which yields the full ordered VoteUpdate/RefUpdate log) and treat service hooks as a latency optimisation, not the source of truth.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/hooks/subscriptions?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "Stable CHANGE IDENTITY independent of commit SHA (the jj change-id requirement)",
          "exists": "partial",
          "detail": "Two halves, one native and one buildable. NATIVE: pullRequestId is a stable integer identity that survives force-push, rebase, conflict resolution and retarget \u2014 every iteration hangs off it, and the documented force-push behaviour means the identity plus its full revision history is preserved across history rewrites. GitPullRequest also exposes artifactId with a documented construction template: 'To generate an artifact ID for a pull request, use this template: vstfs:///Git/PullRequestId/{projectId}/{repositoryId}/{pullRequestId}', which is a stable global URI for the change. That is already a better change identity than GitHub offers. WHAT IT IS NOT: it is a forge-assigned identity scoped to one PR, not a jj-style change-id the agent controls that lives in the commit and can move between PRs, survive abandon-and-recreate, or be carried into a different repository. BUILDABLE: Azure DevOps has a native slot GitHub lacks entirely \u2014 a per-PR external property bag. PATCH .../pullRequests/{id}/properties with Content-Type application/json-patch+json, ops add/replace/remove, path e.g. /jetbridgeChangeId, returns a PropertiesCollection where values keep their type ($type System.String / System.Int32 / System.DateTime, Byte[] as base64). The documented behaviour: 'For add operation, the path can be empty. If the path is empty, the value must be a list of key value pairs. For replace operation, the path cannot be empty. If the path does not exist, the property will be added.' The system already uses this bag for Microsoft.Git.PullRequest.SourceRefName / TargetRefName. So JetBridge can write its own change-id and its sealed review/v1 record pointers directly onto the PR, natively, with no sidecar store. No documented size limit was found for property values \u2014 treat that as uncertain and store pointers, not payloads.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/properties?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/properties?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request-by-id?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Merge / completion under agent control (the 'approved' disposition)",
          "exists": "native",
          "detail": "Completing a PR is a PATCH on the PR setting status=completed with lastMergeSourceCommit, plus completionOptions. GitPullRequestCompletionOptions is documented in full: mergeStrategy (GitPullRequestMergeStrategy: noFastForward 'A two-parent, no-fast-forward merge. The source branch is unchanged. This is the default behavior.'; squash; rebase 'Rebase the source branch on top of the target branch HEAD commit, and fast-forward the target branch. The source branch is updated during the rebase operation.'; rebaseMerge), deleteSourceBranch, mergeCommitMessage, bypassPolicy, bypassReason, transitionWorkItems, autoCompleteIgnoreConfigIds. Note squashMerge is deprecated \u2014 'It is recommended that you explicitly set MergeStrategy in all cases.' Auto-complete is available (autoCompleteSetBy) and is the closest thing to a merge queue \u2014 it completes the PR once required reviewers approve and required policies pass. mergeStatus (PullRequestAsyncStatus: notSet/queued/conflicts/succeeded/rejectedByPolicy/failure) tells the agent whether a rebase is needed before merging, and mergeFailureType (none/unknown/caseSensitive/objectTooLarge) categorises failures. Importantly for the design's 'rebase if needed then merge' step: mergeStrategy=rebase or rebaseMerge lets the SERVER do the rebase at completion time, so the agent often does not need to rebase-and-force-push at all \u2014 which avoids minting a spurious iteration and avoids tripping any vote-reset policy.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request-by-id?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Blocking on unresolved comment threads",
          "exists": "native",
          "detail": "There is a first-class branch policy: 'The Check for comment resolution policy checks whether all PR comments are resolved', settable to Required (blocking) or Optional, and manageable via az repos policy comment-required with --blocking. This is useful to JetBridge because it makes the agent's comment-disposition work load-bearing on merge rather than advisory: if the agent must move every thread out of 'active' before completion can happen, the platform gets a hard, forge-enforced check that the agent actually addressed each item, and the thread status it chose (fixed / wontFix / byDesign) is a machine-readable record of HOW. GitHub has no equivalent branch protection rule \u2014 conversation resolution is a repository setting on GitHub but it is not expressible with the same status vocabulary.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1",
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops"
          ]
        },
        {
          "capability": "Azure DevOps Services (cloud) vs Azure DevOps Server (on-prem) \u2014 api-version support",
          "exists": "partial",
          "detail": "The authoritative mapping is on the REST API landing page and it is the one to design against, because the older rest-api-versioning page's table stops at 7.0 and omits 7.1/7.2 entirely (a documentation inconsistency worth knowing about). Landing-page table: Azure DevOps Server vNext -> 7.2; Azure DevOps Server 2022.1 -> 7.1 (builds >= 19.225.34309.2); Azure DevOps Server 2022 -> 7.0 (builds >= 19.205.33122.1); Azure DevOps Server 2020 -> 6.0; Azure DevOps Server 2019 -> 5.0; TFS 2018 Update 2/3 -> 4.1. It states 'REST API versions are compatible with the Server version listed, as well as Server versions that are newer.' Every iteration/threads/reviewers/properties endpoint above carries an azure-devops-server-rest-7.1 moniker but NO azure-devops-server-rest-7.2 moniker, confirming 7.2 is Services/vNext only. On-prem instance URL form is {server:port}/tfs/{collection} rather than dev.azure.com/{organization}. Practical guidance: pin 7.1 as the floor if you need Azure DevOps Server 2022.1+, and 7.0 if you must support Azure DevOps Server 2022 RTW. Iterations themselves are old \u2014 the endpoints are documented back to api-version 4.1 (TFS 2018 Update 2) \u2014 so the revision noun is available on essentially every supported on-prem release; it is only the newest field additions that are version-sensitive. Two capabilities are explicitly NOT on-prem: the Azure DevOps CLI ('Azure DevOps CLI commands aren't supported for Azure DevOps Server'), and the Git limits page is scoped to Azure DevOps Services only. 'Require at least one approval on every iteration' requires Azure DevOps Server 2022.1 or higher.",
          "endpoints": [
            "GET https://{server}:8080/tfs/{collection}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/?view=azure-devops-rest-7.2",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Where the REST API and the web UI disagree",
          "exists": "partial",
          "detail": "Four concrete divergences worth writing down. (1) TERMINOLOGY: the API says 'iteration'; the UI says 'Updates' (the Updates tab) and 'changesets' (the changes dropdown). The UI names each changeset with 'the commit message from the final commit in each push operation', i.e. the UI label is derived, not the iteration's `description` field. Anyone reading UI screenshots and API docs together will think these are different features. (2) UPDATES vs COMMITS: documented explicitly \u2014 the Updates tab preserves force-pushed history, the Commits tab does not, and 'the commits shown in the Commits tab might differ from the commits shown in the Updates tab'. Two different truths in the same UI, and only one of them (Updates/iterations) is durable. (3) REVIEW LANGUAGE WITHOUT A REVIEW OBJECT: current UI copy says Copilot 'always leaves a Comment review', importing GitHub's vocabulary onto a model that has no review resource \u2014 the API will never show you a review. (4) The UI can diff any two updates by multi-select ('Hold the Shift key when selecting multiple changesets'); the API can only express a contiguous pair via a single $compareTo, so a UI multi-select of non-adjacent changesets is not reproducible in one API call. DOCS ARE THIN in three specific places: every IterationReason value has an empty description; the [Flags] nature is documented only on a 2018 .NET page and resolveConflicts is missing from it; and the CodeReview* thread-property vocabulary (VoteUpdate, MergeAttempt, RefUpdate, ReviewersUpdate, CodeReviewVoteResult, ...) exists only inside a sample payload with no schema behind it.",
          "endpoints": [],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/dotnet/api/microsoft.teamfoundation.sourcecontrol.webapi.iterationreason",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        }
      ],
      "absent": [
        "A submitted-REVIEW object. There is no review resource, no review id, no submitted_at, no review body, and no endpoint analogous to GET /pulls/{n}/reviews. Comments and votes are unrelated resources and nothing joins them into a reviewer's completed pass.",
        "Batched / draft / pending comments. Azure DevOps has no 'start a review, queue N comments, submit them together' flow in the API or the UI. Every comment is published the instant it is created. (CommentThreadStatus has a value 'pending' but that is a thread RESOLUTION status meaning 'under review and awaits something else', not an unpublished-draft state.)",
        "Any per-iteration git ref. There is no Gerrit refs/changes/NN/CCCC/P analogue and no refs/pull/{id}/iterations/{n}. Iterations are database objects, not git refs.",
        "A documented refs/pull/{pullRequestId}/head ref on Azure Repos. Only refs/pull/{pullRequestId}/merge is documented (via Build.SourceBranch = 'refs/pull/1/merge'), and even that does not appear in the Refs - List API's unfiltered sample response.",
        "Any documented object-retention or garbage-collection guarantee for commits that a force-push made unreachable. The iteration METADATA is documented as surviving; the reachability of the underlying git objects is documented nowhere.",
        "A vote-to-iteration association in the API. IdentityRefWithVote has no iteration field and the git.pullrequest.updated payload carries no iteration id, even though branch policies ('Require at least one approval on the last iteration' / 'on every iteration') prove the server tracks it internally.",
        "A delta in the git.pullrequest.updated webhook payload. It ships the whole PR resource including reviewers[].vote but never says which reviewer changed, from what value, or against which iteration.",
        "A declared-resolution mechanism: no field on an iteration, push, or commit that names the comment threads that revision resolves. Thread status must be set imperatively per thread.",
        "A textual diff or patch representation. No unified-diff / patch media type exists anywhere in the Azure DevOps Git REST API \u2014 iteration changes and the Diffs API both return change LISTS (paths, blob objectIds, change types) only.",
        "A per-comment immutable commit OID anchor. Threads anchor to (filePath, line/offset, iteration pair, changeTrackingId), never to a commit SHA. There is no Radicle-style pin-to-an-OID.",
        "A jj-style agent-controlled change-id living in the commit. pullRequestId and artifactId are stable forge-assigned identities for the PR, but nothing carries change identity in the git objects themselves or across PR boundaries.",
        "Per-value documentation for IterationReason. Every one of push, forcePush, create, rebase, unknown, retarget, resolveConflicts has an empty description in every api-version of the REST reference, and resolveConflicts is absent entirely from the .NET enum reference.",
        "A merge queue. Auto-complete is the nearest native feature and it is a per-PR deferred completion, not a serialised queue with speculative testing."
      ],
      "notes": "BOTTOM LINE ON THE HEADLINE QUESTION: yes, Azure DevOps has a real revision noun, and on the revision axis it sits much closer to Gerrit than to GitHub \u2014 it is roughly 70% of the way there. An iteration is a numbered, individually addressable, API-immutable object that names its own base as a full (sourceRefCommit, targetRefCommit, commonRefCommit) triple, records WHY it exists (push / forcePush / create / rebase / retarget / resolveConflicts), records target-branch retargets with both old and new ref names, exposes its own change list and commit list, and \u2014 documented, not inferred \u2014 survives force-push while the flat commit list does not. Two of the three things the team decided to borrow are therefore already built and should NOT be reimplemented: the ordered series of immutable revisions each naming its own base (iterations), and revision-to-revision diffing ($compareTo). A third is two-thirds built: comment anchoring already carries a declared per-file anchor (changeTrackingId), the exact diff the comment was written against (iterationContext), and an explicit machine-readable record of whether and from where the server forwarded it (trackingCriteria) \u2014 strictly better than GitHub's outdated-boolean-and-null-position.\n\nWHERE IT FALLS SHORT OF GERRIT, precisely: iterations are database rows, not git refs. Gerrit's patch sets are refs/changes/NN/CCCC/P \u2014 real, fetchable, GC-protected. Azure DevOps publishes no per-iteration ref at all, only a single mutable refs/pull/{id}/merge, and documents no retention policy for objects a force-push orphaned. So the metadata is durable but the TREES may not be. If JetBridge needs Gerrit-grade replayability of old revisions, the fix is cheap and it should just do it: push its own retention ref (refs/jetbridge/{change-id}/{n}) at seal time. That single move closes the only structural gap between Azure DevOps iterations and Gerrit patch sets.\n\nTHE BIG SURPRISE, AND IT IS GOOD NEWS: Azure DevOps structurally solves the self-trigger bug that was proven live on GitHub. On GitHub the agent's reply is filed under a new review object with a non-null submitted_at, so a review-submitted trigger fires on the agent's own writing \u2014 the bug is inherent to GitHub's data model. On Azure DevOps comments and votes are separate resources on separate event channels: a reply appends a Comment and fires only ms.vss-code.git-pullrequest-comment-event; a disposition change fires git.pullrequest.updated with notificationType=ReviewerVoteNotification. Define the trigger as a vote transition and the agent's replies cannot re-trigger it, by construction rather than by author-filtering heuristics. Three residual vectors remain and must be handled explicitly: the agent's own push (filter on iteration.author.id / push.pushedBy.id), the isReapprove flag (an unchanged-value vote PUT is still a real event, so do not dedupe on value equality alone), and \u2014 new, and unique to this forge \u2014 the 'Reset all approval votes' / 'Reset all code reviewer votes' branch policies, under which the agent's own push causes the platform to zero every vote and emit vote-change events the agent itself caused.\n\nTHE BIG REGRESSION: there is no review object, so the design's central premise \u2014 'the trigger is a completed human review, not an individual comment' \u2014 has no native carrier. Approved and changes-requested survive the port cleanly, because a vote transition is a discrete, well-typed, timestamped event with five levels rather than GitHub's three (10 approved / 5 approved-with-suggestions / 0 no vote / -5 waiting for author / -10 rejected; the 5 level is a genuine fourth disposition GitHub cannot express \u2014 merge AND answer the comments). But COMMENT-ONLY has no completion signal whatsoever. On GitHub a comment-only review is a discrete object with a submitted_at; on Azure DevOps it is an unbounded stream of individual thread events with no 'I am done' marker. JetBridge must invent that boundary \u2014 a quiet-period debounce, an explicit reviewer convention, or requiring a 0/-5 vote to close the pass \u2014 and should treat it as a first-class design decision rather than an implementation detail, because it is the one place the model genuinely does not port.\n\nTWO NATIVE SLOTS TO EXPLOIT THAT GITHUB DOES NOT HAVE. First, the per-PR external property bag (PATCH .../pullRequests/{id}/properties, application/json-patch+json) \u2014 an arbitrary typed key-value store attached to the PR, already used by the system itself for Microsoft.Git.PullRequest.SourceRefName. That is a native home for JetBridge's change-id and its sealed review/v1 pointers, with no sidecar storage. Threads have their own properties bag too, which is where the missing 'this revision resolves threads X, Y, Z' declaration should live. Second, and underrated: every vote change is ALSO written as a system comment thread carrying CodeReviewThreadType=VoteUpdate and CodeReviewVoteResult, alongside RefUpdate and MergeAttempt threads. That means the complete, ordered, timestamped disposition history of a PR is reconstructible from a SINGLE polled endpoint with no dependence on webhook delivery \u2014 which matters a lot for a system whose whole premise is sealing an immutable record. I would build the trigger on polling GET .../threads as the source of truth and treat service hooks purely as a latency optimisation, both because the webhook payload carries no delta and no iteration id, and because the thread log is append-only and replayable.\n\nCONFIDENCE AND SHARP EDGES. Three things I would flag to whoever writes the client. (1) IterationReason is a [Flags] enum with Push=0 \u2014 documented only on a 2018 .NET reference page, absent from every REST doc, and with resolveConflicts missing from it entirely. Reasons can combine, and 'push' is the zero value. Model it as a flag set, never a closed single-valued enum. (2) commonRefCommit is the merge base, but GitPullRequest.hasMultipleMergeBases exists ('Multiple mergebases warning'), so in criss-cross histories it is ONE server-chosen merge base, not a canonical one; seal that flag alongside it. (3) An iteration captures targetRefCommit at creation time, and nothing suggests a plain advance of the target branch mints a new iteration \u2014 so 'no new iteration' does not mean 'the base has not moved', and the live merge preview (lastMergeTargetCommit / lastMergeCommit / mergeStatus) can drift away from the newest iteration's recorded target without any revision boundary. Version floor: pin api-version 7.1 for Azure DevOps Server 2022.1+, 7.0 for Server 2022 RTW; 7.2 is Services/vNext only (no azure-devops-server-rest-7.2 moniker exists). Iterations themselves are available back to api-version 4.1, so the revision noun is safe on essentially every supported on-prem release."
    },
    "checks": [
      {
        "area": "iterations",
        "claims_checked": [
          {
            "claim": "GitPullRequestIteration is a distinct REST resource with int32 id, listable and individually GETtable at .../pullRequests/{id}/iterations[/{iterationId}]?api-version=7.1, with the exact field set listed (_links, author, changeList, commits, commonRefCommit, createdDate, description, hasMoreCommits, id, newTargetRefName, oldTargetRefName, push, reason, sourceRefCommit, targetRefCommit, updatedDate).",
            "status": "CONFIRMED",
            "evidence": "Pull Request Iterations - List (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1) and Get (.../pull-request-iterations/get?view=azure-devops-rest-7.1). Routes, api-version 7.1, and the `includeCommits` query param match exactly. The GitPullRequestIteration definition table lists all 16 fields with the exact names claimed, and the type description reads verbatim: 'Provides properties that describe a Git pull request iteration. Iterations are created as a result of creating and pushing updates to a pull request.' hasMoreCommits reads verbatim 'Indicates if the Commits property contains a truncated list of commits in this pull request iteration.' Both pages are GA at 7.1 and both list an azure-devops-server-rest-7.1 sibling. One nit: 'monotonically numbered from 1' is not stated; the only numbering statement is the iterationId param on the Iteration Changes page ('Allowed values are between 1 and the maximum iteration on this pull request')."
          },
          {
            "claim": "GitPullRequest.supportsIterations exists with the quoted description, and GET .../_apis/git/pullrequests/{pullRequestId}?api-version=7.1 is a real route.",
            "status": "CONFIRMED",
            "evidence": "Pull Requests - Get Pull Request By Id (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request-by-id?view=azure-devops-rest-7.1). Route is exactly `GET https://dev.azure.com/{organization}/{project}/_apis/git/pullrequests/{pullRequestId}?api-version=7.1`. supportsIterations description is verbatim: 'If true, this pull request supports multiple iterations. Iteration support means individual pushes to the source branch of the pull request can be reviewed and comments left in one iteration will be tracked across future iterations.' The sample response includes \"supportsIterations\": true."
          },
          {
            "claim": "Iteration 1 is the head of the source branch at PR creation; subsequent iterations are created by pushes to the source branch (quoted from the iterationId path-parameter description).",
            "status": "CONFIRMED",
            "evidence": "Pull Request Iteration Changes - Get (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1). The iterationId row reads verbatim: 'ID of the pull request iteration.  Iteration one is the head of the source branch at the time the pull request is created and subsequent iterations are created when there are pushes to the source branch. Allowed values are between 1 and the maximum iteration on this pull request.' Exact match, including the double space."
          },
          {
            "claim": "'The IterationReason enum additionally proves that force-push, rebase, retarget (target-branch change) and conflict resolution also mint iterations.'",
            "status": "OVERSTATED",
            "evidence": "The enum values exist, but every single one has an EMPTY description in REST 7.1 (verified on both the List and Get pages). An enum of reason names is not a statement that those events create iterations. Microsoft documents only two creation triggers (PR creation, pushes to the source branch). That force-push/rebase/retarget/resolveConflicts mint iterations is a reasonable inference, not something the docs 'prove'. The claim carries confidence:'documented' for the whole item, which is too strong; the target-branch-advance asymmetry inside the same claim is correctly self-labelled as absent from the docs."
          },
          {
            "claim": "REST 7.1 documents seven IterationReason values \u2014 push, forcePush, create, rebase, unknown, retarget, resolveConflicts \u2014 with empty descriptions for every one.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 \u2014 IterationReason table lists exactly those seven camelCase values in that order, Description column blank for all seven. Identical on the Get page."
          },
          {
            "claim": "The .NET IterationReason enum is declared [System.Flags] with Push=0, ForcePush=1, Create=2, Rebase=4, Unknown=8, Retarget=16, and ResolveConflicts does not appear on that page (last updated 2018).",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/dotnet/api/microsoft.teamfoundation.sourcecontrol.webapi.iterationreason \u2014 page states 'This enumeration supports a bitwise combination of its member values', shows `[System.Flags] public enum IterationReason`, Attributes: FlagsAttribute, and a Fields table of exactly Push 0 / ForcePush 1 / Create 2 / Rebase 4 / Unknown 8 / Retarget 16, all with empty descriptions. ResolveConflicts is absent from both the Fields table and the api_name list. Page metadata: updated_at 2018-12-10. Every numeric value in the claim checks out."
          },
          {
            "claim": "Combined flag values serialize over the wire as a comma-separated camelCase string (e.g. ForcePush|Rebase), and resolveConflicts is presumably 32.",
            "status": "UNVERIFIABLE",
            "evidence": "No Microsoft Learn page documents the JSON serialization of a combined IterationReason, and no sample payload on any of the iteration pages shows a multi-valued `reason`. The claim's own '(presumably 32)' is correctly hedged, but the comma-separated-string statement is presented as expectation-of-fact with no doc backing. The actionable part \u2014 do not model `reason` as a closed single-valued enum \u2014 is sound reasoning from the [Flags] attribute; the wire format is not."
          },
          {
            "claim": "Every iteration carries sourceRefCommit / targetRefCommit / commonRefCommit with the quoted descriptions, and the Diffs API describes the merge base in the same words and returns it as `commonCommit`.",
            "status": "CONFIRMED",
            "evidence": "Pull Request Iterations - Get: sourceRefCommit 'The source Git commit of this iteration.', targetRefCommit 'The target Git commit of this iteration.', commonRefCommit 'The first common Git commit of the source and target refs.' \u2014 all verbatim. Diffs - Get (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/diffs/get?view=azure-devops-rest-7.1) operation description is verbatim 'Find the closest common commit (the merge base) between base and target commits, and get the diff between either the base and target commits or common and target commits.' GitCommitDiffs has a `commonCommit` field (description blank in the schema table, but present and populated in all three documented samples). The route `.../_apis/git/repositories/{repositoryId}/diffs/commits?baseVersion=&baseVersionType=commit&targetVersion=&targetVersionType=commit&api-version=7.1` matches the documented 'Between commit IDs' sample exactly; GitVersionType accepts branch|tag|commit."
          },
          {
            "claim": "hasMultipleMergeBases exists, described only as 'Multiple mergebases warning'; in criss-cross history commonRefCommit is then ONE of several merge bases, chosen by the server, not canonical.",
            "status": "OVERSTATED",
            "evidence": "First half CONFIRMED: Get Pull Request By Id lists hasMultipleMergeBases with the description exactly 'Multiple mergebases warning' (and it also appears in the Pull Requests - Update request body). Second half UNVERIFIABLE: no Microsoft page states what commonRefCommit contains when multiple merge bases exist, or that the server picks arbitrarily among them. That is inference from Git semantics, not documentation. The defensive recommendation (seal the flag, don't treat the base as canonical) is good engineering; the mechanism attributed to Azure DevOps is not documented."
          },
          {
            "claim": "The Azure Repos review doc states verbatim that a force-pushed changeset doesn't overwrite changeset history, and that the Commits tab IS overwritten on force-push.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops (ms.date 2026-05-27), Review changes step 6: 'The changesets are numbered and the most recent changeset appears at the top of the list. Each changeset shows the commits that were pushed in that push operation. A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset.' Step 7: 'The commit history in the Commits tab is overwritten if the PR author force-pushes a different commit history, so the commits shown in the Commits tab might differ from the commits shown in the Updates tab.' Both quotes are exact. Page monikers cover azure-devops, azure-devops-server and azure-devops-2022."
          },
          {
            "claim": "Iteration N remains individually GETtable by id, with its change list and commit list fetchable, after any number of subsequent force-pushes; iterations are 'immutable by API surface as well as by observed behaviour' because there is no PATCH/PUT/DELETE on the iterations route.",
            "status": "OVERSTATED",
            "evidence": "The doc quotes support a UI statement about the Updates tab's changeset list. Nothing in the REST reference or the conceptual doc states that iteration N stays individually retrievable via REST after a force-push, and nothing anywhere equates the UI noun 'changeset' with the REST noun 'iteration' \u2014 the review-pull-requests page never uses the word 'iteration' in that section, and the REST pages never use 'changeset'. That bridge is near-certainly true but is undocumented inference presented as 'explicitly documented \u2014 not inferred'. The 'no documented mutator' point is an argument from absence: I verified only Get and List exist for Pull Request Iterations; I did not exhaustively enumerate the operation TOC. Note also the branch-policies page DOES use 'iteration' in the UI ('Require at least one approval on every iteration'), so the vocabulary is inconsistent across Microsoft's own docs."
          },
          {
            "claim": "Whether git objects of a superseded iteration remain fetchable after force-push is unresolved in the docs; the Git limits page documents size/push/path limits and says nothing about object retention.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops documents exactly: rate limiting, repository size (250 GB hard / 10 GB recommended), the pack-file limit error, 100 MB file size, 5 GB push size, LFS exclusion, path length 32,766 / component 4,096, and error VS403729. There is no mention of garbage collection, pruning, unreachable objects, or any retention window. The claim's 'uncertain' confidence is correctly assigned. Two additions worth carrying: (a) that page's moniker list is `azure-devops` only, i.e. it is Services-scoped and says nothing about Server; (b) the Items fallback route is real \u2014 Items - Get (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/items/get?view=azure-devops-rest-7.1) documents `versionDescriptor.version` and `versionDescriptor.versionType` with GitVersionType values branch|tag|commit."
          },
          {
            "claim": "GET .../iterations/{iterationId}/changes supports $compareTo (default 0 = compare against the common commit), $top (default 100, max 2000), $skip, and returns changeEntries/nextSkip/nextTop with the quoted descriptions.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1. $compareTo verbatim: 'ID of the pull request iteration to compare against. The default value is zero which indicates the comparison is made against the common commit between the source and target branches'. $top verbatim: 'Optional. The number of changes to retrieve. The default value is 100 and the maximum value is 2000.' $skip verbatim. nextSkip/nextTop verbatim: 'Value to specify as skip/top to get the next page of changes. This will be zero if there are no more changes.' The documented 'Changes since an earlier iteration' sample uses `$compareTo=1` and returns an entry with both objectId and originalObjectId and changeType 'edit', exactly as described. Minor: the {objectId, originalObjectId, path} shape of `item` appears only in samples \u2014 the schema types GitPullRequestChange.item as 'string (T)' \u2014 which the claim does acknowledge."
          },
          {
            "claim": "Azure DevOps records a target-branch change as its own iteration with reason=retarget, and PATCH on the PR is the way to retarget.",
            "status": "OVERSTATED",
            "evidence": "The field descriptions are verbatim exact: oldTargetRefName 'If the iteration reason is Retarget, this is the original target refName', newTargetRefName 'If the iteration reason is Retarget, this is the refName of the new target'. `retarget` is a documented IterationReason value. But no Microsoft page states that retargeting CREATES an iteration \u2014 that is inferred from the conditional wording of two field descriptions, exactly the same inference gap flagged above. Additionally the claim omits a documented conditional: Pull Requests - Update (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1) lists the updatable properties as Status, Title, Description, CompletionOptions, MergeOptions, AutoCompleteSetBy.Id, and 'TargetRefName (when the PR retargeting feature is enabled)'. Retarget-by-API is therefore feature-gated per the docs. (Route is documented lowercase: `PATCH .../_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1`.)"
          },
          {
            "claim": "IdentityRefWithVote.vote is documented as '10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'; votes are set with PUT .../reviewers/{reviewerId}; hasDeclined, isReapprove and votedFor exist with the quoted descriptions.",
            "status": "CONFIRMED",
            "evidence": "Pull Request Reviewers - Create Pull Request Reviewer (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1) \u2014 method is PUT, route matches exactly, operation summary is 'Add a reviewer to a pull request or cast a vote', and the vote description is verbatim character-for-character. isReapprove verbatim: 'Indicates if this approve vote should still be handled even though vote didn't change.' hasDeclined and votedFor verbatim (votedFor includes 'Groups and teams can be reviewers on pull requests but can not vote directly. When a member of the group or team votes, that vote is rolled up into the group or team vote.'). The UI-side five options are corroborated on review-pull-requests: Approve / Approve with suggestions / Wait for author / Reject / Reset feedback, plus the az CLI enum {approve, approve-with-suggestions, reject, reset, wait-for-author}. Not independently verified: the sibling GET .../reviewers listing endpoint, which I did not fetch."
          },
          {
            "claim": "Vote changes, merge attempts, reviewer changes and ref updates are written as system comment threads with CodeReviewThreadType = VoteUpdate / MergeAttempt / ReviewersUpdate / RefUpdate and the named property keys.",
            "status": "CONFIRMED",
            "evidence": "The Pull Request Threads - List sample response (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1) contains all four thread types with exactly the property keys claimed: VoteUpdate with CodeReviewVoteResult '10' / CodeReviewVotedByDisplayName / CodeReviewVotedByTfId and content 'Normal Paulk voted 10'; MergeAttempt with CodeReviewMergeCommit / CodeReviewMergeStatus / CodeReviewSourceCommit / CodeReviewTargetCommit; ReviewersUpdate; RefUpdate with CodeReviewRefName / CodeReviewRefNewHeadCommit / CodeReviewRefNewCommits / CodeReviewRefNewCommitsCount / CodeReviewRefUpdatedByTfId. commentType 'system' is a documented CommentType value. The claim's own caveat is exactly right and load-bearing: this vocabulary exists ONLY in a sample payload, never as a schema, so it is observed-not-contracted."
          },
          {
            "claim": "This system-thread log is an 'ordered, timestamped, immutable audit log' / 'append-only'.",
            "status": "OVERSTATED",
            "evidence": "Ordered and timestamped: supported (publishedDate / lastUpdatedDate on every thread). Immutable / append-only: not documented anywhere. GitPullRequestCommentThread carries a mutable `lastUpdatedDate` and an `isDeleted` flag ('Specify if the thread is deleted which happens when all comments are deleted'), and a Pull Request Threads - Update (PATCH) operation exists in the same API family. Nothing states that system-generated threads are exempt from mutation or deletion. Treat 'reconstruct the whole disposition history from one polled endpoint' as plausible-and-useful, not as a documented durability guarantee."
          },
          {
            "claim": "pullRequestThreadContext gives iterationContext {firstComparingIteration, secondComparingIteration}, a required-on-write changeTrackingId, and trackingCriteria as an explicit machine-readable 'this comment was forwarded' signal \u2014 all with the quoted descriptions.",
            "status": "CONFIRMED",
            "evidence": "Threads List and Threads Create (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1) both carry these verbatim. firstComparingIteration: 'The iteration of the file on the left side of the diff when the thread was created. If this value is equal to SecondComparingIteration, then this version is the common commit between the source and target branches of the pull request.' changeTrackingId: 'Used to track a comment across iterations. This value can be found by looking at the iteration's changes list. Must be set for pull requests with iteration support. Otherwise, it's not required for 'legacy' pull requests.' trackingCriteria: 'The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria.' CommentTrackingCriteria carries origFilePath, origLeftFileStart/End, origRightFileStart/End, and first/secondComparingIteration each noting 'Threads were tracked if this is greater than 0.' The Create page's documented sample sends pullRequestThreadContext with changeTrackingId:1 and iterationContext {1,2}, confirming it is a write-time input."
          },
          {
            "claim": "GET .../threads accepts $iteration ('right side of the diff') and $baseIteration ('left side of the diff') to re-anchor thread positions onto an arbitrary iteration pair.",
            "status": "CONFIRMED",
            "evidence": "Threads - List URI Parameters, verbatim: '$iteration \u2014 If specified, thread positions will be tracked using this iteration as the right side of the diff.' and '$baseIteration \u2014 If specified, thread positions will be tracked using this iteration as the left side of the diff.' Present identically on the Azure DevOps SERVER REST 7.1 variant (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-server-rest-7.1), so this is not a cloud-only capability."
          },
          {
            "claim": "There is no way for the next revision to DECLARE which comments it resolves; resolution is imperative via PATCH of thread status to one of unknown/active/fixed/wontFix/closed/byDesign/pending, and the per-thread `properties` bag is a native slot for building it.",
            "status": "CONFIRMED",
            "evidence": "CommentThreadStatus enum matches exactly, including the descriptions 'The thread status is resolved as fixed.' / 'resolved as won't fix.' / 'resolved as by design.' GitPullRequestCommentThread.properties is documented as 'Optional properties associated with the thread as a collection of key-value pairs' (PropertiesCollection) and appears in BOTH the Create request body and the List response, so it does round-trip. I found no commit\u2192thread or iteration\u2192thread resolution linkage anywhere in the Git REST 7.1 surface. Two caveats the claim misses: (1) I did not independently fetch Pull Request Threads - Update, so the PATCH route shape is asserted by family convention here, not verified; (2) REST and UI disagree on this vocabulary \u2014 review-pull-requests lists the user-facing statuses as Active / Pending / Resolved / Won't fix / Closed, i.e. UI 'Resolved' is REST `fixed`, and 'By design' does not appear in the current UI doc at all. An agent writing byDesign is writing a status the current UI documentation does not surface."
          },
          {
            "claim": "Comments and votes are different resources on different event channels: comments fire only ms.vss-code.git-pullrequest-comment-event, vote changes fire git.pullrequest.updated with notificationType=ReviewerVoteNotification \u2014 so a vote-defined trigger CANNOT be re-fired by the agent's own replies, by construction.",
            "status": "OVERSTATED",
            "evidence": "The event catalogue is CONFIRMED (https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops): ms.vss-code.git-pullrequest-comment-event = 'A pull request is commented on'; git.pullrequest.updated = 'A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch'; notificationType values PushNotification ('The source branch is updated'), ReviewersUpdateNotification ('The reviewers change'), StatusUpdateNotification ('The status changes'), ReviewerVoteNotification ('The votes score changes'). What is NOT documented is the exclusivity: no page says posting a comment cannot also raise git.pullrequest.updated. The claim infers it from comments being absent from the updated-event description \u2014 a fair reading, but 'cannot happen ... by construction, not by author-filtering heuristics' is a stronger guarantee than any Microsoft page issues. Separately UNVERIFIABLE: the residual-vector mitigations name payload fields (iteration.author.id, iteration.push.pushedBy.id, comment.author.id) that the service-hooks events page does not document \u2014 it catalogues events and filters, not payload schemas. Verify those field paths against a live payload before building filters on them."
          },
          {
            "claim": "Branch policy 'Minimum number of reviewers' offers the four 'When new changes are pushed' options as quoted, with 'Require at least one approval on every iteration' available in Azure DevOps Server 2022.1 and higher; plus 'Prohibit the most recent pusher from approving their own changes' and az CLI --reset-on-source-push.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops (ms.date 2026-07-15). Verbatim: 'Require at least one approval on every iteration ... is available in Azure DevOps Server 2022.1 and higher.'; 'Require at least one approval on the last iteration'; 'Reset all approval votes (does not reset votes to reject or wait) to remove all approval votes, but keep votes to reject or wait, whenever the source branch changes.'; 'Reset all code reviewer votes to remove all reviewer votes whenever the source branch changes, including votes to approve, reject, or wait.' Also verbatim: 'Selecting this option means the most recent pusher's vote doesn't count, even if they can ordinarily approve their own changes' (the claim truncates before the trailing clause \u2014 a truncation, not a misquote) and 'Allow requestors to approve their own changes'. az repos policy approver-count documents --reset-on-source-push as a required create parameter. GET .../_apis/policy/configurations?api-version=7.1 is real and GA (https://learn.microsoft.com/en-us/rest/api/azure/devops/policy/configurations/list?view=azure-devops-rest-7.1) \u2014 note that page's own advice to use /_apis/git/policy/configurations for scope filtering instead."
          },
          {
            "claim": "Under a vote-reset policy the agent's own push causes votes to drop to 0, 'generating ReviewerVoteNotification events attributable to the agent's push'.",
            "status": "UNVERIFIABLE",
            "evidence": "Both halves are individually documented (the policy resets votes on source-branch change; ReviewerVoteNotification fires when 'the votes score changes'), but no Microsoft page connects them \u2014 nothing states that a policy-driven reset emits a reviewer-vote notification, nor what identity such an event is attributed to. This is the single most operationally consequential inference in the set (it defines a suppression rule), and it rests entirely on composing two independent doc statements. It needs a live test, not a citation."
          },
          {
            "claim": "artifactId gives a stable global URI via the documented template, and PATCH .../pullRequests/{id}/properties with application/json-patch+json is a native per-PR external property bag usable for a JetBridge change-id.",
            "status": "CONFIRMED",
            "evidence": "artifactId description verbatim on Get Pull Request By Id: 'A string which uniquely identifies this pull request. To generate an artifact ID for a pull request, use this template: vstfs:///Git/PullRequestId/{projectId}/{repositoryId}/{pullRequestId}'. Pull Request Properties - Update (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1): PATCH, Media Types 'application/json-patch+json', Operation enum add/remove/replace/move/copy/test, and the operation description verbatim including 'For add operation, the path can be empty. If the path is empty, the value must be a list of key value pairs. For replace operation, the path cannot be empty. If the path does not exist, the property will be added to the collection.' (the claim's quote drops the trailing 'to the collection'). Samples show Microsoft.Git.PullRequest.SourceRefName / TargetRefName in the returned bag. Server parity confirmed at view=azure-devops-server-rest-7.1. 'No documented size limit' is correct \u2014 I found none."
          },
          {
            "claim": "The properties bag returns values that 'keep their type ($type System.String / System.Int32 / System.DateTime, Byte[] as base64)'.",
            "status": "OVERSTATED",
            "evidence": "The PropertiesCollection type description does say Byte[], Int32, Double, DateType and String 'preserve their type' \u2014 but this API's own documented sample contradicts it for Int32: the request sends {\"op\":\"add\",\"path\":\"/sampleId\",\"value\": 8} (a JSON number) and the 200 response returns \"sampleId\": {\"$type\": \"System.String\", \"$value\": \"8\"}. So an integer written through this endpoint came back as System.String in Microsoft's own example. Do not rely on numeric round-tripping; the claim's advice to store pointers rather than payloads happens to sidestep this anyway."
          },
          {
            "claim": "GitPullRequestCompletionOptions / GitPullRequestMergeStrategy / PullRequestAsyncStatus / PullRequestMergeFailureType are as quoted, squashMerge is deprecated, and mergeStrategy=rebase|rebaseMerge lets the server do the rebase at completion time.",
            "status": "CONFIRMED",
            "evidence": "Get Pull Request By Id definitions. completionOptions fields match (autoCompleteIgnoreConfigIds, bypassPolicy, bypassReason, deleteSourceBranch, mergeCommitMessage, mergeStrategy, squashMerge, transitionWorkItems; the claim omits triggeredByAutoComplete, which is harmless). mergeStrategy values verbatim: noFastForward 'A two-parent, no-fast-forward merge. The source branch is unchanged. This is the default behavior.'; squash; rebase 'Rebase the source branch on top of the target branch HEAD commit, and fast-forward the target branch. The source branch is updated during the rebase operation.'; rebaseMerge. Deprecation text verbatim: 'The SquashMerge property is deprecated. It is recommended that you explicitly set MergeStrategy in all cases.' PullRequestAsyncStatus = notSet/queued/conflicts/succeeded/rejectedByPolicy/failure and PullRequestMergeFailureType = none/unknown/caseSensitive/objectTooLarge, both exact. Auto-complete semantics corroborated on review-pull-requests: 'Set auto-complete: Auto-complete the PR when all required reviewers approve it and all required branch policies are met.'"
          },
          {
            "claim": "'Completing a PR is a PATCH on the PR setting status=completed with lastMergeSourceCommit, plus completionOptions.'",
            "status": "OVERSTATED",
            "evidence": "Pull Requests - Update (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1) explicitly enumerates the updatable properties \u2014 Status, Title, Description (up to 4000 characters), CompletionOptions, MergeOptions, AutoCompleteSetBy.Id, TargetRefName (when the PR retargeting feature is enabled) \u2014 and then says: 'Attempting to update other properties outside of this list will either cause the server to throw an InvalidArgumentValueException, or to silently ignore the update.' lastMergeSourceCommit is NOT on that list. It appears in the request-body schema (the schema is just the full GitPullRequest shape) but the docs never state it is required, or even honoured, on completion. Supplying it is well-established community practice; presenting it as the documented completion contract is not supportable from Learn. Also note the documented route is lowercase `/pullrequests/`, and the page has no sample request at all \u2014 this is one of the thinner pages in the set."
          },
          {
            "claim": "The 'Check for comment resolution' branch policy exists and can be set Required (blocking) or Optional, manageable via az repos policy comment-required --blocking.",
            "status": "CONFIRMED",
            "evidence": "branch-policies page, 'Require comment resolution' section: 'The **Check for comment resolution** policy checks whether all PR comments are resolved.' and 'Set **Check for comment resolution** to **On** to configure a comment resolution policy for your branch. Then select whether to make the policy **Required** or **Optional**.' The az CLI section documents az repos policy comment-required create/update, and an example described as updating a policy 'to be blocking. Comments must be resolved before pull requests can merge.' All as claimed. (The GitHub comparison in that claim is outside this documentation lens and was not assessed.)"
          },
          {
            "claim": "Cross-cutting: every cited REST endpoint is GA at api-version 7.1 (no preview endpoints, no api-version mismatches) and every one exists for Azure DevOps Server.",
            "status": "CONFIRMED",
            "evidence": "All nine REST pages I fetched declare 'API Version: 7.1', none carry a -preview suffix, and every one lists azure-devops-server-rest-7.1 among Other Supported Versions. I directly opened two Server variants \u2014 pull-request-properties/update and pull-request-threads/list at view=azure-devops-server-rest-7.1 \u2014 and both are present with identical semantics, including $iteration/$baseIteration and the json-patch media type. No endpoint route in the claim set is fabricated; every route I checked matches the documented URI template."
          },
          {
            "claim": "Cross-cutting caveat the claim set never states: all cited endpoints are given in dev.azure.com form, which is Services-only.",
            "status": "OVERSTATED",
            "evidence": "On Azure DevOps Server the documented host form is `https://{instance}/{collection}/{project}/_apis/...` with required path parameters `instance` ('TFS server name ({server:port})') and `collection` ('The name of the Azure DevOps collection') \u2014 verified on both Server 7.1 pages I opened. Every endpoint listed across all 18 claims uses the dev.azure.com/{organization} form exclusively. The capabilities are genuinely present on Server; the URLs as written are not portable to it. Given the brief explicitly asks for Services-vs-Server distinctions, this belongs in the writeup rather than being left implicit."
          }
        ]
      },
      {
        "area": "iterations",
        "claims_checked": [
          {
            "claim": "GitPullRequestIteration is a first-class, individually addressable revision noun (int32 id, monotonic from 1, listable and GETtable), scoped to the PR.",
            "status": "CONFIRMED",
            "evidence": "Documented at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 and .../get (api-version 7.1). Independently corroborated by field use rather than docs alone: the third-party client jinyeow/ado-pr.nvim ran a live spike against PR !21121 and reports '21 threads, 11 iterations' enumerated and steppable (https://github.com/jinyeow/ado-pr.nvim/blob/main/docs/adr/0002-read-path-and-original-side-content.md, dated 2026-07-31). No community report found in which the iterations route was missing or non-enumerable on a normal PR. Note the source is a single third-party project (n=1), not Microsoft."
          },
          {
            "claim": "supportsIterations is a per-PR boolean and 'legacy' PRs exist without iteration support, so a client must branch on it.",
            "status": "CONFIRMED",
            "evidence": "Confirmed and materially strengthened \u2014 the claim under-specifies the modern cause, which is documented verbatim and is NOT a legacy-only concern: 'If the PR being created contains more than 100,000 modified files, then, for performance and stability reasons, that PR won't support iterations. This means any additional change to such PR will be included but no new iteration will be created for that change. In addition any attempt to create a status for a non-existent iteration will return an error.' (https://learn.microsoft.com/en-us/azure/devops/repos/git/pull-request-status?view=azure-devops). The moniker block on that note is 'azure-devops azure-devops-2022 azure-devops-server', so it applies to Services AND Server, not just cloud. For JetBridge this is a hard failure mode, not a legacy edge: on such a PR the entire revision series silently collapses to one iteration and iteration-scoped status POSTs error."
          },
          {
            "claim": "Iteration 1 is the head of the source branch at PR creation; subsequent iterations are created on pushes to the source branch.",
            "status": "CONFIRMED",
            "evidence": "Verbatim in the iterationId path-parameter description at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1 (api-version 7.1). Also stated conceptually in https://learn.microsoft.com/en-us/azure/devops/repos/git/pull-request-status?view=azure-devops: 'When the source branch in a PR changes, a new \"iteration\" is created to track the latest changes.' No contradicting community report found."
          },
          {
            "claim": "ASYMMETRY: a plain advance of the TARGET branch does not create an iteration; iteration N's targetRefCommit is the target head as of that iteration and can go stale.",
            "status": "CONFIRMED",
            "evidence": "The claim labelled this as inferred-from-absence; it is now corroborated by an independent field report. jinyeow/ado-pr.nvim issue #49 (open, 2026-08-06, https://github.com/jinyeow/ado-pr.nvim/issues/49): 'If the target branch (e.g. main) moves after the PR's first iteration \u2014 via a normal merge, a rebase, or a force-push \u2014 targetRefCommit no longer matches the actual merge-base between the PR's source branch and the current target tip. Diffing iteration 1 against a stale targetRefCommit can then show unrelated upstream changes as part of the PR's diff.' Their resolution is exactly the claim's: use commonRefCommit. Related issue #12 in the same repo documents the same class of bug hit live on PR !21193, where a stale base caused comments to be posted onto '/T0/AzureDevOpsPermissions/README.md, a file not in the PR at all'. Practical consequence for JetBridge: 'no new iteration' is confirmed NOT to mean 'the merge base has not moved'."
          },
          {
            "claim": "IterationReason is declared [System.Flags] with Push=0, ForcePush=1, Create=2, Rebase=4, Unknown=8, Retarget=16, and resolveConflicts is absent from the .NET page.",
            "status": "CONFIRMED",
            "evidence": "Verified verbatim at https://learn.microsoft.com/en-us/dotnet/api/microsoft.teamfoundation.sourcecontrol.webapi.iterationreason. Page states 'This enumeration supports a bitwise combination of its member values', shows '[System.Flags] public enum IterationReason', Attributes FlagsAttribute, and the Fields table is exactly Push=0, ForcePush=1, Create=2, Rebase=4, Unknown=8, Retarget=16 with every Description cell empty. ResolveConflicts is indeed absent. Page updated_at is 2018-12-10, confirming the claim's staleness warning. One correction the claim should absorb: because Push is the zero value, Push can never appear in a combination (Push|Rebase == Rebase), so 'push' is strictly the no-other-reason default and must not be tested with a bitwise AND."
          },
          {
            "claim": "The reason field's JSON wire format for combined flags is an expected comma-separated camelCase string.",
            "status": "UNVERIFIABLE",
            "evidence": "The claim itself hedges this as expected rather than documented, and I could not close it. The REST 7.1 enum at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 lists seven values as a flat list with empty descriptions and shows no combined-value sample. No community post, SDK issue, or captured payload exhibiting a combined reason value was found. Treat combined-reason parsing as untested surface and fail open (parse unknown strings without erroring) rather than assuming the comma-separated form."
          },
          {
            "claim": "Every iteration is a self-describing (base, head, merge-base) triple via sourceRefCommit / targetRefCommit / commonRefCommit, satisfying the ghstack requirement natively.",
            "status": "CONFIRMED",
            "evidence": "Documented at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1 (api-version 7.1). Corroborated in practice by jinyeow/ado-pr.nvim #49, which treats commonRefCommit as 'the actual merge-base commit at that point' and the correct field to diff against, with targetRefCommit as a back-compat fallback 'for older API versions'. That fallback wording is a mild caution the claim omits: at least one practitioner believes commonRefCommit may be absent on older api-versions, so JetBridge should assert its presence rather than assume it."
          },
          {
            "claim": "Iteration records are immutable and remain individually addressable after a force-push; the iteration series is an append-only log a history rewrite cannot rewrite.",
            "status": "CONFIRMED",
            "evidence": "Documented verbatim at https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops ('A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset'), with the explicit contrast that the Commits tab IS overwritten. Immutability by API surface also holds: the iterations route at api-version 7.1 documents only GET (list, get, changes, commits, statuses) and no PATCH/PUT/DELETE on the iteration itself. Field corroboration: ado-pr.nvim's ADR-0002 is written for a team that force-pushes PR branches as routine ('The team force-pushes PR branches') and still enumerates and steps 11 iterations on a live PR. I found no report of an iteration record being renumbered, mutated, or vanishing."
          },
          {
            "claim": "It is unresolved whether the git objects of a superseded iteration remain fetchable by a client after force-push; treat REST as durable and git fetch as best-effort.",
            "status": "CONFIRMED",
            "evidence": "This is the claim I most expected to overturn, and the field evidence instead confirms the pessimistic half and makes it concrete. ado-pr.nvim ADR-0002 (Accepted, 2026-07-31) records as a settled decision: 'The original-side content comes from the items REST endpoint at the iteration's commit, never from local git. The team force-pushes PR branches, so old iteration commits are not reliably present in, or fetchable into, the local clone. ADO serves them regardless.' Their rejected alternative is stated explicitly: 'git fetch the iteration commits and diff locally \u2014 rejected: force-pushed branches make those commits unreachable.' Their issue #12 further flags uncertainty over whether the server even permits fetching a bare SHA ('check whether the server allows fetching a bare SHA via uploadpack.allowReachableSHA1InWant'). They also confirm the REST escape hatch works and measured it: 'items?path=...&versionDescriptor.version=<sha>&versionDescriptor.versionType=commit&includeContent=true returns the file as JSON with the body in .content (13.5 KB confirmed).' Microsoft still documents no retention or GC policy for unreachable objects. Net: the claim's recommendation (a) is validated by practice, and recommendation (b) \u2014 push a JetBridge retention ref at seal time \u2014 remains the only way to get a hard guarantee."
          },
          {
            "claim": "GET iterations/{id}/changes with $compareTo gives the incremental diff between two revisions, with entries carrying changeId, changeTrackingId, changeType and item{objectId, originalObjectId, path}.",
            "status": "OVERSTATED",
            "evidence": "The field list and the $compareTo/paging semantics are correct as documented (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1, api-version 7.1). The word 'diff' is where it overreaches: the endpoint returns a FILE-LEVEL CHANGE LIST with no hunks and no line content. Confirmed by a captured live response in microsoft/azure-devops-mcp issue #1237 (closed, 2026-05-07, https://github.com/microsoft/azure-devops-mcp/issues/1237), where the response 'only included change metadata such as the file path and change type' \u2014 the pasted payload contains solely changeTrackingId, changeId, item{objectId, path} and changeType. JetBridge must fetch blobs by objectId/originalObjectId (or use the Diffs API) and diff them itself. Two secondary traps in that same payload: changeType came back as the integer 1 rather than the documented string 'add', and no originalObjectId appeared on add entries. Separately, microsoft/azure-devops-python-api issue #166 (closed, https://github.com/microsoft/azure-devops-python-api/issues/166) shows the official Python SDK deserialising change entries to change_tracking_id ONLY, with item/path/changeType stranded in additional_properties \u2014 a Microsoft maintainer (tedchamb) confirmed the workaround rather than fixing the model. The REST contract is fine; the generated clients under-model it."
          },
          {
            "claim": "A target-branch change is recorded as its own iteration with reason=retarget carrying oldTargetRefName/newTargetRefName.",
            "status": "CONFIRMED",
            "evidence": "Documented at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 (api-version 7.1), and Retarget=16 exists in the .NET enum, so it is a real emitted value and not doc-only. No contradicting community report found. Caveat carried over from the IterationReason finding: the REST enum descriptions are empty and the .NET page has not been touched since 2018, so retarget's exact trigger boundary (e.g. whether an auto-retarget on target-branch deletion also mints one) is inferred, not documented."
          },
          {
            "claim": "Comment anchoring is declared, not heuristic: pullRequestThreadContext carries iterationContext, changeTrackingId, and trackingCriteria, and 'the presence of trackingCriteria is an explicit machine-readable this-comment-was-FORWARDED signal'.",
            "status": "OVERSTATED",
            "evidence": "The fields exist as documented (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1, api-version 7.1), but the claim states the availability of trackingCriteria far too strongly. Measured field data from ado-pr.nvim's live spike (PR !21121, 21 threads, 11 iterations, ADR-0002): 'Without those parameters, no thread carries trackingCriteria (0 of 21). With them, 3 of 21 do. Tracked positions are opt-in, per call.' So trackingCriteria is absent from the DEFAULT thread listing entirely \u2014 a JetBridge sealer that lists threads without $iteration/$baseIteration will see zero forwarding records and wrongly conclude nothing was forwarded. Same spike: only 5 of 21 threads were file-anchored at all and 16 of 21 were commentType 'system' only, so the anchored population is a minority of the payload. The claim's comparison to GitHub still holds directionally, but 'Azure DevOps gives you the original anchor and the forwarding record' is true only on an explicitly windowed query."
          },
          {
            "claim": "GET threads with $iteration and $baseIteration makes the server re-anchor every thread onto any iteration pair, so JetBridge can seal positions in the coordinate system of the revision the agent is about to act on.",
            "status": "OVERSTATED",
            "evidence": "The mechanism works \u2014 that much is confirmed both by the doc (api-version 7.1) and by the live spike ('passing --query-parameters \"$iteration=2\" \"$baseIteration=1\" changed the response'). But the claim misses an undocumented behaviour that is fatal to naive sealing. ADR-0002, from the same spike: 'The side a thread sits on is a function of the queried iteration window. In the plain list all 5 anchored threads had rightFileStart and none had leftFileStart. Queried with $iteration=2&$baseIteration=1, the same threads returned leftFileStart set and rightFileStart empty.' Their design doc states the consequence as a hard contract: 'Side comes from whichever of leftFileStart / rightFileStart the response populated for that window \u2014 it is not a property of the thread.' Nothing on the Microsoft page says this. Implication: a sealed review/v1 position is meaningless unless the query window (iteration, baseIteration) is sealed alongside it, and code must read side from whichever field is populated rather than assuming right-side. Same spike also measured multi-line spans as common (2 of 5 anchored threads had rightFileEnd.line != rightFileStart.line), so positions must be sealed as ranges, not lines."
          },
          {
            "claim": "SELF-TRIGGER: on Azure DevOps a comment reply cannot re-trigger a vote-based disposition trigger, because comments and votes are different resources on different event channels \u2014 and residual vectors can be filtered on iteration.author.id / iteration.push.pushedBy.id / reviewer id.",
            "status": "OVERSTATED",
            "evidence": "The structural half is CONFIRMED and is genuinely the strongest argument for Azure DevOps: https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops documents ms.vss-code.git-pullrequest-comment-event and git.pullrequest.updated as separate event types, and posting a comment touches no reviewer vote. The filtering half is REFUTED. I pulled the complete verbatim git.pullrequest.updated sample from the doc source (https://raw.githubusercontent.com/MicrosoftDocs/azure-devops-docs/main/docs/service-hooks/events.md, resourceVersion 2.0). Its resource object contains exactly: repository, pullRequestId, status, createdBy, creationDate, closedDate, title, description, sourceRefName, targetRefName, mergeStatus, mergeId, lastMergeSourceCommit, lastMergeTargetCommit, lastMergeCommit, reviewers, commits, url. There is NO notificationType field, NO iteration object, NO push/pushedBy, and NO updatedBy. notificationType (PushNotification, ReviewersUpdateNotification, StatusUpdateNotification, ReviewerVoteNotification) is a SUBSCRIPTION FILTER only \u2014 it is never echoed in the delivered payload. createdBy is the PR author, which for a JetBridge-authored PR is the agent itself, so filtering on it would suppress every human event too. Consequence: none of the claim's three suppression filters is satisfiable from the payload; each requires a follow-up REST call (GET iterations, GET reviewers) plus a diff against locally held prior state, which reintroduces a race the claim asserts is designed away. The only actor signal actually in the payload is the human-readable message.text ('Jamal Hartnett marked the pull request as completed') \u2014 a localized display string, not a contract; do not parse it. By contrast the comment channel IS filterable as claimed: the ms.vss-code.git-pullrequest-comment-event sample carries resource.comment.author with id and uniqueName. Note also its message text is 'has edited a pull request comment', so the event fires on edits, not only creations."
          },
          {
            "claim": "Vote changes produce a distinct event (ReviewerVoteNotification) that a comment cannot produce.",
            "status": "OVERSTATED",
            "evidence": "Distinct at SUBSCRIPTION level, not at payload level. The filter value is documented ('ReviewerVoteNotification \u2014 The votes score changes', https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops), so a dedicated subscription can be narrowed to vote changes and will not fire on comments \u2014 the headline separation survives. But the delivered payload is the identical git.pullrequest.updated shape with no notificationType, so it cannot tell you WHICH reviewer changed, what the PREVIOUS value was, or whether the change was human or policy-driven; it carries only the current reviewers[] array with vote values. Two further gaps I could not close: (1) whether the branch-policy vote reset on push emits ReviewerVoteNotification at all, and if so whether it is attributable to the pushing identity \u2014 no documentation and no field report found; (2) whether a system comment thread (the VoteUpdate thread the design relies on as its audit log) itself fires ms.vss-code.git-pullrequest-comment-event, which would leak votes back into the comment channel and re-open the self-trigger hole from the other side. Both are UNVERIFIABLE and should be settled by live probe before the design depends on them."
          },
          {
            "claim": "The five-level vote enum maps cleanly onto dispositions, with 0 usable as the 'no vote / reset feedback' state.",
            "status": "OVERSTATED",
            "evidence": "The enum and its semantics are documented (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1). The writable-reset half has a credible contradicting report: microsoft/azure-devops-node-api issue #611 (https://github.com/microsoft/azure-devops-node-api/issues/611), 'Resetting Reviewer vote to 0 via GitAPI has no effect' \u2014 the reporter states the PUT returns success yet the reviewer stays Approved. The issue is CLOSED WITH NO MAINTAINER RESPONSE and no stated resolution, so this is an unrebutted single report, not a proven platform bug; I am not treating it as confirmed. Status is OVERSTATED rather than REFUTED because the read mapping is sound and only the vote-to-0 write is in doubt. JetBridge should not build any control flow that depends on programmatically clearing a vote until this is verified live."
          },
          {
            "claim": "Vote changes and lifecycle events are recorded as system comment threads, giving an append-only pollable disposition log from one endpoint.",
            "status": "CONFIRMED",
            "evidence": "The VoteUpdate / MergeAttempt / ReviewersUpdate / RefUpdate system-thread vocabulary appears in the sample response at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1 (api-version 7.1). Field data confirms system threads dominate a real PR's thread payload and are therefore reliably present: ado-pr.nvim's spike measured 16 of 21 threads as commentType 'system' only, and their design doc puts it at '76% of a real PR's payload'. Two caveats the claim already half-states and that the field data sharpens: the property key names are sample-only and not a documented schema, and the system-thread volume means any polling loop must filter aggressively (that project drops system-only threads unconditionally). This polling path is also the recommended mitigation for the webhook-payload gap found above, since GET threads carries the actor identity the git.pullrequest.updated payload does not."
          },
          {
            "claim": "Reported contradiction: 'Pull Request updates page doesn't show force push' (Developer Community idea 891722), which would contradict the documented force-push-preserves-changesets behaviour.",
            "status": "UNVERIFIABLE",
            "evidence": "The item exists in search results at https://developercommunity.visualstudio.com/idea/891722/pull-request-updates-page-doesnt-show-force-push-1.html but I could not read its body, date, vote count, or status. developercommunity.visualstudio.com and developercommunity.azure.com are fully client-rendered and return only chrome to a fetcher; I also tried the /api/ideas/{id} and /_apis/PublicProjects/Tickets/{id} backends and both returned page chrome, not JSON. stackoverflow.com is blocked to this crawler entirely. So a whole tier of practitioner evidence for this area is closed to me and my confidence on 'no contradicting reports exist' is correspondingly weak for claims where my only corroboration was a single project. The title alone suggests a contradiction, but it is filed as an 'idea' (feature request), which is at least as consistent with it predating the current Updates-tab behaviour as with the behaviour being broken. Someone with browser access should read this item before the design leans on force-push durability."
          },
          {
            "claim": "Adjacent trap not in the claim set: comments attached to a commit inside a PR are silently invisible in the PR.",
            "status": "CONFIRMED",
            "evidence": "Microsoft Q&A https://learn.microsoft.com/en-us/answers/questions/1602132/comments-on-commit-level-not-appearing-in-pull-req: a user reports that opening a commit from the Commits tab and commenting on a file there means the comment 'is NOT shown in the PR itself, no notification is created nor email is send. Basically it is lost.' The accepted answer directs users to comment at PR level instead and suggests filing a feature request; a commenter notes a feedback item from October 2018 requesting this has gone unaddressed for 6+ years. Relevance to JetBridge: commit-scoped comments are a dead-end write surface that produces no thread on the PR, no thread event, and no trigger. Every agent write must target the PR thread routes, and any human feedback left at commit level will be invisible to the loop with no error to detect it by."
          }
        ]
      }
    ]
  },
  {
    "key": "threads",
    "survey": {
      "area": "Azure DevOps pull request comment threads \u2014 anchoring, tracking across iterations, thread status, and self-trigger suppression",
      "findings": [
        {
          "capability": "Comment thread + comment CRUD surface",
          "exists": "native",
          "detail": "Threads are first-class objects; comments are sub-resources. POST /threads creates a thread with an initial comments[] array; GET lists all; GET /{threadId} fetches one; PATCH /{threadId} updates. Comments: POST/PATCH/DELETE /threads/{threadId}/comments[/{commentId}]. Comment.id is documented as int16 and 'IDs start at 1 and are unique to a pull request' \u2014 i.e. a PR-wide counter, NOT per-thread, so a comment is addressed by (threadId, commentId) but commentId alone is already unique within the PR. Thread.id is int32. Documented hard limit: 'up to 500 comments can be created per thread'. DELETE is a SOFT delete: the comment remains in the array with isDeleted:true and content stripped (visible in the official List sample, thread 148 comment 2), and thread.isDeleted becomes true 'which happens when all comments are deleted'. Scopes: vso.code_write or vso.threads_full to write, vso.code or vso.threads_full to read.",
          "endpoints": [
            "POST https://dev.azure.com/{org}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET https://dev.azure.com/{org}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1",
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1",
            "POST .../pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1",
            "DELETE .../pullRequests/{pullRequestId}/threads/{threadId}/comments/{commentId}?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-thread-comments/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-thread-comments/delete?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Positional anchoring of a comment (threadContext)",
          "exists": "native",
          "detail": "threadContext = { filePath, leftFileStart, leftFileEnd, rightFileStart, rightFileEnd }, where each position is CommentPosition { line, offset }. Left = the 'before' side of the diff, right = the 'after' side; a comment on added code sets only right*, a comment on deleted code sets only left*. Character-level SPANS are supported (start offset and end offset within a line) \u2014 Microsoft's sample anchors to line 5, offset 1 through offset 13 \u2014 which GitHub review comments cannot express at all. filePath is documented as 'File path relative to the root of the repository. It's up to the client to use any path format'; Azure's own samples use a leading slash ('/new_feature.cpp'). CRITICAL: threadContext carries NO commit id and NO blob OID. The anchor is (path, line, offset) only. threadContext = null makes it a PR-level (Overview) comment; filePath set with null positions is a file-level comment.",
          "endpoints": [
            "POST .../pullRequests/{pullRequestId}/threads?api-version=7.1 (body.threadContext)"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Immutable, individually addressable REVISION noun (pull request iteration) with its own base/head",
          "exists": "native",
          "detail": "This is the single biggest structural win over GitHub and JetBridge should NOT build it. GitPullRequestIteration is an immutable, ordinally numbered (1..N) revision carrying: id, sourceRefCommit (HEAD), targetRefCommit, commonRefCommit ('The first common Git commit of the source and target refs' \u2014 the merge BASE), commits[], push (GitPushRef with pushId/pushedBy/date), author, createdDate, reason, and oldTargetRefName/newTargetRefName for retargets. IterationReason enum: push | forcePush | create | rebase | unknown | retarget | resolveConflicts \u2014 so the adapter can tell a rebase from a content push. Documented: 'Iteration one is the head of the source branch at the time the pull request is created and subsequent iterations are created when there are pushes to the source branch.' Force-push does not destroy the ledger: the UI doc states 'A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset', while the Commits tab IS overwritten. This is precisely ghstack's base/head/orig triple, provided natively and durably. Adopt iterationId as the revision ordinal and (commonRefCommit.commitId, sourceRefCommit.commitId) as the base/head pair.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/iterations?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/iterations/{iterationId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "changeTrackingId \u2014 per-FILE identity stable across iterations",
          "exists": "native",
          "detail": "GitPullRequestChange.changeTrackingId is 'ID used to track files through multiple changes', returned by the iteration-changes endpoint alongside changeId (a per-iteration ordinal), changeType (VersionControlChangeType: add/edit/rename/delete/...), and item { objectId = blob OID after the change, originalObjectId = blob OID before (present when $compareTo is supplied), path }. On the thread side, pullRequestThreadContext.changeTrackingId is 'Used to track a comment across iterations. This value can be found by looking at the iteration's changes list. Must be set for pull requests with iteration support. Otherwise, it's not required for legacy pull requests.' So changeTrackingId is a FILE-level correlation key \u2014 not line-level and not comment-level \u2014 and it is the key the server uses to follow a file through renames and edits. Note item.objectId hands you a real immutable blob OID per changed file per iteration.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/iterations/{iterationId}/changes?$top={n}&$skip={n}&$compareTo={iterationId}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Server-side FORWARDING of comment anchors across iterations, exposed through REST",
          "exists": "native",
          "detail": "THE ANSWER TO THE CRITICAL QUESTION IS YES, AND IT IS REST-EXPOSED, NOT UI-ONLY. GET threads and GET threads/{threadId} both accept $iteration and $baseIteration: '$iteration \u2014 If specified, thread positions will be tracked using this iteration as the right side of the diff' and '$baseIteration \u2014 ... as the left side of the diff'. When supplied, the server recomputes each thread's position for that diff window and returns it in the SAME threadContext fields, while the ORIGINAL position moves into pullRequestThreadContext.trackingCriteria = { origFilePath, origLeftFileStart, origLeftFileEnd, origRightFileStart, origRightFileEnd, firstComparingIteration, secondComparingIteration }. The documented discriminator is presence: 'The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria', reinforced per-field by 'Threads were tracked if this is greater than 0.' RENAMES ARE FOLLOWED: origFilePath 'will be different than the current thread filepath if the file in question was renamed in a later iteration.' Operational rule for JetBridge: call GET threads WITHOUT $iteration to obtain authored/original positions, and WITH $iteration={latest}&$baseIteration={merge-base iteration} to obtain current positions; trackingCriteria present means 'this anchor moved'.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?$iteration={n}&$baseIteration={n}&api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads/{threadId}?$iteration={n}&$baseIteration={n}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/get?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Documented behaviour when the commented line is deleted or tracking fails",
          "exists": "absent",
          "detail": "This is a genuine documentation hole and the riskiest unknown in the area. Microsoft documents the tracking MECHANISM (trackingCriteria, 'tracked if greater than 0') but never documents the FAILURE MODE. Nothing states what threadContext contains when the anchored line no longer exists in the requested iteration: whether positions are nulled, clamped to a neighbouring line, left at their original values, or whether the thread is dropped from the response. There is NO isOutdated / outdated / isStale boolean anywhere on GitPullRequestCommentThread or Comment. Compare GitHub, which nulls PullRequestReviewComment.position and exposes that null as the canonical outdated signal. No sample response anywhere in the Microsoft REST reference shows a thread with trackingCriteria populated \u2014 every published sample has pullRequestThreadContext either null or containing only iterationContext + changeTrackingId. The only inference available is negative (trackingCriteria absent while the thread was authored against an older iteration \u21d2 not tracked into the requested window). Pin this with a live experiment before any code depends on it.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?$iteration={n}&$baseIteration={n}&api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "uncertain",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/get?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "threadContext vs iterationContext vs trackingCriteria \u2014 precise roles and interaction",
          "exists": "native",
          "detail": "Three distinct objects, routinely conflated. (a) threadContext \u2014 WHERE the comment points now, in path/line/offset terms. Effectively mutable: its values depend on the $iteration/$baseIteration you asked for. (b) pullRequestThreadContext.iterationContext { firstComparingIteration, secondComparingIteration } \u2014 WHICH DIFF THE HUMAN WAS VIEWING when the thread was created; authored once and immutable. Documented nuance worth encoding: 'If this value [firstComparingIteration] is equal to SecondComparingIteration, then this version is the common commit between the source and target branches of the pull request' \u2014 so first == second encodes 'viewing the whole PR against the merge base', NOT an empty diff. (c) pullRequestThreadContext.trackingCriteria \u2014 WHERE IT USED TO POINT, plus the iteration pair it was tracked TO; populated by the server only when forwarding actually occurred. Interaction: (b) is the provenance stamp, (a) is the current answer, (c) is the receipt proving (a) differs from the authored position, and changeTrackingId is the file-identity key that makes (c) computable. On CREATE the client supplies threadContext + iterationContext + changeTrackingId; trackingCriteria is server-produced output only.",
          "endpoints": [
            "POST .../pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads?$iteration={n}&$baseIteration={n}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "CommentThreadStatus \u2014 a 7-value enum, per-thread not per-comment",
          "exists": "native",
          "detail": "status lives on the THREAD, never on a comment. Enum: unknown | active | fixed | wontFix | closed | byDesign | pending. The wire accepts both the string and the ordinal integer \u2014 Microsoft's Create samples send \"status\": 1 (active) and responses return \"status\": \"active\". Set it with PATCH .../threads/{threadId} carrying a partial body such as {\"status\": \"fixed\"}. DOC THINNESS: the Update page has NO example at all \u2014 its request-body table is simply the full GitPullRequestCommentThread \u2014 so the universally used 'PATCH only the status field' shape is never actually demonstrated by Microsoft. Also important for normalization: threads that carry no status return with the status key entirely ABSENT (see system threads 141\u2013146 in the official List sample), so a missing status means 'this is an event/system thread, not a discussion', which is different from unknown.",
          "endpoints": [
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Who may set thread status",
          "exists": "partial",
          "detail": "The web UI documentation says 'New comments start with an Active status. PR authors update the status during the review process to indicate how they addressed reviewer feedback and suggestions. PR authors can select a comment status from the status dropdown list', and separately that any replier gets a 'Reply & resolve' action which 'changes the comment status to Resolved'. The REST reference documents NO permission constraint on PATCH thread beyond the vso.code_write / vso.threads_full scope. So in practice any identity with contribute-to-PR permission appears able to set any status via REST \u2014 including on a thread it did not author, which is exactly what the agent needs in order to close reviewer-authored threads \u2014 but Microsoft never states this guarantee. Verify with the agent's actual service identity before relying on it.",
          "endpoints": [
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "uncertain",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "REST/UI vocabulary divergence on thread status",
          "exists": "partial",
          "detail": "The REST enum has 7 values; the documented web UI dropdown offers 5: Active, Pending, Resolved, Won't fix, Closed. Mapping traps: UI 'Resolved' == REST `fixed` (there is no `resolved` member in the enum); REST `byDesign` does not appear in the documented UI dropdown at all; REST `unknown` is not user-selectable. Any JetBridge rendering of status must translate fixed -> 'Resolved' or humans will not recognize their own forge's vocabulary, and must decide what to display for byDesign.",
          "endpoints": [
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Reply threading via parentCommentId",
          "exists": "native",
          "detail": "Replies live inside the SAME thread; there is no cross-thread reply and no cross-thread parent. Comment.parentCommentId is 'The ID of the parent comment. This is used for replies.' Root comments carry parentCommentId: 0 (samples consistently show 0, never null). A reply is POST .../threads/{threadId}/comments with body {\"content\": \"...\", \"parentCommentId\": 1, \"commentType\": 1}. Structurally flat in practice \u2014 the UI renders a thread as a linear list and every reply in Microsoft's samples points at comment id 1; whether reply-to-a-reply nesting is accepted is neither documented nor demonstrated. IMPORTANT TRAP: author is server-assigned from the calling identity and MUST be omitted from the request body; supplying it fails with 'An author of a comment cannot be updated', a well-known defect surface in the official Node client where the generated types mark every field required.",
          "endpoints": [
            "POST .../pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1",
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}/comments/{commentId}?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-thread-comments/create?view=azure-devops-rest-7.1",
            "https://github.com/Microsoft/azure-devops-node-api/issues/173"
          ]
        },
        {
          "capability": "commentType=system as a self-trigger discriminator",
          "exists": "partial",
          "detail": "commentType is reliably `system` for SERVICE-generated threads: every event thread in Microsoft's official List sample (MergeAttempt, ReviewersUpdate, VoteUpdate, RefUpdate) carries commentType: \"system\" with an author of the Project Collection Service Accounts group and isContainer: true. BUT this does not solve JetBridge's problem, because `system` means 'generated by the Azure DevOps service itself', not 'generated by a bot'. A comment JetBridge writes through a PAT or service principal is authored by that identity and returns commentType: \"text\", type-indistinguishable from a human comment. The enum is defined only as 'The comment type at the time of creation', and whether a caller may SET commentType: 3 (system) on create is NOT documented and never demonstrated \u2014 do not assume it is settable. The reliable discriminator is comment.author.id (a stable GUID) compared against the agent's own identity; obtain that identity from a supported identity API rather than by decoding the token, because Microsoft has stated that from summer 2025 tokens are further encrypted and clients that parse token payloads break. THE HEADLINE: because Azure DevOps has no review envelope, an agent replying to a thread does NOT manufacture a new review object \u2014 the exact pathology proven live on GitHub (the agent's reply files under a NEW review with non-null submitted_at and re-triggers the platform) has no analogue here. Suppression collapses to 'ignore events whose comment.author.id is me'.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-thread-comments/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/authentication-guidance?view=azure-devops"
          ]
        },
        {
          "capability": "Thread properties bag for integration metadata (change-id, declarative resolves pointer)",
          "exists": "native",
          "detail": "GitPullRequestCommentThread.properties is a PropertiesCollection \u2014 'Optional properties associated with the thread as a collection of key-value pairs' \u2014 and it appears in the request-body table of BOTH Create and Update, so it is writable by integrations. Values are typed on the wire as {\"$type\": \"System.String\", \"$value\": \"...\"}; Azure's own service uses it heavily and with mixed types (System.String and System.Int32 both appear in the official sample). Keys are arbitrary strings and Microsoft namespaces its own (CodeReview*, Microsoft.TeamFoundation.Discussion.SupportsMarkdown). This is a legitimate, structured home for a JetBridge change-id and a declarative 'resolves' pointer on threads the agent creates. TWO CAVEATS: (i) NO size limit, key-count limit, or key-length limit is documented anywhere \u2014 the PropertiesCollection definition documents accepted TYPES but never limits; (ii) Microsoft shows NO example of an integration writing thread properties, so the accepted request shape (raw JSON scalar versus the {$type,$value} envelope) is undocumented for this endpoint and must be established empirically.",
          "endpoints": [
            "POST .../pullRequests/{pullRequestId}/threads?api-version=7.1 (body.properties)",
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1 (body.properties)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "PR-level property bag \u2014 the better home for a stable change identity",
          "exists": "native",
          "detail": "Distinct from thread properties and a cleaner place for a change-id that must outlive any single thread. PATCH .../pullRequests/{prId}/properties with Content-Type application/json-patch+json and a JSON Patch array; documented semantics: 'The patch operation can be add, replace or remove. For add operation, the path can be empty. If the path is empty, the value must be a list of key value pairs. For replace operation, the path cannot be empty. If the path does not exist, the property will be added... For remove operation... If the path does not exist, no action will be performed.' Returns the whole PropertiesCollection. TYPE COERCION TRAP: the PropertiesCollection definition claims 'Values of type Byte[], Int32, Double, DateType and String preserve their type', yet Microsoft's own sample sends \"value\": 8 and gets back {\"$type\": \"System.String\", \"$value\": \"8\"} \u2014 integers are stringified through this path \u2014 while '2017-09-25T15:26:49.4760511Z' IS preserved as System.DateTime (rounded to milliseconds). Store the JetBridge change-id as a string; never rely on numeric round-tripping. Azure itself keeps Microsoft.Git.PullRequest.SourceRefName / TargetRefName here.",
          "endpoints": [
            "PATCH .../pullRequests/{pullRequestId}/properties?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/properties?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "System threads as a machine-readable event ledger (how to detect dispositions and pushes)",
          "exists": "native",
          "detail": "Because there is no review envelope, the event stream IS the set of system threads \u2014 and their properties bags are structured, not prose. From the official List sample, thread.properties.CodeReviewThreadType takes at least these values with typed companions: (1) 'VoteUpdate' -> CodeReviewVotedByTfId, CodeReviewVotedByDisplayName, CodeReviewVoteResult (the vote as a string, e.g. \"10\"); (2) 'RefUpdate' -> CodeReviewRefName, CodeReviewRefNewHeadCommit, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount (System.Int32), CodeReviewRefUpdatedByTfId \u2014 this is the push / new-iteration signal with the new head SHA inline; (3) 'MergeAttempt' -> CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit; (4) 'ReviewersUpdate' -> CodeReviewReviewersUpdatedAddedTfId / RemovedTfId / NumAdded / NumRemoved. A JetBridge adapter can therefore reconstruct the entire PR history from GET threads ALONE, with no extra endpoint, and correlate each vote to the iteration current at that timestamp. Two honesty notes: the four thread types above are only the ones the sample happens to contain, not a documented exhaustive enum; and the numeric vote mapping (10 approved, 5 approved-with-suggestions, 0 no vote, -5 wait for author, -10 rejected) is documented on the reviewers side, not restated on the threads page \u2014 the UI doc names the five options but not their integers.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/reviewers?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "A review ENVELOPE carrying exactly one disposition (the trigger unit of the design)",
          "exists": "absent",
          "detail": "This is the load-bearing gap and the exact inverse of GitHub's strength. Azure DevOps has NO object equivalent to a submitted GitHub review. There is no way to group N comments into one submission, no way to attach approved / comment-only / changes-requested to that group, no draft/pending batching, no 'submit review' call, and no query analogous to reviews with submitted_at != null. The pieces exist but are disjoint: the vote lives on the PR reviewer record (echoed as a VoteUpdate system thread), while comments are independent threads created one at a time. Consequences for disposition-triggered review: (1) the three dispositions must be derived from the VOTE, not from a review object \u2014 approved <= vote 10 (and arguably 5), changes-requested <= vote -5 (wait for author) or -10 (reject), comment-only <= new active threads with no vote change; (2) there is no atomic boundary, so the adapter must define its own window, e.g. 'all threads with lastUpdatedDate at or before the VoteUpdate thread's publishedDate and after the previous vote', plus debounce, because a human types comments over minutes and then votes; (3) the 'reviewer commented but never voted' case has NO trigger at all and needs a quiet-period timer. JetBridge MUST build the envelope on Azure DevOps \u2014 it is the largest single piece of work the adapter carries.",
          "endpoints": [
            "PUT .../pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/reviewers?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Retrieve only threads changed since a point in time",
          "exists": "absent",
          "detail": "Plainly absent. The ONLY query parameters on GET threads in every published version \u2014 4.1 through 7.2-preview.1 \u2014 are $iteration and $baseIteration. There is no $filter, no since, no $top, no $skip, no continuation token and no pagination of any kind. The entire thread collection is returned on every call, including every system/event thread, with each thread's full comments array inline. Each thread does carry lastUpdatedDate and publishedDate, and each comment carries publishedDate, lastUpdatedDate and lastContentUpdatedDate (the last distinguishing a content edit from a metadata touch), so incremental sync is client-side diffing only. No ETag / If-None-Match conditional-request behaviour is documented for this endpoint. On a long-lived agentic PR this response grows without bound: cache by (threadId, lastUpdatedDate) and drive incremental work from the service hook, using the full GET only as periodic reconciliation.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.2-preview.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.2"
          ]
        },
        {
          "capability": "$expand options on threads or thread comments",
          "exists": "absent",
          "detail": "There are none, in any version. Other Git resources do have $expand (Get Pull Request, for example), but neither Pull Request Threads (List/Get) nor Pull Request Thread Comments (List/Get) accepts $expand from 4.1 through 7.2-preview.1. In practice nothing needs expanding: comments[], author IdentityRefs, usersLiked[], properties, threadContext and pullRequestThreadContext are all returned inline and unconditionally. Do not design around an $expand that does not exist.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.2"
          ]
        },
        {
          "capability": "Push-based triggers (service hooks) for comments and for votes",
          "exists": "partial",
          "detail": "Two subscriptions matter and they have very different value. (a) ms.vss-code.git-pullrequest-comment-event ('Pull request commented on'), publisher tfs, resource name pullrequest, filters: repository (guid) and branch. Fires on comment create AND edit \u2014 the sample message text is 'has edited a pull request comment'. DOC THINNESS: the published payload sample is truncated and shows only resource.comment { id, parentCommentId, author, content, publishedDate, lastUpdatedDate, commentType }. There is NO first-class threadId field in the documented sample \u2014 the thread id appears only inside the message link as the query parameter ?discussionId=5. Whether resource also carries a full pullRequest object is not shown and must be verified live. (b) git.pullrequest.updated ('Pull request updated') carries a notificationType filter whose values include PushNotification (source branch updated, i.e. a new iteration), ReviewerVoteNotification (votes score changed, i.e. a DISPOSITION), ReviewersUpdateNotification and StatusUpdateNotification, plus filters for repository, branch, pullrequestCreatedBy and pullrequestReviewersContains. Subscribing with notificationType=ReviewerVoteNotification is the closest native equivalent to GitHub's review-submitted webhook and should be JetBridge's PRIMARY trigger; the comment event should be a low-priority nudge, not the trigger. UNVERIFIED: whether a status-only PATCH of a thread (no new comment) emits the comment event is not documented \u2014 if it does, an agent resolving threads can loop, so test it before enabling writes.",
          "endpoints": [
            "Service hook event id: ms.vss-code.git-pullrequest-comment-event (publisher tfs)",
            "Service hook event id: git.pullrequest.updated (publisher tfs, setting notificationType=ReviewerVoteNotification|PushNotification|ReviewersUpdateNotification|StatusUpdateNotification)",
            "POST https://dev.azure.com/{org}/_apis/hooks/subscriptions?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "Azure DevOps Services (cloud) vs Azure DevOps Server (on-prem)",
          "exists": "partial",
          "detail": "The threads, comments, iterations, iteration-changes and properties APIs are all published for Server as well as Services \u2014 every reference page carries azure-devops-server-rest-5.0 / 6.0 / 7.0 / 7.1 monikers. VERSION SUPPORT IS DOCUMENTED INCONSISTENTLY: Microsoft's REST API versioning table (page last updated 2025-04-10) stops at 7.0 and shows Services and Server 2022 at 7.0, Server 2020 topping out at 6.0, Server 2019 at 5.0, TFS 2018 at 4.0 \u2014 yet the REST reference itself publishes azure-devops-server-rest-7.1 pages for all of these endpoints, so the table is stale. Safe floor: target api-version=7.1 on Services, 7.0 on Server 2022, 6.0 on Server 2020. 7.2 is PREVIEW ONLY and must be requested as api-version=7.2-preview.1 (not '7.2'); documented policy is that once an API is released its preview 'is deprecated and can be deactivated after 12 weeks', so do not pin production to a preview. The thread/iteration MODEL is unchanged across the range \u2014 changeTrackingId, trackingCriteria, iterationContext, $iteration/$baseIteration and the full 7-value CommentThreadStatus enum are all present as far back as 4.1, so a single adapter works on-prem and cloud. Behavioural differences that do bite on-prem: the Azure DevOps CLI is unsupported on Server; git push output does not carry a create-PR URL; and the email channel needs an administrator-configured SMTP server (webhooks are unaffected).",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.0 (Server 2022)",
            "GET .../pullRequests/{pullRequestId}/threads?api-version=6.0 (Server 2020)",
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.2-preview.1 (Services, preview only)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.2",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Documentation contradiction: CommentPosition.offset base",
          "exists": "partial",
          "detail": "A concrete, citable contradiction to guard against. The 7.1 pages state 'offset \u2014 The character offset of a thread's position inside of a line. Starts at 0.' The 7.2-preview.1 page for the SAME type states 'Starts at 1.' Both are auto-generated from the same docs repo at the same git commit. Microsoft's own Create sample writes rightFileStart.offset: 1 and rightFileEnd.offset: 13 for a span on line 5, which is consistent with 1-based. Treat offset as 1-based and never emit a 0. line is documented as 1-based ('Starts at 1') in both versions and is not in dispute.",
          "endpoints": [
            "POST .../pullRequests/{pullRequestId}/threads?api-version=7.1 (body.threadContext.rightFileStart.offset)"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.2",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Radicle-style pin of a comment to an immutable OID",
          "exists": "workaround-only",
          "detail": "Cannot be done directly \u2014 threadContext has no OID field, and the properties bag is the only place to put one. But it is cheaply synthesizable and every component is immutable. From a thread: pullRequestThreadContext.iterationContext.secondComparingIteration -> GET iterations/{id} -> sourceRefCommit.commitId (head OID) and commonRefCommit.commitId (base/merge-base OID); and GET iterations/{id}/changes -> the entry whose changeTrackingId equals the thread's changeTrackingId -> item.objectId, the immutable BLOB OID of that exact file at that exact iteration. So a JetBridge review/v1 record can pin every comment to (blobObjectId, path, lineRange) at ingest for two extra GETs per iteration, both cacheable forever because iterations never change. RECOMMENDATION: seal (changeTrackingId, iterationId, blobObjectId, origFilePath, line range) into the sealed record at ingest and never re-derive \u2014 that makes the record independent of Azure DevOps's undocumented tracking-failure behaviour, which is the main risk identified above.",
          "endpoints": [
            "GET .../pullRequests/{pullRequestId}/iterations/{iterationId}?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/iterations/{iterationId}/changes?$compareTo={n}&api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "inferred",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Declarative resolution \u2014 the next revision declares which comments it fixed",
          "exists": "partial",
          "detail": "Azure DevOps gets materially closer to the Radicle model than GitHub does, but the link itself is still JetBridge-owned. After pushing iteration N+1 the agent can: (a) PATCH each addressed thread to status fixed \u2014 or byDesign / wontFix, a genuinely expressive vocabulary GitHub simply lacks \u2014 which is a first-class, queryable, per-thread declaration rather than a boolean; (b) POST a reply comment naming the iteration; and (c) stash typed metadata in the thread's properties bag (e.g. jetbridge.resolvedByIteration = 'N+1', jetbridge.changeId = '...'), which survives as structured data rather than being smuggled through markdown. WHAT IS ABSENT: no native field links a thread to the revision that resolved it. Nothing on the thread records 'closed by iteration N+1'; thread.lastUpdatedDate is the only temporal correlate, and it is touched by unrelated edits. So the declaration must be written and read by JetBridge \u2014 but unlike GitHub it can be stored as typed forge metadata rather than parsed out of prose.",
          "endpoints": [
            "PATCH .../pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1 (body.status + body.properties)",
            "POST .../pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "inferred",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        }
      ],
      "absent": [
        "A submitted-review object grouping comments under a single disposition \u2014 no reviews endpoint, no submitted_at, no draft/pending review batching, no 'submit review' call, no way to ask for completed reviews",
        "Any disposition field (approved / comment-only / changes-requested) on a thread or a comment \u2014 disposition exists only as a numeric vote on the PR reviewer record",
        "Per-comment resolution state \u2014 status is thread-level only; individual comments inside a thread cannot be resolved independently",
        "An 'outdated' / 'is stale' / 'position lost' flag on a thread or comment (GitHub's nulled position has no counterpart)",
        "Documented semantics for what happens to a thread's position when its anchored line is deleted, or when tracking otherwise fails \u2014 the mechanism is documented, the failure mode is not, and no published sample shows a populated trackingCriteria",
        "A commit id or blob OID on threadContext \u2014 anchoring is (path, line, offset) only; any OID must be joined in from the iterations API",
        "A native field recording which iteration resolved a thread",
        "$expand on Pull Request Threads or Pull Request Thread Comments, in any api-version from 4.1 through 7.2-preview.1",
        "Any server-side filter for threads changed since a timestamp \u2014 no since, no $filter, no $top/$skip, no pagination, no continuation token on GET threads",
        "Documented ETag / If-None-Match conditional request support on the threads endpoints",
        "A change identity that is stable across pull requests \u2014 iteration ids are scoped to a single PR, and abandoning and reopening starts the numbering over",
        "A GraphQL API of any kind (Azure DevOps is REST only), so no equivalent of GitHub's reviewThreads/isResolved graph queries",
        "Documented size, key-count, or key-length limits for the thread properties bag or the PR properties bag",
        "Any documented ability for an API caller to set commentType = system on a comment it creates",
        "A first-class threadId field in the documented 'Pull request commented on' service hook payload \u2014 the thread id appears only inside the message link as ?discussionId=N"
      ],
      "notes": "STRATEGIC SHAPE: Azure DevOps and GitHub fail in opposite directions, and the trade is unusually clean. Azure DevOps natively provides the two things the borrowed model (Gerrit/Radicle/jj/ghstack) had to invent on GitHub \u2014 an ordered series of immutable, individually addressable revisions that each name their own base (PR iterations, with commonRefCommit as the base and sourceRefCommit as the head, surviving force-push), and server-side comment-anchor forwarding that is exposed through REST rather than only rendered in the UI ($iteration/$baseIteration plus trackingCriteria, which even follows renames). Do not build a revision ledger or a comment-forwarding heuristic for Azure DevOps; adopt iterations and changeTrackingId. What Azure DevOps does NOT have is the trigger unit the whole design is built on: there is no review envelope, so nothing carries exactly one disposition. That must be synthesized from votes plus a time window, and it is the adapter's largest deliverable.\n\nTHE SELF-TRIGGER PROBLEM LARGELY DISAPPEARS. The GitHub pathology proven live \u2014 the agent's reply to a review comment gets filed under a NEW review object with non-null submitted_at, so the platform re-triggers on its own writing \u2014 has no analogue here, because there is no review object to be filed under. An agent reply is just another comment in an existing thread. Suppression reduces to comparing comment.author.id against the agent's own identity GUID. Two residual risks to verify live before enabling writes: (1) whether a status-only PATCH of a thread emits ms.vss-code.git-pullrequest-comment-event (if it does, an agent resolving threads can loop); (2) whether the agent's own writes cause git.pullrequest.updated with notificationType=StatusUpdateNotification. Do not rely on commentType=system for suppression \u2014 it means 'emitted by the Azure DevOps service', not 'emitted by a bot', and the agent's own comments come back as commentType: text.\n\nRECOMMENDED TRIGGER TOPOLOGY: subscribe to git.pullrequest.updated with notificationType=ReviewerVoteNotification as the primary disposition trigger (closest native equivalent to GitHub's review-submitted webhook); treat ms.vss-code.git-pullrequest-comment-event as a low-priority nudge only, since its documented payload is truncated and lacks a first-class threadId; and add a quiet-period timer for the 'reviewer commented but never voted' case, which has no native trigger at all. Reconcile with a full GET threads on a slow cadence, because there is no since filter and no pagination.\n\nFOUR THINGS TO VERIFY EMPIRICALLY BEFORE COMMITTING TO A DESIGN \u2014 all four are places where the docs are genuinely thin rather than merely terse: (a) what threadContext contains when the anchored line was deleted in the requested iteration, and whether the thread still appears in the response at all; (b) the exact accepted write shape for thread properties (raw scalar vs the {$type,$value} envelope) and any practical size ceiling, since none is documented; (c) whether the agent's service identity can PATCH the status of a thread it did not author, which the REST reference never constrains but the UI doc frames as a PR-author action; (d) the full service-hook payload for the comment event.\n\nKNOWN REST/UI AND DOC-INTERNAL DISAGREEMENTS worth encoding as tests: REST `fixed` renders as 'Resolved' in the UI and there is no `resolved` enum member; REST `byDesign` does not appear in the documented UI dropdown; CommentPosition.offset is documented as 0-based on the 7.1 pages and 1-based on the 7.2 page (Microsoft's own sample implies 1-based); the PropertiesCollection type claims Int32 is preserved while the PR-properties sample shows an integer coming back as System.String; and the REST API versioning table (last updated 2025-04-10) stops at 7.0 even though 7.1 Server pages are published for every endpoint in this area.\n\nIMPLEMENTATION GUIDANCE FOR THE SEALED review/v1 RECORD: seal (changeTrackingId, iterationId, item.objectId as the blob OID, origFilePath, line range, thread status, author GUID) at ingest and never re-derive. Iterations are immutable, so those joins are cacheable forever, and sealing them makes the record independent of the one undocumented behaviour that could otherwise silently corrupt it. Store the JetBridge change-id as a STRING in the PR-level property bag (PATCH .../pullRequests/{id}/properties, application/json-patch+json), not as a number, because that path stringifies integers.\n\nNo files were created; this is documentation research only. All claims above are sourced to learn.microsoft.com REST reference pages at api-version 7.1 unless another version is named explicitly."
    },
    "checks": [
      {
        "area": "threads",
        "claims_checked": [
          {
            "claim": "Comment thread + comment CRUD surface: routes, scopes, comment.id int16 'IDs start at 1 and are unique to a pull request' (therefore commentId alone is unique PR-wide), 500-comments-per-thread limit, soft DELETE, thread.isDeleted when all comments deleted.",
            "status": "OVERSTATED",
            "evidence": "Routes, methods and scopes verified verbatim on the four cited pages (POST/GET/GET{id}/PATCH threads with vso.code_write|vso.threads_full to write, vso.code|vso.threads_full to read; POST/DELETE .../threads/{threadId}/comments[/{commentId}] at api-version=7.1). The 500 limit is real but is NOT on any page the claim cites for it - it is the operation summary of Pull Request Thread Comments - Create: 'Create a comment on a specific thread in a pull request (up to 500 comments can be created per thread).' Comment.id IS typed 'integer (int16)' with description 'The comment ID. IDs start at 1 and are unique to a pull request.' and thread.isDeleted IS 'Specify if the thread is deleted which happens when all comments are deleted.' Soft-delete confirmed: Comment.isDeleted = 'Whether or not this comment was soft-deleted', and List sample thread 148 comment 2 returns isDeleted:true with the content key absent. THE OVERSTATEMENT: the derived assertion 'commentId alone is already unique within the PR' is contradicted by Microsoft's own List sample, where comment id 1 appears in all eight threads (141-148) and the reply in thread 148 is id 2. Ids visibly restart per thread; the doc sentence and the sample disagree, so JetBridge must address comments by (threadId, commentId). Secondary inconsistency: the DELETE page types the commentId path parameter as 'integer (int32)' while Comment.id is int16. Pages: /rest/api/azure/devops/git/pull-request-thread-comments/create, /pull-request-thread-comments/delete, /pull-request-threads/list, /pull-request-threads/create (all ?view=azure-devops-rest-7.1)."
          },
          {
            "claim": "Positional anchoring: threadContext = {filePath, leftFileStart/End, rightFileStart/End} of CommentPosition {line, offset}; character-level spans supported (line 5 offset 1-13 in MS sample); filePath 'It's up to the client to use any path format'; threadContext carries NO commit id and NO blob OID.",
            "status": "CONFIRMED",
            "evidence": "CommentThreadContext definition matches exactly - five fields, no commit or OID field anywhere in it. CommentPosition = {line, offset}. filePath description verbatim: 'File path relative to the root of the repository. It's up to the client to use any path format.' The Create sample body uses '/new_feature.cpp' with rightFileStart {line:5, offset:1} and rightFileEnd {line:5, offset:13}, and the second Create example (PR-level comment) sends no threadContext and returns threadContext:null - both sub-claims confirmed. CAVEAT ON A CITED SOURCE: the claim also cites the review-pull-requests UI page for spans; that page describes only 'hover over the line you want to comment on' and 'select multiple lines' and never describes character-offset selection. Sub-line spans are a REST-only affordance, not something the web UI is documented to produce. See also the separate offset-base finding below. Pages: /rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1 and /azure/devops/repos/git/review-pull-requests?view=azure-devops."
          },
          {
            "claim": "GitPullRequestIteration is an IMMUTABLE ordinally-numbered revision carrying id, sourceRefCommit, targetRefCommit, commonRefCommit ('The first common Git commit of the source and target refs'), commits[], push, author, createdDate, reason, old/newTargetRefName; IterationReason = push|forcePush|create|rebase|unknown|retarget|resolveConflicts; force-push does not destroy the ledger.",
            "status": "OVERSTATED",
            "evidence": "Field list and descriptions confirmed verbatim on /rest/api/azure/devops/git/pull-request-iterations/get?view=azure-devops-rest-7.1, including commonRefCommit 'The first common Git commit of the source and target refs.' and the oldTargetRefName/newTargetRefName retarget pair. IterationReason is exactly the seven claimed values in that order. The ordinal claim is supported by the iteration-changes page: 'Allowed values are between 1 and the maximum iteration on this pull request.' The 'Iteration one is the head of the source branch at the time the pull request is created and subsequent iterations are created when there are pushes to the source branch' quote is verbatim but lives on the ITERATION-CHANGES page (iterationId parameter description), not the iterations/get page the claim cites. The force-push sentences are verbatim from the UI doc and are about the Git PR Updates tab, not TFVC: 'A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset' and 'The commit history in the Commits tab is overwritten if the PR author force-pushes a different commit history.' THE OVERSTATEMENT: Microsoft NEVER documents iterations as immutable - the word does not appear - and GitPullRequestIteration carries 'updatedDate | string (date-time) | The updated date of the pull request iteration.', which cuts directly against it. The recommendation to treat (commonRefCommit, sourceRefCommit) as a durable base/head pair rests on an undocumented durability assumption."
          },
          {
            "claim": "changeTrackingId is per-FILE identity stable across iterations, returned with changeId, changeType, and item {objectId = blob OID after change, originalObjectId = blob OID before (present when $compareTo supplied), path}; item.objectId hands you a real immutable blob OID per changed file per iteration.",
            "status": "OVERSTATED",
            "evidence": "The identity half is fully documented on /rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1: GitPullRequestChange.changeTrackingId = 'ID used to track files through multiple changes.' and the thread-side pullRequestThreadContext.changeTrackingId = 'Used to track a comment across iterations. This value can be found by looking at the iteration's changes list. Must be set for pull requests with iteration support. Otherwise, it's not required for legacy pull requests.' - both verbatim. $top/$skip/$compareTo all exist ($top default 100, max 2000; $compareTo 'The default value is zero which indicates the comparison is made against the common commit between the source and target branches'). THE OVERSTATEMENT is the item object: in every definition table on both cited pages GitPullRequestChange.item is typed 'item | string (T) | Current version.' - objectId, originalObjectId and path appear ONLY inside sample JSON and are in no documented schema. Microsoft nowhere states that objectId is a blob OID, nor that it is immutable, nor that originalObjectId appears only under $compareTo; the latter is inferred from a single pair of samples (the plain call omits it, the $compareTo=1 call includes it). Anything JetBridge builds on item.objectId is building on undocumented sample shape, not on the reference."
          },
          {
            "claim": "Server-side FORWARDING of comment anchors across iterations IS REST-exposed via $iteration/$baseIteration on GET threads and GET threads/{threadId}; original position moves into trackingCriteria; presence is the discriminator; renames are followed.",
            "status": "CONFIRMED",
            "evidence": "All four load-bearing quotes verified verbatim on /rest/api/azure/devops/git/pull-request-threads/list and /get (?view=azure-devops-rest-7.1). List page: '$iteration - If specified, thread positions will be tracked using this iteration as the right side of the diff' and '$baseIteration - If specified, thread positions will be tracked using this iteration as the left side of the diff.' Both parameters are present on Get too (wording there is singular, 'thread position will be tracked' - trivial variance the claim does not reflect). CommentTrackingCriteria carries exactly origFilePath, origLeft/RightFileStart/End, firstComparingIteration, secondComparingIteration. Discriminator quote verbatim: 'The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria', reinforced per-field by 'Threads were tracked if this is greater than 0.' Rename quote verbatim: 'Original filepath the thread was created on before tracking. This will be different than the current thread filepath if the file in question was renamed in a later iteration.' The operational rule the claim derives (call without $iteration for authored positions, with it for current) is a sound reading of these but is not itself stated by Microsoft."
          },
          {
            "claim": "Behaviour when the commented line is deleted or tracking fails is NOT documented; there is no isOutdated/outdated/isStale field; no published sample shows trackingCriteria populated.",
            "status": "CONFIRMED",
            "evidence": "Verified as an absence across every version pulled. GitPullRequestCommentThread and Comment definitions at api-version 4.1, 7.1 and 7.2 contain no isOutdated, outdated, isStale or equivalent boolean - the only booleans are isDeleted (thread) and isDeleted (comment). No page states what threadContext contains when the anchored line no longer exists in the requested iteration. On samples: in the List sample (byte-identical in 4.1, 7.1 and 7.2) pullRequestThreadContext is either null (threads 141-147) or {iterationContext, changeTrackingId} with no trackingCriteria (thread 148); the Create page's two samples likewise show no trackingCriteria; and the Get and Update pages have NO Examples section at all, so there is nowhere else in the 7.1 reference for such a sample to exist. The claim's characterisation of this as the riskiest unknown in the area, and its instruction to pin it with a live experiment before any code depends on it, both stand."
          },
          {
            "claim": "threadContext vs iterationContext vs trackingCriteria are three distinct objects with distinct roles; first==second encodes viewing against the merge base; on create the client supplies threadContext + iterationContext + changeTrackingId, trackingCriteria is server-produced output only.",
            "status": "CONFIRMED",
            "evidence": "All three objects exist and are distinct exactly as described. The nuance quote is verbatim on CommentIterationContext.firstComparingIteration: 'The iteration of the file on the left side of the diff when the thread was created. If this value is equal to SecondComparingIteration, then this version is the common commit between the source and target branches of the pull request.' (The claim omits that both iterationContext fields are typed int16 while trackingCriteria's are int32.) The create-side composition is confirmed by the Create sample request, which sends threadContext plus pullRequestThreadContext:{changeTrackingId:1, iterationContext:{firstComparingIteration:1, secondComparingIteration:2}} and no trackingCriteria. TWO SMALL OVERREACHES, neither material: 'authored once and immutable' is not stated anywhere for iterationContext; and 'trackingCriteria is server-produced output only' is an inference - trackingCriteria is a member of GitPullRequestCommentThreadContext, which IS listed in the Create and Update request-body tables, so nothing documented forbids sending it. The doc only implies read-side semantics via 'if this property is filled out when the thread is RETURNED'. Pages: /pull-request-threads/create and /list ?view=azure-devops-rest-7.1."
          },
          {
            "claim": "CommentThreadStatus is a 7-value enum on the THREAD (unknown|active|fixed|wontFix|closed|byDesign|pending); wire accepts string or ordinal (samples send status:1, responses return 'active'); the Update page has NO example; threads with no status omit the key entirely.",
            "status": "CONFIRMED",
            "evidence": "Enum is exactly those seven values with those exact descriptions ('The thread status is resolved as fixed', 'resolved as won't fix', 'resolved as by design', etc.), and status appears only on GitPullRequestCommentThread, never on Comment. Both Create samples send \"status\": 1 and both responses return \"status\": \"active\" - the asymmetry is real and Microsoft-demonstrated. The doc-thinness claim is exactly right: /rest/api/azure/devops/git/pull-request-threads/update?view=azure-devops-rest-7.1 has NO Examples section whatsoever - it goes from Security straight to Definitions - and its request-body table is the full GitPullRequestCommentThread, so the universally used partial-PATCH shape is never demonstrated by Microsoft. The absent-status observation is confirmed in the List sample: system threads 141-146 carry no status key at all, while user threads 147 and 148 carry \"status\": \"active\". Same in the 4.1 and 7.2 renderings."
          },
          {
            "claim": "Who may set thread status is only partially documented: UI doc says PR authors update status and any replier gets Reply & resolve; REST documents no permission constraint beyond scope; verify with the agent's real identity.",
            "status": "CONFIRMED",
            "evidence": "UI quote is verbatim on /azure/devops/repos/git/review-pull-requests?view=azure-devops: 'New comments start with an Active status. PR authors update the status during the review process to indicate how they addressed reviewer feedback and suggestions. PR authors can select a comment status from the status dropdown list.' The Reply & resolve behaviour is documented, though the claim's inner quote is a light paraphrase - the page actually reads 'If you select Reply & resolve, the comment status changes to Resolved. PR authors can also directly change a comment's status.' Confirmed by absence on the REST side: the Update page's only access statement is the oauth2 Scopes block (vso.code_write, vso.threads_full); there is no permission section, no 403 documented, and no statement about authorship. The claim's own 'uncertain' confidence and its instruction to verify with the service identity are the correct posture - Microsoft nowhere guarantees a non-author can set status via REST."
          },
          {
            "claim": "Replies via parentCommentId inside the same thread; roots carry parentCommentId 0; reply body is {content, parentCommentId, commentType}; author is server-assigned and must be omitted or you get 'An author of a comment cannot be updated' (azure-devops-node-api issue 173).",
            "status": "CONFIRMED",
            "evidence": "Comment.parentCommentId = 'The ID of the parent comment. This is used for replies.' verbatim. Every root comment in every sample carries parentCommentId: 0 (never null), and the reply in List thread 148 carries parentCommentId: 1. The reply POST sample body on /pull-request-thread-comments/create?view=azure-devops-rest-7.1 is character-for-character what the claim states: {\"content\": \"Good idea\", \"parentCommentId\": 1, \"commentType\": 1}, returning id 2. GitHub issue Microsoft/azure-devops-node-api#173 exists, is CLOSED, and is titled 'Creating a new PR comment thread fails with An author of a comment cannot be updated.' - it reports the exact error string 'An author of a comment cannot be updated. Parameter name: Author', attributes it to the generated typedefs marking every field required, and confirms that omitting author succeeds. TWO PRECISION NOTES: the issue is about gitApi.createThread (thread creation), not the POST comments endpoint the claim files it under; and 'author' IS listed in the documented request-body table for comment create, so the 'must be omitted' rule is community-established, not documented. The claim's 'structurally flat, reply-to-a-reply nesting undocumented' hedge is correct - no page addresses nesting."
          },
          {
            "claim": "commentType 'system' marks service-generated threads but does NOT identify a bot; agent writes return commentType 'text'; the reliable discriminator is comment.author.id; get identity from a supported API because tokens are encrypted from summer 2025; no review envelope means the GitHub self-retrigger pathology has no analogue.",
            "status": "CONFIRMED",
            "evidence": "Every event thread in the List sample (MergeAttempt 141/146, ReviewersUpdate 142/144, VoteUpdate 143, RefUpdate 145) carries commentType: \"system\" with author displayName '[DefaultCollection]\\\\Project Collection Service Accounts' and isContainer: true; user threads 147/148 carry commentType: \"text\". CommentType is defined only as 'The comment type at the time of creation' with values unknown|text|codeChange|system, and no page documents whether a caller may set it - confirmed by absence, so 'do not assume it is settable' is right. (Note: the ordinal 3 = system that the claim cites is itself undocumented; it follows only from 0-based enum ordering, and samples only ever show commentType: 1 on the wire.) The token quote is verbatim on /azure/devops/integrate/get-started/authentication/authentication-guidance?view=azure-devops: 'Starting summer 2025, Azure DevOps is further encrypting authentication tokens, which means clients can't read token payloads. Any application that decodes tokens to extract claims breaks.' The same page's remedy matches the claim: 'Use supported REST APIs - retrieve user or organization data from Azure DevOps REST APIs.' The headline holds by absence: nothing resembling a review-envelope object exists anywhere in the Git PR reference."
          },
          {
            "claim": "Thread properties bag is writable by integrations (appears in Create AND Update request bodies), values typed as {$type,$value} with mixed types in MS's own sample; no size/key limits documented; no example of an integration writing thread properties.",
            "status": "CONFIRMED",
            "evidence": "properties | PropertiesCollection | 'Optional properties associated with the thread as a collection of key-value pairs.' appears in the request-body table of BOTH /pull-request-threads/create and /pull-request-threads/update (?view=azure-devops-rest-7.1) - I verified each page separately. PropertiesCollection is defined verbatim as claimed, including 'Values of type Byte[], Int32, Double, DateType and String preserve their type, other primitives are retuned as a String.' Mixed types confirmed in the List sample: CodeReviewThreadType is System.String while CodeReviewReviewersUpdatedNumAdded and Microsoft.TeamFoundation.Discussion.SupportsMarkdown are System.Int32; Microsoft namespaces its own keys (CodeReview*, Microsoft.TeamFoundation.Discussion.SupportsMarkdown) as claimed. Both caveats confirmed by absence: no page states a size, key-count or key-length limit for PropertiesCollection anywhere, and no sample on any threads page shows a non-Microsoft caller writing properties - every Create sample returns \"properties\": {} - so the accepted request shape for integration-authored thread properties is genuinely undocumented and must be established empirically."
          },
          {
            "claim": "PR-level property bag via PATCH .../pullRequests/{id}/properties with application/json-patch+json; documented add/replace/remove semantics; type-coercion trap where 8 returns as System.String '8' but a datetime is preserved as System.DateTime rounded to ms; GET .../properties exists.",
            "status": "CONFIRMED",
            "evidence": "On /rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1 the semantics paragraph is verbatim; the claim's ellipsis elides only 'the path cannot be empty' from the remove clause. Media type stated exactly as 'Media Types: \"application/json-patch+json\"'. The coercion trap is demonstrated by Microsoft's own sample: the request sends {\"op\":\"add\",\"path\":\"/sampleId\",\"value\":8} and the response returns \"sampleId\": {\"$type\":\"System.String\",\"$value\":\"8\"}, while {\"value\":\"2017-09-25T15:26:49.4760511Z\"} returns {\"$type\":\"System.DateTime\",\"$value\":\"2017-09-25T15:26:49.477Z\"} - integer stringified, datetime preserved and rounded to milliseconds, exactly as claimed, and directly contradicting the PropertiesCollection definition's own 'Int32 ... preserve their type'. Microsoft.Git.PullRequest.SourceRefName/TargetRefName are present in the response as claimed. I separately verified the GET route the claim lists but did not source: 'Pull Request Properties - List', GET .../pullRequests/{pullRequestId}/properties?api-version=7.1, returns PropertiesCollection, scope vso.code. The store-change-id-as-a-string recommendation is well founded."
          },
          {
            "claim": "System threads form a machine-readable event ledger: CodeReviewThreadType VoteUpdate/RefUpdate/MergeAttempt/ReviewersUpdate with the listed typed companion keys; the four types are sample-derived not an exhaustive enum; the numeric vote mapping is documented on the reviewers side, not on the threads page.",
            "status": "CONFIRMED",
            "evidence": "Every property key and type the claim lists is present verbatim in the List sample. VoteUpdate: CodeReviewVotedByTfId, CodeReviewVotedByDisplayName, CodeReviewVoteResult (System.String \"10\"). RefUpdate: CodeReviewRefName, CodeReviewRefNewHeadCommit, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount (System.Int32 1), CodeReviewRefUpdatedByTfId - the new head SHA is indeed inline. MergeAttempt: CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit. ReviewersUpdate: CodeReviewReviewersUpdatedAddedTfId, RemovedTfId, NumAdded, NumRemoved (both System.Int32). The vote mapping is documented verbatim as IdentityRefWithVote.vote: 'Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'. ENDPOINT PRECISION: that text lives on Pull Request Reviewers - Create Pull Request Reviewer (PUT .../reviewers/{reviewerId}?api-version=7.1), not the GET .../reviewers the claim lists - same returned type, different page. The claim's two honesty notes are both correct: the four thread types are only what the sample happens to contain (no exhaustive enum is published anywhere), and the UI doc names five vote options (Approve, Approve with suggestions, Wait for author, Reject, Reset feedback) with no integers."
          },
          {
            "claim": "Services vs Server: server-rest monikers published through 7.1 while the versioning table stops at 7.0 (stale); safe floor 7.1 Services / 7.0 Server 2022 / 6.0 Server 2020; 7.2 is preview-only as 7.2-preview.1 with a 12-week deprecation policy; the model is unchanged back to 4.1; on-prem caveats (no az CLI, no PR URL in push output, SMTP needed for email).",
            "status": "CONFIRMED",
            "evidence": "On /azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops the table is reproduced exactly as claimed - columns stop at 7.0, Services and Server 2022 both X through 7.0, Server 2020 through 6.0, Server 2019 through 5.0, TFS 2018 through 4.0 - with ms.date 2025-04-10, confirming content and date. It is demonstrably stale: azure-devops-server-rest-7.1 appears in the Other Supported Versions list of every endpoint page I fetched (threads list/create/get/update, thread-comments create/delete, iterations get, iteration-changes get, properties list/update), and the table has no 7.1 column at all even for Services. Preview claim confirmed exactly: the 7.2 threads/list page prints 'API Version: 7.2-preview.1', its template reads api-version=7.2-preview.1, and the parameter row says 'This should be set to 7.2-preview.1 to use this version of the api.' Deprecation quote verbatim: 'After an API is released (1.0, for example), its preview version (1.0-preview) is deprecated and can be deactivated after 12 weeks.' Back-compat spot-checked at 4.1: $iteration and $baseIteration both present with identical descriptions, CommentTrackingCriteria present in full, GitPullRequestCommentThreadContext carries all three of changeTrackingId/iterationContext/trackingCriteria, and CommentThreadStatus has all seven values. All three on-prem caveats verbatim: 'Azure DevOps CLI commands aren't supported for Azure DevOps Server.', 'This feature is available in Azure DevOps Services only. Azure DevOps Server doesn't display the pull request URL in push output.', and 'For the email feature to work, your administrator must configure an SMTP server.' ONE ADDITION THE CLAIM MISSES, material on-prem: the vso.code_write / vso.threads_full scopes the CRUD claim leans on are an OAuth construct, and the authentication guidance states 'OAuth 2.0 and Microsoft Entra ID authentication are available for Azure DevOps Services only, not Azure DevOps Server' - on-prem the adapter must use PAT or Windows auth, and the documented Scopes block does not describe its auth path."
          },
          {
            "claim": "Radicle-style OID pin is workaround-only but cheaply synthesizable via iteration -> sourceRefCommit/commonRefCommit and iterations/{id}/changes -> entry matching changeTrackingId -> item.objectId, 'the immutable BLOB OID of that exact file at that exact iteration'; every component is immutable; two cacheable GETs per iteration.",
            "status": "OVERSTATED",
            "evidence": "The negative half is correct and confirmed: threadContext has no OID field, and the properties bag is the only writable place to put one. The navigation path is real - iterationContext.secondComparingIteration, GET iterations/{id} returning sourceRefCommit.commitId and commonRefCommit.commitId, and GET iterations/{id}/changes returning entries keyed by changeTrackingId all exist as described. BUT the load-bearing terminal step is not documented at all: GitPullRequestChange.item is typed 'string (T) | Current version' in every definition table, so item.objectId is sample-only; Microsoft never calls it a blob OID; and 'every component is immutable' is asserted for objects Microsoft never describes as immutable (GitPullRequestIteration even carries updatedDate). The stated confidence 'inferred' is right in kind but understates the gap - this is an inference from undocumented sample keys, not from documented fields. The sealing recommendation is still the right architectural call, since it is the only thing that makes the record independent of the undocumented tracking-failure behaviour, but the schema of what gets sealed must be pinned empirically rather than read off the reference."
          },
          {
            "claim": "Declarative resolution is partial: agent can PATCH status to fixed/byDesign/wontFix, reply naming the iteration, and stash typed metadata in thread properties; but NO native field links a thread to the revision that resolved it, and lastUpdatedDate is the only temporal correlate.",
            "status": "CONFIRMED",
            "evidence": "Both write mechanisms are documented: the /pull-request-threads/update?view=azure-devops-rest-7.1 request-body table contains status (CommentThreadStatus) and properties (PropertiesCollection), so status plus typed metadata on an existing thread is a documented single PATCH. The absence is confirmed across all three definition tables (4.1, 7.1, 7.2): GitPullRequestCommentThread has no resolvedBy, resolvedIn, closedByIteration or equivalent, and GitPullRequestCommentThreadContext's only iteration-bearing fields are iterationContext (authoring-time) and trackingCriteria (server tracking) - neither records a resolving revision. lastUpdatedDate ('The time this thread was last updated') is indeed the only temporal correlate. IMPORTANT CAVEAT ON THE 'expressive vocabulary' argument: byDesign is settable via REST but is NOT offered in the web UI. The UI status dropdown is documented as exactly five options - Active, Pending, Resolved, Won't fix, Closed - with no 'By design', and the UI's 'Resolved' is the REST enum's 'fixed'. A status the agent writes as byDesign therefore has no documented UI rendering, and the two vocabularies disagree in both directions."
          },
          {
            "claim": "[NEW FINDING, in no submitted claim] CommentPosition.offset has a self-contradictory documented base across api-versions, which is load-bearing for character-level anchoring.",
            "status": "CONFIRMED",
            "evidence": "Same field, same generated reference, two different renderings. At api-version 7.1 (Create, List, Get and Update all agree): 'offset | integer (int32) | The character offset of a thread's position inside of a line. Starts at 0.' At api-version 4.1 and at 7.2-preview.1: 'The character offset of a thread's position inside of a line. Starts at 1.' The sibling line field says 'Starts at 1' consistently everywhere. 7.1 - the version this whole research targets - is the odd one out, and it is the text an implementer would read. Microsoft's own sample anchors offset 1 through offset 13 within a line, which reads naturally as 1-based and sits awkwardly with the 7.1 'Starts at 0' text. Any JetBridge code that computes, seals or replays character spans must pin this empirically and must not trust the 7.1 page. Pages: /rest/api/azure/devops/git/pull-request-threads/list at ?view=azure-devops-rest-7.1, ?view=azure-devops-rest-4.1 and ?view=azure-devops-rest-7.2."
          }
        ]
      },
      {
        "area": "threads",
        "claims_checked": [
          {
            "claim": "Comment thread + comment CRUD surface is native and first-class; Comment.id is int16, 'IDs start at 1 and are unique to a pull request'; Thread.id is int32; hard limit 'up to 500 comments can be created per thread'; DELETE is a soft delete (isDeleted:true, content stripped); thread.isDeleted 'happens when all comments are deleted'; scopes vso.code_write / vso.threads_full.",
            "status": "CONFIRMED",
            "evidence": "Every element verified verbatim. Comment definition table on both /pull-request-threads/create and /pull-request-thread-comments/create (api-version 7.1): 'id | integer (int16) | The comment ID. IDs start at 1 and are unique to a pull request.' and 'parentCommentId | integer (int16)'. GitPullRequestCommentThread: 'id | integer (int32)'. The 500 limit is the operation description itself on the thread-comments Create page: 'Create a comment on a specific thread in a pull request (up to 500 comments can be created per thread).' \u2014 note it is a one-line description, not a callout, so it is easy to miss. Soft delete verified in a live sample: the List sample (I read the tfs-4.1 rendering, identical to 7.1) shows thread 148 comment id 2 with \"isDeleted\": true and NO content key at all, while the parent thread keeps \"isDeleted\": false. Scopes table on Create/Update lists exactly vso.code_write and vso.threads_full. No community report contradicting any of this was found. Two omissions worth noting: (i) CommentType has FOUR values, not two \u2014 'unknown | text | codeChange | system'; the claim set never mentions codeChange, and an adapter that switches on text/system will mis-bucket it. (ii) GitPullRequestCommentThread also carries an 'identities' map (<string, IdentityRef>) in 7.1 that is absent in the tfs-4.1 shape."
          },
          {
            "claim": "threadContext gives positional anchoring with character-level spans (leftFileStart/End, rightFileStart/End, each CommentPosition{line,offset}), carries NO commit id and NO blob OID, and filePath is 'up to the client'.",
            "status": "CONFIRMED",
            "evidence": "Definition tables verified verbatim on the 7.1 Create page: CommentThreadContext has exactly filePath/leftFileEnd/leftFileStart/rightFileEnd/rightFileStart; CommentPosition is {line int32 'Starts at 1', offset int32 'Starts at 0'}; filePath is 'File path relative to the root of the repository. It's up to the client to use any path format.' No commit/OID field exists anywhere on the object. Microsoft's own sample anchors line 5 offset 1 \u2192 line 5 offset 13. PRACTICE CAVEAT the claim does not carry, and it bites bots specifically: there is NO server-side validation of the span. microsoft/azure-devops-mcp issue #793 (opened 2025-12-12 by dlecan, labelled Bug, closed via PR #802, shipped v2.4.0) \u2014 an agent posted a thread with rightFileStart but no rightFileEnd; the REST API accepted it and returned 200 with that exact threadContext, and the Azure DevOps web UI then threw 'TypeError: can't access property \"line\", s is undefined' in ms.vss-code-web.pr-details-content, breaking the whole PR-details region. Microsoft collaborator danhellem confirmed: 'I will fix the tool to make sure that filestart and fileEnd both have values in order for you save' \u2014 i.e. the fix was client-side; the service still accepts the corrupting payload. Follow-on issue #1265 shows the offsets are interpreted as a REPLACE RANGE: a ```suggestion block with rightFileEndOffset shorter than the original line silently leaves the tail of the original line concatenated onto the suggestion ('padding: functions.toRem(8) functions.toRem(16);6);'). Both issues cite #514 'LLMs struggle with offset parameters'. Conclusion: spans are real and more expressive than GitHub, but they are unvalidated and silently corrupting when set wrong."
          },
          {
            "claim": "GitPullRequestIteration is an immutable, ordinally numbered revision with its own base/head, and 'A force-pushed changeset doesn't overwrite the changeset history and appears in the changeset list like any other changeset' \u2014 the iteration ledger survives force push.",
            "status": "CONFIRMED",
            "evidence": "The quote is verbatim and current: learn.microsoft.com/azure/devops/repos/git/review-pull-requests (ms.date 2026-05-27, monikers azure-devops / -2022 / -server), step 6 of 'Review changes as a human reviewer'. The complementary sentence is also there and is the sharper one: 'The commit history in the Commits tab is overwritten if the PR author force-pushes a different commit history, so the commits shown in the Commits tab might differ from the commits shown in the Updates tab.' Independently corroborated behaviourally by developercommunity 891722 (Mikhail Dobrinin, 2020-01-21): after a squash + `git push -f`, the Updates tab still showed BOTH 'update 1' and 'update 2' \u2014 the earlier iteration was not destroyed. Note the doc's odd use of TFVC vocabulary ('changeset') for what the REST API calls an iteration; that is Microsoft's wording, not an error in the claim. UI/REST disagreement worth recording: the Updates tab renders a force push as an ordinary 'pushed 1 commit creating update 2' with no force-push label, which is the entire substance of 891722 (closed 2021-07-22 as an inactive suggestion). The IterationReason enum value `forcePush` is REST-only."
          },
          {
            "claim": "(adversarial focus a) The commits named by an old iteration remain fetchable git objects after a force push and after garbage collection.",
            "status": "UNVERIFIABLE",
            "evidence": "This is the load-bearing durability question behind the whole 'immutable revision series' design and I could not settle it. Microsoft documents that the iteration LEDGER survives force push (the Updates tab entry persists, and GitPullRequestIteration retains sourceRefCommit/commonRefCommit/commits[]), but nothing in the Azure Repos documentation states whether the referenced commit OBJECTS are kept reachable \u2014 no documented per-iteration keep-alive ref, no statement of a retention window, no GC policy page for Azure Repos. I found no community report either confirming durability or reporting a 'commit not found' on an old iteration. Do not assume the ledger's SHAs are dereferenceable. Concrete test: create a PR, push twice, force-push over iteration 2, wait, then GET /iterations/{1}/changes and GET /commits/{iteration1.sourceRefCommit.commitId} and see whether both still resolve. Related documented hard limit the claim set omits entirely: per learn.microsoft.com/azure/devops/repos/git/pull-request-status, a PR created with more than 100,000 modified files 'won't support iterations' at all \u2014 subsequent changes are included but no new iteration is created. For JetBridge that means the revision noun is not universally available."
          },
          {
            "claim": "changeTrackingId is a per-FILE identity stable across iterations, found in the iteration's changes list, and required on thread create for iteration-supporting PRs.",
            "status": "CONFIRMED",
            "evidence": "Verbatim on the Create page: 'changeTrackingId | integer (int32) | Used to track a comment across iterations. This value can be found by looking at the iteration's changes list. Must be set for pull requests with iteration support. Otherwise, it's not required for legacy pull requests.' Present unchanged as far back as the vsts-rest-tfs-4.1 rendering of the same definition, so it is not a recent addition. No contradicting practice report found. What is NOT verified: that the same changeTrackingId is actually reused for a given file across iterations including renames \u2014 that is the documented intent, but I found no sample, no release note and no community report demonstrating it, and given that the line-tracking layer built on top of it is officially best-effort (see next claim) I would treat file-identity stability as unproven until measured."
          },
          {
            "claim": "Server-side forwarding of comment anchors across iterations is native, REST-exposed via $iteration/$baseIteration, returns trackingCriteria as the 'this moved' receipt, follows renames, and is BETTER than GitHub.",
            "status": "OVERSTATED",
            "evidence": "The API SHAPE is fully confirmed \u2014 $iteration ('thread positions will be tracked using this iteration as the right side of the diff') and $baseIteration are documented query parameters on both List and Get, present as far back as api-version=4.1, and CommentTrackingCriteria carries origFilePath ('will be different than the current thread filepath if the file in question was renamed in a later iteration') plus first/secondComparingIteration with 'Threads were tracked if this is greater than 0'. What is overstated is the reliability, and Microsoft says so in its own words. developercommunity 700623 'Pull request comments repeatedly move to the wrong line after update' \u2014 reported 2019-08-19, four independent reporters through 2020, Microsoft Resolution by Pablo N\u00fa\u00f1ez, status Closed - Lower Priority (2021-01-28): 'We try a best-effort approach to match lines from past iterations with new iterations which doesn't pretend to be infallible. In the Overview tab, the \"View original diff\" button is intended view the discussion in its original context... We're not able to prioritize this issue over other backlog items.' One reporter documented a comment still pinned to line 226 after the commented code moved to line 219. That is precisely the HEURISTIC FORWARDING the JetBridge design set out to replace with declaration \u2014 Azure DevOps does not escape it, it just does it server-side, silently, with no staleness flag. Second, corroborating report on Azure DevOps Server: developercommunity 1034197 (2020-05-15), where tracking worked on a fresh small PR (with the 'View original diff / View latest diff' toggle present) but on the team's large real PR 'this button isn't even there! It is just missing' \u2014 i.e. the tracking layer can be absent entirely, and the UI affordance disappearing implies trackingCriteria is simply not produced in that state. Closed - Not Enough Info. Verdict: keep the capability, drop 'better'; treat forwarded positions as advisory, never as the sealed anchor."
          },
          {
            "claim": "Behaviour when the commented line is deleted or tracking fails is undocumented; there is no isOutdated/outdated/isStale boolean anywhere; no published sample shows trackingCriteria populated.",
            "status": "CONFIRMED",
            "evidence": "Confirmed by exhaustion against the definition tables. I read the complete Definitions sections of pull-request-threads Create, Update (7.1) and List (tfs-4.1): GitPullRequestCommentThread has exactly _links, comments, id, identities, isDeleted, lastUpdatedDate, properties, publishedDate, pullRequestThreadContext, status, threadContext \u2014 no outdated/stale field. Comment has no such field either. Every published sample I read (7.1 Create x2, tfs-4.1 List with 8 threads) has pullRequestThreadContext either null or containing only iterationContext + changeTrackingId; trackingCriteria appears in NO sample at any api-version. The risk is worse than the claim states: this is not merely a documentation hole, it is an acknowledged best-effort mechanism (see previous claim) with an explicitly deprioritised bug, so the undocumented failure path is a path the system is known to take. The claim's mitigation \u2014 seal the anchor at ingest and never re-derive \u2014 is the right call and I would raise it from 'recommended' to 'required'."
          },
          {
            "claim": "threadContext / iterationContext / trackingCriteria have three distinct roles; first==second encodes 'viewing the whole PR against the merge base'; trackingCriteria is server-produced output only.",
            "status": "CONFIRMED",
            "evidence": "All three definitions verified verbatim on the 7.1 Create page, including 'If this value is equal to SecondComparingIteration, then this version is the common commit between the source and target branches of the pull request' and 'The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria.' trackingCriteria being output-only is consistent with every sample (never sent, never returned). One typed asymmetry the claim misses and an adapter will trip on: CommentIterationContext.firstComparingIteration/secondComparingIteration are declared integer (int16), while CommentTrackingCriteria.firstComparingIteration/secondComparingIteration are declared integer (int32). Same conceptual value, two widths, across the two objects \u2014 and the same page declares Comment.id and parentCommentId as int16 too, which caps a PR at 32,767 comments."
          },
          {
            "claim": "CommentThreadStatus is a 7-value enum on the THREAD; the Update page has no example; threads with no status return with the key entirely absent.",
            "status": "CONFIRMED",
            "evidence": "Enum verified verbatim on Create, Update and the tfs-4.1 List page: unknown | active | fixed | wontFix | closed | byDesign | pending. 'Update page has NO example' independently confirmed \u2014 I read the full /pull-request-threads/update?view=azure-devops-rest-7.1 page and there is no Examples section at all, only URI Parameters, a Request Body table that is the entire GitPullRequestCommentThread, Responses, Security and Definitions. Missing-status-on-system-threads confirmed in the List sample: threads 141\u2013146 (MergeAttempt, ReviewersUpdate, VoteUpdate, RefUpdate) have no \"status\" key, while user threads 147 and 148 carry \"status\": \"active\". REST/UI DISAGREEMENT the claim does not flag, and it matters for the declarative-resolution design: the web UI exposes only FIVE statuses \u2014 'Active, Pending, Resolved, Won't fix, Closed' (learn.microsoft.com/azure/devops/repos/git/review-pull-requests, 'Change comment status'). 'By design' has no UI dropdown entry and 'unknown' is not offered. A thread the agent sets to byDesign via REST is in a state a human reviewer cannot select and whose rendering is untested."
          },
          {
            "claim": "Any identity with contribute permission can set any thread's status via REST, including on threads it did not author.",
            "status": "UNVERIFIABLE",
            "evidence": "The two documentation halves are confirmed verbatim \u2014 the UI page says 'New comments start with an Active status. PR authors update the status during the review process... PR authors can select a comment status from the status dropdown list', and separately that a replier may choose 'Reply & resolve', which 'changes the comment status to Resolved'; the REST Update page states no permission constraint beyond the vso.code_write / vso.threads_full scopes. But the UI text names PR AUTHORS as the actors for the dropdown, which is weaker than 'anyone with contribute', and I found no community report \u2014 success or failure \u2014 of a bot/service identity PATCHing status on a reviewer-authored thread. Note the adjacent evidence that Azure DevOps does silently no-op some cross-identity writes: microsoft/azure-devops-node-api issue #611 (Tommy Wilkinson, 2024-10-03) reports that resetting a reviewer vote to 0 returns success with 'No effect on reviewer vote status after performing vote reset REST call'. Test PATCH-status-as-agent-on-reviewer-thread with the real service identity before any code depends on it, and assert the response body, not the HTTP status."
          },
          {
            "claim": "Replies use parentCommentId within the same thread; root comments carry 0; supplying `author` on create fails with 'An author of a comment cannot be updated' \u2014 'a well-known defect surface'.",
            "status": "OVERSTATED",
            "evidence": "The mechanics are confirmed: 'parentCommentId | The ID of the parent comment. This is used for replies'; every sample root comment has \"parentCommentId\": 0 and the reply sample is {content, parentCommentId: 1, commentType: 1} \u2192 returns id 2. Flatness in practice confirmed by the tfs-4.1 List sample (thread 148: comment 1 parent 0, comment 2 parent 1). Reply-to-a-reply nesting remains genuinely undocumented and undemonstrated. What is overstated is 'well-known defect surface': microsoft/azure-devops-node-api#173 is a SINGLE report (opened 2018-04-12 against vsts-node-api 6.5.0 / TFS 16.122), self-diagnosed by the reporter ('omitting the author property DOES work'), closed and relabelled as an ENHANCEMENT, with no other confirmations in the thread and none found elsewhere. It is a real trap but it is n=1 and eight years old. Worth adding: the doc actively invites it \u2014 `author | IdentityRef | The author of the comment` is listed in the documented Request Body table for POST .../threads/{id}/comments, so the reference tells you to send a field the service rejects."
          },
          {
            "claim": "commentType=system marks service-generated threads but NOT bot comments; the reliable discriminator is comment.author.id; and because Azure DevOps has no review envelope, an agent's reply cannot manufacture a new review object, so the GitHub self-retrigger pathology has no analogue.",
            "status": "CONFIRMED",
            "evidence": "Confirmed on all three legs. (1) Every event thread in the List sample carries \"commentType\": \"system\" with author displayName '[DefaultCollection]\\\\Project Collection Service Accounts', isContainer: true; user comments carry \"commentType\": \"text\". (2) (adversarial focus c) The service-hook payload DOES carry the identity needed for self-suppression \u2014 I read the raw source (MicrosoftDocs/azure-devops-docs/docs/service-hooks/events.md): the ms.vss-code.git-pullrequest-comment-event resource contains comment.author.id ('11bb11bb-cc22-dd33-ee44-55ff55ff55ff'), comment.id, comment.commentType and a full pullRequest object. (3) No review object exists anywhere in the Git REST surface, so the GitHub failure mode is structurally impossible. THREE PRACTICAL WRINKLES the claim omits: (i) the THREAD ID is not a first-class field in the payload \u2014 it appears only inside comment._links.self.href ('.../pullRequests/1/threads/5/comments/2') and comment._links.threads.href, so an adapter must URL-parse to reply; (ii) the only subscription filters documented for that event are `repository` (guid) and `branch` \u2014 there is NO author or identity filter, so the agent's own writes WILL be delivered to its own endpoint and every bit of suppression is client-side; (iii) the payload contains no threadContext, no iteration and no thread status, so any disposition decision requires a follow-up GET. Field note: a real production integration (ReviewPR, Optimizely blog, 2026-05) subscribes to git.pullrequest.created/updated rather than the comment event and dedupes by scanning threads for a marker string in its own comment content \u2014 i.e. content-marker suppression, not author-id suppression, is what practitioners are actually shipping."
          },
          {
            "claim": "(adversarial focus c, residual) Service-generated system threads (VoteUpdate, RefUpdate, MergeAttempt) do not themselves emit ms.vss-code.git-pullrequest-comment-event.",
            "status": "UNVERIFIABLE",
            "evidence": "This is the self-trigger risk that survives the 'no review envelope' argument and nobody has written it down. System events in Azure DevOps ARE comment threads \u2014 RefUpdate, VoteUpdate, MergeAttempt are literally rows in GET /threads with commentType 'system'. Microsoft's events documentation never states whether creating one of those threads fires the pull-request-comment event, and I found no community report either way. If it does, the agent's own push creates a RefUpdate system thread, which fires a comment event, which is not authored by the agent's identity (it is authored by Project Collection Service Accounts) and so passes an author-id self-filter \u2014 a loop that author-based suppression alone will not stop. Suppress on commentType=='system' as well as on author.id, and verify empirically with a throwaway webhook receiver before relying on either."
          },
          {
            "claim": "Thread properties bag is writable by integrations and is a legitimate home for a change-id / declarative resolves pointer; no size or key limits are documented; no Microsoft example shows an integration writing it.",
            "status": "CONFIRMED",
            "evidence": "`properties | PropertiesCollection | Optional properties associated with the thread as a collection of key-value pairs` appears in the Request Body table of BOTH Create and Update at 7.1, so it is declared writable. Wire shape confirmed in the List sample with mixed types in the same bag \u2014 CodeReviewMergeCommit as {\"$type\":\"System.String\",\"$value\":\"39f52d...\"}, CodeReviewRefNewCommitsCount and Microsoft.TeamFoundation.Discussion.SupportsMarkdown as {\"$type\":\"System.Int32\",\"$value\":1}. Confirmed by exhaustion that the PropertiesCollection definition documents accepted TYPES and never any limit on size, key count or key length. Confirmed that no Microsoft example anywhere shows an integration writing thread properties \u2014 every properties block in every sample is Azure's own CodeReview*/Microsoft.* keys. So the accepted request shape for a caller (raw scalar vs the {$type,$value} envelope) really is undetermined for this endpoint. No community report of anyone writing custom thread properties was found either, which is itself mildly discouraging: if this were a well-trodden extension point there would be blog posts."
          },
          {
            "claim": "PR-level property bag via PATCH .../pullRequests/{id}/properties with application/json-patch+json; add/replace/remove semantics as quoted; integers are stringified while DateTime is preserved.",
            "status": "CONFIRMED",
            "evidence": "Verified verbatim on /git/pull-request-properties/update?view=azure-devops-rest-7.1. Operation description matches the claim word for word including 'For add operation, the path can be empty. If the path is empty, the value must be a list of key value pairs' and 'For remove operation, the path cannot be empty. If the path does not exist, no action will be performed.' Media type is documented as 'application/json-patch+json'. The type-coercion trap is real and visible in Microsoft's own sample: the request sends \"value\": 8 and the response returns \"sampleId\": {\"$type\": \"System.String\", \"$value\": \"8\"}, while \"2017-09-25T15:26:49.4760511Z\" comes back as {\"$type\": \"System.DateTime\", \"$value\": \"2017-09-25T15:26:49.477Z\"} \u2014 precision truncated to milliseconds. Base64 'bytes' also round-trips as System.String, not Byte[], contradicting the PropertiesCollection blurb. Storing the change-id as a string is correct; storing anything whose exact bytes matter (a timestamp used for ordering) is not."
          },
          {
            "claim": "System threads are a machine-readable event ledger: CodeReviewThreadType VoteUpdate / RefUpdate / MergeAttempt / ReviewersUpdate with typed companion properties, letting an adapter reconstruct PR history from GET threads alone.",
            "status": "CONFIRMED",
            "evidence": "Verified property-by-property against the full List sample (threads 141\u2013146). VoteUpdate carries CodeReviewVotedByTfId, CodeReviewVotedByDisplayName and CodeReviewVoteResult as {\"$type\":\"System.String\",\"$value\":\"10\"} with comment content 'Normal Paulk voted 10'. RefUpdate carries CodeReviewRefName, CodeReviewRefNewHeadCommit, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount (System.Int32) and CodeReviewRefUpdatedByTfId \u2014 and also CodeReviewRefUpdatedBy and CodeReviewRefUpdatedByDisplayName, which the claim omits. MergeAttempt and ReviewersUpdate match as described. The claim's two honesty notes are correct and should be kept: the four types are only what the sample happens to contain, not a documented exhaustive enum, and the numeric vote mapping is not restated on the threads page (the UI page names Approve / Approve with suggestions / Wait for author / Reject / Reset feedback without integers). Caution: because a vote is recorded as a system thread whose CodeReviewVoteResult is a STRING, an adapter must string-compare, and 'Reset feedback' has no demonstrated representation in this ledger at all."
          },
          {
            "claim": "(adversarial focus d) A vote change produces a distinct, separately-subscribable event.",
            "status": "CONFIRMED",
            "evidence": "Two independent distinct signals, both confirmed. (1) Webhook: git.pullrequest.updated exposes a notificationType filter with exactly four values, verified verbatim from the docs source \u2014 PushNotification ('The source branch is updated'), ReviewersUpdateNotification ('The reviewers change'), StatusUpdateNotification ('The status changes'), ReviewerVoteNotification ('The votes score changes'). A vote is therefore separable from a push at subscription time, which GitHub cannot do. (2) Ledger: each vote also lands as its own VoteUpdate system thread with the voter GUID and the vote value. TWO UNRESOLVED EDGES: (i) nothing documents whether re-casting the SAME vote value, or a 'Reset feedback' to 0, emits ReviewerVoteNotification or creates a VoteUpdate thread \u2014 and there is a standing report that vote reset does not take effect at all through the API (azure-devops-node-api#611, 2024-10-03, closed with no resolution and no maintainer reply), so 'no vote' may be unobservable; (ii) the phrase 'votes score changes' suggests edge-triggering on change, which would mean an idempotent re-approve is silent. Both need a live probe."
          },
          {
            "claim": "Azure DevOps Services vs Server: the threads/iterations/properties model is unchanged from 4.1 through 7.1 on both; Microsoft's REST versioning table stops at 7.0 and is stale relative to the published azure-devops-server-rest-7.1 pages; preview APIs are deprecated 12 weeks after GA; on-prem loses the CLI, the push create-PR URL, and needs admin-configured SMTP.",
            "status": "CONFIRMED",
            "evidence": "Every leg verified. Versioning table read verbatim (learn.microsoft.com/azure/devops/integrate/concepts/rest-api-versioning, ms.date 2025-04-10, page updated_at 2026-07-23): columns run 1.0\u20137.0 only; Services and Server 2022 X through 7.0; Server 2020 tops out at 6.0; Server 2019 at 5.0; TFS 2018 at 4.0. Staleness confirmed directly \u2014 the threads Create/Update/List reference pages each list azure-devops-server-rest-7.1 in 'Other Supported Versions'. Preview policy verbatim: 'After an API is released (1.0, for example), its preview version (1.0-preview) is deprecated and can be deactivated after 12 weeks... Once a preview API is deactivated, requests that specify a -preview version get rejected.' Model stability at 4.1 confirmed by reading the vsts-rest-tfs-4.1 List page in full: it documents $iteration and $baseIteration with identical wording, and its Definitions include CommentTrackingCriteria (all six orig* fields + 'Threads were tracked if this is greater than 0'), changeTrackingId, iterationContext, and the complete 7-value CommentThreadStatus including byDesign and pending. On-prem behavioural notes confirmed verbatim: 'Azure DevOps Server doesn't display the pull request URL in push output', 'Azure DevOps CLI commands aren't supported for Azure DevOps Server' (repeated per-tab), and 'For the email feature to work, your administrator must configure an SMTP server' (moniker azure-devops-2022 / azure-devops-server)."
          },
          {
            "claim": "A Radicle-style pin of a comment to an immutable blob OID is synthesizable from iteration + iteration-changes (item.objectId) for two extra cacheable GETs.",
            "status": "CONFIRMED",
            "evidence": "Upgraded from the claim's 'inferred' to confirmed-in-practice on the mechanism, with one residual risk. The pattern is in production: the ReviewPR integration (Optimizely blog, 2026-05) fetches GET .../iterations/{n}/changes?$compareTo={m}&api-version=7.1 and then dereferences the returned blob sha via GET .../blobs/{sha}?$format=text&api-version=7.1 \u2014 proving item.objectId is a real, independently fetchable blob OID and not an opaque token. Combined with iterations/{id}.sourceRefCommit / commonRefCommit, every component of the (blobObjectId, path, lineRange, iterationId, changeTrackingId) tuple is obtainable. TWO RESIDUAL RISKS: (1) 'cacheable forever because iterations never change' assumes the underlying objects stay fetchable \u2014 that is exactly the GC question I could not settle (see the iteration-durability entry); a cache of OIDs is only as good as the objects behind it. (2) PRs over 100,000 modified files have no iterations at all, so this scheme has no input for them. The recommendation to seal the tuple at ingest is right and, given the confirmed best-effort nature of server-side tracking, is the only defensible design."
          },
          {
            "claim": "Declarative resolution is partial: status fixed/byDesign/wontFix plus a reply plus thread properties get closer than GitHub, but nothing natively links a thread to the iteration that resolved it.",
            "status": "CONFIRMED",
            "evidence": "Confirmed by exhaustion against the full GitPullRequestCommentThread and GitPullRequestCommentThreadContext definition tables at 7.1 and 4.1 \u2014 there is no resolvedBy, no resolvedInIteration, no closingIteration, no field of any kind pointing at a revision. lastUpdatedDate is indeed the only temporal correlate and is touched by any edit. Two qualifications to the 'better than GitHub' framing: (i) as noted above, byDesign is not selectable in the web UI's five-option status dropdown, so an agent using it writes a state humans cannot author or easily revert \u2014 prefer `fixed` and `wontFix`, which do map to the UI's 'Resolved' and 'Won't fix'; (ii) the properties-bag half of the mechanism has no demonstrated integration write path (see the thread-properties entry), so 'stored as typed forge metadata rather than parsed out of prose' is a design intention that has not yet been shown to work against this endpoint. The status half is solid; the metadata half needs a spike."
          }
        ]
      }
    ]
  },
  {
    "key": "votes-policies",
    "survey": {
      "area": "Azure DevOps reviewer votes, branch policies, and PR statuses \u2014 mapping the \"disposition\" and the \"gate\" of JetBridge's disposition-triggered review loop",
      "findings": [
        {
          "capability": "Reviewer vote model and the five vote integers",
          "exists": "native",
          "detail": "Confirmed verbatim against the 7.1 IdentityRefWithVote schema: `vote` is `integer (int16)` documented as \"Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected\". The team's assumed values are exactly right. Writing a vote is PUT of an IdentityRefWithVote body (minimally `{\"vote\": 10, \"id\": \"<reviewerGuid>\"}`) to the reviewer sub-resource; the same PUT both ADDS a reviewer and CASTS a vote (doc title: \"Add a reviewer to a pull request or cast a vote\"), so there is no separate 'become a reviewer' step. Reading is GET on the same collection or the `reviewers[]` array embedded in GET pullrequests/{id}. `az repos pr set-vote --vote {approve, approve-with-suggestions, reject, reset, wait-for-author}` is the CLI surface and confirms the same five-valued domain. Note the asymmetry vs GitHub: Azure DevOps has FIVE dispositions, not three, and two of them (`5 approved with suggestions`, `-5 waiting for author`) have no GitHub equivalent. `-5 waiting for author` is semantically the closest thing to GitHub's `changes_requested`; `-10 rejected` is stronger than anything GitHub offers (there is no GitHub 'reject'). A three-way JetBridge disposition must therefore collapse a five-valued domain, and the collapse is a policy decision, not a mechanical mapping. Suggested collapse: {10} -> approved; {5, 0} -> comment-only; {-5, -10} -> changes-requested. Warning: `5` (approve with suggestions) SATISFIES the minimum-reviewer approval policy (it is an approval vote) while carrying review comments the agent is expected to act on \u2014 so `5` is simultaneously 'merge is unblocked' and 'there is work to do'. That combination does not exist on GitHub and the routing table must decide it explicitly rather than falling through.",
          "endpoints": [
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers?api-version=7.1 (operation: Pull Request Reviewers - List)",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1 (operation: Pull Request Reviewers - Get)",
            "DELETE https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "An atomic 'submitted review' object bundling N inline comments + one verdict",
          "exists": "absent",
          "detail": "UNAMBIGUOUS ANSWER: Azure DevOps has NO object equivalent to a GitHub submitted review. Votes and comment threads are entirely independent writes, at every layer:\n\n(1) SEPARATE RESOURCES. A vote is a field on IdentityRefWithVote under `/pullRequests/{id}/reviewers/{reviewerId}`. A comment is a Comment inside a GitPullRequestCommentThread under `/pullRequests/{id}/threads`. There is no field on either that references the other \u2014 no reviewId, no submission id, no grouping key. Nothing in the GitPullRequestCommentThread schema (_links, comments, id, identities, isDeleted, lastUpdatedDate, properties, publishedDate, pullRequestThreadContext, status, threadContext) can associate a thread with a vote.\n\n(2) SEPARATE AUTH SCOPES. Voting requires `vso.code_write`; thread writes accept `vso.threads_full` (or `vso.code_write`). Microsoft models them as different capabilities.\n\n(3) SEPARATE EVENTS. Comments fire `ms.vss-code.git-pullrequest-comment-event`; votes fire `git.pullrequest.updated` with `notificationType = ReviewerVoteNotification`. Two different event ids.\n\n(4) NO BATCHING IN THE UI EITHER. The web UI posts each comment immediately on the per-comment **Comment** button (or **Reply**/**Reply & resolve**); the vote is an independent dropdown (Approve / Approve with suggestions / Wait for author / Reject / Reset feedback). There is no pending/draft review buffer and no 'Submit review' action anywhere in the documented reviewer flow. Azure DevOps reviewers do not compose a review; they emit a stream of independent writes.\n\nCONSEQUENCE FOR JETBRIDGE: the trigger unit 'one completed review' DOES NOT EXIST natively and cannot be reconstructed with certainty \u2014 there is no server-side record that reviewer R's comments 7, 8, 9 belong with R's vote at T. JetBridge must define its own trigger. The recommended substitute is the VOTE CHANGE as the trigger edge, with the comment set derived by a debounce window: on `ReviewerVoteNotification` for reviewer R, seal a review/v1 record containing R's vote plus every thread/comment authored by R with `publishedDate` after R's previous vote timestamp and at or before the current vote timestamp. Both timestamps are retrievable (see the VoteUpdate system-thread finding), so this is reconstructable deterministically after the fact \u2014 it is just not what the forge means by it. The failure mode to accept up front: a reviewer who comments and never votes produces no trigger at all, and a reviewer who votes first then comments produces a review record missing those comments.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "Vote history and timestamps \u2014 retrievable event log via system comment threads",
          "exists": "native",
          "detail": "THIS IS THE MOST IMPORTANT POSITIVE FINDING. IdentityRefWithVote carries ONLY the current vote \u2014 no timestamp, no previous value, no history. But the vote history IS retrievable, and it is documented: `GET .../pullRequests/{id}/threads` returns system-generated threads alongside user threads. The official 7.1 sample response on the Threads-List page shows these verbatim. A vote change appears as a thread with `comments[0].commentType == \"system\"`, author = `Project Collection Service Accounts`, content = e.g. \"Normal Paulk voted 10\", and a typed `properties` bag:\n  CodeReviewThreadType    = \"VoteUpdate\"   (System.String)\n  CodeReviewVotedByTfId   = \"d6245f20-2af8-44f4-9451-8107cb2767db\" (System.String, reviewer GUID)\n  CodeReviewVotedByDisplayName = \"Normal Paulk\" (System.String)\n  CodeReviewVoteResult    = \"10\"           (System.String \u2014 NOTE: the integer is stringified)\nThe thread's own `publishedDate` gives the vote timestamp, and thread `id` is monotonically increasing in event order (sample: 141..148 ascending by time). Previous values are recoverable by reading the ordered sequence of VoteUpdate threads for that reviewer GUID.\n\nThe same mechanism yields the rest of the PR timeline in ONE ordered stream, which is directly useful for the revision model:\n  CodeReviewThreadType = \"RefUpdate\"  -> props CodeReviewRefName, CodeReviewRefNewHeadCommit, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount, CodeReviewRefUpdatedByTfId/DisplayName/(email). This is a per-push immutable record naming the new head SHA.\n  CodeReviewThreadType = \"ReviewersUpdate\" -> props CodeReviewReviewersUpdatedAddedTfId/AddedDisplayName/RemovedTfId/RemovedDisplayName/NumAdded/NumRemoved/UpdatedByTfId.\n  CodeReviewThreadType = \"MergeAttempt\" -> props CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit.\nUser threads instead carry `Microsoft.TeamFoundation.Discussion.SupportsMarkdown`.\n\nDOCUMENTATION CAVEAT \u2014 read this before building on it: these property KEYS appear only in the sample response JSON. They are NOT in any schema/definition table; `properties` is typed merely as `PropertiesCollection` (\"Optional properties associated with the thread as a collection of key-value pairs\"). The `CodeReviewThreadType` value set is nowhere enumerated, so the four values above are the four that happen to appear in the sample and there are certainly others (policy evaluation, autocomplete set/cancelled, status/title/description edits, isDraft transitions). Treat the shape as documented-by-example and the value set as open. JetBridge should parse defensively: match on `CodeReviewThreadType` with an explicit allowlist, ignore unknown types, and never assume a property is present. This is strictly BETTER than GitHub: GitHub gives you a timeline API with a fixed typed vocabulary but no equivalent single ordered stream that interleaves votes, pushes, reviewer changes and merge attempts with commit SHAs attached.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?$iteration={iteration}&$baseIteration={baseIteration}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Native vote-change event (the disposition trigger) \u2014 and immunity to agent self-trigger",
          "exists": "native",
          "detail": "Service hooks publisher `tfs` exposes `git.pullrequest.updated`, which fires on status changes, reviewer-list changes, REVIEWER VOTE CHANGES, and source-branch pushes. Crucially it supports a `notificationType` filter with four documented values: `PushNotification` (source branch updated), `ReviewersUpdateNotification` (reviewers changed), `StatusUpdateNotification` (status changed), and `ReviewerVoteNotification` (votes score changed). Additional filters: `repository` (guid), `branch`, `pullrequestCreatedBy`, `pullrequestReviewersContains`. Comments fire a DIFFERENT event, `ms.vss-code.git-pullrequest-comment-event` (filters: repository, branch).\n\nTHIS SOLVES THE GITHUB SELF-TRIGGER BUG STRUCTURALLY. On GitHub the agent's reply to a review comment is filed under a new review object with non-null submitted_at, so the platform re-triggers on its own writing \u2014 proven live. On Azure DevOps that cannot happen: the agent's textual replies are thread/comment writes, which fire the comment event and never the vote event. Subscribe the trigger exclusively to `git.pullrequest.updated` filtered to `notificationType = ReviewerVoteNotification` and the agent's own prose is structurally incapable of re-triggering the loop. No `sender != self` heuristic needed for the comment path.\n\nTwo residual self-trigger vectors remain and must still be filtered explicitly:\n  (a) The agent PUSHING a revision fires `PushNotification` (and creates a RefUpdate system thread). If any subscription is broader than the ReviewerVoteNotification filter, that is a self-trigger.\n  (b) If the agent's own identity ever casts a vote, that is a genuine ReviewerVoteNotification from itself. Filter on the actor GUID (`CodeReviewVotedByTfId` / the payload's reviewer id) against the agent's own identity id \u2014 a single equality check, far more robust than GitHub's situation.\nRECOMMENDATION: subscribe narrowly on notificationType, and additionally drop any event whose voting identity equals the agent identity.",
          "endpoints": [
            "Service hook subscription: publisherId `tfs`, eventType `git.pullrequest.updated`, filter `notificationType = ReviewerVoteNotification`",
            "Service hook subscription: publisherId `tfs`, eventType `ms.vss-code.git-pullrequest-comment-event`",
            "Service hook subscription: publisherId `tfs`, eventType `git.pullrequest.merged` (filter `mergeResult` in Succeeded|Unsuccessful|Conflicts|Failure|RejectedByPolicy)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/create-pr-status-server-with-azure-functions?view=azure-devops"
          ]
        },
        {
          "capability": "isFlagged, hasDeclined, isRequired, isReapprove, votedFor semantics",
          "exists": "native",
          "detail": "Verbatim from the 7.1 IdentityRefWithVote definition:\n  `isFlagged` (boolean) \u2014 \"Indicates if this reviewer is flagged for attention on this pull request.\" PATCHable.\n  `hasDeclined` (boolean) \u2014 \"Indicates if this reviewer has declined to review this pull request.\" PATCHable. This is a DECLINE-TO-REVIEW signal, orthogonal to the vote \u2014 it is not a rejection and should NOT be mapped to changes-requested. It means 'I am not going to look at this'. JetBridge should treat hasDeclined=true as 'this reviewer will never produce a disposition' and stop waiting on them.\n  `isRequired` (boolean) \u2014 \"Indicates if this is a required reviewer for this pull request. Branches can have policies that require particular reviewers are required for pull requests.\" READ-ONLY via this API: the Update-Pull-Request-Reviewer doc says only isFlagged and hasDeclined are patchable, and the plural PATCH says explicitly it \"does not support updating required reviewers (use policy)\". Requiredness is owned by branch policy, not by the PR.\n  `isReapprove` (boolean) \u2014 \"Indicates if this approve vote should still be handled even though vote didn't change.\" This is a write-side flag: it lets a caller re-assert an existing approval so the server processes it as a fresh approval event even though the integer is unchanged. Relevant if JetBridge ever needs to force policy re-evaluation without a value change.\n  `votedFor` (IdentityRefWithVote[]) \u2014 \"Groups or teams that this reviewer contributed to. Groups and teams can be reviewers on pull requests but can not vote directly. When a member of the group or team votes, that vote is rolled up into the group or team vote. VotedFor is a list of such votes.\" IMPORTANT for JetBridge: a reviewer entry may be a GROUP, and a group's vote is a rollup of member votes. The `reviewers[]` array therefore contains entries that are not people. Filter on `isContainer` (IdentityRef, \"can be inferred from the subject type of the descriptor (Descriptor.IsGroupType)\") before attributing a disposition to a human, and read `votedFor` to find who actually voted inside a group.\nThe patchable-fields split means: the vote is written with PUT (create-pull-request-reviewer), while isFlagged/hasDeclined are written with PATCH on the same URL. Two different verbs on one resource.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1 (patchable: isFlagged, hasDeclined)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/update-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/update-pull-request-reviewers?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Vote reset when a new iteration is pushed (branch policy)",
          "exists": "native",
          "detail": "Configured on the **Require a minimum number of reviewers** policy, under a \"When new changes are pushed\" group with FOUR options (they are alternatives, not a single boolean):\n  - \"Require at least one approval on every iteration\" \u2014 requires an approval vote on the last source-branch change; the user's own approval is not counted against any previous unapproved iteration pushed by that user, so another user must approve the last iteration. Available in Azure DevOps Server 2022.1 and higher.\n  - \"Require at least one approval on the last iteration\".\n  - \"Reset all approval votes (does not reset votes to reject or wait)\" \u2014 removes approvals, KEEPS -5 and -10.\n  - \"Reset all code reviewer votes\" \u2014 removes every vote including approve, reject, and wait.\nThe CLI surface exposes only a single boolean: `az repos policy approver-count create/update --reset-on-source-push {false,true}` (\"Reset votes when changes are pushed to the source\"), alongside `--allow-downvotes`, `--creator-vote-counts`, `--minimum-approver-count`, `--blocking`, `--enabled`. So the CLI is LOSSY relative to the web UI \u2014 it cannot express the approvals-only variant or the per-iteration variants. If JetBridge configures policy programmatically, use the Policy Configurations REST API with the raw `settings` JObject, not the CLI. (Azure DevOps CLI commands are not supported against Azure DevOps Server at all.)\nDOES THE RESET GENERATE AN EVENT? Not documented anywhere I could find. The push itself definitively fires `git.pullrequest.updated` with `notificationType = PushNotification`, and creates a `RefUpdate` system thread. Whether the policy-driven vote clear ALSO emits a separate `ReviewerVoteNotification` and/or a `VoteUpdate` system thread is UNSPECIFIED in the docs. This matters directly: if it does emit, every agent push that clears votes will fire the JetBridge trigger from the agent's own action. VERIFY EMPIRICALLY against a live org before shipping \u2014 push to a PR with reset-on-source-push enabled and an existing approval, then diff `GET .../threads` and capture the webhook deliveries. Until verified, defensively drop any ReviewerVoteNotification whose vote value is 0 and which arrives within a short window of a PushNotification for the same PR.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1 with type.id = <Minimum approval count> and settings {minimumApproverCount, creatorVoteCounts, allowDownvotes, resetOnSourcePush, scope:[{repositoryId, refName, matchKind}]}",
            "PUT https://dev.azure.com/{organization}/{project}/_apis/policy/configurations/{configurationId}?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/policy/configurations/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Programmatic vote reset without a policy (bulk PATCH)",
          "exists": "native",
          "detail": "There is a dedicated endpoint whose ENTIRE documented purpose is resetting votes: \"Reset the votes of multiple reviewers on a pull request. NOTE: This endpoint only supports updating votes, but does not support updating required reviewers (use policy) or display names.\" Request body is `IdentityRefWithVote[]` documented as \"IDs of the reviewers whose votes will be reset to zero\" \u2014 so it resets to 0 and does NOT let you set arbitrary vote values in bulk (arbitrary values need the per-reviewer PUT). Returns 200 with no body.\nThis gives JetBridge a policy-free option: instead of depending on `resetOnSourcePush` (which is repo-wide, affects human workflows, and has the unresolved does-it-emit-an-event question above), the agent can push a new revision and then explicitly PATCH-reset the reviewers it considers stale. That keeps revision/vote invalidation inside JetBridge's own state machine rather than delegating it to an org-level policy the team may not control. Note the agent needs the permission to write other people's reviewer entries to do this (see the identity finding) \u2014 verify empirically that a non-admin bot identity is allowed to reset another user's vote, since the docs do not state the permission requirement for this route.\nVERSION FLOOR: this operation exists at api-version 6.0, 6.1, 7.0, 7.1, 7.2 and at azure-devops-server-rest 6.0/7.0/7.1 \u2014 it is NOT present in the 4.1 or 5.x doc sets. On-prem below Azure DevOps Server 2020 does not have it.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/update-pull-request-reviewers?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "PR status scoped to a specific iteration",
          "exists": "native",
          "detail": "MATERIALLY BETTER THAN GITHUB, and this is the single strongest structural advantage for the revision model. GitHub commit statuses attach to a SHA and a GitHub PR has no revision noun (which is exactly why ghstack invents base/head/orig ref triples). Azure DevOps PR statuses attach to the PR *or* to a numbered ITERATION of it. `GitPullRequestStatus.iterationId` is `integer (int32)`, \"ID of the iteration to associate status with. Minimum value is 1\", and the create doc says explicitly \"Note that you can specify iterationId in the request body to post the status on the iteration.\" There is a first-class documented sample titled \"On iteration\".\nThe conceptual doc states the intent directly: \"When the source branch in a PR changes, a new 'iteration' is created to track the latest changes... Posting status to a specific iteration of a PR guarantees that status applies only to the code that was evaluated and none of the future updates.\" That is precisely the immutable, individually addressable revision the team wanted to borrow from Gerrit/jj \u2014 Azure DevOps already has it, server-side, with a stable small-integer identity. JetBridge should NOT build a parallel revision numbering scheme on Azure DevOps; it should adopt iterationId as the revision ordinal and only supply its own stable CHANGE identity across PRs.\nFull GitPullRequestStatus: `_links`, `context` (GitStatusContext {genre, name}), `createdBy` (IdentityRef), `creationDate`, `description`, `id` (int32), `iterationId`, `properties` (PropertiesCollection \u2014 arbitrary typed key/values, useful for stamping a JetBridge revision id or sealed-record digest onto the status), `state` (GitStatusState), `targetUrl`, `updatedDate`.\nGitStatusState enum: `notSet` (\"Status state not set. Default state.\"), `pending`, `succeeded`, `failed`, `error`, `notApplicable` (\"Status is not applicable to the target object.\").\nUPDATE SEMANTICS: posting again APPENDS, it does not replace \u2014 \"A service may update a PR status for a single PR by posting additional statuses, only the latest of which is shown for each unique `context`.\" So the status list is an append-only log keyed by genre/name, with last-write-wins display. That is a usable audit trail for the gate.\nLIMIT: \"If the PR being created contains more than 100,000 modified files... that PR won't support iterations... any attempt to create a status for a non-existent iteration will return an error.\" Handle the no-iterations degenerate case.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/statuses?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/statuses?api-version=7.1",
            "POST (same route) with body {\"iterationId\": 1, \"state\": \"succeeded\", \"description\": \"...\", \"context\": {\"name\": \"...\", \"genre\": \"...\"}, \"targetUrl\": \"...\"}"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-statuses/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/pull-request-status?view=azure-devops"
          ]
        },
        {
          "capability": "The Status branch policy (external status required for completion)",
          "exists": "native",
          "detail": "The gate exists and is fully configurable. UI path: Branch policies > **Status checks** > **+**. \"In **Status to check**, select the posted check from the list. If the service hasn't posted status yet, type the `genre/name` value directly.\" The policy \"requires that a status of `succeeded` with the `context` matching the selected name be present in order for this policy to pass.\"\nConfigurable knobs, all documented:\n  - **Policy requirement**: Required (blocks completion) or Optional (informational). Maps to `isBlocking` on the policy configuration.\n  - **Authorized identity**: \"used to enforce that status from only the specified identity will be counted towards the policy fulfillment.\" DIRECTLY USEFUL: pin the gate to the JetBridge service identity so no other actor can satisfy it.\n  - **Reset conditions**: \"used to determine when a posted status is no longer valid. If the status posted is specific to the latest code (i.e. a build), check **Reset status whenever there are new changes**.\" The status-checks reference states the default: \"By default, pushing a new commit resets required status checks to pending.\" Combine with iteration-scoped status and the gate is revision-correct by construction.\n  - **Policy applicability**: **Apply by default** (policy applies as soon as the PR is created and blocks until a `succeeded` arrives; a PR can be exempted by posting `notApplicable`) vs **Conditional** (\"the policy applies only after the first status is posted\"). The `notApplicable` escape hatch is the clean way for JetBridge to say 'this PR is not agent-managed' without editing policy.\n  - **Path filter** and **Default display name**.\nMerge behaviour table (Services): `succeeded` unblocks; `failed` and `error` block; `pending` blocks (awaiting result); `notApplicable` bypasses the requirement; `notSet` is treated as pending.\nPOLICY TYPE GUID IS NOT DOCUMENTED. The Configurations-Create page enumerates sample GUIDs for Minimum approval count (fa4e907d-c16b-4a4c-9dfa-4906e5d171dd), Build (0609b952-1397-4640-95ec-e00a01b2c241), Required reviewers (fd2167ab-b0be-447a-8ec8-39368250530e), Git case enforcement (7ed39669-655c-494e-b4a0-a08b4da0fcce), Max blob size (2e26e725-8201-4edd-8bf5-978563c34a80), Merge strategy (fa4e907d-c16b-4a4c-9dfa-4916e5d171ab), Work item linking (40e92b44-2fe1-4dd6-b3d8-74a9c21d0c6e) \u2014 but NOT the Status policy. The Policy Types - List page documents only the PolicyType shape, with no sample enumeration. DO NOT HARDCODE A GUID FROM MEMORY OR A BLOG. Discover it at runtime: `GET _apis/policy/types?api-version=7.1` and match on `displayName`. Likewise the `settings` object for the status policy is untyped (`settings` is a bare `JObject`, \"The policy configuration settings\") and its field names (status name, status genre, authorized identity, reset-on-source-update, applicability) are NOT specified anywhere on Learn. If JetBridge must create this policy programmatically, the safe route is: configure one instance by hand in the UI, `GET _apis/policy/configurations`, and template from the returned settings JSON. Anything else is guessing.\nEDITIONS: available on Azure DevOps Services, Azure DevOps Server, and Azure DevOps Server 2022 (pr-status-policy and pull-request-status both carry the \"Azure DevOps Services | Azure DevOps Server | Azure DevOps Server 2022\" banner and the branch-policies 'Require status checks' section is scoped to monikers azure-devops, azure-devops-2022, azure-devops-server). There is NO paid-tier gate on branch policies \u2014 the requirement is Basic access or higher (Stakeholder access has no code access in private projects) plus **Edit policies** permission on the repo/branch to configure them. Note the built-in-status-check reference page (available-pr-status-checks) is Azure DevOps Services ONLY; the first-party checks it lists (AdvancedSecurity/AllHighAndCritical, AdvancedSecurity/NewHighAndCritical, {pipeline-name}/codecoverage) should not be assumed present on-prem.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/types?api-version=7.1 (discover the Status policy type id \u2014 do not hardcode)",
            "POST https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1 (template the settings JSON from a hand-configured instance)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/pr-status-policy?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/pull-request-status?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/available-pr-status-checks?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/policy/configurations/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/policy/types/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Completing a PR: status, completionOptions, merge strategies",
          "exists": "native",
          "detail": "PATCH the PR with `status: \"completed\"` plus `completionOptions`. The update doc lists exactly what is patchable \u2014 \"Status, Title, Description (up to 4000 characters), CompletionOptions, MergeOptions, AutoCompleteSetBy.Id, TargetRefName (when the PR retargeting feature is enabled)\" \u2014 and warns \"Attempting to update other properties outside of this list will either cause the server to throw an `InvalidArgumentValueException`, or to silently ignore the update.\" The silent-ignore branch is a real trap: a malformed completion PATCH may return 200 having done nothing. Always re-read the PR and assert `status == completed` rather than trusting the response.\nPullRequestStatus enum: `notSet`, `active`, `abandoned`, `completed`, `all` (search-only).\nGitPullRequestCompletionOptions (all fields): `autoCompleteIgnoreConfigIds` (int32[] \u2014 \"List of any policy configuration Id's which auto-complete should not wait for. Only applies to optional policies (isBlocking == false). Auto-complete always waits for required policies (isBlocking == true).\"), `bypassPolicy` (bool), `bypassReason` (string), `deleteSourceBranch` (bool), `mergeCommitMessage` (string), `mergeStrategy` (GitPullRequestMergeStrategy), `squashMerge` (bool, DEPRECATED), `transitionWorkItems` (bool), `triggeredByAutoComplete` (bool, \"Used internally\").\nGitPullRequestMergeStrategy enum, verbatim: `noFastForward` (\"A two-parent, no-fast-forward merge. The source branch is unchanged. This is the default behavior.\"), `squash` (\"Put all changes from the pull request into a single-parent commit.\"), `rebase` (\"Rebase the source branch on top of the target branch HEAD commit, and fast-forward the target branch. The source branch is updated during the rebase operation.\"), `rebaseMerge` (\"Rebase the source branch on top of the target branch HEAD commit, and create a two-parent, no-fast-forward merge. The source branch is updated during the rebase operation.\" \u2014 this is the UI's 'Semi-linear merge'; GitHub has no equivalent). The docs are emphatic that `squashMerge` is deprecated and \"It is recommended that you explicitly set MergeStrategy in all cases\" \u2014 JetBridge should always send mergeStrategy explicitly.\nSERVER-SIDE REBASE IS THE 'agent rebases if needed and merges' STEP. Three documented situations where rebase-on-completion is impossible: (1) \"If a policy on the target branch prohibits using rebase strategies, you need **Override branch policies** permission to rebase.\"; (2) \"If the PR source branch has policies, you can't rebase it. Rebasing would modify the source branch without going through the policy approval process.\" \u2014 this is a real constraint if JetBridge protects agent branches; (3) if the Merge Conflict Extension was used to resolve conflicts. Fallback in all three: rebase locally and push, or squash-merge.\nOther completion states: `mergeStatus` (PullRequestAsyncStatus: notSet, queued, conflicts, succeeded, rejectedByPolicy, failure) and `mergeFailureType` (none, unknown, caseSensitive, objectTooLarge). `rejectedByPolicy` is the value to watch when an autocomplete attempt is blocked by the gate.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1",
            "PATCH (same) body {\"status\":\"completed\",\"lastMergeSourceCommit\":{...},\"completionOptions\":{\"mergeStrategy\":\"rebaseMerge\",\"deleteSourceBranch\":true,\"transitionWorkItems\":false}}"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/complete-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Auto-complete (autoCompleteSetBy) \u2014 the PR merges itself once policies pass",
          "exists": "native",
          "detail": "`autoCompleteSetBy` is an `IdentityRef` on GitPullRequest, documented as \"If set, auto-complete is enabled for this pull request and this is the identity that enabled it.\" It is explicitly listed among the patchable properties as `AutoCompleteSetBy.Id`. So the REST contract is: PATCH the PR with `{\"autoCompleteSetBy\": {\"id\": \"<identity guid>\"}, \"completionOptions\": {...}}`; clear it by patching the id to the empty GUID (the UI's \"Cancel auto-complete\"). The CLI equivalent is `az repos pr update --id <n> --auto-complete true` (and it can be set at creation with `az repos pr create --auto-complete true`).\nSEMANTICS: \"complete and merge the PR changes as soon as conditions satisfy all branch policies\". \"By default, a PR that's set to autocomplete waits only on required policies\" \u2014 you can opt into waiting on optional ones too (the REST lever for the inverse is `autoCompleteIgnoreConfigIds`, which skips specific OPTIONAL policies; \"Auto-complete always waits for required policies\"). Merge conflicts must be resolved before autocomplete can even be set: \"You must resolve any merge conflicts between the PR branch and the target branch before you can merge a PR or set the PR to autocomplete.\" On failure the setter is emailed. The PR shows an Auto-complete badge.\nAvailability: \"available in Azure Repos and TFS 2017 and higher WHEN YOU HAVE BRANCH POLICIES. If you don't see **Set auto-complete**, you don't have any branch policies.\" So autocomplete is meaningless without at least one policy \u2014 which is fine, since JetBridge's gate IS a policy.\nCAN A SERVICE IDENTITY SET IT? Almost certainly yes, but this is INFERRED, not stated. The reasoning: `AutoCompleteSetBy.Id` is a plain patchable field on a REST route scoped to `vso.code_write`, service principals and managed identities are first-class Azure DevOps identities that \"Access Azure DevOps resources with proper permissions\", and nothing in the docs restricts autoCompleteSetBy to interactive users. No Learn page states that a service identity can or cannot be the auto-complete setter. VERIFY EMPIRICALLY. Also verify whether a service identity may set autoCompleteSetBy to an identity OTHER than itself \u2014 untested and undocumented; assume self-only.\nDESIGN NOTE: autocomplete is the cleanest realisation of 'approved -> the agent rebases if needed and merges'. Rather than polling for approval, the agent sets autoCompleteSetBy + completionOptions{mergeStrategy: rebase|rebaseMerge} once, and Azure DevOps performs the merge itself the instant the reviewer's approving vote satisfies the minimum-reviewer policy and the JetBridge status gate is `succeeded`. That removes an entire class of race between 'approval observed' and 'merge attempted'. GitHub's auto-merge is comparable but does not offer semi-linear merge or the ignore-specific-optional-policies control.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1 with {\"autoCompleteSetBy\":{\"id\":\"<identityGuid>\"}}"
          ],
          "vs_github": "better",
          "confidence": "inferred",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/complete-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops"
          ]
        },
        {
          "capability": "Bot identity and per-operation permissions",
          "exists": "native",
          "detail": "PERMISSION PER OPERATION (repository-level Git permissions):\n  - Vote / comment / create a PR: covered by **Read** plus **Contribute to pull requests**. The default-permissions table describes the row as \"**Read** (clone, fetch, and explore the contents of a repository); also, can create, comment on, vote, and **Contribute to pull requests**\" and marks it granted to Readers, Contributors, Build Admins and Project Admins. Note the table CONFLATES two separately-named permissions in one row, so treat 'Readers can vote by default' as documented-but-loosely-worded and confirm on the target org.\n  - Post PR status: stated explicitly \u2014 \"To post a status check via the REST API, the calling identity needs the **Contribute to pull requests** permission on the repository.\"\n  - Push to the source branch: **Contribute** (plus **Create branch** to open new ones). Granted to Contributors, Build Admins, Project Admins \u2014 NOT to Readers.\n  - Complete a PR: no extra permission beyond being able to contribute, PROVIDED all policies pass. To force-complete against failing policies you need **Bypass policies when completing pull requests** (this and **Bypass policies when pushing** replaced the old **Exempt from policy enforcement**; both are \"not set for any security group\" by default). JetBridge should NEVER hold bypass permissions \u2014 the gate is the whole point.\n  - Configure the Status branch policy: **Edit policies** on the repo or branch, or Project Administrators membership. Since Azure DevOps sprint 224 / Server 2022.1, **Edit policies is no longer granted automatically to branch creators** \u2014 it must be granted explicitly. If JetBridge self-provisions its gate policy, this permission must be arranged deliberately.\nAUTH TOKEN SCOPES: `vso.code_write` (\"read, update, and delete source code... create and manage pull requests and code reviews\") for votes and PR completion; `vso.code_status` (\"read and write commit and pull-request status\") for the gate; `vso.threads_full` (\"read and write to pull request comment threads\") for comments; `vso.code` for reads. Scope inheritance: `vso.code_manage` \u2283 `vso.code_write` \u2283 `vso.code`. `vso.code_status` is INDEPENDENT \u2014 it is not inherited from code_write, so a token that can vote cannot necessarily post status. Request `vso.code_write + vso.code_status + vso.threads_full` for the full loop.\nIDENTITY CHOICE (Azure DevOps Services): use a Microsoft Entra service principal or managed identity, not a PAT. Documented capabilities: \u2705 generate Entra tokens for API access, \u2705 access resources with proper permissions, \u2705 join security groups and teams. Documented limitations that bite here: \u274c **cannot create PATs or SSH keys**, \u274c cannot sign in interactively or use the web UI, \u274c cannot create/own organizations, \u274c **do not support Azure DevOps OAuth flows** (use Entra OAuth; Azure DevOps OAuth is deprecated and stops accepting new registrations). Each identity needs a license in EVERY org it joins (no multi-org discount, group licensing rules do not apply \u2014 assign directly), and must be **explicitly added** by a PCA/PA (adding it to an Entra group does not grant org access). Use the service principal's object ID from the **Enterprise applications** pane, not the app registration's object ID. Token audience: `https://app.vssps.visualstudio.com/.default` (or resource GUID `499b84ac-1321-427f-aa17-267ca6975798`). Common error to expect: \"The Git repository with name or identifier doesn't exist or you don't have permissions\" -> the SP has a Stakeholder licence; it needs at least Basic.\nON-PREM: service principals and managed identities are **Azure DevOps Services only** (the page carries only the `azure-devops` moniker). On Azure DevOps Server the bot must be a real domain/AD account using Windows auth, a PAT, or a client library \u2014 plan for two identity strategies.",
          "endpoints": [
            "POST https://login.microsoftonline.com/{tenant-id}/oauth2/v2.0/token (client_credentials, scope=https://app.vssps.visualstudio.com/.default)",
            "ServicePrincipalEntitlements REST API (add the identity to the org programmatically)",
            "Graph Service Principals REST API (programmatic permission management)"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/set-git-repository-permissions?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/available-pr-status-checks?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops"
          ]
        },
        {
          "capability": "Policies that silently disqualify the agent's own vote (self-approval traps)",
          "exists": "native",
          "detail": "Three separate settings can cause a vote to be CAST SUCCESSFULLY (HTTP 200, vote=10 readable via the API) yet NOT COUNT toward completion. Any JetBridge logic that reads `vote == 10` and concludes 'this PR is approved' will be wrong under these settings. On the **Require a minimum number of reviewers** policy:\n  - \"**Allow requestors to approve their own changes** to allow a PR's creator to vote on its approval. Otherwise, the creator can still vote **Approve** on the PR, but their vote doesn't count toward the minimum number of reviewers.\" \u2014 the doc states the discrepancy explicitly: the vote is recorded but discounted.\n  - \"**Prohibit the most recent pusher from approving their own changes** to enforce segregation of duties. By default, anyone with push permission on the source branch can both add commits and vote on PR approval. Selecting this option means the most recent pusher's vote doesn't count, even if they can ordinarily approve their own changes.\" \u2014 THIS IS THE ONE THAT MATTERS MOST FOR AN AGENT. The agent is by construction the most recent pusher on every revision.\n  - \"**Allow completion even if some reviewers vote to wait or reject**\" (the `--allow-downvotes` CLI flag) \u2014 when ON, a -5/-10 does NOT block completion as long as the minimum approvals are met. So a `changes-requested` disposition may not actually gate the merge.\nThe **Automatically included reviewers** policy has its OWN independent \"Allow requestors to approve their own changes\" setting, and the docs devote an FAQ to the confusion: \"In each policy, the setting applies only to that policy. The setting doesn't affect the other policy\" \u2014 a vote can satisfy required-reviewers while failing minimum-reviewers.\nCONSEQUENCE: never infer merge-readiness from the vote integers. Read the authoritative policy evaluation instead (`az repos pr policy list --id <n>` shows Evaluation ID / Policy / Blocking / Status / Expired per policy; the REST equivalent is the policy evaluations API), or simply set autoCompleteSetBy and let the server decide. \"The server reevaluates branch policies when pull request owners push changes and when reviewers vote.\"\nOne more state-clearing behaviour worth knowing: **Mark as draft** \"Return the PR to draft status and remove all votes.\" Draft transitions are a silent vote-wipe.",
          "endpoints": [
            "az repos pr policy list --id {pullRequestId}  (per-policy evaluation status; Azure DevOps Services only \u2014 CLI is unsupported on Server)"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/complete-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Comment thread status as a secondary disposition signal",
          "exists": "native",
          "detail": "Threads carry their own lifecycle independent of votes, which partially substitutes for the missing review object when routing comment-only vs changes-requested. CommentThreadStatus enum verbatim: `unknown`, `active` (\"The thread status is active\"), `fixed` (\"resolved as fixed\"), `wontFix`, `closed`, `byDesign`, `pending`. The UI labels these Active / Pending / Resolved / Won't fix / Closed \u2014 note the UI name **Resolved** maps to the API value `fixed`, and the UI exposes no separate `byDesign` label in the status dropdown described; the REST vocabulary and the UI vocabulary DISAGREE and JetBridge must translate rather than string-match.\n\"New comments start with an **Active** status. PR authors update the status during the review process to indicate how they addressed reviewer feedback\". Reviewers can \"**Reply & resolve**\" which both replies and sets Resolved.\nCommentType enum: `unknown`, `text` (\"regular user comment\"), `codeChange` (\"comes as a result of a code change\"), `system` (\"represents a system message\"). When JetBridge enumerates human feedback it must filter to `text` \u2014 `system` threads are the event log, not review content, and mixing them will inject \"X voted 10\" into the agent's input.\nThere is also a **Comment requirements** branch policy (visible in the `az repos pr policy list` sample output) that can require all comments be resolved before completion \u2014 worth checking on the target repo, since it makes thread status part of the gate.\nRELEVANCE TO THE DISPOSITION: because the vote alone is ambiguous (vote `5` = approved-with-suggestions carries work; vote `0` with active threads = comment-only), the count of the reviewer's `active` `text` threads at the moment of the vote is the cleanest disambiguator available. This is a JetBridge-side derivation, not a forge concept.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads/{threadId}?api-version=7.1 (set thread status)"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/review-pull-requests?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/complete-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Azure DevOps Services vs Azure DevOps Server (on-prem) differences relevant to this loop",
          "exists": "partial",
          "detail": "WORKS ON BOTH (Services, Server 2022, Server): reviewer votes and the five vote values (api-versions back to 4.1 and server-rest 5.0); PR threads incl. system threads; PR statuses with iterationId; the Status branch policy; minimum-reviewer policy with the vote-reset options; completion + completionOptions + all four merge strategies; auto-complete (TFS 2017+); service hooks for git.pullrequest.updated / .created / .merged and the comment event.\nSERVICES ONLY:\n  - Service principals and managed identities as Azure DevOps identities (the doc carries only the `azure-devops` moniker). On-prem needs an AD account / PAT / client library instead.\n  - Azure DevOps CLI (`az repos ...`) \u2014 the branch-policies, review, and complete docs all state \"Azure DevOps CLI commands aren't supported for Azure DevOps Server\" in every Server moniker block. Any automation built on `az repos pr set-vote` / `az repos policy approver-count` is Services-only. Use REST for portability.\n  - OAuth 2.0 authorization-code flows: \"OAuth 2.0 is available only for Azure DevOps Services, not Azure DevOps Server. For on-premises scenarios, use Client libraries, Windows Authentication, or personal access tokens.\"\n  - The built-in status-check catalogue (available-pr-status-checks: AdvancedSecurity/*, {pipeline}/codecoverage) is `azure-devops` moniker only.\nVERSION FLOORS ON-PREM:\n  - Bulk vote reset (PATCH .../reviewers) exists at server-rest 6.0/7.0/7.1 only \u2014 absent from the 4.1/5.x doc sets, so absent before Azure DevOps Server 2020.\n  - \"Require at least one approval on every iteration\" is \"available in Azure DevOps Server 2022.1 and higher\".\n  - The Edit-policies-not-auto-granted-to-branch-creators change landed in sprint 224 / Server 2022.1.\nRECOMMENDATION: build the Azure DevOps adapter against REST api-version 7.1 (present in both the Services and Server doc sets), avoid the CLI entirely, and gate the bulk-vote-reset path behind a capability probe rather than assuming it exists.",
          "endpoints": [
            "All routes above are documented at both `?view=azure-devops-rest-7.1` and `?view=azure-devops-server-rest-7.1` unless noted"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/update-pull-request-reviewers?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/available-pr-status-checks?view=azure-devops"
          ]
        }
      ],
      "absent": [
        "An atomic submitted-review object. There is nothing in Azure DevOps that bundles 'my N inline comments AND my verdict' into one artifact. Votes live on the reviewer sub-resource; comments live in threads; nothing links them. The team's trigger unit ('one completed review') has no native counterpart and must be redefined.",
        "Batched / draft / pending review composition in the web UI. Every comment posts immediately on its own button press; the vote is an independent dropdown. There is no 'Submit review' action to observe, so even the UI cannot produce the bundle the API lacks.",
        "A distinct 'request changes' event carrying comments. The closest is vote -5 ('waiting for author'), which is a bare integer with no attached prose.",
        "Any timestamp or previous-value field on IdentityRefWithVote. The reviewer object is current-state only. Vote timing is recoverable ONLY by reading VoteUpdate system comment threads.",
        "A first-class vote-history / audit endpoint. No REST route returns 'the votes cast on this PR over time'. The system-thread stream is the substitute and it is documented only by example.",
        "A formal schema for PR thread `properties`. `CodeReviewThreadType`, `CodeReviewVoteResult`, `CodeReviewVotedByTfId`, `CodeReviewRefNewHeadCommit` and friends appear only in a sample response; no definition table lists them and no page enumerates the legal `CodeReviewThreadType` values.",
        "Documented policy type GUIDs for the Status policy, and any documented schema for its `settings` object. The Configurations-Create page samples seven other policy types but not this one; `settings` is typed as a bare JObject. The GUID and field names must be discovered at runtime, never hardcoded from memory.",
        "Documented confirmation that a policy-driven vote reset emits an event. The push emits PushNotification; whether the vote clear additionally emits ReviewerVoteNotification and/or a VoteUpdate system thread is unspecified. Must be tested live.",
        "Documented confirmation that a service identity may set autoCompleteSetBy, or that it may set it to an identity other than itself. Both are plausible and untested.",
        "Documented confirmation of who may reset ANOTHER user's vote via the bulk PATCH route. The endpoint exists and its purpose is stated; the permission requirement is not.",
        "Service principals and managed identities on Azure DevOps Server. They are Azure DevOps Services only. Service principals also cannot create PATs or SSH keys, cannot sign in to the web UI, and do not support Azure DevOps OAuth flows.",
        "Azure DevOps CLI on Azure DevOps Server. Every `az repos` recipe in the branch-policy, review and completion docs is explicitly unsupported on-prem.",
        "The bulk vote-reset endpoint on Azure DevOps Server earlier than 2020 (absent from the 4.1 and 5.x REST doc sets)."
      ],
      "notes": "HEADLINE FOR THE DESIGN DECISION \u2014 three things, in order of importance.\n\n1. THE TRIGGER UNIT DOES NOT EXIST, BUT THE SELF-TRIGGER BUG DOES NOT EITHER. Azure DevOps has no submitted review, so 'one completed review' must be redefined as 'one vote change'. In exchange, the GitHub bug the team proved live becomes structurally impossible: the agent's replies are thread writes that fire `ms.vss-code.git-pullrequest-comment-event`, while the trigger listens only to `git.pullrequest.updated` filtered to `notificationType = ReviewerVoteNotification`. Two different event ids, two different resources, two different OAuth scopes. Subscribe narrowly and the agent cannot re-trigger itself by writing prose. The only remaining self-trigger vectors are the agent's own push (PushNotification \u2014 filtered out by the notificationType filter) and the agent casting its own vote (filter on actor GUID). This is the strongest argument in favour of the Azure DevOps mapping.\n\n2. TWO OF THE THREE BORROWED PRIMITIVES ARE ALREADY NATIVE \u2014 DO NOT BUILD THEM.\n   - The ORDERED SERIES OF IMMUTABLE REVISIONS is `iterationId`. It exists server-side with a stable small-integer identity, statuses can be pinned to it (`GitPullRequestStatus.iterationId`, minimum value 1), and threads can be read against it (`$iteration` / `$baseIteration`). The ghstack base/head/orig ref-triple hack is a workaround for a GitHub deficiency that Azure DevOps does not have. Adopt iterationId as the revision ordinal.\n   - The ORDERED EVENT LOG with commit SHAs attached is the system-thread stream. `RefUpdate` threads name `CodeReviewRefNewHeadCommit` per push; `VoteUpdate` threads name the voter GUID, the vote integer and the timestamp; `MergeAttempt` threads name source/target/merge commits. All in one `GET .../threads` call, ordered by thread id.\n   What Azure DevOps does NOT give you is the STABLE CHANGE IDENTITY across PRs \u2014 that remains JetBridge's to supply (jj-style change-id commit header, or a JetBridge id stamped into `GitPullRequestStatus.properties`, which is a typed arbitrary property bag and is the natural carrier).\n\n3. THE FIVE-VALUED VOTE DOMAIN AND THE POLICY DISCOUNT ARE THE TWO SHARP EDGES.\n   Azure DevOps has five dispositions, not three, and vote `5` (approved with suggestions) is simultaneously merge-unblocking AND work-carrying \u2014 a combination GitHub cannot express. Separately, three policy settings can make a recorded `vote == 10` NOT count: 'Allow requestors to approve their own changes', 'Prohibit the most recent pusher from approving their own changes' (which by construction always describes the agent), and the interaction between the minimum-reviewer and automatically-included-reviewer policies which each carry an independent copy of the setting. NEVER infer merge-readiness from vote integers. Either read the policy evaluations, or \u2014 better \u2014 set `autoCompleteSetBy` plus `completionOptions{mergeStrategy}` once and let the server decide when to merge. That collapses the 'approved -> rebase if needed and merge' branch into a single idempotent PATCH and eliminates the approval-observed/merge-attempted race entirely.\n\nWHAT TO VERIFY EMPIRICALLY BEFORE COMMITTING TO A DESIGN (docs are silent on all four):\n  (a) Does a policy-driven vote reset on push emit a ReviewerVoteNotification and/or create a VoteUpdate system thread? If yes, every agent push self-triggers and the whole trigger design needs a guard. Highest-priority test.\n  (b) Can the bot's service identity set `autoCompleteSetBy` (to itself)?\n  (c) Can a non-admin bot identity reset ANOTHER user's vote via `PATCH .../reviewers`?\n  (d) The full `CodeReviewThreadType` value set and the exact `settings` JSON of a hand-configured Status policy (configure one in the UI, then GET it and template from the result \u2014 do not guess field names, and do not hardcode the policy type GUID; discover it via `GET _apis/policy/types`).\n\nREST/UI DISAGREEMENTS FOUND: CommentThreadStatus `fixed` is displayed as 'Resolved'; `GitPullRequestMergeStrategy.rebaseMerge` is displayed as 'Semi-linear merge'; the repository-permissions table conflates 'Read' and 'Contribute to pull requests' into one row; `CodeReviewVoteResult` is a stringified integer (\"10\") despite `vote` being int16 on the wire; the `az repos policy approver-count --reset-on-source-push` boolean cannot express the four-way 'When new changes are pushed' choice available in the UI.\n\nDOCS KNOWN TO BE THIN: PR thread `properties` (sample-only, no schema, no enumerated values); the Status policy type GUID and settings shape (absent entirely); whether vote writes are self-only (unstated); service-hook payload field-level documentation for `git.pullrequest.updated`.\n\nTARGET API VERSION: 7.1 throughout. Every route cited is documented at both `?view=azure-devops-rest-7.1` and `?view=azure-devops-server-rest-7.1`, which is the portability floor for supporting Services and on-prem from one adapter. Avoid the Azure DevOps CLI entirely \u2014 it is unsupported on Server."
    },
    "checks": [
      {
        "area": "votes-policies",
        "claims_checked": [
          {
            "claim": "IdentityRefWithVote.vote is `integer (int16)` documented as \"Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected\"",
            "status": "CONFIRMED",
            "evidence": "Verbatim in the Definitions table on create-pull-request-reviewer?view=azure-devops-rest-7.1, and repeated identically on update-pull-request-reviewer, update-pull-request-reviewers, and pull-requests/update. Type is literally `integer (int16)`. Five-valued domain is exactly as claimed."
          },
          {
            "claim": "Writing a vote is a PUT to .../pullRequests/{id}/reviewers/{reviewerId}?api-version=7.1 with an IdentityRefWithVote body, and the same PUT both adds a reviewer and casts a vote (title: \"Add a reviewer to a pull request or cast a vote\")",
            "status": "CONFIRMED",
            "evidence": "Page H1 is \"Pull Request Reviewers - Create Pull Request Reviewer\"; the summary line is verbatim \"Add a reviewer to a pull request or cast a vote.\" Route and api-version match exactly. Two examples titled \"Add a reviewer\" (body {\"vote\":0,\"id\":...}) and \"Set vote\" (body {\"vote\":10,\"id\":...}) confirm the minimal body shape the claim gives."
          },
          {
            "claim": "`az repos pr set-vote --vote {approve, approve-with-suggestions, reject, reset, wait-for-author}` is the CLI surface",
            "status": "CONFIRMED",
            "evidence": "learn.microsoft.com/en-us/cli/azure/repos/pr?view=azure-cli-latest, section `az repos pr set-vote`: \"--vote {approve, approve-with-suggestions, reject, reset, wait-for-author}\", Accepted values row identical. Command Status is GA (azure-devops extension), not preview."
          },
          {
            "claim": "Vote history is retrievable via GET .../pullRequests/{id}/threads, where a vote change appears as a system thread with CodeReviewThreadType=VoteUpdate and properties CodeReviewVotedByTfId / CodeReviewVotedByDisplayName / CodeReviewVoteResult (stringified integer)",
            "status": "CONFIRMED",
            "evidence": "pull-request-threads/list?view=azure-devops-rest-7.1 sample response, thread id 143: content \"Normal Paulk voted 10\", commentType \"system\", properties CodeReviewThreadType={$type:System.String,$value:VoteUpdate}, CodeReviewVotedByTfId=d6245f20-2af8-44f4-9451-8107cb2767db, CodeReviewVoteResult={$type:System.String,$value:\"10\"}. The int-as-String encoding is exactly as claimed."
          },
          {
            "claim": "The same thread stream yields RefUpdate (with CodeReviewRefNewHeadCommit etc.), ReviewersUpdate, and MergeAttempt system threads",
            "status": "CONFIRMED",
            "evidence": "Sample response threads 145 (RefUpdate: CodeReviewRefName, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount, CodeReviewRefNewHeadCommit, CodeReviewRefUpdatedBy, CodeReviewRefUpdatedByDisplayName, CodeReviewRefUpdatedByTfId), 142/144 (ReviewersUpdate), 141/146 (MergeAttempt: CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit). User threads 147/148 carry Microsoft.TeamFoundation.Discussion.SupportsMarkdown as claimed."
          },
          {
            "claim": "These property keys appear only in the sample JSON; `properties` is typed merely as PropertiesCollection (\"Optional properties associated with the thread as a collection of key-value pairs\") and CodeReviewThreadType's value set is nowhere enumerated",
            "status": "CONFIRMED",
            "evidence": "The GitPullRequestCommentThread definition table types `properties` as PropertiesCollection with exactly that description. No definition table on the page names any CodeReview* key, and there is no enumeration for CodeReviewThreadType. The claim's own caveat is accurate and is the correct reading."
          },
          {
            "claim": "Thread `id` is monotonically increasing in event order (sample: 141..148 ascending by time)",
            "status": "OVERSTATED",
            "evidence": "The sample is consistent with this (141-148 ascend with publishedDate), but no Learn page states ordering, monotonicity, or that the response is time-sorted. This is a single-sample observation presented as a usable property. Do not order the event log by thread id; sort on publishedDate, which is a documented field."
          },
          {
            "claim": "System-thread author is `Project Collection Service Accounts` and reviewer identity can be matched on the actor GUID",
            "status": "OVERSTATED",
            "evidence": "Actual sample displayName is \"[DefaultCollection]\\\\Project Collection Service Accounts\" (collection-prefixed), and the identity carries isContainer:true. More seriously, the claim missed a live parsing hazard in its own source: GUID formatting is INCONSISTENT across thread types. VoteUpdate's CodeReviewVotedByTfId is dashed (\"d6245f20-2af8-44f4-9451-8107cb2767db\") while ReviewersUpdate's CodeReviewReviewersUpdatedAddedTfId is undashed (\"2428198325304a9caeb788d60d57acfd\"). Key casing is also irregular (CodeReviewReviewersUpdatedByDisplayname, lowercase n). Any equality check on actor GUID must normalize both dash form and case."
          },
          {
            "claim": "CommentThreadStatus enum is unknown/active/fixed/wontFix/closed/byDesign/pending and CommentType is unknown/text/codeChange/system",
            "status": "CONFIRMED",
            "evidence": "Both enumerations appear verbatim with those exact values and descriptions in the Definitions section of pull-request-threads/list?view=azure-devops-rest-7.1 (\"The thread status is resolved as fixed\", \"This is a regular user comment\", \"The comment represents a system message\", etc.)."
          },
          {
            "claim": "The UI labels these Active/Pending/Resolved/Won't fix/Closed, so UI name \"Resolved\" maps to API value `fixed` and the UI exposes no separate byDesign label \u2014 REST and UI vocabularies disagree",
            "status": "UNVERIFIABLE",
            "evidence": "I did not locate this mapping on review-pull-requests or any other Learn page during this pass. The REST enum is confirmed; the asserted UI label set and the specific Resolved->fixed / missing-byDesign mapping are not sourced. Treat as an empirical claim to check in a live org, not a documented one."
          },
          {
            "claim": "Service hook publisher `tfs`, event `git.pullrequest.updated`, supports a `notificationType` filter with values PushNotification, ReviewersUpdateNotification, StatusUpdateNotification, ReviewerVoteNotification",
            "status": "CONFIRMED",
            "evidence": "service-hooks/events?view=azure-devops: \"Publisher ID: tfs\", \"Event ID: git.pullrequest.updated\", event description \"A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch.\" Settings list all four notificationType values with exactly the claimed meanings, plus repository (guid), pullrequestCreatedBy, pullrequestReviewersContains, branch."
          },
          {
            "claim": "Comments fire a different event, ms.vss-code.git-pullrequest-comment-event, with filters repository and branch",
            "status": "CONFIRMED",
            "evidence": "Same page: \"Pull request commented on\", Publisher ID tfs, Event ID ms.vss-code.git-pullrequest-comment-event, Settings subsection lists `repository` (data type guid) and `branch`. Comments are notably absent from the git.pullrequest.updated trigger description."
          },
          {
            "claim": "git.pullrequest.merged supports a mergeResult filter with values Succeeded, Unsuccessful, Conflicts, Failure, RejectedByPolicy",
            "status": "CONFIRMED",
            "evidence": "service-hooks/events: \"Pull request merge attempted (git.pullrequest.merged)\", Settings mergeResult with exactly those five values."
          },
          {
            "claim": "Subscribing to notificationType=ReviewerVoteNotification makes the agent's own prose \"structurally incapable\" of re-triggering the loop \u2014 stated at confidence \"documented\"",
            "status": "OVERSTATED",
            "evidence": "The event facts are documented; the immunity conclusion is inference. Comments are absent from the git.pullrequest.updated description and have their own event, which supports the inference strongly, but no Learn page asserts that a comment write can never raise a vote notification. The confidence label should be \"inferred\", not \"documented\" \u2014 especially since the same research package (vote-reset claim) concedes it is undocumented whether a policy-driven vote clear emits a ReviewerVoteNotification, which is exactly the hole in the immunity argument."
          },
          {
            "claim": "isFlagged, hasDeclined, isRequired, isReapprove and votedFor carry the quoted descriptions; only isFlagged and hasDeclined are patchable; the plural PATCH explicitly does not support updating required reviewers",
            "status": "CONFIRMED",
            "evidence": "update-pull-request-reviewer?view=azure-devops-rest-7.1 summary is verbatim \"Edit a reviewer entry. These fields are patchable: isFlagged, hasDeclined\". update-pull-request-reviewers summary is verbatim \"Reset the votes of multiple reviewers on a pull request. NOTE: This endpoint only supports updating votes, but does not support updating required reviewers (use policy) or display names.\" All five field descriptions match the Definitions table word for word, including the votedFor group-rollup paragraph."
          },
          {
            "claim": "Filter on `isContainer` before attributing a disposition to a human",
            "status": "OVERSTATED",
            "evidence": "isContainer exists on IdentityRef but its documented description is \"Deprecated - Can be inferred from the subject type of the descriptor (Descriptor.IsGroupType)\". The claim recommends building routing logic on a field Microsoft marks deprecated without saying so. The doc's own pointer is to inspect `descriptor` subject type instead."
          },
          {
            "claim": "PATCH .../pullRequests/{id}/reviewers?api-version=7.1 resets votes to zero in bulk; body is IdentityRefWithVote[] \"IDs of the reviewers whose votes will be reset to zero\"; 200 with no body",
            "status": "CONFIRMED",
            "evidence": "update-pull-request-reviewers?view=azure-devops-rest-7.1: route, method and api-version match; Request Body row is verbatim \"body | IdentityRefWithVote[] | IDs of the reviewers whose votes will be reset to zero\"; Responses table shows \"200 OK\" with an empty Type cell."
          },
          {
            "claim": "The bulk-reset operation exists at 6.0/6.1/7.0/7.1/7.2 and server-rest 6.0/7.0/7.1, not in 4.1 or 5.x, so on-prem below Azure DevOps Server 2020 lacks it",
            "status": "CONFIRMED",
            "evidence": "The \"Other Supported Versions\" list on update-pull-request-reviewers contains exactly rest-6.0/6.1/7.0/7.2 and server-rest-6.0/7.0/7.1, with no 4.1, 5.0, 5.1 or server-rest-5.0 entries (unlike sibling pages, which do list them). The Server-2020 floor is a two-document inference completed by integrate/concepts/rest-api-versioning, whose table gives Azure DevOps Server 2020 = up to 6.0 and Server 2019 = up to 5.0."
          },
          {
            "claim": "GitPullRequestStatus.iterationId is `integer (int32)`, \"ID of the iteration to associate status with. Minimum value is 1\", and the create doc says \"Note that you can specify iterationId in the request body to post the status on the iteration\"",
            "status": "CONFIRMED",
            "evidence": "pull-request-statuses/create?view=azure-devops-rest-7.1: description sentence verbatim, iterationId row verbatim, and the first documented example is titled \"On iteration\" with body {\"iterationId\":1,\"state\":\"succeeded\",...}. Full field list (_links, context, createdBy, creationDate, description, id, iterationId, properties, state, targetUrl, updatedDate) matches the claim exactly."
          },
          {
            "claim": "GitStatusState enum is notSet, pending, succeeded, failed, error, notApplicable with the quoted descriptions",
            "status": "CONFIRMED",
            "evidence": "Definitions section of pull-request-statuses/create: \"notSet | Status state not set. Default state.\" ... \"notApplicable | Status is not applicable to the target object.\" Exact match."
          },
          {
            "claim": "Posting status appends rather than replaces \u2014 \"A service may update a PR status for a single PR by posting additional statuses, only the latest of which is shown for each unique context\" \u2014 and PRs over 100,000 modified files do not support iterations",
            "status": "CONFIRMED",
            "evidence": "repos/git/pull-request-status?view=azure-devops, sections \"Updating status\" and \"Iteration status\": both sentences appear verbatim, including \"any attempt to create a status for a non-existent iteration will return an error.\" The iteration rationale sentence the claim quotes (\"Posting status to a specific iteration of a PR guarantees that status applies only to the code that was evaluated and none of the future updates.\") is also verbatim."
          },
          {
            "claim": "Status policy UI: \"In Status to check, select the posted check from the list. If the service hasn't posted status yet, type the genre/name value directly\"; knobs are Policy requirement, Authorized identity, Reset conditions, Policy applicability, Path filter, Default display name",
            "status": "CONFIRMED",
            "evidence": "The quoted sentence is verbatim on repos/git/branch-policies (\"Require status checks\" section), preceded by \"In Branch policies, under Status checks, select +.\" The six knobs and the quoted Authorized-identity and Reset-conditions sentences are verbatim on repos/git/pr-status-policy. Note the claim attributed the first quote to pr-status-policy, where it does not appear in that form \u2014 the source is branch-policies."
          },
          {
            "claim": "The merge-behaviour table (succeeded unblocks; failed/error block; pending blocks; notApplicable bypasses; notSet treated as pending) is documented",
            "status": "CONFIRMED",
            "evidence": "repos/git/available-pr-status-checks?view=azure-devops, table \"Status values and merge behavior\" reproduces all six rows exactly as claimed. Caveat: that page's banner is **Azure DevOps Services** only and it is marked ai-usage: ai-assisted, so the table should not be assumed to describe Azure DevOps Server."
          },
          {
            "claim": "\"By default, pushing a new commit resets required status checks to pending\"",
            "status": "OVERSTATED",
            "evidence": "The sentence is verbatim on available-pr-status-checks. But it is contradicted by pull-request-status, which instructs: \"When configuring the status policy, if iteration status is being used, the Reset conditions should be set to Reset status whenever there are new changes\" \u2014 advice that is meaningless if reset is already the default. The claim quotes the default-on statement as settled fact and omits the conflict. Given available-pr-status-checks is Services-only and ai-assisted while pull-request-status carries the Server banner, JetBridge must set Reset conditions explicitly rather than rely on a default."
          },
          {
            "claim": "Pin the status gate to the JetBridge service identity via Authorized identity, and use iteration-scoped status, to make the gate revision-correct",
            "status": "OVERSTATED",
            "evidence": "The claim's own cited source warns against exactly this: available-pr-status-checks states \"Leave **Advanced Options** at their defaults when configuring the status check policy. Changing the authorized identity or requiring an iteration ID prevents status checks from posting correctly.\" Scoped there to the AdvancedSecurity checks, but it is an unqualified operational warning about both levers the claim recommends, and it reveals an undocumented policy setting (\"requiring an iteration ID\") that appears nowhere else. Also note pull-request-status calls the same field \"Authorized account\" while pr-status-policy calls it \"Authorized identity\" \u2014 a doc-vs-doc naming disagreement the claim did not flag."
          },
          {
            "claim": "No policy type GUID is documented for the Status policy; Configurations-Create enumerates GUIDs only for Minimum approval count, Build, Required reviewers, Git case enforcement, Max blob size, Merge strategy, Work item linking; Policy Types - List documents only the shape",
            "status": "CONFIRMED",
            "evidence": "policy/configurations/create?view=azure-devops-rest-7.1 has exactly seven examples with the seven GUIDs the claim lists, all matching character for character, and no Status example. policy/types/list?view=azure-devops-rest-7.1 has no Examples section at all \u2014 only the PolicyType object (_links, description, displayName, id, url). The claim's \"discover it at runtime, do not hardcode\" guidance is correct. (Bonus doc bug: the Git-case-enforcement and max-blob-size sample RESPONSES both mislabel displayName as \"Required reviewers\".)"
          },
          {
            "claim": "Policy `settings` is an untyped JObject and the status policy's settings field names are unspecified on Learn",
            "status": "CONFIRMED",
            "evidence": "PolicyConfiguration definition: \"settings | JObject | The policy configuration settings.\" JObject is defined only as \"Represents a JSON object.\" No Learn page gives status-policy settings keys. isBlocking is confirmed as a real top-level field (\"Indicates whether the policy is blocking.\"), though the claim's mapping of the UI's \"Policy requirement\" onto isBlocking is inference, not stated."
          },
          {
            "claim": "Approval-count policy settings JSON includes `allowDownvotes` and `resetOnSourcePush` alongside minimumApproverCount / creatorVoteCounts / scope",
            "status": "UNVERIFIABLE",
            "evidence": "The documented \"Approval count policy\" sample body on configurations/create contains only {minimumApproverCount, creatorVoteCounts, scope}. `allowDownvotes` and `resetOnSourcePush` are documented ONLY as az CLI flag names (--allow-downvotes, --reset-on-source-push on branch-policies), never as REST settings keys. The claim silently converts CLI flag names into JSON field names. Template the settings object from a GET of a hand-configured policy, as the claim itself advises elsewhere."
          },
          {
            "claim": "\"When new changes are pushed\" offers four alternatives: approval on every iteration (Server 2022.1+), approval on the last iteration, reset all approval votes (does not reset reject/wait), reset all code reviewer votes",
            "status": "CONFIRMED",
            "evidence": "repos/git/branch-policies?view=azure-devops, lines under \"Under **When new changes are pushed**:\" reproduce all four verbatim, including \"**Require at least one approval on every iteration** is available in Azure DevOps Server 2022.1 and higher.\" and \"Reset all approval votes (does not reset votes to reject or wait)\"."
          },
          {
            "claim": "The CLI is lossy: `az repos policy approver-count` exposes only a boolean --reset-on-source-push, and Azure DevOps CLI commands are not supported against Azure DevOps Server",
            "status": "CONFIRMED",
            "evidence": "branch-policies documents `az repos policy approver-count create --allow-downvotes {false,true} ... --minimum-approver-count ... --reset-on-source-push {false,true}` with description \"Reset votes when changes are pushed to the source\" (and it is marked **Required** on create). The sentence \"Azure DevOps CLI commands aren't supported for Azure DevOps Server.\" appears repeatedly on branch-policies and complete-pull-requests under the azure-devops-2022/azure-devops-server moniker."
          },
          {
            "claim": "Whether a policy-driven vote reset emits its own ReviewerVoteNotification and/or VoteUpdate thread is not documented",
            "status": "CONFIRMED",
            "evidence": "Accurate statement of absence. Nothing on branch-policies, service-hooks/events, pull-request-threads/list or configurations/create addresses it. The claim's instruction to verify empirically before shipping is the right call, and this gap is load-bearing: it is the one hole in the self-trigger-immunity argument of the vote-event claim."
          },
          {
            "claim": "PR update is patchable only for Status, Title, Description (up to 4000 chars), CompletionOptions, MergeOptions, AutoCompleteSetBy.Id, TargetRefName, and other properties either throw InvalidArgumentValueException or are silently ignored",
            "status": "CONFIRMED",
            "evidence": "pull-requests/update?view=azure-devops-rest-7.1 lists exactly those seven bullets followed verbatim by \"Attempting to update other properties outside of this list will either cause the server to throw an `InvalidArgumentValueException`, or to silently ignore the update.\" The claim's advice to re-read and assert status==completed rather than trust a 200 is sound."
          },
          {
            "claim": "The suggested completion PATCH body {\"status\":\"completed\",\"lastMergeSourceCommit\":{...},\"completionOptions\":{...}}",
            "status": "OVERSTATED",
            "evidence": "Internally inconsistent with the claim's own quoted patchable list: `lastMergeSourceCommit` is not among the seven patchable properties, so by the sentence quoted two lines earlier it will be silently ignored or rejected. The endpoint and the status/completionOptions parts are correct; the lastMergeSourceCommit element is not documented as accepted."
          },
          {
            "claim": "GitPullRequestCompletionOptions fields and GitPullRequestMergeStrategy values (noFastForward, squash, rebase, rebaseMerge) with the quoted descriptions; squashMerge deprecated; PullRequestStatus and PullRequestAsyncStatus enums",
            "status": "CONFIRMED",
            "evidence": "pull-requests/update Definitions: all nine completion-option fields match (including autoCompleteIgnoreConfigIds \"Only applies to optional policies (isBlocking == false). Auto-complete always waits for required policies (isBlocking == true).\"). All four merge-strategy descriptions match verbatim, as does \"It is recommended that you explicitly set MergeStrategy in all cases.\" PullRequestStatus = notSet/active/abandoned/completed/all; PullRequestAsyncStatus = notSet/queued/conflicts/succeeded/rejectedByPolicy/failure. One naming slip: the enum is defined as `PullRequestMergeFailureType`, not GitPullRequestMergeFailureType; its values (none/unknown/caseSensitive/objectTooLarge) are as claimed."
          },
          {
            "claim": "Three documented situations block rebase-on-completion: target-branch policy prohibiting rebase (needs Override branch policies), source branch has policies, Merge Conflict Extension used",
            "status": "CONFIRMED",
            "evidence": "repos/git/complete-pull-requests?view=azure-devops, section \"Rebase during PR completion\": all three bullets verbatim, including \"Rebasing would modify the source branch without going through the policy approval process\" and the fallback \"you can still rebase your branch locally and then push upstream, or squash-merge your changes\". Worth flagging: this page names the permission \"Override branch policies\" and elsewhere \"Exempt from policy enforcement\", while set-git-repository-permissions says that permission was replaced by \"Bypass policies when completing pull requests\" / \"Bypass policies when pushing\". Three names for overlapping concepts across live pages."
          },
          {
            "claim": "autoCompleteSetBy is \"If set, auto-complete is enabled for this pull request and this is the identity that enabled it\", is patchable as AutoCompleteSetBy.Id, waits only on required policies by default, requires branch policies (TFS 2017+), and requires conflicts resolved first",
            "status": "CONFIRMED",
            "evidence": "Field description verbatim on pull-requests/update; AutoCompleteSetBy.Id is in the patchable bullet list. complete-pull-requests gives verbatim: \"The **Set auto-complete** option is available in Azure Repos and TFS 2017 and higher when you have branch policies. If you don't see **Set auto-complete**, you don't have any branch policies.\"; \"By default, a PR that's set to autocomplete waits only on required policies.\"; \"You must resolve any merge conflicts between the PR branch and the target branch before you can merge a PR or set the PR to autocomplete.\" CLI `az repos pr update --id <PR Id> --auto-complete true` and `az repos pr create --auto-complete true` both confirmed."
          },
          {
            "claim": "Auto-complete is cleared by patching autoCompleteSetBy.id to the empty GUID",
            "status": "UNVERIFIABLE",
            "evidence": "complete-pull-requests documents only the UI action \"Select **Cancel auto-complete** to turn off autocomplete.\" No Learn page and no REST reference states the empty-GUID mechanism or any REST clear semantics. This is an undocumented implementation detail presented in the same register as the documented PATCH contract."
          },
          {
            "claim": "A service identity can set autoCompleteSetBy \u2014 explicitly labelled confidence \"inferred\"",
            "status": "CONFIRMED",
            "evidence": "Correctly hedged. No Learn page permits or forbids it; the field is a plain patchable property on a vso.code_write route and service principals are documented as first-class identities that \"Access Azure DevOps resources with proper permissions\". The claim's instruction to verify empirically, and to assume self-only for setting another identity, is the right posture."
          },
          {
            "claim": "Default Git permissions: Read row reads \"**Read** (clone, fetch, and explore the contents of a repository); also, can create, comment on, vote, and **Contribute to pull requests**\" granted to Readers/Contributors/Build Admins/Project Admins; Contribute granted to Contributors and up",
            "status": "CONFIRMED",
            "evidence": "repos/git/set-git-repository-permissions?view=azure-devops \"Default repository permissions\" table reproduces that row verbatim with four checkmarks, and \"**Contribute**, **Create branches**, **Create tags**, and **Manage notes**\" with three (not Readers). The claim's own caveat that the row conflates two separately-named permissions is fair. Minor: the permission is \"Create branches\" plural, not \"Create branch\"."
          },
          {
            "claim": "Posting PR status via REST requires the Contribute to pull requests permission; bypass permissions are not set for any security group by default and replaced Exempt from policy enforcement",
            "status": "CONFIRMED",
            "evidence": "available-pr-status-checks: \"To post a status check via the REST API, the calling identity needs the **Contribute to pull requests** permission on the repository.\" set-git-repository-permissions: row \"**Bypass policies when completing pull requests**, **Bypass policies when pushing**, **Force push** ... (not set for any security group)\", plus the section \"The following two permissions replace the former permission\" naming both."
          },
          {
            "claim": "Since Azure DevOps sprint 224 / Server 2022.1, Edit policies is no longer granted automatically to branch creators",
            "status": "CONFIRMED",
            "evidence": "set-git-repository-permissions verbatim: \"Beginning with Azure DevOps sprint 224 (Azure DevOps Services and Azure DevOps Server 2022.1 and higher), Edit policies permission is no longer granted automatically to branch creators... You must have the **Edit policies** permission granted explicitly (either manually or through REST API)...\""
          },
          {
            "claim": "Token scopes: vso.code_write for votes/completion, vso.code_status for the gate, vso.threads_full for comments; code_manage \u2283 code_write \u2283 code; vso.code_status is independent and NOT inherited from code_write",
            "status": "CONFIRMED",
            "evidence": "integrate/get-started/authentication/oauth?view=azure-devops \"Available scopes\" table has an explicit \"Inherits from\" column: vso.code_write inherits vso.code; vso.code_manage inherits vso.code_write; vso.code_status (\"Grants the ability to read and write commit and pull-request status.\") has an EMPTY Inherits-from cell, confirming independence. vso.threads_full = \"Grants the ability to read and write to pull request comment threads.\", also no inheritance. Page also documents \"Scope inheritance: Some scopes include others (for example, vso.code_manage includes vso.code_write).\""
          },
          {
            "claim": "Service principals/managed identities cannot create PATs or SSH keys, cannot sign in interactively, cannot create or own organizations, do not support Azure DevOps OAuth flows; need a license in every org; must be explicitly added by a PCA/PA; use the Enterprise applications object ID; scope https://app.vssps.visualstudio.com/.default",
            "status": "CONFIRMED",
            "evidence": "service-principal-managed-identity?view=azure-devops \"Key differences from user accounts\" lists verbatim: \u274c Create PATs or Secure Shell keys / \u274c Sign in interactively or access via a web UI / \u274c Create or own organizations / \u274c Support Azure DevOps OAuth flows. Licensing and \"Group-based licensing rules don't automatically apply\" confirmed; \"Adding a service principal to a Microsoft Entra security group doesn't grant access to your organization. A Project Collection Administrator (PCA) or Project Administrator (PA) must explicitly add the service principal\" confirmed; Enterprise applications object ID confirmed; client_credentials body with scope=https://app.vssps.visualstudio.com/.default confirmed."
          },
          {
            "claim": "Resource GUID 499b84ac-1321-427f-aa17-267ca6975798 is an alternative token audience",
            "status": "OVERSTATED",
            "evidence": "The GUID does appear on the page, but only inside the cross-tenant managed-identity workaround C# sample as `AcquireTokenForClient(new[] { \"499b84ac-1321-427f-aa17-267ca6975798/.default\" })`. It is never presented as a general-purpose audience alongside app.vssps.visualstudio.com/.default. Usable, but the doc does not endorse it as an interchangeable option."
          },
          {
            "claim": "Service principals and managed identities are Azure DevOps Services only; on-prem must use a domain account with Windows auth, PAT, or client library",
            "status": "CONFIRMED",
            "evidence": "service-principal-managed-identity carries the banner \"**Azure DevOps Services**\" and its monikers list is `azure-devops` alone. Independently corroborated by the oauth page: \"OAuth 2.0 is available only for Azure DevOps Services, not Azure DevOps Server. For on-premises scenarios, use Client libraries, Windows Authentication, or personal access tokens.\" Also confirmed: \"Azure DevOps OAuth 2.0 is deprecated and no longer accepts new registrations\" / \"New app registrations are no longer accepted as of April 2025. The service is scheduled for full deprecation in 2026.\""
          },
          {
            "claim": "The Basic-vs-Stakeholder failure surfaces as \"The Git repository with name or identifier doesn't exist or you don't have permissions\"",
            "status": "CONFIRMED",
            "evidence": "service-principal-managed-identity, \"Common errors and solutions\": that exact heading, with \"**Solution:** Ensure that the service principal has at least a Basic license. Stakeholder licenses don't provide repository access.\""
          },
          {
            "claim": "Three settings let a vote be cast successfully yet not count: Allow requestors to approve their own changes, Prohibit the most recent pusher from approving their own changes, Allow completion even if some reviewers vote to wait or reject",
            "status": "CONFIRMED",
            "evidence": "branch-policies?view=azure-devops reproduces all three verbatim, including the decisive discrepancy sentence \"Otherwise, the creator can still vote **Approve** on the PR, but their vote doesn't count toward the minimum number of reviewers\" and \"Selecting this option means the most recent pusher's vote doesn't count, even if they can ordinarily approve their own changes.\" The agent-is-always-the-most-recent-pusher hazard the claim highlights is real and correctly derived."
          },
          {
            "claim": "Automatically included reviewers has its own independent Allow requestors setting, and the docs devote an FAQ to it",
            "status": "CONFIRMED",
            "evidence": "branch-policies FAQ \"Why can't I complete my own pull requests when I set 'Allow requestors to approve their own changes'?\" states verbatim \"In each policy, the setting applies only to that policy. The setting doesn't affect the other policy.\" and adds \"Other policies might prevent you from approving your own changes... For example, **Prohibit the most recent pusher from approving their own changes**.\""
          },
          {
            "claim": "Read authoritative policy evaluation instead of inferring merge-readiness from vote integers; `az repos pr policy list` shows Evaluation ID / Policy / Blocking / Status / Expired per policy; the server reevaluates on push and on vote",
            "status": "CONFIRMED",
            "evidence": "complete-pull-requests shows the `az repos pr policy list --id 28 --output table` sample with columns Evaluation ID, Policy, Blocking, Status, Expired, Build ID (claim omitted Build ID) and rows including \"Comment requirements | False | Approved\", confirming the Comment-requirements policy referenced in the thread-status claim. branch-policies states verbatim \"The server reevaluates branch policies when pull request owners push changes and when reviewers vote.\""
          },
          {
            "claim": "Mark as draft silently wipes votes",
            "status": "CONFIRMED",
            "evidence": "complete-pull-requests, Complete-button dropdown options: \"**Mark as draft**: Return the PR to draft status and remove all votes.\" Verbatim."
          },
          {
            "claim": "General source-reliability note on the conceptual pages underpinning the policy and permissions claims",
            "status": "CONFIRMED",
            "evidence": "Five of the conceptual pages cited across these claims carry `ai-usage: ai-assisted` front matter: branch-policies, complete-pull-requests, set-git-repository-permissions, pr-status-policy, available-pr-status-checks. The REST reference pages (autogenerated from the service's swagger, git_commit_id cb0d0b30ca) do not. Where the two disagree \u2014 the status-reset default, the Authorized identity/account naming, the Override-branch-policies vs Bypass-policies permission naming \u2014 prefer the REST reference and verify against a live org."
          }
        ]
      },
      {
        "area": "votes-policies",
        "claims_checked": [
          {
            "claim": "Reviewer vote model: `vote` is int16 with the five values 10/5/0/-5/-10, and a single PUT to /reviewers/{reviewerId} both adds a reviewer and casts the vote; `az repos pr set-vote` exposes the same five-valued domain.",
            "status": "CONFIRMED",
            "evidence": "Verified against the current 7.1 Pull Request Reviewers docs (create-pull-request-reviewer, doc title literally 'Add a reviewer to a pull request or cast a vote'). The five CLI values (approve, approve-with-suggestions, reject, reset, wait-for-author) are confirmed on learn.microsoft.com/en-us/cli/azure/repos/pr. No practitioner report contradicts the value set. NOTE the adversarial caveat filed separately below: a 200 OK from these routes does NOT prove the vote was written."
          },
          {
            "claim": "Vote history is retrievable via system comment threads: `CodeReviewThreadType=VoteUpdate` with `CodeReviewVotedByTfId`, `CodeReviewVotedByDisplayName`, `CodeReviewVoteResult` (stringified integer), content 'Normal Paulk voted 10', author 'Project Collection Service Accounts', thread ids 141..148 ascending by publishedDate.",
            "status": "CONFIRMED",
            "evidence": "Read the full 7.1 Pull-Request-Threads/List sample response verbatim. Thread 143 is exactly as described: commentType 'system', content 'Normal Paulk voted 10', properties {CodeReviewThreadType:'VoteUpdate', CodeReviewVotedByDisplayName:'Normal Paulk', CodeReviewVotedByTfId:'d6245f20-...', CodeReviewVoteResult:{$type:'System.String',$value:'10'}}. MergeAttempt (141,146), ReviewersUpdate (142,144), RefUpdate (145) all present as described; ids ascend with publishedDate. One correction: the RefUpdate email key is `CodeReviewRefUpdatedBy` (not a '(email)' suffix variant), and `CodeReviewRefNewCommits`/`CodeReviewRefNewHeadCommit` hold the same SHA in the sample \u2014 the plural key is not demonstrated to be a list. The claim's own 'documented-by-example, value set is open' caveat is correct: `properties` is typed only as PropertiesCollection and CodeReviewThreadType is nowhere enumerated."
          },
          {
            "claim": "Previous vote values are recoverable by reading the ordered sequence of VoteUpdate threads for a given reviewer GUID (i.e. each vote change appends a NEW VoteUpdate thread).",
            "status": "UNVERIFIABLE",
            "evidence": "This is the load-bearing half of the 'vote history is retrievable' finding and it is NOT supported. The 7.1 sample contains exactly ONE VoteUpdate thread; nothing in the docs states whether a reviewer's second vote appends another VoteUpdate thread or mutates the existing one. GitPullRequestCommentThread carries both `publishedDate` and `lastUpdatedDate`, so in-place mutation is a mechanically available implementation. I found no practitioner post either way (Stack Overflow is blocked to my crawler; see the coverage-limitation entry). Treat 'full vote history' as an assumption to prove on a live org before any sealed review/v1 record depends on it."
          },
          {
            "claim": "Self-suppression on the vote path is 'a single equality check' on the actor GUID from the payload / CodeReviewVotedByTfId.",
            "status": "REFUTED",
            "evidence": "Two independent defects. (1) THE WEBHOOK PAYLOAD CARRIES NO ACTOR. I read the git.pullrequest.updated and git.pullrequest.merged sample payloads in the doc source (MicrosoftDocs/azure-devops-docs/docs/service-hooks/events.md, lines 2283-2405 and 2150-2282). The only identity in `resource` is `createdBy` \u2014 the PR AUTHOR \u2014 plus the full current `reviewers[]` array. There is no 'who did this' field and no old/new diff (work-item events have such a diff; PR events do not). The actor's name exists only inside the prose `message.text`/`detailedMessage.text`. So determining which reviewer voted requires either diffing reviewers[] against locally-held prior state, parsing English prose, or a callback to GET .../threads. (2) EVEN VIA THREADS THE COMPARISON IS NOT A PLAIN EQUALITY: in the SAME 7.1 threads sample, `CodeReviewVotedByTfId` is dashed ('d6245f20-2af8-44f4-9451-8107cb2767db') while `CodeReviewReviewersUpdatedByTfId` / `...AddedTfId` are dashless 32-char ('b335b0d4578f4944b94ca45216eb1a1a', '2428198325304a9caeb788d60d57acfd'). Any GUID comparison across thread types must normalize format."
          },
          {
            "claim": "Subscribe to `git.pullrequest.updated` filtered to `notificationType = ReviewerVoteNotification` and the trigger becomes vote-only.",
            "status": "OVERSTATED",
            "evidence": "The four notificationType values are confirmed verbatim in the doc source, but `notificationType` is a SUBSCRIPTION FILTER ONLY \u2014 it appears exactly once in the entire events.md file, in the Settings list, and never in any payload. Consequence for the design: a handler cannot read the trigger reason off the event; it must infer it from which subscription (i.e. which endpoint URL) delivered it, so JetBridge must create one subscription per notificationType and route by path. The narrow-subscription strategy still works, but the docs give no guarantee that a policy-driven vote reset is classified as ReviewerVoteNotification rather than (or in addition to) PushNotification \u2014 see the next entry."
          },
          {
            "claim": "Whether the policy-driven vote reset on push also emits a ReviewerVoteNotification / VoteUpdate thread is unspecified and must be verified empirically.",
            "status": "CONFIRMED",
            "evidence": "Confirmed as genuinely undocumented \u2014 I could find no Learn page, release note, or practitioner report resolving it. One adjacent artifact exists and is worth noting but does NOT settle it: `ReviewersVotesResetEvent` in the azure-devops-extension-api JS surface (learn.microsoft.com/en-us/javascript/api/azure-devops-extension-api/reviewersvotesresetevent) \u2014 that is a client-side extension event, not a service hook, so it proves the server models 'votes reset' as a distinct concept but says nothing about webhook emission. The claim's defensive rule (drop vote=0 events arriving near a PushNotification for the same PR) should be treated as required, not optional."
          },
          {
            "claim": "The agent's textual replies fire only the comment event, never the vote event, so prose is structurally incapable of re-triggering the loop.",
            "status": "CONFIRMED",
            "evidence": "Confirmed at the contract level: comments are a separate event id (`ms.vss-code.git-pullrequest-comment-event`) with its own filter set (repository, branch only) and a different resource shape. This remains the strongest genuine advantage over GitHub's review-object conflation. Worth adding: unlike the vote path, the COMMENT payload DOES carry the actor \u2014 `resource.comment.author.id` is present in the sample \u2014 so self-suppression is a real equality check on the comment path and is NOT on the vote path. That asymmetry is the opposite of what the claim implies."
          },
          {
            "claim": "Branch-policy vote reset is configured under 'When new changes are pushed' with FOUR options that 'are alternatives, not a single boolean'.",
            "status": "OVERSTATED",
            "evidence": "The four UI strings are verbatim correct (branch-policies.md lines 180-184, incl. the Azure DevOps Server 2022.1 floor on 'every iteration'). But they are FOUR INDEPENDENT BOOLEAN SETTINGS, not mutually exclusive alternatives: `resetOnSourcePush` (reset approvals only), `resetRejectionsOnSourcePush` (reset all votes), `requireVoteOnLastIteration`, `requireVoteOnEachIteration` \u2014 plus `blockLastPusherVote`, `allowDownvotes`, `creatorVoteCounts`, `minimumApproverCount`. Field names cross-confirmed from microsoft/terraform-provider-azuredevops issues #206/#289/#783 and Azure/azure-devops-cli-extension #1389. JetBridge must handle combinations, not a five-way enum."
          },
          {
            "claim": "The az CLI is lossy: it 'cannot express the approvals-only variant or the per-iteration variants'.",
            "status": "REFUTED",
            "evidence": "Half wrong in a way that matters. `az repos policy approver-count --reset-on-source-push` maps to `resetOnSourcePush`, which IS the approvals-only variant ('Reset all approval votes (does not reset votes to reject or wait)'). The CLI genuinely cannot express `resetRejectionsOnSourcePush` (reset ALL votes), `requireVoteOnLastIteration`, `requireVoteOnEachIteration`, or `blockLastPusherVote` \u2014 Azure/azure-devops-cli-extension issue #1389 requests exactly these three, is still OPEN with no assignee and no PR, and Azure/azure-cli issue #22625 covers the 'Prohibit the most recent pusher' gap. Conclusion (use the REST API, not the CLI) survives; the stated reason does not."
          },
          {
            "claim": "Programmatic vote reset without a policy: the bulk PATCH /reviewers endpoint lets the agent reset the votes of reviewers it considers stale.",
            "status": "REFUTED",
            "evidence": "THE MOST IMPORTANT ADVERSARIAL FINDING IN THIS AREA. microsoft/azure-devops-node-api issue #611 ('Resetting Reviewer vote to 0 via GitAPI has no effect'): reporter set an existing 'Approved' reviewer's vote to 0, 'REST call completes successfully, but vote status will not have been reset (will still be Approved)'. The maintainer closed it with a working repro and this explanation, quoted from the issue thread: the feature works, 'you have to use correct vote.id, and PAT owner (which you are using in the node-api) can reset their own vote.' The maintainer's own repro resets the CALLER's vote. So: (a) the endpoint works, but the demonstrated capability is SELF-reset; there is no evidence a bot identity may reset a human reviewer's vote, and the docs state no permission requirement for this route; (b) the failure mode is SILENT \u2014 HTTP 200 with no state change. Note also the reporter used the SINGULAR `updatePullRequestReviewer` while the maintainer's fix used the PLURAL `updatePullRequestReviewers`; the singular PATCH documents only isFlagged/hasDeclined as patchable, so setting `vote` on it is a second independent silent no-op. The claim's recommendation to prefer explicit PATCH-reset over the branch policy should be withdrawn pending live verification."
          },
          {
            "claim": "The Status policy type GUID is not documented; discover it at runtime via GET _apis/policy/types and template settings from a hand-configured instance.",
            "status": "CONFIRMED",
            "evidence": "Verified exhaustively against the current 7.1 Configurations-Create page. It shows seven examples and seven real (non-anonymized) GUIDs \u2014 fa4e907d-c16b-4a4c-9dfa-4906e5d171dd (Minimum approval count), 0609b952-1397-4640-95ec-e00a01b2c241 (Build), fd2167ab-b0be-447a-8ec8-39368250530e (Required reviewers), 7ed39669-655c-494e-b4a0-a08b4da0fcce (Git case enforcement), 2e26e725-8201-4edd-8bf5-978563c34a80 (Max blob size), fa4e907d-c16b-4a4c-9dfa-4916e5d171ab (Merge strategy), 40e92b44-2fe1-4dd6-b3d8-74a9c21d0c6e (Work item linking) \u2014 and NO Status/status-check example. Two extra reasons to trust the 'discover, don't hardcode' advice: (i) the page has a real doc bug \u2014 the sample RESPONSES for Git case enforcement and Max blob size both report `type.displayName: 'Required reviewers'`, so displayName in the docs is unreliable; (ii) learn.microsoft.com/en-us/azure/devops/cli/policy-configuration-file now shows ANONYMIZED placeholder GUIDs for the same Build policy ('bbbbbbbb-1111-2222-3333-cccccccccccc'), i.e. two Learn pages disagree about the same identifier."
          },
          {
            "claim": "The POST body for the approver-count policy is settings {minimumApproverCount, creatorVoteCounts, allowDownvotes, resetOnSourcePush, scope}.",
            "status": "OVERSTATED",
            "evidence": "The Learn 7.1 sample settings for the Approval count policy contain ONLY `minimumApproverCount`, `creatorVoteCounts`, and `scope`. `allowDownvotes` and `resetOnSourcePush` are real (CLI docs + terraform provider) but are NOT in any Learn REST sample. `settings` is a bare JObject with no schema. This is the same 'documented-by-example' hazard the research correctly flagged for the Status policy \u2014 it applies to the approver-count policy too, and the write-up presents that body as if it were documented."
          },
          {
            "claim": "Self-approval traps: votes can be recorded (200, vote=10 readable) yet not count \u2014 'Allow requestors to approve their own changes', 'Prohibit the most recent pusher from approving their own changes', 'Allow completion even if some reviewers vote to wait or reject'; the two policies' settings are independent.",
            "status": "CONFIRMED",
            "evidence": "All three verbatim in branch-policies.md (lines 170, 172, 174), including the explicit vote-recorded-but-discounted wording. The independence FAQ is real (lines 1088-1103) and even names the interaction: 'Other policies might prevent you from approving your own changes, even if Allow requestors to approve their own changes is set. For example, Prohibit the most recent pusher from approving their own changes.' The agent-specific hazard is correctly identified \u2014 the agent is by construction the most recent pusher. Also confirmed: 'The server reevaluates branch policies when pull request owners push changes and when reviewers vote' (line 1059), and 'Mark as draft: Return the PR to draft status and remove all votes' (complete-pull-requests.md line 138)."
          },
          {
            "claim": "resetOnSourcePush is a clean, well-behaved way to invalidate stale approvals when the agent pushes.",
            "status": "REFUTED",
            "evidence": "Practitioner report that the reset is too coarse for an agent loop: Microsoft Q&A 1688029 ('Azure DevOps PRs reset approvals even when commits make no changes or sync with main branch') \u2014 approvals are reset by EMPTY commits and by merges from the target branch that change nothing. Microsoft did not answer on the merits; the moderator redirected the asker on the grounds that Azure DevOps is not supported on Microsoft Q&A, so it stands unrebutted and unfixed. Direct consequence for the design: the 'approved -> agent rebases if needed and merges' step will itself wipe the approval it is acting on whenever resetOnSourcePush is enabled, because a rebase/sync is a source-branch push."
          },
          {
            "claim": "Auto-complete: PATCH the PR with autoCompleteSetBy + completionOptions and Azure DevOps merges itself once policies pass (claim marked 'inferred' for service identities).",
            "status": "OVERSTATED",
            "evidence": "The mechanism is documented (autoCompleteSetBy is an IdentityRef and AutoCompleteSetBy.Id is explicitly patchable; 'By default, a PR that's set to autocomplete waits only on required policies'; TFS 2017+ and requires at least one branch policy \u2014 all verified in complete-pull-requests.md lines 294-346). But the claim understates a KNOWN failure mode that hits exactly this field: MicrosoftDocs/vsts-rest-api-specs issue #134, 'Set autocomplete via REST Api example doesn't work' \u2014 reporter followed the Learn example and 'the response shows setAutocompleteBy being set for the given userId, but in my case this section is missing my response', with completionOptions echoed in the response but not applied. The issue is labeled 'investigating' and is UNRESOLVED with no maintainer reply. A second, older report exists with the on-point title \"'autoCompleteSetBy' ignored in Pull Request REST API\" (Developer Community 298596) \u2014 title only; see the coverage-limitation entry. This is the concrete instance of the PR-update doc's own 'silently ignore the update' warning, so 're-read the PR and assert' is mandatory, not defensive hygiene. Whether a SERVICE PRINCIPAL specifically can set autoCompleteSetBy remains unverified in both directions."
          },
          {
            "claim": "isFlagged / hasDeclined are PATCHable; isRequired is policy-owned and read-only; isReapprove re-asserts an unchanged approval; votedFor holds group rollups.",
            "status": "UNVERIFIABLE",
            "evidence": "The schema text is correctly quoted from the 7.1 IdentityRefWithVote definition, and I did not find anything contradicting it \u2014 but I also found NO practitioner evidence that any of hasDeclined, isReapprove, or votedFor behave as documented in practice, and these are exactly the thinly-documented corners where the API and the web UI tend to diverge. One suggestive but unreadable signal: a Developer Community ticket titled 'Added as a reviewer when declining to review a PR' (t/1244100), whose title implies the decline path has UI side effects on the reviewer list; I could not read the body. `isReapprove` in particular ('this approve vote should still be handled even though vote didn't change') is a write-side behavior with zero examples anywhere. Do not build routing on hasDeclined or isReapprove without a live probe."
          },
          {
            "claim": "PR status can be scoped to an iteration (iterationId >= 1) and the Status branch policy resets on new changes, making the gate revision-correct.",
            "status": "CONFIRMED",
            "evidence": "Documented as described (pull-request-status.md and pr-status-policy.md, both still current in the docs repo); branch-policies.md line 830 still lists Authorized identity / Reset conditions / Policy applicability / Path filter as the configurable knobs. I found no report of iteration-scoped status misbehaving. Caveat on the adversarial lens rather than the claim: I could not find ANY independent practitioner account of iteration-scoped status being used in anger, so 'documented and unchallenged' is the honest status, not 'proven in the field'. The >100,000-file no-iterations degenerate case the claim flags is real and remains the only documented failure mode."
          },
          {
            "claim": "Bot identity: prefer a Microsoft Entra service principal / managed identity over a PAT (Services only); on Azure DevOps Server use a domain account or PAT.",
            "status": "CONFIRMED",
            "evidence": "Confirmed and now MORE urgent than the research states, for a reason the research predates or omits: Azure DevOps is retiring GLOBAL Personal Access Tokens. Per the sprint-270 release note (ms.date 2026-03-05), creation and regeneration of global PATs was to be blocked on March 15, 2026 (that bullet is struck through in the current page, so the date moved), and 'December 1, 2026, all existing global PATs will be fully decommissioned and stop working.' Org-scoped PATs and Entra auth are the supported paths. Any JetBridge design that falls back to a PAT \u2014 notably the Azure DevOps Server branch, where service principals are unavailable \u2014 must use org-scoped tokens and has a hard deadline."
          },
          {
            "claim": "Environmental change the research does not account for: PRs may already be auto-complete at creation.",
            "status": "CONFIRMED",
            "evidence": "Sprint 270 (ms.date 2026-03-05) added a project- and repository-level setting 'Set PRs to auto-complete on creation by default': 'When this setting is enabled, every new PR will automatically have Set auto-complete turned on.' Project settings > Repositories > Settings. This changes the baseline the disposition state machine starts from \u2014 an agent-created PR in such a repo is armed to merge the instant policies pass, before any human disposition exists. The same sprint added a repo setting to suppress the PR-ID prefix on completion commit messages, which breaks any commit-message parsing that assumes the 'Merged PR N:' prefix."
          },
          {
            "claim": "COVERAGE LIMITATION on this verification pass (not a claim from the research).",
            "status": "UNVERIFIABLE",
            "evidence": "Disclosing the gaps so the confidence levels above are read correctly. (1) stackoverflow.com and reddit.com are blocked to my web-fetch user agent \u2014 the single largest body of ADO practitioner reports was inaccessible. (2) developercommunity.visualstudio.com is a client-rendered SPA with no server-side rendering and no readable API: every URL, including /api/* probes, returns only the shell (76KB of chrome, og:title 'Developer Community'). Its mirror hosts surfaced by search (vsf-prod.westus.cloudapp.azure.com, developercommunityapi.westus.cloudapp.azure.com) do not resolve. So every Developer Community ticket cited here \u2014 298596 (autoCompleteSetBy ignored), 1159179 (Set AutoComplete inconsistent), 1244100 (added as reviewer when declining) \u2014 is TITLE-ONLY evidence; I have not read a single body, vote count, Microsoft response, or status. (3) The GitHub REST API was rate-limited and `gh` is not installed, so GitHub issue evidence came from scraping the HTML pages, which worked for microsoft/azure-devops-node-api#611 (full body plus maintainer resolution) but returned only summaries elsewhere. Anything above resting solely on a Developer Community title should be re-checked by a human with a browser."
          }
        ]
      }
    ]
  },
  {
    "key": "events-identity",
    "survey": {
      "area": "Azure DevOps event delivery and bot identity \u2014 trigger transport and self-suppression for disposition-triggered review",
      "findings": [
        {
          "capability": "Vote-change trigger: is there a dedicated event for a vote?",
          "exists": "partial",
          "detail": "PRECISE ANSWER: there is NO separate eventType for a vote change. A vote change surfaces as `git.pullrequest.updated` (publisherId `tfs`, resource name `pullrequest`). HOWEVER the publisher exposes a server-side filter input `notificationType` whose documented valid values are exactly `PushNotification`, `ReviewersUpdateNotification`, `StatusUpdateNotification`, `ReviewerVoteNotification`. So you CAN create a subscription that fires only on vote changes \u2014 the discrimination happens at subscription-creation time, not in the payload. CRITICAL CONSEQUENCE: the delivered payload is byte-shape-identical for all four notificationTypes and contains NO field naming which notificationType fired. The only in-payload signal is prose in `message.text` / `detailedMessage.text` (e.g. 'Jamal Hartnett marked the pull request as completed'). Therefore JetBridge MUST create four separate subscriptions and give each a DISTINCT receiver URL (path or query discriminator in consumerInputs.url) to recover the notificationType. Do not try to classify by parsing message.text \u2014 it is display prose, localized-looking, and not a documented contract. Other documented filters on this event: `repository` (GUID), `pullrequestCreatedBy` (group), `pullrequestReviewersContains` (group), `branch`. Sample payload `resourceVersion` is `2.0`.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1  (publisherId=tfs, eventType=git.pullrequest.updated, publisherInputs.notificationType=ReviewerVoteNotification)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/events.md",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/subscriptions/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Vote value enum \u2014 the disposition scalar",
          "exists": "native",
          "detail": "Documented verbatim on IdentityRefWithVote.vote (integer int16): `10 - approved`, `5 - approved with suggestions`, `0 - no vote`, `-5 - waiting for author`, `-10 - rejected`. This is a per-reviewer scalar on the PR, not a per-review object. Mapping onto the three JetBridge dispositions is NOT 1:1: 10 -> approved; -5 and -10 both plausibly -> changes-requested (they differ in whether the reviewer blocks); 5 (approved with suggestions) is a genuine FOURTH disposition GitHub does not have and must be decided explicitly (it approves AND leaves work); 0 is 'no vote' and is also what a vote RESET produces. Also documented: `isReapprove` \u2014 'Indicates if this approve vote should still be handled even though vote didn't change.' That means a re-approval can be signalled with an UNCHANGED vote value, so deduplicating a trigger purely on (reviewerId, voteValue) will silently swallow real re-approvals. Also `hasDeclined` (reviewer declined to review) and `isRequired` (policy-required reviewer), and `votedFor[]` (group/team roll-up: groups can be reviewers but cannot vote directly; a member's vote rolls up).",
          "endpoints": [
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Self-identification in the event payload \u2014 'was this event caused by ME?'",
          "exists": "partial",
          "detail": "THE SINGLE MOST IMPORTANT FINDING, and it splits by event type. (a) `ms.vss-code.git-pullrequest-comment-event`: the actor IS present \u2014 `resource.comment.author.id` (identity GUID). Self-suppression by actor GUID works. (b) `git.push`: actor present \u2014 `resource.pushedBy.id`. Note the doc sample shows an MSA form with a suffix (`...@Live.com`) and `uniqueName` of `Windows Live ID\\\\user@host`, so compare on the GUID prefix / use Graph descriptors, not on uniqueName. (c) `git.pullrequest.updated` and `git.pullrequest.merged`: THE ACTOR IS ABSENT. `resource.createdBy` is the PR AUTHOR, not whoever caused the update. `resource.reviewers[]` carries each reviewer's CURRENT `vote` but no indication of who just changed and to what. There is no `updatedBy`, no `actor`, no `changedBy` field anywhere in the documented payload. The only naming of the actor is prose inside `message.text`/`detailedMessage.text`. CONSEQUENCE: for the vote-change trigger \u2014 the exact trigger JetBridge wants \u2014 identity-based self-suppression from the payload alone is IMPOSSIBLE. You must do a follow-up read (see the system-thread finding) to learn who voted.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/events.md",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "System threads as a durable, ordered, queryable review-activity log",
          "exists": "native",
          "detail": "THE STRUCTURAL WIN, and the fix for the missing actor above. Every state change on an Azure Repos PR is materialised as a real comment thread with `commentType: \"system\"`, authored by the collection identity `[DefaultCollection]\\\\Project Collection Service Accounts` (isContainer: true), carrying a typed property bag. From the official 7.1 sample response, `properties.CodeReviewThreadType` takes at least: `VoteUpdate`, `ReviewersUpdate`, `RefUpdate`, `MergeAttempt`. Per type the properties are: VoteUpdate -> `CodeReviewVoteResult` (e.g. \"10\"), `CodeReviewVotedByTfId` (actor GUID), `CodeReviewVotedByDisplayName`; RefUpdate -> `CodeReviewRefName`, `CodeReviewRefNewHeadCommit`, `CodeReviewRefNewCommits`, `CodeReviewRefNewCommitsCount`, `CodeReviewRefUpdatedByTfId`, `CodeReviewRefUpdatedBy`; ReviewersUpdate -> `CodeReviewReviewersUpdatedAddedTfId`, `...RemovedTfId`, `...UpdatedByTfId`, `...NumAdded`, `...NumRemoved`; MergeAttempt -> `CodeReviewMergeCommit`, `CodeReviewMergeStatus`, `CodeReviewSourceCommit`, `CodeReviewTargetCommit`. Each thread carries `id`, `publishedDate`, `lastUpdatedDate`, `isDeleted`. This is a persistent, monotonically-appended, single-GET history of every vote and every source-branch head change with actor GUIDs and commit SHAs attached \u2014 GitHub has no single equivalent read (its nearest analogue, the timeline API, does not carry vote state or push head SHAs in one shape). RECOMMENDED DESIGN: treat the webhook purely as a wake-up signal and derive the sealed review/v1 record from the threads read, which is authoritative, replayable, and carries the actor. CAVEAT: only the four thread types above appear in the official sample; the COMPLETE `CodeReviewThreadType` enum and the complete property-key set are NOT documented anywhere on Microsoft Learn \u2014 treat any additional value as unverified until observed live.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "commentType=system as a self-suppression filter",
          "exists": "absent",
          "detail": "DOES NOT DO WHAT THE QUESTION HOPES. `CommentType` is a documented enum with values `unknown`, `text`, `codeChange`, `system`. `system` marks comments Azure DevOps itself generated (vote/reviewer/ref/merge notices, authored by Project Collection Service Accounts). Your bot's own comments are `text` \u2014 indistinguishable by commentType from a human's. So commentType CANNOT be used to skip your own machine-generated content; it only lets you skip AZURE DEVOPS's machine-generated content. Self-suppression must be by `resource.comment.author.id` GUID. Separately, `codeChange` exists as an enum value but no Microsoft Learn page documents which action produces it \u2014 treat as unverified. DOCS ARE SILENT on whether system threads fire `ms.vss-code.git-pullrequest-comment-event` at all; this must be measured live before relying on either answer.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Native loop suppression (an equivalent of GitHub's GITHUB_TOKEN rule)",
          "exists": "absent",
          "detail": "THERE IS NONE, and the adjacent feature does not substitute. Azure Pipelines has commit-message skip tokens, documented verbatim for Azure Repos Git: `[skip ci]` or `[ci skip]`, `skip-checks: true` or `skip-checks:true`, `[skip azurepipelines]` or `[azurepipelines skip]`, `[skip azpipelines]` or `[azpipelines skip]`, `[skip azp]` or `[azp skip]`, `***NO_CI***` \u2014 placed in the message OR description of any commit in a push. These are scoped to AZURE PIPELINES CI TRIGGERS ONLY. Two documented holes even within Pipelines: (1) 'The pipelines specified by the target branch's build validation policy will run on the merge commit ... regardless if there exist pushed commits whose messages or descriptions contain [skip ci] (or any of its variants)'; (2) 'after you merge the PR, Azure Pipelines will run the CI pipelines triggered by pushes to the target branch, even if some of the merged commits' messages or descriptions contain [skip ci]'. Crucially, NOTHING in the Service Hooks documentation (events, consumers, create-subscription, webhooks, troubleshoot) mentions any skip token, any actor-based suppression, or any loop guard. There is no field on a subscription to exclude an actor. Azure DevOps will happily deliver `ms.vss-code.git-pullrequest-comment-event` for the bot's own reply and `git.push` for the bot's own push. JetBridge must implement suppression itself, by actor GUID, on the receiving side.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/pipelines/repos/azure-repos-git?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/pipelines/build/triggers?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops"
          ]
        },
        {
          "capability": "Subscription-level filtering \u2014 can you filter by vote value or by reviewer?",
          "exists": "partial",
          "detail": "BY VOTE VALUE: NO. There is no publisherInput accepting a vote integer on any event. `notificationType=ReviewerVoteNotification` narrows to 'a vote changed', never to 'a vote became 10'. BY REVIEWER: NO, not by a specific identity. The only reviewer-shaped filter is `pullrequestReviewersContains` \u2014 'Include only events for pull requests with reviewers in a specific group' \u2014 which is (a) group-granularity, not identity, and (b) a predicate on the PR's reviewer LIST, not on the actor who caused this event. Likewise `pullrequestCreatedBy` filters by the PR author's group. Complete documented filter surface per event: git.push -> branch, pushedBy (group), repository; git.pullrequest.created / .updated / .merged -> repository, pullrequestCreatedBy, pullrequestReviewersContains, branch, plus notificationType (updated only) and mergeResult (merged only: Succeeded, Unsuccessful, Conflicts, Failure, RejectedByPolicy); ms.vss-code.git-pullrequest-comment-event -> repository, branch ONLY. CONSEQUENCE: the comment event cannot be filtered by author at the subscription level at all, so the bot's own comments WILL be delivered and must be dropped by the receiver.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/events.md"
          ]
        },
        {
          "capability": "PR comment event payload shape",
          "exists": "partial",
          "detail": "`ms.vss-code.git-pullrequest-comment-event`, publisherId `tfs`, `resourceVersion: \"1.0\"`. resource = { comment, pullRequest }. comment carries: `id` (comment id, unique within the PR), `parentCommentId`, `author` (IdentityRef with GUID), `content`, `publishedDate`, `lastUpdatedDate`, `lastContentUpdatedDate`, `commentType`, `_links{self, repository, threads}`. THREE GAPS THAT MATTER FOR COMMENT ANCHORING: (1) there is NO `threadId` scalar field \u2014 the thread id is only recoverable by parsing `_links.threads.href` (or `_links.self.href`, which is .../pullRequests/{n}/threads/{threadId}/comments/{commentId}). (2) there is NO `threadContext` \u2014 no filePath, no line/offset, no iteration context. The webhook tells you a comment happened, not where in the diff. (3) there is no discrete `action` field distinguishing created / edited / deleted; the doc's own sample is an EDIT and says so only in `message.text` ('Jamal Hartnett has edited a pull request comment'). So the comment webhook is a wake-up signal at best; anchoring data must come from the threads read.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1",
            "GET .../pullRequests/{pullRequestId}/threads/{threadId}/comments?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/events.md",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Delivery guarantees: retries, probation, auto-disable",
          "exists": "partial",
          "detail": "DOCUMENTED IN DETAIL, and the shape is NOT plain at-least-once. Three failure classes: (1) TERMINAL \u2014 HTTP 410 Gone is the only terminal code; 'the subscription is automatically disabled no matter its prior state'. (2) TRANSIENT \u2014 408, 502, 503, 504; Azure DevOps 'attempts to resend the notification up to eight times, with an increasing delay between each attempt', delays from 1s to a 60s max backoff, ~183s cumulative by the eighth retry. (3) ENDURING \u2014 every other HTTP error; puts the subscription ON PROBATION. While on probation: up to seven further retry attempts with backoff from 20 minutes to 15 hours, up to ~36 hours total \u2014 AND, quoting the doc, while on probation 'any new events are lost'. A success during probation restores the subscription to enabled; otherwise it becomes `disabledBySystem`. THE OPERATIONAL HAZARD FOR JETBRIDGE: a receiver returning 500 for 20 minutes does not queue events \u2014 it silently DROPS every review that lands in the probation window, and the drop is invisible to the reviewer. SubscriptionStatus enum: `enabled`, `onProbation`, `disabledByUser`, `disabledBySystem`, `disabledByInactiveIdentity`. The Subscription object exposes `probationRetries` and `lastProbationRetryDate` so a health check can detect probation before events are lost.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1",
            "GET https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/troubleshoot?view=azure-devops",
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/troubleshoot.md",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/subscriptions/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Idempotency key / delivery id header",
          "exists": "partial",
          "detail": "THE DOCS ARE SILENT ON HTTP HEADERS. No Microsoft Learn page (webhooks consumer page, events page, consumers page, create-subscription page) documents ANY header sent with a service-hook webhook POST \u2014 there is no documented delivery-id header, no signature header, no subscription-id header. Do not build on any header name; several third-party blogs assert `x-vss-subscriptionid` but that is community folklore, not documentation. WHAT IS DOCUMENTED: the payload's top-level `id` field is a UUID, and the Event contract defines it as 'Gets or sets the unique identifier of this event'. That is the correct dedup key. INFERRED (not stated): the same event id is re-sent on retry, because NotificationDetails models `requestAttempts` ('Number of requests attempted to be sent to the consumer') as a counter on ONE notification bound to ONE `eventId` \u2014 i.e. retries are re-sends of the same notification, not new events. Treat as inferred and verify live. RECOMMENDATION: dedup on payload `id`, and additionally on a natural key (pullRequestId + threadId + commentId + lastUpdatedDate, or pullRequestId + reviewerId + vote + system-thread publishedDate) so a re-subscription or a resourceVersion change cannot replay work.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/notificationsquery?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "inferred",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops"
          ]
        },
        {
          "capability": "Delivery ordering guarantee",
          "exists": "absent",
          "detail": "MICROSOFT LEARN IS SILENT. No page states that service-hook deliveries are ordered, and none states that they are unordered. There is no sequence number, no per-PR partition key, and no documented ordering semantics anywhere in the Service Hooks documentation. Assertions that ordering 'is not guaranteed' circulate on third-party blogs but are not sourced to Microsoft. Because it is undocumented in BOTH directions, JetBridge must be order-independent: never derive PR state by applying webhook deltas in arrival order. The safe pattern is signal-then-read \u2014 the webhook says 'PR N changed', and the sealed record is built from a fresh authoritative read of PR + threads + iterations, which is self-consistent regardless of delivery order. This also happens to be the pattern that survives the probation-window event loss described above.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "uncertain",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/troubleshoot?view=azure-devops"
          ]
        },
        {
          "capability": "Delivery history / replay API (the recovery path for lost events)",
          "exists": "native",
          "detail": "There IS a first-class notification-history API and it is stronger than the webhook itself. POST /_apis/hooks/notificationsquery accepts `subscriptionIds[]`, `publisherId`, `minCreatedDate`, `maxCreatedDate`, `maxResults`, `maxResultsPerSubscription`, `status` (NotificationStatus: `queued`, `processing`, `requestInProgress`, `completed`), `resultType` (NotificationResult: `pending`, `succeeded`, `failed`, `filtered`), and `includeDetails` ('If true, we will return all notification history for the query provided; otherwise, the summary is returned'). Each Notification returns `id`, `eventId`, `subscriptionId`, `subscriberId`, `createdDate`, `modifiedDate`, `status`, `result`, and NotificationDetails containing `queuedDate`, `dequeuedDate`, `processedDate`, `completedDate`, `requestAttempts`, `requestDuration`, `request`, `response`, `errorMessage`, `errorDetail`, AND the full `event` object (eventType, resource, resourceVersion, resourceContainers, message, detailedMessage). That means the ORIGINAL EVENT BODY is recoverable server-side after the fact \u2014 a genuine recovery path for the probation-window loss, and a time-bounded (minCreatedDate/maxCreatedDate) catch-up cursor. Note `result: filtered` explicitly tells you an event was evaluated and excluded by your publisherInputs, which is a useful trigger-design debugging signal GitHub does not expose. Retention of notification history is NOT documented \u2014 do not assume it is long.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/notificationsquery?api-version=7.1",
            "GET https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}/notifications?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Consumers: webhook vs Azure Service Bus vs Azure Storage Queue",
          "exists": "native",
          "detail": "Three machine-consumable consumers, all documented as supporting ALL events. (1) `webHooks` / action `httpRequest`: consumerInputs `url` (required), `basicAuthCredentials` (marked REQUIRED in the consumers table), `httpHeaders` (key:value per line), `acceptUntrustedCerts`, plus payload-size knobs. Documented restriction: 'Webhooks can't target localhost (loopback) or special range IPv4/IPv6 addresses' \u2014 so no in-cluster shortcut; the receiver needs a public HTTPS endpoint (and Azure DevOps outbound IPs allow-listed if behind a VPN). (2) `azureServiceBus` / `serviceBusQueueSend` or `serviceBusTopicSend`: queue/topic name, SAS connection string OR an Entra service connection (`AuthenticationMechanismInputId`, `ServiceConnectionInputId`, `ServiceBusHostNameInputId`). CRITICAL GO TRAP: `bypassSerializer` \u2014 'Send as nonserialized string ... Select this setting when the receiver isn't a .NET client'. A Go consumer that omits bypassSerializer receives .NET-serialized strings, not JSON. (3) `azureStorageQueue` / `enqueue`: `accountName`, `accountKey` or service connection, `queueName` (lowercase only), `visiTimeout`, `ttl` (max 604800s = 7 days). THE ARCHITECTURAL ADVANTAGE: routing to a durable Service Bus queue or Storage queue converts the fragile push-with-probation model into a broker-buffered pull model \u2014 the queue absorbs receiver downtime, so JetBridge stops losing events during an outage. GitHub offers webhooks only. All three consumers carry `resourceDetailsToSend` (All / Minimal / None), `messagesToSend`, `detailedMessagesToSend` to trim payload size; the docs note Minimal/None are also a security posture \u2014 'The caller must call back into Azure DevOps Services and go through normal security and permission checks to get more details', which pairs naturally with the signal-then-read design.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1  (consumerId: webHooks|azureServiceBus|azureStorageQueue)"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops"
          ]
        },
        {
          "capability": "Webhook payload authentication (HMAC signature)",
          "exists": "absent",
          "detail": "THERE IS NO PAYLOAD SIGNING. Azure DevOps has no equivalent of GitHub's `X-Hub-Signature-256`. The only documented ways to authenticate the sender are `basicAuthCredentials` (HTTP basic, and the docs insist 'You must use HTTPS for basic authentication on a webhook') and arbitrary `httpHeaders` carrying a shared secret. Both are stored on the subscription, and the consumers doc warns explicitly about httpHeaders: 'These values are viewable by anyone who has access to the service hook subscription.' So the shared secret is readable by every Project Collection Administrator and anyone with subscription read access \u2014 it is not a strong bearer secret. PRACTICAL CONSEQUENCE: treat the webhook body as untrusted input. Never seal a review/v1 record straight from the POST body; use the POST only to learn 'PR N in repo R may have changed', then re-read PR/threads/iterations over an authenticated API call with the platform's own credential. That happens to be the same conclusion the ordering and probation findings reach independently.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops"
          ]
        },
        {
          "capability": "Service Hooks REST API for per-repository/per-project subscription management",
          "exists": "native",
          "detail": "Fully programmatic. POST /_apis/hooks/subscriptions?api-version=7.1 with body { publisherId, eventType, resourceVersion, consumerId, consumerActionId, publisherInputs{}, consumerInputs{} }. Scoping is via publisherInputs: `projectId` (project GUID, obtained from the Core Projects API) and, for git events, `repository` (repository GUID) and `branch`. Response echoes server-injected `hostId` and `tfsSubscriptionId`. Documented behaviour on event: 'When an event occurs, all enabled subscriptions in the project are evaluated. Then the consumer action is performed for all matching subscriptions' \u2014 so N subscriptions matching one event produce N deliveries; the four-notificationType design above therefore costs four deliveries only when four filters match, which they cannot simultaneously. PIN THE PAYLOAD CONTRACT: 'If you don't specify a resource version, the latest version, latest released, is used. To help ensure a consistent event payload over time, always specify a resource version.' This is a real advantage over GitHub, which has no per-subscription payload versioning \u2014 JetBridge should pin resourceVersion explicitly (git.pullrequest.updated samples at `2.0`; git.pullrequest.merged at `1.0-preview.1`; the comment event at `1.0`) so Microsoft cannot change the sealed record's input shape underneath a running deployment. PERMISSIONS \u2014 THE DOCS DISAGREE WITH THEMSELVES: the Webhooks how-to states the prerequisite is 'Member of the Project Collection Administrators group', while the programmatic create-subscription page states only 'Project member'. Assume PCA is required for org/project-wide subscriptions and verify empirically for the identity you choose; this is a real onboarding blocker if the bot identity is not a PCA. OAuth scope for creating git subscriptions: `vso.code`.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1",
            "GET https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1",
            "PUT https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}?api-version=7.1",
            "DELETE https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/create-subscription?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/subscriptions/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops"
          ]
        },
        {
          "capability": "Polling-friendly monotonic cursor over PR review activity",
          "exists": "partial",
          "detail": "NO SERVER-SIDE DELTA FILTER EXISTS on the threads endpoint. GET .../pullRequests/{id}/threads?api-version=7.1 documents exactly two optional query parameters \u2014 `$iteration` and `$baseIteration` \u2014 and both are for repositioning thread anchors against a diff, not for filtering by time. There is no `since`, no `$filter`, no `lastUpdatedDate` predicate, no continuationToken. WHAT YOU DO GET: every thread returns `publishedDate` and `lastUpdatedDate`, and every comment returns `publishedDate`, `lastUpdatedDate` and `lastContentUpdatedDate`. So a reliable delta is achievable but only CLIENT-SIDE: fetch all threads for the PR and compare against the high-water mark you stored. That is O(all threads) per poll per PR, which is fine at JetBridge's scale (one PR per agent run) and is exactly the read you want anyway for building the sealed record. Combined with the system threads (VoteUpdate / RefUpdate / MergeAttempt with their publishedDate), one threads GET yields a complete, ordered, actor-attributed activity log with a usable water mark \u2014 which makes polling a viable PRIMARY transport with webhooks as a latency optimisation, not the other way round.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Audit API as a polling cursor",
          "exists": "absent",
          "detail": "RULED OUT \u2014 do not design around it. Four disqualifying facts, all documented: (1) 'Auditing isn't available for on-premises deployments of Azure DevOps Server' \u2014 so it cannot be part of a forge-portable design. (2) 'Auditing is only available for organizations backed by Microsoft Entra ID', and it is OFF by default, requiring a PCA to enable it. (3) 'Auditing is currently in public preview.' (4) 'Audit events are stored for 90 days, after which they're deleted', and scope is ORGANIZATION-level administrative activity \u2014 'Permissions changes, Deleted resources, Branch policy changes, Log access and downloads' \u2014 not PR comment/vote traffic. The Audit Log Query API does expose a real cursor (GET https://auditservice.dev.azure.com/{organization}/_apis/audit/auditlog?api-version=7.1-preview.1 with startTime, endTime, batchSize, continuationToken, and a response carrying continuationToken + hasMore; the continuationToken is a composite `{tick};{actorId};{correlationId}` string, i.e. effectively a monotonic position), and DecoratedAuditLogEntry does carry `actorClientId` ('The Actor's Client Id (if actor is a service principal)') which would be ideal for attribution \u2014 but none of that helps because PR review activity is not in the audit stream.",
          "endpoints": [
            "GET https://auditservice.dev.azure.com/{organization}/_apis/audit/auditlog?api-version=7.1-preview.1",
            "GET https://auditservice.dev.azure.com/{organization}/_apis/audit/actions"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/organizations/audit/azure-devops-auditing?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/audit/audit-log/query?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Notification service (bell/email) as an alternative transport",
          "exists": "absent",
          "detail": "NOT A MACHINE TRANSPORT \u2014 ruled out. The Notification service (POST /_apis/notification/subscriptions?api-version=7.1) is a separate subsystem from Service Hooks and delivers to human channels: the documented channel type in every sample is `EmailHtml` (with optional custom address). Its ISubscriptionChannel contract is open-typed but no HTTP/webhook channel is documented, and its own SubscriptionDiagnostics contract carries a field described as 'Diagnostics settings for retaining delivery results. Used for Service Hooks subscriptions' \u2014 i.e. Service Hooks is the machine path and Notification is the human path. Its filter model IS richer than Service Hooks (type `Expression` with criteria.clauses of {fieldName, operator, value, logicalOperator}, plus type `Artifact` with artifactType `PullRequestId` and an artifactId of `{projectId}/{repositoryId}/{pullRequestId}` \u2014 genuinely per-PR subscription granularity, which Service Hooks cannot express). But without a webhook channel it cannot carry the trigger. Worth noting only as evidence that the underlying event bus HAS per-PR and field-level filtering that Service Hooks does not surface.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/notification/subscriptions?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/notification/subscriptions/create?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Bot identity: which principal should JetBridge use",
          "exists": "partial",
          "detail": "CLOUD ANSWER: a Microsoft Entra ID SERVICE PRINCIPAL (app registration) or MANAGED IDENTITY. The Learn article is published without a preview banner and applies to 'Azure DevOps Services'. Documented capability list, verbatim: CAN 'Generate Microsoft Entra tokens for API access', 'Access Azure DevOps resources with proper permissions', 'Join security groups and teams'; CANNOT 'Create PATs or Secure Shell keys', 'Sign in interactively or access via a web UI', 'Create or own organizations', 'Support Azure DevOps OAuth flows'. Tokens expire hourly (vs PATs up to a year). Onboarding gotchas that WILL bite: it must be EXPLICITLY added to the org by a PCA/PA ('Service principals don't automatically appear in Azure DevOps. Adding a service principal to a Microsoft Entra security group doesn't grant access'); use the SERVICE PRINCIPAL's object ID from Enterprise Applications, NOT the app registration's object ID; it needs at least a BASIC license \u2014 the documented error 'The Git repository with name or identifier doesn't exist or you don't have permissions' resolves to 'Stakeholder licenses don't provide repository access'; group-based licensing rules do NOT apply, licences must be assigned directly; and it costs a full licence in EVERY org with no multi-org discount. Token audience for Azure DevOps: scope `https://app.vssps.visualstudio.com/.default` (or the resource GUID `499b84ac-1321-427f-aa17-267ca6975798`). Rate limits are the same as users. Nothing in the docs restricts a service principal from commenting, voting, or pushing \u2014 those are ordinary Azure DevOps permissions on the repo \u2014 but that specific combination is not called out by name, so verify vote-casting as a service principal live.",
          "endpoints": [
            "POST https://login.microsoftonline.com/{tenant-id}/oauth2/v2.0/token  (grant_type=client_credentials, scope=https://app.vssps.visualstudio.com/.default)",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/memberentitlementmanagement/service-principal-entitlements?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/graph/service-principals?view=azure-devops-rest-7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops"
          ]
        },
        {
          "capability": "Bot identity on Azure DevOps Server (on-prem)",
          "exists": "partial",
          "detail": "SERVICE PRINCIPALS AND MANAGED IDENTITIES ARE NOT AVAILABLE ON-PREM. The Learn article's applies-to line reads exactly 'Azure DevOps Services' and its moniker list is `azure-devops` only (the sole other listed version is tfs-2018, which is a redirect stub, not support). Azure DevOps OAuth is likewise Services-only AND deprecated. So the on-prem bot identity is a PERSONAL ACCESS TOKEN belonging to a dedicated Windows/AD service account \u2014 which means: a long-lived secret, manual rotation, a human-shaped identity in the reviewer list, and an identity GUID that must be discovered per-deployment rather than being a stable app id. THE PORTABILITY CONSEQUENCE FOR JETBRIDGE: the platform must abstract 'the bot's identity GUID' as configuration resolved at runtime (via the Graph/Identities API for the configured principal), not as a compile-time notion of 'service principal', because self-suppression compares against that GUID and it is the ONLY self-suppression mechanism available on either deployment. Everything else in this brief \u2014 Service Hooks, all six git events, the notificationType filter, the threads API with system threads, all three consumers, the notificationsquery history API \u2014 IS available on Azure DevOps Server (docs carry the azure-devops-2022 / azure-devops-server monikers). Only five pipeline events and the three Advanced Security events are marked Services-only, and none of those are in the review loop. Practical on-prem caveat (inferred, not documented): the azureServiceBus and azureStorageQueue consumers require outbound connectivity from the on-prem application tier to Azure, which many air-gapped installs will not have \u2014 so on-prem realistically means webhook or polling.",
          "endpoints": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/subscriptions/create?view=azure-devops-server-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-server-rest-7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/create-subscription?view=azure-devops"
          ]
        },
        {
          "capability": "Azure DevOps OAuth apps as the integration identity",
          "exists": "absent",
          "detail": "DO NOT BUILD ON IT. Quoted verbatim from Learn: 'Azure DevOps OAuth is deprecated and scheduled for removal in 2026. This documentation is for existing Azure DevOps OAuth apps only. New app registrations are no longer accepted as of April 2025.' And: 'Existing apps continue to function until Azure DevOps OAuth is fully deprecated in 2026.' Microsoft has not published the specific 2026 date. Replacement is Microsoft Entra ID OAuth. Two further reasons it was never right for this loop even before deprecation: it is a DELEGATED user flow (acts on behalf of a signed-in user, so the bot's comments would be attributed to whichever human authorised it \u2014 fatal for self-suppression by actor GUID), its secrets expire every 60 days, and service principals explicitly 'do not support Azure DevOps OAuth flows'. It is also Services-only. Note the separate org policy that can break Entra/OAuth callers silently: if 'Third-party application access through OAuth' is disabled at https://dev.azure.com/{org}/_settings/organizationPolicy, the authorisation flow succeeds but API calls fail with TF400813 \u2014 worth a preflight check in JetBridge's forge connection test.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/azure-devops-oauth?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops"
          ]
        },
        {
          "capability": "Project Collection Build Service identity \u2014 is it the right bot?",
          "exists": "native",
          "detail": "IT EXISTS BUT IS THE WRONG CHOICE. Documented precisely: Azure DevOps has two built-in scoped build identities \u2014 collection-scoped `Project Collection Build Service ({OrgName})` and project-scoped `{Project Name} Build Service ({Org Name})`. Which one a pipeline job gets is determined by the 'Limit job authorization scope to current project' settings; the collection-scoped one is the default unless restricted. They are real users in the org, addressable in permission UIs, and back the dynamically-issued job access token (System.AccessToken). WHY NOT TO USE IT AS JETBRIDGE'S REVIEWER BOT: (1) it is SHARED \u2014 every pipeline in the collection (or project) already acts as this identity, so an actor-GUID self-suppression check would also suppress legitimate events caused by unrelated pipelines, and conversely JetBridge could not distinguish its own writes from another pipeline's. (2) The project-scoped variant 'will only be created after you run the pipeline once', so its GUID is not stable configuration you can provision ahead of time. (3) Its permissions are entangled with CI needs and with the 'Protect access to repositories in YAML pipelines' setting. (4) It has no lifecycle you control. RECOMMENDATION: provision a DEDICATED principal (Entra service principal on Services, dedicated PAT-backed account on Server) whose identity GUID exists solely to be the JetBridge agent, so that 'author.id == our bot' is an exact, unambiguous self-suppression predicate.",
          "endpoints": [],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/pipelines/process/access-tokens?view=azure-devops"
          ]
        },
        {
          "capability": "Complete git/PR service hook event inventory",
          "exists": "native",
          "detail": "EXHAUSTIVE list of code-area events (publisherId `tfs` for all): `tfvc.checkin` (TFVC, irrelevant here); `git.push` (resource `push`; trigger 'Code is pushed to a Git repository'; carries commits[] with author/committer, and `pushedBy`); `git.pullrequest.created` (resource `pullrequest`); `git.pullrequest.updated` (trigger, verbatim: 'A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch'); `git.pullrequest.merged` (trigger: 'A pull request merge is ATTEMPTED' \u2014 note attempted, not completed; hence the mergeResult filter with Succeeded/Unsuccessful/Conflicts/Failure/RejectedByPolicy); `ms.vss-code.git-pullrequest-comment-event` (trigger: 'A pull request is commented on'). THAT IS ALL SIX. There is NO event for: a PR status/check being posted, a comment thread's STATUS changing (active -> fixed/wontFix/closed/byDesign/pending), a PR being abandoned or reactivated as a distinct event, a policy evaluation completing, an iteration being created, or a draft PR being published. Note the asymmetry that matters for the loop: a push to the PR's source branch produces BOTH a `git.push` (repo-level, with no pullRequestId anywhere in its payload) AND a `git.pullrequest.updated` with notificationType PushNotification (PR-level, but with no indication of WHICH commits arrived). Neither alone gives you 'PR N advanced to head SHA X' \u2014 the RefUpdate system thread does, via CodeReviewRefNewHeadCommit.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops",
            "https://github.com/MicrosoftDocs/azure-devops-docs/blob/main/docs/service-hooks/events.md"
          ]
        },
        {
          "capability": "OAuth scopes / PAT scopes needed for the review loop",
          "exists": "native",
          "detail": "Documented per-operation. Creating git service-hook subscriptions: `vso.code` ('Also grants the ability to ... get notified about version control events via service hooks'). Reading PR threads: `vso.code` OR `vso.threads_full` ('Grants the ability to read and write to pull request comment threads'). Casting a vote / adding a reviewer: `vso.code_write` ('Grants the ability to read, update, and delete source code ... and to create and manage pull requests and code reviews and to receive notifications about version control events via service hooks'). Querying service-hook notification history: `vso.code` (for code-publisher notifications). Audit log: `vso.auditlog`. Notification-service subscriptions: `vso.notification_write`. Microsoft Learn states Entra ID OAuth and Azure DevOps OAuth 'use the same scope definitions', so these scope names carry over to the Entra path.",
          "endpoints": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/subscriptions/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/azure-devops-oauth?view=azure-devops"
          ]
        }
      ],
      "absent": [
        "A 'submitted review' object. Azure DevOps has NO review noun of any kind \u2014 no submit action, no submitted_at, no draft-review batching, no pending-comment staging. There is only (a) a flat set of comment threads, each comment published immediately on its own, and (b) a single mutable integer vote per reviewer. The GitHub trigger unit 'a review with a disposition' does not exist and cannot be read back.",
        "Any native loop suppression. There is no equivalent of GitHub's rule that GITHUB_TOKEN-caused events do not trigger workflows. Azure Pipelines' commit-message skip tokens ([skip ci], [ci skip], skip-checks: true, [skip azurepipelines], [skip azpipelines], [skip azp], ***NO_CI***) apply only to Azure Pipelines CI triggers, are documented not to suppress PR build-validation policies or post-merge CI, and have no effect whatsoever on Service Hooks.",
        "An actor field on git.pullrequest.updated and git.pullrequest.merged. No updatedBy / actor / changedBy / votedBy anywhere in the documented payload; resource.createdBy is the PR author, not the cause of the event. The actor is named only in free-text message.text.",
        "A vote-value filter on any subscription. notificationType=ReviewerVoteNotification narrows to 'some vote changed', never to a value or a direction.",
        "A specific-reviewer filter. pullrequestReviewersContains matches 'reviewers in a specific GROUP' and is a predicate on the PR's reviewer list, not on the event's actor. There is no identity-level filter.",
        "Any author filter on ms.vss-code.git-pullrequest-comment-event. Its only documented settings are repository and branch, so the bot's own comments are always delivered.",
        "HMAC / signature verification of webhook payloads. No X-Hub-Signature equivalent. Only HTTP basic auth and custom headers, both of which the docs state are viewable by anyone with access to the subscription.",
        "Any documented HTTP header on service-hook webhook deliveries. No delivery-id header, no subscription-id header, no event-type header is documented on any Microsoft Learn page. The payload's top-level `id` UUID is the only documented per-event identifier.",
        "A documented ordering guarantee. Microsoft Learn is silent in both directions \u2014 it neither promises nor disclaims ordering, and there is no sequence number or partition key in the payload.",
        "At-least-once delivery across all failure modes. Documented: while a subscription is on probation (up to ~36 hours after any non-408/502/503/504 error) 'any new events are lost'. Events in that window are dropped, not queued.",
        "A threadId scalar and a threadContext (file path / line / iteration) on the comment webhook payload. The thread id is only recoverable by parsing _links.threads.href or _links.self.href; the diff anchor is absent entirely.",
        "A discrete action field on the comment event distinguishing created / edited / deleted. Only prose in message.text.",
        "A server-side delta filter on the PR threads endpoint. The only documented query parameters are $iteration and $baseIteration; there is no since / $filter / lastUpdatedDate predicate and no continuationToken.",
        "Service principals and managed identities on Azure DevOps Server. The feature is documented as Azure DevOps Services only; on-prem must use a PAT on a dedicated service account.",
        "Auditing on Azure DevOps Server, and audit coverage of PR review activity on Services. Auditing is Services-only, Entra-backed orgs only, public preview, off by default, 90-day retention, and scoped to organization administrative activity \u2014 not PR comments, votes, or pushes.",
        "A webhook/HTTP delivery channel on the Notification (bell) service. Documented channels are email; the machine path is Service Hooks.",
        "Service hook events for: PR status/check posted, comment-thread status change (active -> fixed/wontFix/closed/byDesign/pending), PR abandoned or reactivated as a distinct event, policy evaluation completed, iteration created, draft PR published.",
        "Documented retention period for service-hook notification history (the notificationsquery results), so its usefulness as a replay window is unbounded-unknown."
      ],
      "notes": "HEADLINE FOR THE DESIGN DECISION\n\nAzure DevOps is BETTER than GitHub at the trigger and WORSE at everything downstream of it, and the two facts cancel into one recommendation: subscribe narrowly, then read authoritatively.\n\nBetter at the trigger. GitHub forces you to poll /pulls/{n}/reviews and infer disposition from a review object that the agent's own reply pollutes. Azure DevOps lets you create a subscription that fires ONLY on `notificationType=ReviewerVoteNotification`, and the vote is a documented five-valued scalar (10/5/0/-5/-10) that already IS the disposition. The self-trigger pathology proven live on GitHub \u2014 the agent's reply being filed as a new submitted review \u2014 has no analogue here, because casting a vote and writing a comment are different operations on different endpoints. The agent can reply to a hundred comments without touching any reviewer's vote, so a vote-filtered subscription is structurally immune to the loop that broke the GitHub design. That is the single strongest argument for the Azure DevOps mapping.\n\nWorse everywhere downstream. The vote event does not say who voted, what they voted, or which notificationType fired. The comment event does not say which thread, which file, which line, or whether it was a create or an edit. Neither can be filtered by author. There is no payload signature. And events are silently DROPPED (not queued) for up to 36 hours whenever the receiver returns anything outside 408/502/503/504.\n\nTHE ARCHITECTURE THESE FACTS FORCE\n\nFour independent findings \u2014 the missing actor on pullrequest.updated, the undocumented ordering, the probation-window loss, and the unsigned payload \u2014 all converge on the same shape: the webhook must be a WAKE-UP SIGNAL ONLY, carrying no authority. Set `resourceDetailsToSend: Minimal` (the docs endorse this explicitly as a security posture), extract only { repositoryId, pullRequestId }, and rebuild the sealed review/v1 record from an authenticated read of PR + threads + iterations. That read is self-consistent regardless of delivery order, survives dropped events, and cannot be forged by whoever can POST to the endpoint.\n\nConcretely:\n1. Create four subscriptions on git.pullrequest.updated, one per notificationType, each with a DISTINCT consumerInputs.url path \u2014 that is the only way to recover which notificationType fired, since the payload does not carry it.\n2. Pin resourceVersion on every subscription. Azure DevOps lets you version-pin the payload contract per subscription; GitHub does not. Unpinned means Microsoft can change the sealed record's input shape under a running deployment.\n3. Route through Azure Service Bus or Storage Queue rather than raw webhook where the deployment allows it. This is the ONLY way to eliminate the probation-window data loss, and it is a capability GitHub simply does not have. If you use Service Bus from Go, you MUST set `bypassSerializer` \u2014 otherwise you receive .NET-serialized strings, not JSON.\n4. Poll as the floor, not the ceiling. Because one threads GET yields the complete ordered activity log, polling is a fully sufficient primary transport; webhooks are a latency optimisation. Given the delivery hazards, treat it that way rather than the reverse.\n\nTHE SLEEPER ASSET: SYSTEM THREADS\n\nThe highest-value discovery in this research is not an event \u2014 it is that Azure DevOps materialises every review state change as a durable `commentType: \"system\"` thread with a typed property bag. `CodeReviewThreadType: VoteUpdate` carries `CodeReviewVoteResult` + `CodeReviewVotedByTfId`; `RefUpdate` carries `CodeReviewRefNewHeadCommit` + `CodeReviewRefUpdatedByTfId` + commit list; `MergeAttempt` carries source/target/merge commits and status; `ReviewersUpdate` carries added/removed identities. One GET returns an append-only, timestamped, actor-attributed history of every vote and every source-branch advance. This supplies exactly the actor identity the vote webhook omits, AND it is a natural revision spine \u2014 RefUpdate entries are effectively the forge's own record of the ordered series of revisions, with the base/head SHAs already recorded. GitHub has no single read that returns this. Build the review/v1 projection from these threads and the missing-actor problem, the ordering problem, and part of the revision-identity problem all dissolve together.\n\nCaveat and required verification: the four CodeReviewThreadType values above are the ones that appear in the official 7.1 sample response. The COMPLETE enum and the complete property-key set are undocumented anywhere on Microsoft Learn. Enumerate them empirically against a real PR before making them load-bearing, and fail closed on unknown values rather than silently ignoring them.\n\nSELF-SUPPRESSION: THE ONE MECHANISM THAT WORKS\n\nThere is no native loop suppression and no adjacent feature that substitutes. The commit-message skip tokens belong to Azure Pipelines and have no effect on Service Hooks \u2014 and even inside Pipelines they are documented not to suppress PR build-validation policies or post-merge CI. `commentType=system` does not help either: it identifies AZURE DEVOPS's machine content, not yours, and your bot's comments are `text` exactly like a human's.\n\nThe only mechanism is actor-GUID comparison, and it must be built:\n- Comments: compare `resource.comment.author.id` against the bot's identity GUID. Works directly from the payload.\n- Pushes: compare `resource.pushedBy.id`, matching on the GUID prefix \u2014 the doc sample shows MSA identities with an `@Live.com` suffix and a `Windows Live ID\\\\...` uniqueName, so never compare on uniqueName.\n- Votes / PR updates: the payload cannot tell you. Resolve the actor from the VoteUpdate system thread's `CodeReviewVotedByTfId`.\n\nDesign implication: 'the bot's identity GUID' must be RUNTIME CONFIGURATION resolved per deployment (via the Graph/Identities API for the configured principal), never a compile-time notion of 'service principal'. On Services the principal is an Entra service principal; on Server it is a PAT-backed AD service account. Same predicate, different provisioning. Do not use Project Collection Build Service \u2014 it is shared with every pipeline in the collection, so 'was this me?' becomes unanswerable, and the project-scoped variant does not even exist until a pipeline has run once.\n\nTHREE THINGS THAT WILL BITE DURING IMPLEMENTATION\n\n`isReapprove` \u2014 documented as 'Indicates if this approve vote should still be handled even though vote didn't change.' A reviewer can re-approve with an UNCHANGED value. Deduplicating triggers on (reviewerId, voteValue) will silently swallow real re-approvals. Dedupe on the VoteUpdate thread's publishedDate instead.\n\nVote 5 = 'approved with suggestions' is a genuine FOURTH disposition with no GitHub equivalent. It approves AND leaves work. The three-way routing model must decide this explicitly \u2014 it is not a corner case, it is a commonly-used vote. Similarly -5 ('waiting for author') and -10 ('rejected') both map toward changes-requested but differ in whether the reviewer blocks the merge; collapsing them loses real signal.\n\nPermissions documentation contradicts itself: the Webhooks how-to requires 'Member of the Project Collection Administrators group', while the programmatic create-subscription page says only 'Project member'. Assume PCA and verify empirically for your chosen principal \u2014 this is an onboarding blocker, not a detail. Also preflight the org policy 'Third-party application access through OAuth': when disabled, auth flows succeed but API calls fail with TF400813.\n\nDOC GAPS REQUIRING LIVE MEASUREMENT (do not guess these)\n\n1. Do system threads (commentType=system) fire ms.vss-code.git-pullrequest-comment-event? Docs silent in both directions. If they do, every vote produces BOTH a pullrequest.updated and a comment event, and the comment event's author is the collection service account \u2014 which would need its own suppression rule.\n2. Is the payload's top-level `id` UUID stable across retries? Inferred yes from NotificationDetails.requestAttempts modelling retries as re-sends of one notification bound to one eventId, but never stated.\n3. What HTTP headers actually arrive? Entirely undocumented. Third-party blogs claim `x-vss-subscriptionid`; that is folklore, not contract. Observe, but do not depend on it \u2014 use distinct URLs per subscription instead, which is contractual.\n4. Delivery ordering under concurrent activity on one PR.\n5. Retention window of notificationsquery history \u2014 undocumented, and it bounds the replay/recovery path.\n6. Whether an Entra service principal can cast a vote and push. Nothing forbids it (these are ordinary repo permissions) but the combination is never called out by name.\n\nON-PREM PORTABILITY (better than expected)\n\nEverything load-bearing survives on Azure DevOps Server: Service Hooks, all six git events, the notificationType filter, the threads API with system threads, all three consumer types, and the notificationsquery history API all carry azure-devops-2022 / azure-devops-server monikers. Only pipeline and Advanced Security events are Services-only, and none are in the review loop. The two real on-prem divergences are identity (PAT instead of service principal) and, inferred rather than documented, that Service Bus / Storage Queue consumers need outbound Azure connectivity many installs will not have \u2014 so on-prem realistically means webhook or polling, which is another argument for making polling the primary transport rather than a fallback."
    },
    "checks": [
      {
        "area": "events-identity",
        "claims_checked": [
          {
            "claim": "Claim 1: There is NO separate service-hook eventType for a reviewer vote; a vote surfaces as `git.pullrequest.updated` (publisherId `tfs`, resource name `pullrequest`).",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops (source: MicrosoftDocs/azure-devops-docs/docs/service-hooks/events.md, ms.date 01/07/2026). The Code section's event-ID list contains no vote event. Verbatim: 'Event: A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch. * Publisher ID: `tfs` * Event ID: `git.pullrequest.updated` * Resource name: `pullrequest`'. Page monikerRange '<= azure-devops' with the Services/Server 2022/Server 2020 include, so it is claimed for Server too (per-event Server availability is NOT annotated)."
          },
          {
            "claim": "Claim 1: The `notificationType` publisher input's documented valid values are exactly PushNotification, ReviewersUpdateNotification, StatusUpdateNotification, ReviewerVoteNotification; other filters are repository (guid), pullrequestCreatedBy, pullrequestReviewersContains, branch.",
            "status": "CONFIRMED",
            "evidence": "events.md lines 2296-2306, verbatim: '`notificationType`: Include only events for pull requests with a specific change. Valid values: `PushNotification` - The source branch is updated. `ReviewersUpdateNotification` - The reviewers change. `StatusUpdateNotification` - The status changes. `ReviewerVoteNotification` - The votes score changes.' followed by `repository` (Data type: `guid`), `pullrequestCreatedBy`, `pullrequestReviewersContains`, `branch`. No enum value was quoted from memory incorrectly."
          },
          {
            "claim": "Claim 1: The delivered payload is 'byte-shape-identical for all four notificationTypes' and contains NO field naming which notificationType fired; sample resourceVersion is 2.0.",
            "status": "OVERSTATED",
            "evidence": "resourceVersion `2.0` CONFIRMED (events.md, git.pullrequest.updated sample). The absence of a notificationType discriminator in the ONE documented sample is CONFIRMED \u2014 top-level keys are id, eventType, publisherId, scope, message, detailedMessage, resource, resourceVersion, resourceContainers, createdDate; none names the notification type. But 'byte-shape-identical for all four' is stated NOWHERE on Learn \u2014 only a single sample exists, and Microsoft documents no per-notificationType payload contract. Mark inferred, not documented. Also materially incomplete: the payload's `resource.reviewers[]` DOES carry each reviewer's `vote` integer, so vote STATE is in-payload even though the change discriminator is not \u2014 the 'only in-payload signal is prose in message.text' assertion is wrong."
          },
          {
            "claim": "Claim 1: Therefore JetBridge MUST create four subscriptions with distinct receiver URLs; do not classify by parsing message.text.",
            "status": "UNVERIFIABLE",
            "evidence": "This is design reasoning, not documentation. Nothing on Learn requires or describes this pattern. The premise it rests on (payload carries no discriminator) is only established for the single documented sample. Note also an undiscussed documented alternative: the webHooks consumer has an `httpHeaders` input ('HTTP header keys and values in the form of key-value pairs') per https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops, so a per-subscription discriminator can be injected as a header rather than by minting four URLs."
          },
          {
            "claim": "Claim 2: IdentityRefWithVote.vote (integer int16) is documented as 10 - approved, 5 - approved with suggestions, 0 - no vote, -5 - waiting for author, -10 - rejected.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1 \u2014 Definitions > IdentityRefWithVote, verbatim: 'vote | integer (int16) | Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'. Route confirmed verbatim: PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1. GA, not preview. Server parity CONFIRMED: azure-devops-server-rest-7.1 and -7.0 listed under Other Supported Versions."
          },
          {
            "claim": "Claim 2: isReapprove is documented as 'Indicates if this approve vote should still be handled even though vote didn't change'; hasDeclined, isRequired, votedFor[] also documented as described.",
            "status": "CONFIRMED",
            "evidence": "Same page, IdentityRefWithVote table, verbatim: isReapprove 'Indicates if this approve vote should still be handled even though vote didn't change.'; hasDeclined 'Indicates if this reviewer has declined to review this pull request.'; isRequired 'Indicates if this is a required reviewer for this pull request. Branches can have policies that require particular reviewers are required for pull requests.'; votedFor 'Groups or teams that this reviewer contributed to. Groups and teams can be reviewers on pull requests but can not vote directly. When a member of the group or team votes, that vote is rolled up into the group or team vote.' A field the claim omits and which matters for a reviewer bot: `isFlagged` 'Indicates if this reviewer is flagged for attention on this pull request.'"
          },
          {
            "claim": "Claim 3: Every PR state change is materialised as a comment thread with commentType 'system', authored by '[DefaultCollection]\\\\Project Collection Service Accounts' (isContainer: true), carrying a typed property bag; readable in one GET.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1 \u2014 route verbatim GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/threads?api-version=7.1 (optional $iteration/$baseIteration). Sample response shows five system threads with author id 41113706-4320-4083-9150-925feb93fc22, displayName '[DefaultCollection]\\\\Project Collection Service Accounts', isContainer true, commentType 'system'. CommentType enum documented: unknown, text, codeChange, system. Thread fields id/publishedDate/lastUpdatedDate/isDeleted/status/properties confirmed on GitPullRequestCommentThread. Server parity: azure-devops-server-rest-7.1 listed."
          },
          {
            "claim": "Claim 3: CodeReviewThreadType takes at least VoteUpdate, ReviewersUpdate, RefUpdate, MergeAttempt, with the listed per-type property keys (CodeReviewVoteResult/VotedByTfId; CodeReviewRefNewHeadCommit/RefUpdatedByTfId/etc.; CodeReviewReviewersUpdated*; CodeReviewMergeCommit/MergeStatus/SourceCommit/TargetCommit).",
            "status": "CONFIRMED",
            "evidence": "All four values and every property key cited appear literally in the 7.1 sample response: MergeAttempt {CodeReviewMergeCommit, CodeReviewMergeStatus:'Succeeded', CodeReviewSourceCommit, CodeReviewTargetCommit}; ReviewersUpdate {CodeReviewReviewersUpdatedAddedTfId, ...AddedDisplayName, ...RemovedTfId, ...RemovedDisplayName, CodeReviewReviewersUpdatedByTfId, CodeReviewReviewersUpdatedByDisplayname, ...NumAdded, ...NumRemoved}; VoteUpdate {CodeReviewVoteResult:'10', CodeReviewVotedByTfId, CodeReviewVotedByDisplayName}; RefUpdate {CodeReviewRefName, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount, CodeReviewRefNewHeadCommit, CodeReviewRefUpdatedBy, CodeReviewRefUpdatedByDisplayName, CodeReviewRefUpdatedByTfId}. Implementation trap the claim misses: casing is inconsistent \u2014 'CodeReviewReviewersUpdatedByDisplayname' (lowercase n) vs 'CodeReviewVotedByDisplayName' / 'CodeReviewRefUpdatedByDisplayName'. Values are wrapped in {$type,$value} (PropertiesCollection), e.g. CodeReviewVoteResult is System.String '10', not an integer."
          },
          {
            "claim": "Claim 3: The COMPLETE CodeReviewThreadType enum and property-key set are NOT documented anywhere on Microsoft Learn.",
            "status": "CONFIRMED",
            "evidence": "The 7.1 pull-request-threads/list page's Definitions section enumerates Comment, CommentIterationContext, CommentPosition, CommentThreadContext, CommentThreadStatus, CommentTrackingCriteria, CommentType, GitPullRequestCommentThread, GitPullRequestCommentThreadContext, IdentityRef, PropertiesCollection, ReferenceLinks \u2014 there is no CodeReviewThreadType definition. `properties` is typed only as the untyped PropertiesCollection ('a collection of key-value pairs'). The four values are sample-only. The caveat as written is correct and should be kept."
          },
          {
            "claim": "Claim 3: The threads read is a 'monotonically-appended' history and GitHub has no single equivalent read.",
            "status": "UNVERIFIABLE",
            "evidence": "Monotonic append is not documented: threads carry `lastUpdatedDate` and `isDeleted` (and comments carry `isDeleted: true` in the sample), which is evidence AGAINST treating the log as append-only-immutable. Nothing on Learn states ordering or immutability of system threads. The GitHub comparison cannot be checked against Microsoft docs at all."
          },
          {
            "claim": "Claim 4: No Microsoft Learn page documents ANY HTTP header sent with a service-hook webhook POST \u2014 no delivery-id, no signature, no subscription-id header.",
            "status": "CONFIRMED",
            "evidence": "Checked full source text of docs/service-hooks/services/webhooks.md, docs/service-hooks/consumers.md, docs/service-hooks/create-subscription.md, docs/service-hooks/events.md and docs/service-hooks/troubleshoot.md. The only header mentions are (a) webhooks.md: 'HTTP has the potential to send private data, including authentication headers, unencrypted' and (b) consumers.md `httpHeaders` \u2014 an input where YOU supply headers. No inbound header is named anywhere. `x-vss-subscriptionid` appears in no Microsoft doc."
          },
          {
            "claim": "Claim 4: The payload's top-level `id` is a UUID defined as 'Gets or sets the unique identifier of this event'; retries re-send the same event id (inferred from requestAttempts on one notification bound to one eventId).",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1 \u2014 Event definition: 'id | string (uuid) | Gets or sets the unique identifier of this event.' Notification has 'eventId | string (uuid) | The event id associated with this notification'; NotificationDetails has 'requestAttempts | integer (int32) | Number of requests attempted to be sent to the consumer'. The retry-reuses-eventId conclusion is genuinely INFERRED, and the claim labels it so \u2014 correct handling. Supporting (not proving) doc: troubleshoot.md documents transient-failure retries with backoff on 'the notification'."
          },
          {
            "claim": "Claim 5: Microsoft Learn is silent in BOTH directions on service-hook delivery ordering; no sequence number or partition key exists.",
            "status": "CONFIRMED",
            "evidence": "Grep for 'order|sequence' across docs/service-hooks/{events,consumers,create-subscription,troubleshoot,services/webhooks}.md returns no ordering statement (only 'executionOrder' inside an unrelated pipeline-approval payload sample). Notification/NotificationDetails carry no sequence field \u2014 only createdDate/queuedDate/dequeuedDate/processedDate/completedDate. The related 'probation-window event loss' premise IS documented verbatim in troubleshoot.md: 'When a subscription is on probation, any new events are lost.' and 'If all seven retries fail, the subscription state gets set to _DisabledBySystem_.' Confidence label 'uncertain' is the right call."
          },
          {
            "claim": "Claim 6: POST /_apis/hooks/notificationsquery?api-version=7.1 accepts subscriptionIds[], publisherId, minCreatedDate, maxCreatedDate, maxResults, maxResultsPerSubscription, status, resultType, includeDetails; returns NotificationDetails including the full original `event` object; NotificationResult includes `filtered`.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1 \u2014 route and every listed body field confirmed verbatim, incl. includeDetails 'If true, we will return all notification history for the query provided; otherwise, the summary is returned.' NotificationDetails includes 'event | Event | Gets or sets this notification detail's event content.' plus request/response/errorMessage/errorDetail/requestAttempts/requestDuration and all five timestamps. NotificationResult enum: pending, succeeded, failed, filtered ('The notification was filtered by the Delivery Job'). NotificationStatus enum: queued, processing, requestInProgress, completed. Server parity: azure-devops-server-rest-7.1 listed. Second endpoint also CONFIRMED: GET https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}/notifications?api-version=7.1 with query params maxResults (default 100), result, status."
          },
          {
            "claim": "Claim 6: Retention of notification history is NOT documented.",
            "status": "OVERSTATED",
            "evidence": "True for the REST API \u2014 neither notifications/query nor notifications/list states a retention window. But Learn is not wholly silent: https://learn.microsoft.com/en-us/azure/devops/service-hooks/troubleshoot?view=azure-devops states 'The **Service Hooks** page in the web access admin summarizes activity from the last seven days for each subscription.' That documented seven-day window is the only retention signal Microsoft publishes and should be assumed as the practical bound until measured live \u2014 a materially different planning input than 'undocumented, could be long'."
          },
          {
            "claim": "Claim 7: Three machine-consumable consumers \u2014 webHooks/httpRequest, azureServiceBus/serviceBusQueueSend|serviceBusTopicSend, azureStorageQueue/enqueue \u2014 all documented 'Supported events: All events', with the cited inputs, the localhost restriction, bypassSerializer, and ttl max 604800s.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops \u2014 all three consumer IDs, action IDs and 'Supported events: All events' confirmed. webHooks inputs: url (Required Yes), acceptUntrustedCerts, basicAuthCredentials (Required **Yes** \u2014 the claim's flag of this oddity is correct), httpHeaders, resourceDetailsToSend, messagesToSend, detailedMessagesToSend. azureStorageQueue: accountName (Yes), accountKey (No), queueName ('lowercase-only', Yes), visiTimeout (Yes), ttl (Yes, 'maximum value you can use is seven days, or 604,800 seconds'). bypassSerializer verbatim: 'An option for sending messages to Service Bus as nonserialized strings instead of as .NET serialized strings. Select this setting when the receiver isn't a .NET client\u2026' \u2014 the Go trap is real. Entra inputs AuthenticationMechanismInputId / ServiceConnectionInputId / ServiceBusHostNameInputId confirmed. Localhost restriction verbatim from services/webhooks.md: 'Webhooks can't target localhost (loopback) or special range IPv4/IPv6 addresses.' Security note verbatim: 'The caller must call back into Azure DevOps Services and go through normal security and permission checks to get more details about the resource.' Consumers page monikerRange '<= azure-devops' with Server 2022/2020 include."
          },
          {
            "claim": "Claim 7: resourceDetailsToSend takes the values All / Minimal / None.",
            "status": "OVERSTATED",
            "evidence": "'All', 'Minimal', 'None' are web-UI labels on services/webhooks.md ('The default is **All**, but you can also choose to send **Minimal** \u2026 or **None**'). The consumers.md API table describes the input only prosaically: 'The number of resource fields to send to the queue. Possibilities are all fields, a minimum number, and none.' No documented enum of literal wire values for the subscription JSON. Do not hardcode these strings without a live check \u2014 this is exactly a UI-vs-REST divergence."
          },
          {
            "claim": "Claim 8: Subscriptions are fully programmatic at api-version 7.1 \u2014 POST/GET /_apis/hooks/subscriptions, PUT/DELETE /_apis/hooks/subscriptions/{subscriptionId}; response echoes hostId and tfsSubscriptionId.",
            "status": "CONFIRMED",
            "evidence": "POST https://dev.azure.com/{organization}/_apis/hooks/subscriptions?api-version=7.1 (rest/.../hooks/subscriptions/create), DELETE https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}?api-version=7.1 (subscriptions/delete), PUT https://dev.azure.com/{organization}/_apis/hooks/subscriptions/{subscriptionId}?api-version=7.1 (subscriptions/replace-subscription). GET List and Create Subscriptions Query also exist. All GA at 7.1, all list azure-devops-server-rest-7.1. Create sample response contains server-injected 'hostId' and 'tfsSubscriptionId' inside publisherInputs, exactly as claimed. Minor doc bug worth knowing: the replace-subscription sample request URL omits {subscriptionId} even though the route requires it."
          },
          {
            "claim": "Claim 8: 'When an event occurs, all enabled subscriptions in the project are evaluated. Then the consumer action is performed for all matching subscriptions', and 'If you don't specify a resource version, the latest version, latest released, is used. To help ensure a consistent event payload over time, always specify a resource version.'",
            "status": "CONFIRMED",
            "evidence": "Both quoted verbatim from https://learn.microsoft.com/en-us/azure/devops/service-hooks/create-subscription?view=azure-devops (ms.date 06/25/2025). However the SAME page immediately precedes the second quote with contradictory guidance: 'Resource versioning is applicable when an API is in preview. For most scenarios, specifying `1.0` as the resource version is the safest route.' \u2014 so 'pin resourceVersion to the sample value' is doc-supported but the docs simultaneously recommend `1.0`. Flag this conflict rather than presenting a single rule."
          },
          {
            "claim": "Claim 8: The docs contradict themselves on permissions \u2014 webhooks how-to says Project Collection Administrators, programmatic create-subscription says Project member.",
            "status": "CONFIRMED",
            "evidence": "services/webhooks.md Prerequisites: 'Member of the [Project Collection Administrators group]. Organization owners are automatically members of this group.' create-subscription.md Prerequisites: 'Project access | [Project member].' Genuine contradiction between two live Learn pages. Note the REST reference page (hooks/subscriptions/create, 7.1) states NO permission prerequisite at all \u2014 it lists only OAuth scopes \u2014 so the claim's phrase 'the programmatic create-subscription page' refers to the conceptual how-to, not the REST page."
          },
          {
            "claim": "Claim 8: Sample resourceVersions are git.pullrequest.updated `2.0`, git.pullrequest.merged `1.0-preview.1`, and the comment event `1.0`.",
            "status": "REFUTED",
            "evidence": "The comment event's documented sample resourceVersion is `2.0`, not `1.0`. events.md, ms.vss-code.git-pullrequest-comment-event sample payload: '\"resourceVersion\": \"2.0\"'. The other two are correct: git.pullrequest.updated `2.0`, git.pullrequest.merged `1.0-preview.1`. Full code-section ordering verified by extracting every resourceVersion line between the Code heading and the Service connection heading: 2.0 (tfvc.checkin), 2.0 (git.push), 2.0 (pullrequest.created), 1.0-preview.1 (pullrequest.merged), 2.0 (pullrequest.updated), 2.0 (comment event), then 1.0-preview.1 x5 for the git.repo.* events. Pinning the comment subscription to `1.0` would request a different, undocumented payload shape."
          },
          {
            "claim": "Claim 9: The code-area service-hook event inventory is EXHAUSTIVELY six events \u2014 tfvc.checkin, git.push, git.pullrequest.created, git.pullrequest.merged, git.pullrequest.updated, ms.vss-code.git-pullrequest-comment-event. 'THAT IS ALL SIX.'",
            "status": "REFUTED",
            "evidence": "The Code section of events.md documents ELEVEN events. The six named are correct, but it also documents git.repo.created, git.repo.deleted, git.repo.forked, git.repo.renamed, git.repo.statuschanged (events.md lines 2557-2935, each publisherId `tfs`, resourceVersion 1.0-preview.1). The six-event framing is accurate for PR/push events only and must be restated as such; as written it is a false exhaustiveness claim, and git.repo.* is directly relevant to a forge integration (repo deletion/disable while a change is in flight)."
          },
          {
            "claim": "Claim 9: git.pullrequest.merged's trigger is 'A pull request merge is ATTEMPTED', with mergeResult filter values Succeeded, Unsuccessful, Conflicts, Failure, RejectedByPolicy.",
            "status": "CONFIRMED",
            "evidence": "events.md verbatim: 'Event: A pull request merge is attempted in a Git repository.' and '`mergeResult`: Include only events for pull requests with a specific merge result. Valid values: `Succeeded` `Unsuccessful` `Conflicts` `Failure` `RejectedByPolicy`'. Its other filters are repository (guid), pullrequestCreatedBy, pullrequestReviewersContains, branch \u2014 note it does NOT accept notificationType."
          },
          {
            "claim": "Claim 9: A push to a PR source branch produces both git.push (no pullRequestId anywhere in payload, carries commits[] with author/committer and pushedBy) and git.pullrequest.updated (no indication which commits arrived); only the RefUpdate system thread gives 'PR N advanced to head SHA X'.",
            "status": "CONFIRMED",
            "evidence": "git.push sample payload (events.md 1942-2043) has resource.commits[] each with author{name,email,date} and committer{name,email,date}, resource.refUpdates[] with name/oldObjectId/newObjectId, and resource.pushedBy \u2014 and grep confirms no 'pullRequestId' occurs anywhere in that section. The git.pullrequest.updated sample's resource.commits[] carries one commit id but is not documented as 'the commits from this push'. RefUpdate thread's CodeReviewRefNewHeadCommit + CodeReviewRefName is the only documented place tying a PR to a new head SHA. Caveat on the claimed dual-fire: no Learn page states that a source-branch push emits BOTH events \u2014 that is inferred from the two trigger sentences, not asserted by Microsoft."
          },
          {
            "claim": "Claim 9: There is NO event for a PR status/check being posted, a comment thread's status changing (active -> fixed/wontFix/closed/byDesign/pending), a PR being abandoned or reactivated as a distinct event, a policy evaluation completing, an iteration being created, or a draft PR being published.",
            "status": "CONFIRMED",
            "evidence": "Verified by extracting every 'Event ID:' line in events.md (41 total across all sections). No such event IDs exist. The CommentThreadStatus enum (unknown, active, fixed, wontFix, closed, byDesign, pending) is documented on the threads API but has no corresponding hook. Nearest adjacent events are ms.vss-pipelinechecks-events.check-updated-event (pipeline checks, not branch policy or PR status) and git.pullrequest.updated with StatusUpdateNotification (PR status field, not the PR Statuses/checks API). Worth adding for the design: the comment event's `resource` carries BOTH `comment` (id, parentCommentId, author.id, commentType, publishedDate, lastUpdatedDate, lastContentUpdatedDate, _links.threads) AND `pullRequest` (pullRequestId, status, createdBy, source/targetRefName, lastMergeSourceCommit), which is a stronger self-suppression input than the claim credits."
          },
          {
            "claim": "Claim 10: Two built-in scoped build identities exist \u2014 'Project Collection Build Service ({OrgName})' and '{Project Name} Build Service ({Org Name})'; collection-scoped is the default; the project-scoped one is only created after the pipeline runs once; they back the dynamically-issued job access token.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/pipelines/process/access-tokens?view=azure-devops \u2014 name formats verbatim at lines 126 and 130-131. Verbatim: 'By default, the collection-scoped identity is used, unless configured otherwise as described in the previous Job authorization scope section.' Verbatim: 'The build service account on which you can manage permissions will only be created after you run the pipeline once.' Verbatim: 'A **job access token** is a security token that is dynamically generated by Azure Pipelines for each job at run time.' The 'Protect access to repositories in YAML pipelines' entanglement is also documented (enabled by default for orgs/projects created after May 2020). The recommendation to provision a dedicated principal instead is sound engineering judgment, not documentation \u2014 and note Entra service principals are Azure DevOps Services-only, so the Server fallback to a PAT-backed account is the right split."
          },
          {
            "claim": "Claim 11: Scope names vso.code, vso.code_write, vso.threads_full, vso.auditlog, vso.notification_write are real, with the quoted descriptions; and Entra ID OAuth and Azure DevOps OAuth 'use the same scope definitions'.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops 'Available scopes' table \u2014 every name exists with the quoted text, incl. vso.threads_full 'PR threads | Grants the ability to read and write to pull request comment threads.' and vso.code_write 'Grants the ability to read, update, and delete source code \u2026 create and manage pull requests and code reviews and receive notifications about version control events via service hooks.' Verbatim: 'Both Microsoft Entra ID OAuth and Azure DevOps OAuth use the same scope definitions.' Two corrections: (a) the table shows vso.code and vso.build and vso.work each 'Inherits from vso.hooks_write', and dedicated service-hooks scopes vso.hooks / vso.hooks_write / vso.hooks_interact exist marked '(No longer public.)' \u2014 so vso.code grants subscription-create transitively, which the claim states as a flat fact; (b) the REST pages for hooks subscription create/replace/delete and notifications query/list list THREE scopes (vso.work, vso.build, vso.code), not vso.code alone."
          },
          {
            "claim": "Claim 11 (implicit): these OAuth scopes are a usable auth path, cited via the azure-devops-oauth page.",
            "status": "OVERSTATED",
            "evidence": "The same Learn page carries two blocking caveats the claim omits. Server parity: 'OAuth 2.0 is available only for Azure DevOps Services, not Azure DevOps Server. For on-premises scenarios, use Client libraries, Windows Authentication, or personal access tokens.' Lifecycle: 'Azure DevOps OAuth is deprecated. New app registrations are no longer accepted as of April 2025. The service is scheduled for full deprecation in 2026.' Citing learn.microsoft.com/.../authentication/azure-devops-oauth as a live integration path is wrong for a system being designed in 2026 \u2014 the Entra ID OAuth path (or PATs on Server) is the only forward-looking option, and the scope NAMES carry over."
          }
        ]
      },
      {
        "area": "events-identity",
        "claims_checked": [
          {
            "claim": "Vote-change trigger: there is NO separate eventType for a vote; a vote surfaces as git.pullrequest.updated, and the publisher exposes a server-side filter `notificationType` with exactly PushNotification / ReviewersUpdateNotification / StatusUpdateNotification / ReviewerVoteNotification, so four separate subscriptions with distinct receiver URLs are required to recover which fired.",
            "status": "CONFIRMED",
            "evidence": "Verified verbatim against https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops and the raw source https://raw.githubusercontent.com/MicrosoftDocs/azure-devops-docs/main/docs/service-hooks/events.md. The four notificationType values are documented exactly as claimed; the other filters (repository guid, pullrequestCreatedBy, pullrequestReviewersContains, branch) are present; the sample carries no field naming the notificationType; sample resourceVersion is 2.0. No dedicated vote eventType exists in the code-area event list. PRACTICE RISK NOT IN THE CLAIM: publisherInputs set via REST can be invisible and uneditable in the Service Hooks web UI. Microsoft Q&A https://learn.microsoft.com/en-us/answers/questions/5666112/how-i-can-set-the-workitemtype-of-one-webhook documents this for the work-item publisher's workItemType filter: 'the filter is applied and works correctly, but the Service Hooks UI does not display API-defined work item type filters ... The UI always shows Any ... Only the REST API can show or change it.' The same UI/REST divergence class plausibly applies to notificationType (not separately verified). Consequence: never let anyone open and re-save a JetBridge subscription in the UI, and treat GET /_apis/hooks/subscriptions as the only truth. SECOND CONSEQUENCE NOT IN THE CLAIM: create-subscription documents 'When an event occurs, all enabled subscriptions in the project are evaluated', and each subscription carries its own status/probationRetries, so four subscriptions means four independent failure/probation/disable states to monitor, not one."
          },
          {
            "claim": "Sub-claim: the delivered payload's ONLY in-payload signal of what happened is prose in message.text / detailedMessage.text.",
            "status": "OVERSTATED",
            "evidence": "The git.pullrequest.updated sample resource does carry a `reviewers[]` array including each reviewer's current `vote` integer (confirmed in the events doc sample), plus mergeStatus, status, lastMergeSourceCommit and commits. So the payload does carry machine-readable state, not only prose. What it genuinely lacks is (a) any notificationType field, (b) any previous-value/delta, and (c) any actor for the update \u2014 the only identity field is `createdBy`, which is the PR AUTHOR, not the person who voted or pushed (confirmed against the raw events.md sample key list). The claim's operational conclusion (do not classify by parsing message.text; use distinct receiver URLs) survives, but 'only signal is prose' is too strong and could lead a reader to discard usable payload state."
          },
          {
            "claim": "Vote value enum on IdentityRefWithVote is 10/5/0/-5/-10 with those meanings; isReapprove exists and means a re-approval can be signalled with an unchanged vote value; hasDeclined, isRequired, votedFor[] exist.",
            "status": "CONFIRMED",
            "evidence": "Verbatim from https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/create-pull-request-reviewer?view=azure-devops-rest-7.1 (api-version=7.1): vote is 'integer (int16)' documented as 'Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'; isReapprove is 'Indicates if this approve vote should still be handled even though vote didn't change.'; hasDeclined, isRequired and votedFor (group roll-up, 'Groups and teams can be reviewers on pull requests but can not vote directly') all confirmed. Scope vso.code_write confirmed. PRACTICE CONTRADICTION FOUND: microsoft/azure-devops-node-api issue #611 (https://github.com/microsoft/azure-devops-node-api/issues/611) reports that PUT-ing vote 0 to reset an existing approval returns HTTP 200 but leaves the reviewer showing Approved \u2014 'Updating a reviewer vote to 0 should reset the vote'; no Microsoft response, no workaround, no resolution in the thread. n=1 and not independently reproduced here, so treat as PLAUSIBLE rather than proven, but it means JetBridge must not assume it can clear a stale approval by writing 0, and must re-read the reviewer after writing rather than trusting the 200."
          },
          {
            "claim": "System threads (commentType 'system') are a durable, ordered, queryable review-activity log; CodeReviewThreadType takes at least VoteUpdate/ReviewersUpdate/RefUpdate/MergeAttempt with the listed property keys; deriving the sealed review/v1 record from GET .../threads is authoritative and carries the actor.",
            "status": "CONFIRMED",
            "evidence": "Every property key claimed is present verbatim in the 7.1 sample response at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1 \u2014 VoteUpdate {CodeReviewVoteResult:'10', CodeReviewVotedByTfId, CodeReviewVotedByDisplayName}; RefUpdate {CodeReviewRefName, CodeReviewRefNewCommits, CodeReviewRefNewCommitsCount, CodeReviewRefNewHeadCommit, CodeReviewRefUpdatedBy, CodeReviewRefUpdatedByDisplayName, CodeReviewRefUpdatedByTfId}; ReviewersUpdate {...AddedTfId, ...RemovedTfId, ...ByTfId, ...NumAdded, ...NumRemoved}; MergeAttempt {CodeReviewMergeCommit, CodeReviewMergeStatus, CodeReviewSourceCommit, CodeReviewTargetCommit}. Author is '[DefaultCollection]\\\\Project Collection Service Accounts' with isContainer:true, exactly as claimed. FOUR CORRECTIONS/TRAPS THE CLAIM MISSES, all visible in that same sample: (1) IDENTITY GUIDS ARE NOT FORMATTED CONSISTENTLY \u2014 CodeReviewVotedByTfId is dashed ('d6245f20-2af8-44f4-9451-8107cb2767db') while CodeReviewReviewersUpdatedAddedTfId is undashed 32-hex ('2428198325304a9caeb788d60d57acfd'). A naive `author.id == our bot` string compare across thread types will silently fail; normalize before matching. (2) key casing is inconsistent: 'CodeReviewReviewersUpdatedByDisplayname' (lowercase n) vs 'CodeReviewVotedByDisplayName'. (3) GET threads takes $iteration/$baseIteration which change the returned thread POSITIONS, so the read is not position-stable unless you pin those params; and no continuation token / paging is documented for this endpoint, so behaviour on very long PRs is undocumented. (4) MergeAttempt threads are emitted on every merge preview (two appear in an 8-thread sample), so the log is noisy and JetBridge must filter by CodeReviewThreadType, not by recency. The claim's own caveat that the complete CodeReviewThreadType enum is undocumented is correct \u2014 nothing on Microsoft Learn enumerates it."
          },
          {
            "claim": "Idempotency key / delivery id header: the docs are silent on ALL webhook HTTP headers; there is no documented delivery-id, signature or subscription-id header; `x-vss-subscriptionid` is community folklore; dedup on the payload's top-level `id` UUID.",
            "status": "CONFIRMED",
            "evidence": "Checked https://learn.microsoft.com/en-us/azure/devops/service-hooks/services/webhooks?view=azure-devops (full text) and https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops \u2014 neither documents any header Azure DevOps sends. The only header surface documented is the OUTBOUND `httpHeaders` consumer input you populate yourself ('HTTP header keys and values in the form of key-value pairs ... These values are viewable by anyone who has access to the service hook subscription'), which means JetBridge can inject its own static shared-secret header but gets no per-delivery id. On `x-vss-subscriptionid`: a web search returned a confident assertion that the header exists, but every source it cited was a Microsoft Learn page that does not contain the string \u2014 this is precisely the folklore pattern the claim warns about, and I found no primary source. Event.id is confirmed as 'Gets or sets the unique identifier of this event' (uuid) in the Event contract on https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1. The retry-resends-same-event-id inference remains INFERRED: NotificationDetails.requestAttempts is 'Number of requests attempted to be sent to the consumer' on a Notification bound to one eventId, which is consistent but not a statement. NOTE: the consumer inputs also expose no HMAC/signature option at all, so `basicAuthCredentials` + your own httpHeaders secret is the ONLY authentication of an inbound delivery \u2014 weaker than GitHub's X-Hub-Signature-256, and it means JetBridge must not trust payload contents without a read-back."
          },
          {
            "claim": "Delivery ordering guarantee: Microsoft Learn is silent in BOTH directions; there is no sequence number or partition key; JetBridge must be order-independent (signal-then-read).",
            "status": "CONFIRMED",
            "evidence": "I could not find any Microsoft statement asserting or denying ordering across the service-hooks overview, events, consumers, webhooks, troubleshoot, create-subscription, or the Hooks REST reference. The Notification contract exposes createdDate/queuedDate/dequeuedDate/processedDate/completedDate but no sequence field, and no publisher input partitions by PR. The claim's 'uncertain' confidence is the right grade. STRENGTHENING EVIDENCE the claim does not cite: the transient-failure retry table on https://learn.microsoft.com/en-us/azure/devops/service-hooks/troubleshoot?view=azure-devops shows a single notification can be retried with backoff up to 183 seconds total, during which later notifications for the same PR are not blocked \u2014 so out-of-order delivery is a mechanical consequence of the documented retry model, not merely an unproven risk. Signal-then-read is not just safe, it is required."
          },
          {
            "claim": "Delivery history / replay API is a genuine recovery path for the probation-window event loss; the original event body is recoverable server-side; retention is NOT documented.",
            "status": "REFUTED",
            "evidence": "The API shape is confirmed \u2014 https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/notifications/query?view=azure-devops-rest-7.1 documents NotificationDetails.event as 'Gets or sets this notification detail's event content' typed as the full Event (eventType, resource, resourceVersion, resourceContainers, message, detailedMessage), plus every query field claimed, plus NotificationResult 'filtered'. But the two load-bearing conclusions are wrong. (1) NOT A RECOVERY PATH FOR PROBATION LOSS: https://learn.microsoft.com/en-us/azure/devops/service-hooks/troubleshoot?view=azure-devops states flatly, under Probation, 'When a subscription is on probation, any new events are lost.' Events dropped during probation never become notifications, so notificationsquery cannot return them. Probation lasts up to ~36 hours across 7 retries (documented backoff table), after which the subscription goes to DisabledBySystem. There is NO catch-up API for that window; the only recovery is to re-poll Azure Repos state directly (PRs + threads + iterations). (2) RETENTION IS PARTLY DOCUMENTED, contradicting 'not documented': the same page states 'The Service Hooks page in the web access admin summarizes activity from the last seven days for each subscription.' Treat ~7 days as the practical history horizon rather than an open question. USEFUL SURFACE THE CLAIM MISSES: SubscriptionStatus is a documented enum \u2014 enabled / onProbation / disabledByUser / disabledBySystem / disabledByInactiveIdentity \u2014 and Subscription carries probationRetries and lastProbationRetryDate. Polling GET /_apis/hooks/subscriptions for onProbation is the actual early-warning signal, and it is the thing JetBridge must alert on, because by the time deliveries stop, events are already being discarded."
          },
          {
            "claim": "Consumers: webhook vs Azure Service Bus vs Azure Storage Queue, with bypassSerializer as a Go trap, ttl max 604800, lowercase queue names, localhost blocked, and routing to a broker converting push into a durable pull model so JetBridge stops losing events during an outage.",
            "status": "OVERSTATED",
            "evidence": "Every documented detail checks out verbatim on https://learn.microsoft.com/en-us/azure/devops/service-hooks/consumers?view=azure-devops: bypassSerializer 'Send as nonserialized string ... Select this setting when the receiver isn't a .NET client, for instance, when the client uses Azure Client Library for Node'; azureStorageQueue queueName 'The lowercase-only name of the queue'; ttl and visiTimeout both capped at 'seven days, or 604,800 seconds' and both marked Required=Yes (the claim omits that visiTimeout is required); webHooks inputs url/acceptUntrustedCerts/basicAuthCredentials/httpHeaders/resourceDetailsToSend/messagesToSend/detailedMessagesToSend; localhost restriction and the 'caller must call back into Azure DevOps Services and go through normal security and permission checks' rationale for Minimal/None both confirmed on the webhooks page. TWO PROBLEMS. (1) The Required column is not a contract: basicAuthCredentials is marked Required=Yes for webHooks, yet the official step-by-step walkthrough on the same site creates a working webhook without ever setting it, and Service Bus marks queueName Required=Yes but connectionString Required=No. Do not code against that column. (2) THE ARCHITECTURAL CLAIM IS UNVERIFIED: nothing in the docs says that the failure/probation state machine is bypassed for the Service Bus or Storage Queue consumers. The documented model is explicitly HTTP-status-based (410 terminal, 408/502/503/504 transient, everything else enduring), and how a Service Bus send failure maps onto it is undocumented. The broker does absorb RECEIVER downtime (Azure DevOps' send succeeds even if JetBridge is down), which is a real and significant win over webhooks \u2014 but 'JetBridge stops losing events during an outage' only holds for outages of the JetBridge receiver, not for outages or throttling of the broker itself, and the probation-loss behaviour in that case is unknown. Also note the payload limit is 2 MB (troubleshoot FAQ), which is why resourceDetailsToSend matters."
          },
          {
            "claim": "Service Hooks REST API for per-repository/per-project subscription management, with resourceVersion pinning as an advantage over GitHub, and a docs-disagree-with-themselves permissions note (PCA vs Project member).",
            "status": "CONFIRMED",
            "evidence": "Endpoints, publisherInputs scoping (projectId, repository, branch), the server-injected hostId/tfsSubscriptionId echo, and 'When an event occurs, all enabled subscriptions in the project are evaluated. Then the consumer action is performed for all matching subscriptions' are all confirmed at https://learn.microsoft.com/en-us/azure/devops/service-hooks/create-subscription?view=azure-devops. THE PERMISSIONS CONTRADICTION IS WORSE THAN CLAIMED \u2014 there are THREE mutually inconsistent statements, not two: create-subscription Prerequisites says 'Project access: Project member'; the Webhooks how-to Prerequisites says 'Member of the Project Collection Administrators group. Organization owners are automatically members of this group.'; and the troubleshoot FAQ says 'By default, only project administrators have these permissions' (Edit subscriptions / View subscriptions), pointing at the Security REST API to delegate. Assume PCA and verify empirically. RESOURCEVERSION PINNING IS A TRAP AS WRITTEN: the same create-subscription page says 'Resource versioning is applicable when an API is in preview. For most scenarios, specifying 1.0 as the resource version is the safest route' \u2014 but the events samples show git.pullrequest.updated at 2.0 and git.pullrequest.created at 2.0. Following the Learn recommendation of '1.0' would pin an older, differently-shaped payload than every documented sample. Pin the version that matches the sample you built against, not the one the how-to recommends. Finally, the claim's assertion that the comment event samples at resourceVersion '1.0' appears to be WRONG: the raw events.md reports the pull request commented event at '1.0-preview.1' (matching git.pullrequest.merged), i.e. it is preview-versioned. Verify live before pinning; do not pin '1.0' on that event on the strength of the claim."
          },
          {
            "claim": "Project Collection Build Service identity exists but is the wrong bot; provision a dedicated principal (Entra service principal on Services, PAT-backed account on Server) so 'author.id == our bot' is exact.",
            "status": "CONFIRMED",
            "evidence": "The reasoning holds and the identity facts are documented, but there are two hard constraints the claim does not surface, and they change the provisioning design. (1) THE SUBSCRIPTION'S OWNING IDENTITY IS A LIVE FAILURE MODE. SubscriptionStatus includes disabledByInactiveIdentity \u2014 'The subscription is disabled because the owner is inactive or is missing permissions' (Hooks REST 7.1) \u2014 and the troubleshoot FAQ documents a 'Disabled (user left project)' state meaning 'The user who created the subscription is no longer a member of the team'. Microsoft Q&A https://learn.microsoft.com/en-us/answers/questions/5493288/azure-devops-service-hook-subscriptions-disabledby (reported 2025-07-25) shows subscriptions created with an org owner's OAuth token intermittently flipping to disabledByInactiveIdentity while the owner was still an active org owner; the Microsoft reply attributes it to Entra token expiry ('Microsoft Entra ID tokens typically expire after one hour. If the user's session is expired ADO may misinterpret the identity as invalid') and recommends migrating off legacy OAuth. Single report, unresolved, so PLAUSIBLE not proven \u2014 but combined with the documented enum value it means JetBridge must poll subscription.status and re-enable, and must not treat subscription creation as fire-and-forget. (2) THE RECOMMENDED IDENTITY IS CLOUD-ONLY AND CANNOT USE THE OAUTH PATH. https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/service-principal-managed-identity?view=azure-devops is scoped '**Azure DevOps Services**' only (no Server moniker) and lists under Key differences: '\u274c Create PATs or Secure Shell keys' and '\u274c Support Azure DevOps OAuth flows'. So on Services the bot authenticates with Entra client-credentials tokens against https://app.vssps.visualstudio.com/.default and cannot hold a PAT at all; on Azure DevOps Server service principals are not available and a real user account with a PAT is the only option. Each identity also consumes a paid license per organization with no multi-org discount."
          },
          {
            "claim": "Complete git/PR service hook event inventory is exactly six events; there is NO event for thread status change, policy evaluation, iteration creation, draft publish, or abandon/reactivate; git.push carries no pullRequestId and git.pullrequest.updated carries no indication of which commits arrived.",
            "status": "CONFIRMED",
            "evidence": "Verified against https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops and the raw markdown: the code-area events are tfvc.checkin, git.push, git.pullrequest.created, git.pullrequest.merged, git.pullrequest.updated, ms.vss-code.git-pullrequest-comment-event. Nothing for comment-thread status transitions (active/fixed/wontFix/closed/byDesign/pending are documented as CommentThreadStatus on the threads API but have no event), nothing for policy evaluation, iterations, draft publish, or abandon. The 'merge is ATTEMPTED' wording on git.pullrequest.merged is confirmed. TWO ADDITIONS THAT MATTER FOR SELF-SUPPRESSION. (a) GOOD NEWS: the comment event's sample resource DOES carry the comment author identity \u2014 'comment': { 'id': 2, 'parentCommentId': 1, 'author': { 'displayName': ..., 'id': '11bb11bb-...', 'uniqueName': ... }, ... } \u2014 so payload-level self-suppression on resource.comment.author.id is possible for the comment trigger without a read-back. (b) BAD NEWS: the comment event's ONLY publisher input filters are `repository` (guid) and `branch`. There is NO author or createdBy filter, so a bot cannot be excluded server-side; every JetBridge-authored comment will be delivered back to JetBridge and must be dropped at the receiver. Combined with git.pullrequest.updated having no updater field (only createdBy = PR author), the claim's conclusion that the system-threads read is required to get an actor is correct."
          },
          {
            "claim": "OAuth/PAT scopes: vso.code for subscriptions, vso.code or vso.threads_full for threads, vso.code_write for votes, and 'Entra ID OAuth and Azure DevOps OAuth use the same scope definitions' so these names carry over to the Entra path.",
            "status": "OVERSTATED",
            "evidence": "The per-operation scope names are confirmed on the 7.1 pages: threads/list lists vso.code and vso.threads_full ('Grants the ability to read and write to pull request comment threads'); create-pull-request-reviewer lists vso.code_write with the quoted description including 'to receive notifications about version control events via service hooks'. One correction: notificationsquery does NOT list only vso.code \u2014 it lists vso.work, vso.build AND vso.code (publisher-dependent). The real problem is the Entra carry-over. For the identity the companion claim recommends (an Entra service principal), the service-principal page states '\u274c Support Azure DevOps OAuth flows', tokens are acquired for the single resource scope https://app.vssps.visualstudio.com/.default, and the page states explicitly 'Azure DevOps doesn't use Microsoft Entra ID application permissions. All access control is managed through the Azure DevOps permission system.' So an Entra SP token is NOT vso.*-scoped and cannot be least-privileged by scope at all \u2014 it carries whatever the principal's Azure DevOps permissions allow. The vso.* scope list is a correct map of what each operation needs, but it is NOT a security boundary on the recommended cloud identity; least privilege has to be enforced with Azure DevOps group/permission assignments instead."
          },
          {
            "claim": "LENS (a): iterations are truly immutable and their commits survive force-push and GC.",
            "status": "UNVERIFIABLE",
            "evidence": "No Microsoft Learn page states a retention or immutability guarantee for pull request iteration commits, and I found no credible community report either confirming or contradicting survival across force-push plus server-side GC. Searches across developercommunity.visualstudio.com, github.com and learn.microsoft.com surfaced only adjacent material (a Developer Community request to expose git gc at all, and Microsoft Q&A noting PRs can break and be auto-abandoned on failure to create an iteration for very large change counts). The iterations API documents sourceRefCommit/targetRefCommit/commonRefCommit and a hasMoreCommits truncation flag but says nothing about lifetime. Do not design the sealed revision series on an assumption that iteration commit OIDs stay fetchable \u2014 verify empirically with a force-push-then-fetch test against a real org, and if the guarantee cannot be established, JetBridge must keep its own immutable copy of each revision's commit (its own ref or object store) rather than relying on Azure Repos to keep orphaned objects alive."
          },
          {
            "claim": "LENS (b): comment anchors survive across iterations rather than silently going stale.",
            "status": "REFUTED",
            "evidence": "The docs themselves describe tracking as best-effort and conditional, not guaranteed. On the 7.1 threads contract: GitPullRequestCommentThreadContext.trackingCriteria is 'The criteria used to track this thread. IF this property is filled out when the thread is returned, then the thread has been tracked'; CommentTrackingCriteria.firstComparingIteration / secondComparingIteration are documented as 'Threads were tracked if this is greater than 0' \u2014 i.e. a returned thread may legitimately have no tracking at all, and the caller cannot tell in advance. origFilePath exists precisely because 'the file in question was renamed in a later iteration', so anchors are re-derived, not pinned. changeTrackingId is documented as 'Used to track a comment across iterations ... Must be set for pull requests with iteration support' \u2014 meaning correct anchoring requires the WRITER to supply a changeTrackingId obtained from the iteration's changes list, and a thread created without it is unanchored. There is a live REST/UI defect here: microsoft/azure-devops-mcp issue #793 (opened 2025-12-12, assigned, labelled Bug/Repos) reports that threads created via the API with only threadContext.rightFileStart cause the Azure DevOps PR UI to throw 'TypeError: can't access property \"line\", s is undefined' and break the discussion view \u2014 the REST call succeeds, the UI does not render. This directly supports the design decision to anchor by declaration (immutable commit OID + next revision declares what it resolves) rather than trusting Azure DevOps tracking, and it warns that JetBridge must set the full context (rightFileEnd and changeTrackingId, not just rightFileStart) or it will produce threads that break the human reviewer's UI."
          }
        ]
      }
    ]
  },
  {
    "key": "git-and-clients",
    "survey": {
      "area": "Azure Repos Git primitives and Go client libraries \u2014 can the Gerrit/jj/ghstack-borrowed model (stable change identity, ordered immutable revisions with declared bases, declaration-based comment anchoring) be implemented on Azure DevOps?",
      "findings": [
        {
          "capability": "Ref management via REST, with compare-and-swap semantics",
          "exists": "native",
          "detail": "GET/POST `_apis/git/repositories/{repositoryId}/refs`. Request body of the POST is a `GitRefUpdate[]`: `{name, oldObjectId, newObjectId, isLocked, repositoryId}`. Docs state the CAS contract explicitly: \"Updating a ref means making it point at a different commit than it used to. You must specify both the old and new commit to avoid race conditions.\" Creation uses oldObjectId of 40 zeros; deletion uses newObjectId of 40 zeros. Response is `GitRefUpdateResult[]` with per-element `success`, `customMessage`, `rejectedBy` (\"Name of the plugin that rejected the updated\") and a 16-value `GitRefUpdateStatus` enum: succeeded, forcePushRequired, staleOldObjectId, invalidRefName, unprocessed, unresolvableToCommit, writePermissionRequired, manageNotePermissionRequired, createBranchPermissionRequired, createTagPermissionRequired, rejectedByPlugin, locked, refNameConflict, rejectedByPolicy, succeededNonExistentRef, succeededCorruptRef. This is strictly better than GitHub: GitHub's `PATCH /repos/{o}/{r}/git/refs/{ref}` has no expected-old-SHA parameter, only `force` true/false, so JetBridge cannot detect a lost race there. Azure gives real optimistic concurrency per ref (`staleOldObjectId`), which is exactly what a revision-publishing agent needs to avoid clobbering a concurrent human push.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?filter={prefix}&$top={n}&continuationToken={t}&api-version=7.1",
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Batched multi-ref update as a single transaction (ghstack base+head+orig triple published atomically)",
          "exists": "workaround-only",
          "detail": "Two separate mechanisms, neither transactional. (1) Over git: `git push --atomic` is NOT supported. Microsoft Q&A answer to \"[ADO] [Repo] - Support for git push atomic\" (asked 2025-05-01 against build AzureDevOps_M254_20250410.4, answered 2025-05-02) states plainly: \"Azure DevOps Repos currently does not support atomic Git pushes (git push --atomic)\" because \"Azure DevOps uses a custom Git server that doesn't advertise support for --atomic\", and \"Azure DevOps does not allow changing low-level Git server settings, even on self-hosted/on-prem environments.\" Client fails with `fatal: the receiving end does not support --atomic push`. (2) Over REST: POST /refs accepts an array of GitRefUpdate, but the response is an array of per-ref GitRefUpdateResult each with its own `success` boolean and the enum includes `unprocessed` (\"The request was not processed\") \u2014 the shape is designed for partial success, and no doc claims all-or-nothing. So a ghstack-style triple must be published as a best-effort batch plus a compensating rollback, and the platform must tolerate a torn triple on every restart. The suggested workaround in the MS answer is manual rollback (`git push origin tag || git push origin :branch`). GitHub is equally non-atomic across refs, so this is a wash \u2014 but it means the base/head/orig invariant cannot be enforced by the forge on either side and must be reconstructed by JetBridge.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/answers/questions/2262768/(ado)-(repo)-support-for-git-push-atomic",
            "https://developercommunity.visualstudio.com/t/Atomic-push-support-for-Azure-DevOps/10215909",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Arbitrary ref namespaces (e.g. refs/jetbridge/changes/<id>/<n>)",
          "exists": "absent",
          "detail": "Do not design on this. Azure Repos documents exactly three writable ref classes, each with its own permission bit and its own rejection status: branches (`CreateBranch` \u2014 \"Can create and publish branches in the repository\" \u2192 `createBranchPermissionRequired`), tags (`CreateTag` \u2014 \"Can push tags to the repository\" \u2192 `createTagPermissionRequired`), and notes (`ManageNote` \u2014 \"Can push and edit Git notes\" \u2192 `manageNotePermissionRequired`). There is no permission bit, no rejection status, and no documented token shape for any other namespace, and `invalidRefName` exists as a distinct failure mode. The security-token format for branch-level ACLs, documented in the Microsoft DevOps blog, is `repoV2/{projectId}/{repoId}/refs/heads/{utf16le-hex-per-segment}/` and covers only `refs/heads/` \u2014 the post says nothing about tags, notes, or custom namespaces, so custom refs are likely unsecurable even if writable. Counter-evidence worth noting: the Refs-List sample response in the official docs contains `refs/remotes/origin/HEAD` and `refs/remotes/origin/master` entries, proving the storage layer can hold and list non-heads/non-tags refs (these arrive via repository import, not via push). So the backend can store them; nothing documents that a push or a POST /refs can create them. Test before relying on it: POST /refs with `name: \"refs/jetbridge/test\"`, oldObjectId all-zeros, and read `updateStatus` \u2014 expect `invalidRefName` or `writePermissionRequired`. Fallback that is documented to work: hide revisions under `refs/heads/jetbridge/<change-id>/<n>` (branch namespace, so CreateBranch covers it) and accept that they appear in the branch picker.",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?filter=jetbridge/&api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "uncertain",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/organizations/security/permissions?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list?view=azure-devops-rest-7.1",
            "https://devblogs.microsoft.com/devops/git-repo-tokens-for-the-security-service/",
            "https://learn.microsoft.com/en-us/azure/devops/organizations/security/namespace-reference?view=azure-devops"
          ]
        },
        {
          "capability": "Durability of force-pushed / unreachable commits (does an old revision survive?)",
          "exists": "native",
          "detail": "Azure Repos is materially better than GitHub here, and this removes most of the reason to keep `orig` refs at all. Documented: \"There's no retention policy on deleted branches. You can restore a deleted Git branch at any time, regardless of when it was deleted.\" Restore \"gets recreated at the last commit to which it pointed\" (policies and permissions are not restored). Beyond that, the Pushes API is a permanent, queryable per-ref update ledger: `searchCriteria.refName` plus `searchCriteria.includeRefUpdates=true` returns every push touching that ref with `oldObjectId`/`newObjectId` per update \u2014 i.e. a server-side reflog that survives force-push and branch deletion. The docs even direct users to it: \"go to the Pushes page of the restored branch to see the entire history of the branch\", then \"go to a specific commit, then select New branch\". There is NO documented unreachable-object GC/prune policy anywhere; the Git limits page mentions only repacking (\"Azure Repos continuously reduces the overall size and increases the efficiency of Git repositories by consolidating similar files into packs\"). Absence of a documented prune plus a documented unlimited branch-restore is strong evidence that force-pushed commits are retained indefinitely, but the object-level guarantee itself is inferred, not stated. Practical consequence for the borrowed model: JetBridge can drop ghstack-style `orig` refs on Azure and reconstruct the revision series from Pushes history; on GitHub it cannot, because GitHub prunes unreachable objects and its Events API is short-lived.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pushes?searchCriteria.refName={refName}&searchCriteria.includeRefUpdates=true&$top={n}&$skip={n}&api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/restore-deleted-branch?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops"
          ]
        },
        {
          "capability": "Push options (git push -o), Gerrit refs/for, Forgejo agit",
          "exists": "absent",
          "detail": "No push options of any kind. There is no `git push -o` documentation on learn.microsoft.com, no Azure-specific push option (no work-item linking via push option, no create-PR-on-push option), no `refs/for/` magic-ref review target, and no agit equivalent. The only push-side affordance documented is cosmetic: pushing a new branch makes the git output include a URL that opens the create-PR page. The structural reason is the same one that kills --atomic: Azure DevOps runs a custom (non-canonical) Git server and does not expose or allow configuration of receive-side capabilities. Note this is an absence of documentation plus a documented absence of the sibling capability (--atomic), not a positive statement that push-options is unadvertised \u2014 but there is no basis to design on it. Consequence: JetBridge cannot signal revision intent in-band on the push. All revision metadata must travel out-of-band via the REST API (PR Properties \u2014 see below), which on Azure is actually the better channel anyway.",
          "endpoints": [],
          "vs_github": "equivalent",
          "confidence": "inferred",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/pushing?view=azure-devops",
            "https://learn.microsoft.com/en-us/answers/questions/2262768/(ado)-(repo)-support-for-git-push-atomic",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/command-prompt?view=azure-devops"
          ]
        },
        {
          "capability": "jj-style change-id as an arbitrary COMMIT HEADER",
          "exists": "absent",
          "detail": "Not viable on Azure Repos \u2014 reject this piece of the borrowed model. Three independent blockers. (1) Write path via REST is impossible: `Pushes - Create` builds commits server-side from a `GitCommitRef` whose only authorable fields are `comment`, `changes`, `author`, `committer`, `parents`. There is no header field and no raw-object upload. (2) Read path via REST does not exist at all: `GitCommitRef` (returned by Commits, Pushes, PR Iterations, PR Commits \u2014 every commit-bearing response) exposes only author/committer/comment/commitId/parents/changeCounts/changes/statuses/workItems. No endpoint returns a raw commit object, so even a header successfully pushed over the git wire could never be read back through the API \u2014 JetBridge would need a full git clone on every trigger just to read its own change-id. (3) Merge destroys it regardless: `rebase` (\"Rebase the source branch on top of the target branch HEAD commit, and fast-forward the target branch. The source branch is updated during the rebase operation\"), `rebaseMerge`, and `squash` (\"Put all changes from the pull request into a single-parent commit\") all rewrite the commit objects server-side, so any header dies at completion under three of the four strategies. Whether a custom header survives a plain `git push` over the wire is genuinely UNDOCUMENTED \u2014 to test it: `git commit --amend` with a hand-written object carrying `change-id: <ulid>` between `committer` and the blank line (or use `git hash-object -t commit`), push to Azure Repos, then `git fetch` into a fresh clone and `git cat-file commit HEAD` to see whether the header round-tripped. Even a positive result does not make it usable, because of blockers (2) and (3).",
          "endpoints": [
            "POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pushes?api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/commits?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/create?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Pull Request Properties \u2014 a native key/value bag for third-party state (the right home for change identity)",
          "exists": "native",
          "detail": "This is the standout Azure-only primitive and it is exactly what the borrowed model needs. The API's own description: \"This API provides a way to manage external properties associated with a pull request. Third party services can use this API to store additional information on the pull request without maintaining their own storage.\" GitHub has NO equivalent \u2014 on GitHub the change-id and revision ledger have to be smuggled into the PR body or a hidden comment, where a human can edit or delete them. Update is JSON Patch (`Content-Type: application/json-patch+json`) with ops add/replace/remove; for `add` the path may be empty, in which case value must be an object of key/value pairs; for replace/remove the path is required, and replace on a missing path adds it. Reads return a `PropertiesCollection` where each entry is `{\"$type\": ..., \"$value\": ...}`. Two real constraints. (a) Primitives only: \"Values of all primitive types (any type with a TypeCode != TypeCode.Object) except for DBNull are accepted\" \u2014 you cannot store a nested JSON object, so a revision ledger must be serialized to a JSON string or base64 `Byte[]`. (b) Docs vs behaviour: the definition claims \"Values of type Byte[], Int32, Double, DateType and String preserve their type\", but Microsoft's own sample sends `\"value\": 8` and the sample response returns `\"sampleId\": {\"$type\": \"System.String\", \"$value\": \"8\"}` \u2014 a JSON number came back as a string. Treat every read as a string and parse client-side. Also note Azure pre-populates its own keys (`Microsoft.Git.PullRequest.SourceRefName`, `Microsoft.Git.PullRequest.TargetRefName`), so namespace your keys. Recommendation: store the stable change-id, the revision ledger (revision n \u2192 sourceRefCommit/targetRefCommit/commonRefCommit), and the resolved-comment declarations here as one namespaced JSON string.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/properties?api-version=7.1",
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/properties?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Ordered immutable revisions each naming its own base (the ghstack base/head/orig triple)",
          "exists": "native",
          "detail": "Azure Repos already HAS the revision noun that GitHub lacks, so JetBridge should not build ghstack ref triples on Azure. `GitPullRequestIteration` carries: `id` (ordinal, 1-based, monotonically increasing), `sourceRefCommit`, `targetRefCommit`, `commonRefCommit` (\"The first common Git commit of the source and target refs\" \u2014 i.e. the merge base, the declared base), `push` (GitPushRef), `commits`, `changeList`, `createdDate`, `author`, and `reason` (`IterationReason`: push, forcePush, create, rebase, unknown, retarget, resolveConflicts). The doc states iterations are created by pushes: \"Iterations are created as a result of creating and pushing updates to a pull request.\" The `forcePush` and `rebase` reasons are the load-bearing detail \u2014 Azure mints a NEW iteration on force-push and on server-side rebase rather than mutating the old one, so revisions stay individually addressable across exactly the history-rewriting operations that break GitHub. The PR object also exposes `supportsIterations`: \"Iteration support means individual pushes to the source branch of the pull request can be reviewed and comments left in one iteration will be tracked across future iterations.\" Additionally `GitPullRequestChange` carries `changeTrackingId` (\"ID used to track files through multiple changes\"), a native cross-iteration file identity. GitHub has no revision object at all \u2014 a PR is a mutable branch ref plus a mutable head SHA, which is precisely why ghstack had to invent the triple. Design consequence: on Azure, map review/v1 revisions onto iteration ids directly; use PR Properties only for the change-id and the resolution declarations, not for reconstructing bases.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations?includeCommits=true&api-version=7.1",
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/iterations/{iterationId}/changes?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Commit-message trailers surviving merge; control of the squash commit message",
          "exists": "partial",
          "detail": "Trailers are the viable substitute for the rejected commit-header approach, but only because Azure lets you dictate the merge message. `GitPullRequestCompletionOptions.mergeCommitMessage`: \"If set, this will be used as the commit message of the merge commit.\" This is settable on the PR via PATCH (completionOptions is one of the documented updatable properties) and therefore applies to auto-complete too, so the agent can guarantee its `Change-Id:` trailer lands on the squash/merge commit regardless of strategy. Per-strategy trailer survival: `noFastForward` (\"A two-parent, no-fast-forward merge. The source branch is unchanged\") preserves every source commit and its trailers; `squash` produces a new single-parent commit whose message is whatever you set; `rebase` and `rebaseMerge` rewrite source commits (message text is carried by git's rebase, so trailers survive the rewrite even though SHAs do not). WHAT IS NOT DOCUMENTED: the DEFAULT squash commit message. Neither the merge-strategies conceptual page nor the completionOptions reference states what Azure composes when `mergeCommitMessage` is omitted \u2014 whether it concatenates the squashed commits' messages, uses the PR title/description, or something else. The docs here are thin. Test by completing a squash PR with completionOptions omitted and reading `lastMergeCommit.comment`. Mitigation is trivial: always set `mergeCommitMessage` explicitly and never depend on the default. Also note `squashMerge` is deprecated: \"It is recommended that you explicitly set MergeStrategy in all cases. If an explicit value is provided for MergeStrategy, the SquashMerge property will be ignored.\"",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/merging-with-squash?view=azure-devops"
          ]
        },
        {
          "capability": "Merge queue / merge train / Gerrit submit strategies",
          "exists": "absent",
          "detail": "Azure DevOps has no merge queue, no merge train, and no Gerrit-style submit strategies. What it has is auto-complete, which is a different thing: \"Branches with branch policies configured for pull requests have the Set auto-complete button. Select this option to set a pull request to autocomplete once it fulfills all policies.\" Auto-complete is per-PR and evaluates that PR's own policies against the CURRENT target head \u2014 it does not serialize competing PRs, does not speculatively test the merged result of a batch, and does not re-run validation against a moved target before landing. `completionOptions.autoCompleteIgnoreConfigIds` only lets you skip specific OPTIONAL policies (\"Auto-complete always waits for required policies (isBlocking == true)\"). The branch-policies reference (last updated 2026-07-15) contains no queue concept at all. For the disposition-triggered loop this matters concretely: on an `approved` disposition the agent must itself detect that the target moved, rebase, and re-verify \u2014 Azure will not hold a slot for it, and two agents approving simultaneously can both land onto a target neither tested against. GitHub's merge queue does exactly this work. This is the clearest place Azure is worse.",
          "endpoints": [
            "PATCH https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/complete-pull-requests?view=azure-devops"
          ]
        },
        {
          "capability": "Approval votes bound to a specific revision (does a new push invalidate approval?)",
          "exists": "native",
          "detail": "Azure ties votes to iterations natively and with more granularity than GitHub's single 'Dismiss stale pull request approvals' checkbox. Under the minimum-reviewers policy, 'When new changes are pushed' offers four documented behaviours: \"Require at least one approval on every iteration\" (\"The user's approval isn't counted against any previous unapproved iteration pushed by that user. As a result, another approval on the last iteration is required to be done by another user\" \u2014 Azure DevOps Server 2022.1+); \"Require at least one approval on the last iteration\"; \"Reset all approval votes (does not reset votes to reject or wait)\" \u2014 note it deliberately preserves reject/wait; and \"Reset all code reviewer votes\" which clears everything including reject and wait. Policy re-evaluation is documented as event-driven: \"The server reevaluates branch policies when pull request owners push changes and when reviewers vote.\" Vote values on `IdentityRefWithVote.vote` are int16: 10 approved, 5 approved with suggestions, 0 no vote, -5 waiting for author, -10 rejected. The distinction between -5 (waiting for author) and -10 (rejected) plus 5 (approved with suggestions) gives a richer disposition vocabulary than GitHub's three-way APPROVED/COMMENTED/CHANGES_REQUESTED, and the reset semantics let JetBridge configure the repo so that its own push provably clears approval \u2014 a useful structural guard against the self-trigger problem.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers?api-version=7.1",
            "PUT https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "microsoft/azure-devops-go-api health and fitness for purpose",
          "exists": "partial",
          "detail": "Alive but in maintenance mode, and pinned to preview api-versions that Microsoft reserves the right to switch off. Measured, not inferred. RELEASES (Go module proxy): the only published v7 version is `github.com/microsoft/azure-devops-go-api/azuredevops/v7 v7.1.0`, Time 2023-04-17T21:26:59Z \u2014 over three years old as of Aug 2026. v6 latest is v6.0.1 (2022-01-26); the unversioned module tops out at v1.0.0-b5 (2020-08-23). COMMITS (dev branch atom feed): the only 2026 activity is 2026-06-09/10 and it is entirely CI/security plumbing \u2014 \"update Go toolchain version to 1.22.5 for CodeQL compatibility\", \"add CGO_ENABLED variable to disable cgo\", \"add CodeQL Go wrapper script\", merged as PR #198. The last API-affecting commit is 2024-10-11 (\"add group deleted flag\", PR #185). Repo is NOT archived; 54 open issues, 15 open PRs; README carries no maintenance-mode or deprecation notice. GENERATED: yes \u2014 every file header reads \"Generated file, DO NOT EDIT / Changes may cause incorrect behavior and will be lost if the code is regenerated.\" go.mod declares `go 1.12` with a single dependency, `github.com/google/uuid v1.1.1`. COVERAGE is complete for this loop: GetThreads/GetPullRequestThread/CreateThread/UpdateThread/CreateComment/UpdateComment/GetComments/DeleteComment; GetPullRequestIterations/GetPullRequestIteration/GetPullRequestIterationChanges/GetPullRequestIterationCommits; CreatePullRequestStatus/CreateCommitStatus/GetPullRequestIterationStatuses/UpdatePullRequestStatuses; CreatePullRequestReviewer(s)/UpdatePullRequestReviewer/GetPullRequestReviewers; GetRefs/UpdateRef/UpdateRefs; CreatePush/GetPushes/GetPushCommits; GetPullRequestProperties/UpdatePullRequestProperties. THE BLOCKER: the generated git client hardcodes preview api-versions \u2014 114 occurrences of \"7.1-preview.1\" and 5 of \"7.1-preview.2\" in git/client.go, and zero occurrences of GA \"7.1\". Microsoft's own versioning policy says: \"After an API is released (1.0, for example), its preview version (1.0-preview) is deprecated and can be deactivated after 12 weeks... Once a preview API is deactivated, requests that specify a -preview version get rejected.\" A three-year-old unmaintained SDK pinned to deprecated preview versions is a live outage risk, not just staleness.",
          "endpoints": [
            "https://proxy.golang.org/github.com/microsoft/azure-devops-go-api/azuredevops/v7/@latest",
            "https://proxy.golang.org/github.com/microsoft/azure-devops-go-api/azuredevops/v7/@v/list"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://github.com/microsoft/azure-devops-go-api",
            "https://proxy.golang.org/github.com/microsoft/azure-devops-go-api/azuredevops/v7/@latest",
            "https://github.com/microsoft/azure-devops-go-api/commits/dev.atom",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops"
          ]
        },
        {
          "capability": "Alternative Go clients for Azure DevOps",
          "exists": "absent",
          "detail": "There is no viable alternative \u2014 all three community clients are abandoned. Measured via the Go module proxy: `github.com/mikaelkrief/go-azuredevops-sdk` latest is v0.0.0-20190401120440-0bd749fea120 (2019-04-01, never tagged a release); `github.com/benmatselby/go-azuredevops` latest is v0.4.0 (2021-05-25) and its own README describes it as work-in-progress covering \"only a small subset of the API\"; `github.com/mcdafydd/go-azuredevops` latest is v0.12.1 (2021-10-21) and is a fork of benmatselby scoped to Atlantis integration. None has been touched in ~5 years. RECOMMENDATION: hand-roll a thin REST client. The endpoint surface JetBridge actually needs is about a dozen routes (threads, comments, iterations, iteration changes, PR get/update, PR properties, reviewers, statuses, refs, pushes), the wire format is plain JSON, auth is a single header, and pinning `api-version=7.1` (GA) yourself is the whole point. Optionally vendor the SDK's `git/models.go` for the struct definitions \u2014 it is MIT licensed and the models are correct even where the client's version pinning is not \u2014 but do not take its `client.go`. This inverts the usual advice, and the reason is specific: the official SDK's only differentiator is generated coverage, and its generated coverage is what carries the preview-version defect.",
          "endpoints": [],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://proxy.golang.org/github.com/benmatselby/go-azuredevops/@latest",
            "https://proxy.golang.org/github.com/mcdafydd/go-azuredevops/@latest",
            "https://proxy.golang.org/github.com/mikaelkrief/go-azuredevops-sdk/@latest",
            "https://github.com/benmatselby/go-azuredevops"
          ]
        },
        {
          "capability": "Authentication from Go, and the least-privilege scope set",
          "exists": "native",
          "detail": "MECHANISMS, in Microsoft's current recommended order: managed identity (\"Strongest option for Azure-hosted workloads\"), service principal, Azure DevOps service connection, then PAT last \u2014 \"Use personal access tokens sparingly, and only when Microsoft Entra ID isn't available.\" PAT wire format is HTTP Basic with an EMPTY username: `Authorization: Basic base64(\":\" + PAT)`. CRITICAL SPLIT: \"OAuth 2.0 and Microsoft Entra ID authentication are available for Azure DevOps Services only, not Azure DevOps Server. For on-premises scenarios, use .NET client libraries, Windows authentication, or personal access tokens.\" So a Go integration targeting on-prem has exactly one option: PAT. DEPRECATION: legacy Azure DevOps OAuth (app.vssps.visualstudio.com, the flow still shown in every REST reference page's security block) is dead \u2014 \"New app registrations are no longer accepted as of April 2025. The service is scheduled for full deprecation in 2026\" and \"Existing Azure DevOps OAuth apps stop working when the service is fully deprecated in 2026.\" Build on Entra ID OAuth. Also: never parse the token \u2014 \"Starting summer 2025, Azure DevOps is further encrypting authentication tokens, which means clients can't read token payloads.\" LEAST-PRIVILEGE SCOPES for this loop: `vso.code_write` covers read PRs, create/update PRs, push, update refs, PR properties, and complete PR (it is the documented scope on every one of those endpoints); `vso.code_status` (\"Grants the ability to read and write commit and pull-request status\") for posting status; `vso.threads_full` (\"PR threads \u2014 Grants the ability to read and write to pull request comment threads\") exists as a separate scope for comments, though vso.code_write's description also claims code-review management \u2014 test which is actually enforced. Do NOT request `vso.code_manage` or `vso.code_full`: they add repository create/delete. REPO-SIDE ACL for the agent identity (separate from OAuth scopes, both are enforced): `GenericRead` + `GenericContribute` + `CreateBranch` + `PullRequestContribute` (\"Can create, comment on, and vote on pull requests\"). Add `ForcePush` only if the agent rewrites revisions \u2014 it is coarse: \"Can force an update to a branch, delete a branch, and modify the commit history of a branch. Can delete tags and notes.\" Add `ManageNote` only if using refs/notes. Never grant `PolicyExempt` or `PullRequestBypassPolicy`. GOTCHA: if the repo has 'Strict Vote Mode' on (default On) \u2014 \"requires Contribute permission to vote on pull requests\" \u2014 a read-only agent identity cannot vote.",
          "endpoints": [
            "Authorization: Basic base64(\":\"+PAT)",
            "Authorization: Bearer <entra-access-token> (Services only; resource/scope 499b84ac-1321-427f-aa17-267ca6975798)"
          ],
          "vs_github": "equivalent",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/authentication-guidance?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/organizations/security/permissions?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/repository-settings?view=azure-devops"
          ]
        },
        {
          "capability": "api-version to target in 2026, preview-vs-GA status, deprecation behaviour",
          "exists": "native",
          "detail": "TARGET `api-version=7.1` (GA). PR threads, PR iterations, PR properties, refs and pushes are all GA at 7.1 \u2014 each reference page states \"This should be set to '7.1' to use this version of the api\", none is marked preview. 7.2 exists and is the forward line, mapped to \"Azure DevOps Server vNext\". RULES: \"API version must be specified with every request.\" Format `{major}.{minor}[-{stage}[.{resource-version}]]`. It may be sent as a query parameter OR as a header: `Accept: application/json;api-version=7.1`. DEPRECATION: \"After an API is released (1.0, for example), its preview version (1.0-preview) is deprecated and can be deactivated after 12 weeks. During this time, you should upgrade to the released version of the API. Once a preview API is deactivated, requests that specify a -preview version get rejected.\" No comparable statement exists about deactivating GA versions \u2014 GA versions back to 1.0 are still listed as supported on Services, so the practical deprecation risk for 7.1 is low. DOCS ARE STALE HERE: the REST-API-versioning page (page updated 2026-07-23) carries a product/version support matrix whose columns stop at 7.0 \u2014 it does not list 7.1 or 7.2 at all. The authoritative mapping lives on a different page (the REST index), which gives: Azure DevOps Server vNext \u2192 7.2; Server 2022.1 \u2192 7.1 (builds \u2265 19.225.34309.2); Server 2022 \u2192 7.0; Server 2020 \u2192 6.0; Server 2019 \u2192 5.0. That page also states \"REST API versions are compatible with the Server version listed, as well as Server versions that are newer\". Do not use the versioning page's matrix.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/_apis/{area}/{resource}?api-version=7.1",
            "Accept: application/json;api-version=7.1"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/?view=azure-devops-rest-7.2"
          ]
        },
        {
          "capability": "Rate limiting / throttling for a polling integration (TSTU model)",
          "exists": "native",
          "detail": "Azure DevOps Services meters in TSTUs (Azure DevOps throughput units), a deliberately abstract blend of SQL DTUs, compute and storage bandwidth. NUMBERS: \"One TSTU represents the average load generated by a typical Azure DevOps user over five minutes\"; normal activity spikes \u226410 TSTU/5min; \"Larger but less frequent spikes can reach up to 100 TSTUs\"; \"The global limit is 200 TSTUs within any sliding five-minute window.\" Users are also delayed if \"their personal usage exceeds 200 times the consumption of a typical user within a sliding five-minute window.\" Azure Pipelines gets a separate 200 TSTU/5min budget per pipeline. THE TRAP FOR A POLLING INTEGRATION \u2014 throttling is applied first as SILENT LATENCY, not as an error: \"Honor the Retry-After header: If you receive it in a response, wait the specified time before sending another request. The response still returns HTTP 200, so retry logic isn't required.\" Delays \"range from a few milliseconds per request up to 30 seconds\", clear within five minutes once consumption drops, and \"If consumption stays high, delays can continue indefinitely.\" A naive client sees no error at all \u2014 just calls that get progressively slower \u2014 so JetBridge MUST read response headers on every call, not only on failures. Only when actually blocked do you get HTTP 429 with `TF400733: The request has been canceled: Request was blocked due to exceeding usage of resource <resource name> in namespace <namespace ID>.` HEADERS (all sent BEFORE delays begin, except X-RateLimit-Delay): `Retry-After` (seconds), `X-RateLimit-Resource`, `X-RateLimit-Delay` (seconds, 3dp), `X-RateLimit-Limit` (total TSTUs before delays), `X-RateLimit-Remaining` (0 if already delayed/blocked), `X-RateLimit-Reset` (unix epoch), `X-RateLimit-Cost` (TSTUs consumed by this request, 5dp). Microsoft warns X-RateLimit-Resource is unstable: \"Threshold types and service names might vary over time and without warning. We recommend displaying this string to a human, but not relying on it for computation.\" ESCAPE HATCH: assigning the integration identity the Basic + Test Plans access level raises the limits \u2014 \"Only the Basic + Test Plans access level provides an increase to these limits\", billed only while assigned. VS GITHUB: worse for capacity planning. GitHub publishes concrete request budgets you can compute against; Azure explicitly refuses to \u2014 \"You can't calculate usage in TSTUs for an action with a formula\" and \"Some operations, like work item queries, vary in consumption as your organization grows.\" You cannot derive a safe poll interval in advance; you must measure X-RateLimit-Cost empirically per endpoint and adapt. Budget for this in the design: an adaptive poller keyed on X-RateLimit-Remaining/Cost, not a fixed interval.",
          "endpoints": [
            "Response headers: Retry-After, X-RateLimit-Resource, X-RateLimit-Delay, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Cost"
          ],
          "vs_github": "worse",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rate-limits?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops"
          ]
        },
        {
          "capability": "Azure DevOps Server (on-prem) parity in 2026",
          "exists": "partial",
          "detail": "THE PRODUCT CHANGED SHAPE IN DEC 2025. The newest on-prem release went GA on 2025-12-09 and DROPPED year-based versioning \u2014 it is now just \"Azure DevOps Server\" (no 2025/2026 suffix), delivered under the Modern Lifecycle Policy with continuous updates rather than multi-year version jumps; you stay supported by staying on a supported build. Upgrade is direct \"from Azure DevOps Server RC or any supported version of Team Foundation Server (TFS 2015 and newer)\" (TFS 2015 itself hit end of support 2025-10-14). Supported platforms at GA: Windows Server 2025/2022 and SQL Server 2025/2022. A patch on 2026-03-13 fixed a group-membership deactivation bug from the GA build. REST mapping: Server vNext \u2192 7.2; Server 2022.1 \u2192 7.1; Server 2022 \u2192 7.0. GAPS THAT MATTER TO THIS DESIGN: (1) No Entra ID and no OAuth of any kind \u2014 \"OAuth 2.0 is available only for Azure DevOps Services, not Azure DevOps Server\"; on-prem auth is PAT or Windows authentication only, so the Go client needs two distinct auth paths. (2) No TSTU rate limiting \u2014 the rate-limits page applies to \"Azure DevOps Services\" only, so an on-prem poller has no documented throttle to design around (and no headers to read). (3) No Azure CLI \u2014 the branch-policies docs state flatly \"Azure DevOps CLI commands aren't supported for Azure DevOps Server\", so any tooling that shells out to `az repos` is Services-only. (4) `Require at least one approval on every iteration` requires Server 2022.1 or higher. (5) Commit author email validation and File path validation push policies require Server 2020.1 or later. (6) PublishPipelineArtifact is unsupported on-prem (\"architectural dependency on Azure Storage\"). WHAT IS THE SAME: PR properties, PR iterations, PR threads, refs, and pushes all have azure-devops-server-rest-7.1 and -7.0 doc monikers, so the core primitives this design depends on exist on-prem at 7.1.",
          "endpoints": [
            "GET https://{server}:8080/tfs/{collection}/_apis/git/repositories/{repositoryId}/refs?api-version=7.1"
          ],
          "vs_github": "not-comparable",
          "confidence": "documented",
          "sources": [
            "https://devblogs.microsoft.com/devops/announcing-azure-devops-server-general-availability/",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/?view=azure-devops-rest-7.2",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rate-limits?view=azure-devops"
          ]
        },
        {
          "capability": "Push-side validation policies the agent's pushes must satisfy",
          "exists": "native",
          "detail": "Repository-level push policies can reject the agent's pushes for reasons unrelated to content correctness, and each needs an explicit error mapping. All default Off except where noted: `Commit author email validation` \u2014 \"Block pushes with a commit author email that doesn't match the specified patterns\" (Server 2020.1+); this is the one most likely to bite a bot identity, since the agent's committer email must be allowlisted. `File path validation` \u2014 \"Block pushes from introducing file paths that match the specified patterns\" (Server 2020.1+). `Case enforcement` \u2014 \"blocking pushes that change name casing on files, folders, branches, and tags\" (relevant because Azure refs are case-insensitive-conflicting: see the `refNameConflict` update status, \"in case-insensitive mode, the ref name conflicts with an existing, differently-cased ref name\"). `Reserved names` \u2014 blocks platform-reserved names/incompatible characters. `Maximum path length` and `Maximum file size`. Separately, two HARD limits are always on and cannot be disabled or overridden: total path length 32,766 characters and path component length 4,096, rejected as `VS403729`. Also: pushes are capped at 5 GB; repositories should stay under 250 GB (10 GB recommended); individual files should be under 100 MB. One more trap worth designing around: \"When you set any policy on a branch, the following policies are automatically enforced: Pull requests are required to update the branch. The branch can't be deleted.\" \u2014 so if JetBridge ever attaches a policy to its own revision branches, it can no longer clean them up.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/policy/configurations?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/repository-settings?view=azure-devops",
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1"
          ]
        },
        {
          "capability": "Multiple-merge-base hazard affecting revision diffs",
          "exists": "native",
          "detail": "Azure exposes a correctness hazard that JetBridge must check before trusting any iteration's changeList, and GitHub has no equivalent signal. `GitPullRequest.hasMultipleMergeBases` (\"Multiple mergebases warning\") is a first-class field on the PR object. The docs explain that the Files tab diff is a three-way comparison against `commonRefCommit`, and \"in some cases, there's more than one true base. In most repositories this situation is rare, but in large repositories with many active users, it can be common.\" The documented consequences are exactly the ones that would corrupt a sealed review/v1 record: \"A malicious user could abuse the UI algorithm to commit malicious changes that aren't present in the PR\"; \"If changes proposed in the PR are already in the target branch, they're displayed in the Files tab, but they might not trigger branch policies that are mapped to folder changes\"; and \"Two sets of changes to the same files from multiple merge bases might not be present in the PR.\" There is also a documented REST-vs-UI divergence: \"While Azure DevOps is running the detection of multiple merge bases, it doesn't check if potential merge base was already merged or not... This is why Azure DevOps might display the message even when git merge-base reports only one merge base\" \u2014 i.e. the flag over-reports relative to git itself. Recommendation: treat hasMultipleMergeBases as a hard gate on sealing a revision record; when set, recompute the diff locally from commonRefCommit rather than trusting iteration changeList.",
          "endpoints": [
            "GET https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/pullrequests/{pullRequestId}?api-version=7.1"
          ],
          "vs_github": "better",
          "confidence": "documented",
          "sources": [
            "https://learn.microsoft.com/en-us/azure/devops/repos/git/merging-with-squash?view=azure-devops",
            "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1"
          ]
        }
      ],
      "absent": [
        "Merge queue / merge train / merge-train batching \u2014 Azure has only per-PR auto-complete, which does not serialize competing PRs and does not speculatively test a batched merge result. No Gerrit-style submit strategies either.",
        "Push options (`git push -o <key>=<value>`) \u2014 no support, no Azure-specific push options (no work-item linking or PR creation via push option), no Gerrit `refs/for/` magic refs, no Forgejo agit.",
        "Atomic push (`git push --atomic`) \u2014 explicitly unsupported; Azure DevOps runs a custom Git server that does not advertise the capability, and low-level receive-side git config cannot be changed even on-premises.",
        "Any REST read path for raw git commit objects \u2014 `GitCommitRef` exposes only author/committer/comment/commitId/parents/changes/statuses/workItems. A jj-style change-id commit HEADER could therefore never be read back through the API even if it survived the push; only a full clone would see it.",
        "Any REST write path for arbitrary commit headers \u2014 `Pushes - Create` composes commits server-side from `comment` + `changes`; there is no header field and no raw-object upload.",
        "Documented support for arbitrary/custom ref namespaces. Only three writable ref classes are documented, each with its own permission bit and rejection status: branches (CreateBranch), tags (CreateTag), notes (ManageNote). The branch-level security token format documented by Microsoft covers only `refs/heads/`.",
        "A documented unreachable-object garbage-collection or prune policy. This absence cuts in Azure's favour: deleted branches have no retention policy at all (\"You can restore a deleted Git branch at any time, regardless of when it was deleted\"), and the Pushes API retains every old/new object ID per ref indefinitely.",
        "A documented default squash commit message. Neither the merge-strategies page nor the completionOptions reference states what Azure composes when `mergeCommitMessage` is omitted \u2014 always set it explicitly.",
        "Microsoft Entra ID / OAuth 2.0 on Azure DevOps Server (on-prem). PAT or Windows authentication only.",
        "TSTU rate limiting on Azure DevOps Server (on-prem) \u2014 the rate-limits documentation applies to Azure DevOps Services only.",
        "Azure CLI (`az repos`, `az devops`) support against Azure DevOps Server \u2014 \"Azure DevOps CLI commands aren't supported for Azure DevOps Server.\"",
        "A maintained third-party Go client. All three community alternatives are abandoned: mikaelkrief/go-azuredevops-sdk (2019-04-01, never tagged), benmatselby/go-azuredevops v0.4.0 (2021-05-25), mcdafydd/go-azuredevops v0.12.1 (2021-10-21)."
      ],
      "notes": "HEADLINE: the borrowed model splits cleanly in two on Azure DevOps. The REVISION half is already built \u2014 do not reimplement it. The IDENTITY half must move off commit headers and onto PR Properties.\n\n1. Drop ghstack ref triples on Azure. `GitPullRequestIteration` is a native, ordered, server-maintained revision object carrying `sourceRefCommit` / `targetRefCommit` / `commonRefCommit` \u2014 literally head/base/merge-base \u2014 and `IterationReason` includes `forcePush` and `rebase`, so a new iteration is minted rather than an old one mutated across exactly the history-rewriting operations that force ghstack to exist on GitHub. GitHub has no revision noun; Azure does. Map review/v1 revisions onto iteration ids.\n\n2. Drop the jj-style change-id COMMIT HEADER entirely \u2014 it is unimplementable here for three independent reasons, and the read-path blocker alone is fatal: no Azure REST endpoint returns a raw commit object, so JetBridge could never read its own header back without a full clone on every trigger. Whether a custom header survives the git wire is undocumented; test it if you're curious, but it doesn't change the conclusion. Put the change-id in PR Properties, mirrored as a message trailer for human/CLI legibility.\n\n3. PR Properties is the single best Azure-only primitive for this design and has no GitHub analogue. Its own docs say it exists so \"third party services can... store additional information on the pull request without maintaining their own storage.\" Two constraints to design around: primitives only (serialize the revision ledger to a JSON string or base64), and a docs-vs-behaviour mismatch where a JSON number `8` comes back as `System.String \"8\"` despite the type table claiming Int32 is preserved \u2014 treat all reads as strings.\n\n4. The strongest unexpected win is durability. `POST /refs` takes `oldObjectId` and returns `staleOldObjectId` on a lost race \u2014 real per-ref compare-and-swap that GitHub's refs API cannot express. And Azure documents no retention policy on deleted branches plus a permanent per-ref push ledger (`searchCriteria.refName` + `includeRefUpdates`) recording every old/new SHA. Between them, `orig` refs become unnecessary on Azure: force-pushed revisions are recoverable from the Pushes ledger indefinitely.\n\n5. The two places Azure is genuinely worse, both of which need explicit design work rather than a shrug:\n   - NO MERGE QUEUE. On an `approved` disposition the agent must detect target movement, rebase, and re-verify itself; nothing holds a slot, and two agents can land onto a target neither tested against.\n   - RATE LIMITS ARE UNCOMPUTABLE AND SILENT. TSTUs are deliberately non-formulaic (\"You can't calculate usage in TSTUs for an action with a formula\"), and throttling arrives first as latency on HTTP 200 responses with a `Retry-After` header \u2014 not as 429. A poller that only inspects status codes will see no error, just calls degrading toward 30s each. Build an adaptive poller keyed on `X-RateLimit-Remaining` and `X-RateLimit-Cost` from the start; retrofitting this is painful. Consider the Basic + Test Plans access level on the agent identity, which is the only documented limit increase.\n\n6. GO CLIENT RECOMMENDATION \u2014 hand-roll thin REST, do not adopt the SDK. This inverts the usual advice, for a specific measured reason: `microsoft/azure-devops-go-api`'s only published v7 module is v7.1.0 from 2023-04-17, its last API-affecting commit was 2024-10-11, its sole 2026 activity is CodeQL/CI plumbing \u2014 and its generated git client hardcodes `7.1-preview.1` (114 sites) and `7.1-preview.2` (5 sites) with zero uses of GA `7.1`. Microsoft's own policy is that preview versions \"can be deactivated after 12 weeks\" post-GA and then \"requests that specify a -preview version get rejected.\" That is a live outage risk, not mere staleness. The needed surface is ~12 routes; pin `api-version=7.1` yourself. Vendoring the SDK's MIT-licensed `git/models.go` structs is fine \u2014 its `client.go` is not.\n\n7. Two correctness gates worth wiring in early because Azure surfaces signals GitHub doesn't: check `GitPullRequest.hasMultipleMergeBases` before sealing any revision record (Microsoft documents that a bad merge base lets changes appear in the Files tab that aren't in the PR and can bypass path-scoped policies), and configure the minimum-reviewers policy's \"When new changes are pushed\" behaviour deliberately \u2014 \"Require at least one approval on every iteration\" plus vote-reset semantics give a structural guard against the agent's own push re-triggering approval, which is a partial answer to the self-trigger problem proven live on GitHub.\n\n8. On-prem is a real fork in the code, not a config flag. The Dec 2025 GA release dropped year versioning and moved to the Modern Lifecycle Policy; it has no Entra ID/OAuth (PAT or Windows auth only), no TSTU rate limiting, and no `az` CLI support. Plan two auth paths and skip the throttling layer on-prem.\n\nDOCS QUALITY CAVEATS: (a) the REST-API-versioning page's product support matrix stops at 7.0 and omits 7.1/7.2 entirely despite a 2026-07-23 update \u2014 use the REST index page's Server-version table instead; (b) the default squash commit message is undocumented; (c) custom ref namespace behaviour is undocumented in both directions \u2014 the Refs-List sample response does show `refs/remotes/*` entries, proving the store can hold non-heads refs, but nothing says a push can create them; (d) every REST reference page still advertises the legacy `app.vssps.visualstudio.com` OAuth flow in its security block even though that service stopped accepting registrations in April 2025 and is scheduled for full deprecation in 2026.\n\nUNVERIFIED / NEXT TESTS (cheap, high-value, all need a scratch Azure Repos project): (1) POST a ref named `refs/jetbridge/test` and read `updateStatus`; (2) complete a squash PR with `mergeCommitMessage` omitted and read back `lastMergeCommit.comment`; (3) push a commit carrying a custom header, re-clone, `git cat-file commit HEAD`; (4) POST a multi-element `GitRefUpdate[]` where one element is deliberately stale, and check whether the others still applied (determines whether the batch is transactional \u2014 the response shape suggests not); (5) measure `X-RateLimit-Cost` for GetThreads / GetPullRequestIterations to size the poll loop."
    },
    "checks": [
      {
        "area": "git-and-clients",
        "claims_checked": [
          {
            "claim": "Ref CAS: POST/GET `_apis/git/repositories/{repositoryId}/refs` at api-version=7.1, GitRefUpdate[] body {name, oldObjectId, newObjectId, isLocked, repositoryId}, race-condition sentence, GitRefUpdateResult fields, 16-value GitRefUpdateStatus enum.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1 \u2014 route is exactly `POST https://dev.azure.com/{organization}/{project}/_apis/git/repositories/{repositoryId}/refs?api-version=7.1`; page header says 'API Version: 7.1' with no preview marker; verbatim: 'Updating a ref means making it point at a different commit than it used to. You must specify both the old and new commit to avoid race conditions.' GitRefUpdate fields are exactly isLocked/name/newObjectId/oldObjectId/repositoryId. rejectedBy description is verbatim 'Name of the plugin that rejected the updated.' The enum has exactly the 16 quoted values in the quoted order, and staleOldObjectId's description confirms the CAS semantics: '...the old object ID presented in the request was not the object ID of the ref when the database attempted the update. The most likely scenario is that the caller lost a race to update the ref.' SERVER PARITY: azure-devops-server-rest-7.1 is listed in Other Supported Versions. Refs-List route/params also confirmed at https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list?view=azure-devops-rest-7.1 ($top max 1000, continuationToken, filter, filterContains)."
          },
          {
            "claim": "Sub-claim: 'Creation uses oldObjectId of 40 zeros; deletion uses newObjectId of 40 zeros.'",
            "status": "OVERSTATED",
            "evidence": "Creation is documented \u2014 the Update-Refs sample request posts oldObjectId '0000000000000000000000000000000000000000' to create refs/heads/vsts-api-sample/answer-woman-flame. The DELETION convention (all-zero newObjectId) is NOT stated or sampled anywhere on that page. It is only inferable from two enum descriptions that mention deletes ('succeededNonExistentRef ... This should only happen during deletes', 'succeededCorruptRef ... This should only happen during deletes'). Documented for create, inferred for delete."
          },
          {
            "claim": "Sub-claim: GitHub's `PATCH /repos/{o}/{r}/git/refs/{ref}` has no expected-old-SHA parameter, only force true/false, so a lost race is undetectable there.",
            "status": "UNVERIFIABLE",
            "evidence": "Out of scope for this lens \u2014 no Microsoft Learn page or azure-devops SDK type definition can establish a GitHub API shape. It matches my knowledge of GitHub's documented request body (`sha`, `force`), but I could not locate it in an official source within this pass. The Azure half of the comparison is solid; the GitHub half is unsourced in the claim."
          },
          {
            "claim": "Arbitrary ref namespaces (refs/jetbridge/...) are absent: only branches/tags/notes are writable ref classes, each with its own permission bit and rejection status; invalidRefName exists; branch ACL token format covers only refs/heads/; but refs/remotes/* appear in the Refs-List sample.",
            "status": "CONFIRMED",
            "evidence": "Every supporting quote is literal. Permissions page (https://learn.microsoft.com/en-us/azure/devops/organizations/security/permissions?view=azure-devops): 'Create branch' = 'Can create and publish branches in the repository...'; 'Create tag' = 'Can push tags to the repository.'; 'Manage notes' = 'Can push and edit Git notes.' Update-Refs enum contains createBranchPermissionRequired, createTagPermissionRequired, manageNotePermissionRequired, invalidRefName and no equivalent for any other namespace. Refs-List sample response does contain refs/remotes/origin/HEAD, refs/remotes/origin/feature/replacer and refs/remotes/origin/master, so the store can hold non-heads/non-tags refs. Namespace-reference (https://learn.microsoft.com/en-us/azure/devops/organizations/security/namespace-reference?view=azure-devops) gives Git Repositories token forms 'repoV2/PROJECT_ID' and 'repoV2/PROJECT_ID/REPO_ID' and explicitly delegates branch-level tokens to the devblogs post. That post (https://devblogs.microsoft.com/devops/git-repo-tokens-for-the-security-service/) shows exactly 'repoV2/{projectGuid}/{repoGuid}/refs/heads/6d0061007300740065007200/' with UTF-16LE hex per path segment, and mentions no refs/tags, refs/notes, or custom namespace. The claim's self-assigned confidence 'uncertain' and its 'test before relying on it' instruction are the correct posture; nothing here is a fabricated endpoint."
          },
          {
            "claim": "Durability: 'There's no retention policy on deleted branches. You can restore a deleted Git branch at any time, regardless of when it was deleted'; restore recreates at the last commit; policies/permissions are not restored; Pushes API exposes per-ref oldObjectId/newObjectId; docs direct users to the Pushes page.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/restore-deleted-branch?view=azure-devops carries all of it verbatim, including 'The branch gets recreated at the last commit to which it pointed. Branch policies and permissions do not get restored.' and 'go to the Pushes page of the restored branch to see the entire history of the branch' and 'You can go to a specific commit, then select New branch from the ... icon.' Applies to Azure DevOps Services | Azure DevOps Server | Azure DevOps Server 2022. Pushes-List (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/list?view=azure-devops-rest-7.1) confirms searchCriteria.refName and searchCriteria.includeRefUpdates ('If true, include the list of refs that were updated by the push'), with $top/$skip as TOP-LEVEL params (not searchCriteria-prefixed) exactly as the claim wrote them; GitPush.refUpdates is GitRefUpdate[] with oldObjectId/newObjectId, and a sample response shows a non-zero\u2192non-zero ref update. Server 7.1 parity present."
          },
          {
            "claim": "Durability, load-bearing part: the Pushes ledger is 'permanent', it is 'a server-side reflog that survives force-push and branch deletion', there is NO documented unreachable-object GC/prune policy 'anywhere', and force-pushed commits are retained indefinitely \u2014 such that JetBridge can drop ghstack `orig` refs and reconstruct the revision series from Pushes history.",
            "status": "OVERSTATED",
            "evidence": "No Microsoft Learn page states that push records are permanent, that they survive branch deletion, or that unreachable objects are never pruned. The Git limits page (https://learn.microsoft.com/en-us/azure/devops/repos/git/limits?view=azure-devops) says only 'Azure Repos continuously reduces the overall size and increases the efficiency of Git repositories by consolidating similar files into packs' \u2014 that quote is verbatim and correct, but it is about repacking, not retention, and the page's only appearance of the word 'prune' is inside a client-side `git count-objects -vH` sample output ('prune-packable: 83'), which is not an Azure statement at all. The claim itself concedes the object-level guarantee is 'inferred', yet the entry is filed as confidence:'documented' and exists:'native' \u2014 those two fields contradict the body text. Argument-from-absence over the whole of learn.microsoft.com is not verifiable in principle. Additionally: the limits page moniker is `azure-devops` only (header reads '**Azure DevOps Services**'), so even the weak absence evidence has zero Azure DevOps Server coverage. Recommend downgrading to inferred and requiring a live force-push-then-fetch-old-SHA test before dropping `orig` refs."
          },
          {
            "claim": "No push options (git push -o), no refs/for magic ref, no agit equivalent; revision metadata must travel out-of-band via REST.",
            "status": "CONFIRMED",
            "evidence": "I read both cited Learn pages in full. https://learn.microsoft.com/en-us/azure/devops/repos/git/pushing?view=azure-devops contains no occurrence of push options, `-o`, `--atomic`, `refs/for`, or agit \u2014 its command-line tab covers only git push forms, --set-upstream and --force. https://learn.microsoft.com/en-us/azure/devops/repos/git/command-prompt?view=azure-devops likewise has none of them (its Sync-changes table lists only fetch/pull/push/push --force). The absence is real on both cited pages."
          },
          {
            "claim": "Sub-claim: 'The only push-side affordance documented is cosmetic: pushing a new branch makes the git output include a URL that opens the create-PR page.'",
            "status": "REFUTED",
            "evidence": "Not present on either cited page. The pushing page's closest sentence is 'Once you've pushed your commits, you can create a pull request to let others know you'd like to have your changes reviewed' \u2014 no mention of a URL in git output. The command-prompt page says nothing about it either. The only push-output text quoted anywhere on the pushing page is a Visual Studio Team Explorer message: 'The current branch does not track a remote branch...'. This sub-claim is not supported by the sources given."
          },
          {
            "claim": "Sub-claim: the --atomic evidence and the 'Azure DevOps runs a custom (non-canonical) Git server' structural reason.",
            "status": "OVERSTATED",
            "evidence": "The cited page exists (https://learn.microsoft.com/en-us/answers/questions/2262768/, posted 2025-05-01) and does contain 'Azure DevOps Repos currently does not support atomic Git pushes (git push --atomic)' and 'Azure DevOps uses a custom Git server that doesn't advertise support for --atomic'. BUT the answer is attributed to 'Anonymous' on Microsoft Q&A \u2014 community-contributed content on a learn.microsoft.com domain, not Microsoft product documentation and not a generated SDK type. Presenting it as 'a documented absence of the sibling capability' overstates its authority. There is no first-party Learn statement anywhere that Azure Repos runs a non-canonical Git server or refuses receive-side capability configuration."
          },
          {
            "claim": "PR Properties: GET/PATCH `.../pullRequests/{pullRequestId}/properties?api-version=7.1`; 'This API provides a way to manage external properties associated with a pull request. Third party services can use this API to store additional information on the pull request without maintaining their own storage.'; JSON Patch with application/json-patch+json; add/replace/remove semantics; PropertiesCollection primitives-only; Microsoft.Git.PullRequest.* pre-populated keys.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties?view=azure-devops-rest-7.1 carries the description sentence verbatim. https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1 confirms the exact route, 'Media Types: \"application/json-patch+json\"', and verbatim: 'For add operation, the path can be empty. If the path is empty, the value must be a list of key value pairs. For replace operation, the path cannot be empty. If the path does not exist, the property will be added to the collection. For remove operation, the path cannot be empty. If the path does not exist, no action will be performed.' PropertiesCollection definition is verbatim: 'Values of all primitive types (any type with a TypeCode != TypeCode.Object) except for DBNull are accepted. Values of type Byte[], Int32, Double, DateType and String preserve their type, other primitives are retuned as a String. Byte[] expected as base64 encoded string.' (Microsoft's own typos 'DateType'/'retuned' reproduced faithfully by the claim.) Sample response contains Microsoft.Git.PullRequest.SourceRefName and Microsoft.Git.PullRequest.TargetRefName. GA at 7.1, and azure-devops-server-rest-7.1 is listed \u2014 on-prem parity holds."
          },
          {
            "claim": "PR Properties docs-vs-behaviour catch: Microsoft's own sample sends \"value\": 8 and the sample response returns \"sampleId\": {\"$type\": \"System.String\", \"$value\": \"8\"} \u2014 a JSON number came back as a string.",
            "status": "CONFIRMED",
            "evidence": "Exactly reproduced in the Update page's 'Add properties' example. The 'Remove and replace' example repeats it: request sends \"value\": 12, response returns \"sampleId\": {\"$type\": \"System.String\", \"$value\": \"12\"}. Note the same response DOES preserve a type for startedDateTime ('$type': 'System.DateTime'), so the failure is specific to numerics. Worth adding one nuance the claim missed: the request-body definition describes `value` as 'This is either a primitive or a JToken', which sits in mild tension with the primitives-only PropertiesCollection rule \u2014 and the third sample op posts an OBJECT at path \"\", which the service flattens into separate top-level string keys rather than storing nested. The claim's 'treat every read as a string and parse client-side' guidance is the right conclusion."
          },
          {
            "claim": "Iterations are a native revision noun: GitPullRequestIteration carries id, sourceRefCommit, targetRefCommit, commonRefCommit ('The first common Git commit of the source and target refs'), push, commits, changeList, createdDate, author, reason; IterationReason enum = push, forcePush, create, rebase, unknown, retarget, resolveConflicts; 'Iterations are created as a result of creating and pushing updates to a pull request'; supportsIterations description; GitPullRequestChange.changeTrackingId = 'ID used to track files through multiple changes'.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 \u2014 route `GET .../pullRequests/{pullRequestId}/iterations?includeCommits={includeCommits}&api-version=7.1` exact; every named field present with the quoted descriptions verbatim; IterationReason lists exactly those seven values in exactly that order; the operation-group blurb is verbatim 'Iterations are created as a result of creating and pushing updates to a pull request.' supportsIterations verbatim on https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/update?view=azure-devops-rest-7.1: 'If true, this pull request supports multiple iterations. Iteration support means individual pushes to the source branch of the pull request can be reviewed and comments left in one iteration will be tracked across future iterations.' changeTrackingId verbatim. The second cited endpoint `.../iterations/{iterationId}/changes` also exists and is GA at 7.1 (https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iteration-changes/get?view=azure-devops-rest-7.1) with $top max 2000, $skip, and $compareTo whose default 'zero ... indicates the comparison is made against the common commit between the source and target branches'. Server 7.1 parity on both."
          },
          {
            "claim": "Sub-claim: iteration id is an 'ordinal, 1-based, monotonically increasing'.",
            "status": "CONFIRMED",
            "evidence": "True, but NOT on the page the claim cites. The iterations/list page describes id only as 'ID of the pull request iteration.' The 1-based ordinal is documented on a different page \u2014 Pull Request Iteration Changes - Get, iterationId parameter: 'Iteration one is the head of the source branch at the time the pull request is created and subsequent iterations are created when there are pushes to the source branch. Allowed values are between 1 and the maximum iteration on this pull request.' Cite that page instead."
          },
          {
            "claim": "Sub-claim: 'The forcePush and rebase reasons are the load-bearing detail \u2014 Azure mints a NEW iteration on force-push and on server-side rebase rather than mutating the old one.'",
            "status": "OVERSTATED",
            "evidence": "The enum VALUES forcePush and rebase are documented; their SEMANTICS are not. Every IterationReason row on the 7.1 page has an EMPTY description cell. The 'mints a new iteration rather than mutating the old one' behaviour is an inference from the value names plus 'Iterations are created as a result of creating and pushing updates'. This is the single most load-bearing design assumption in the whole claim set and it rests on undocumented enum semantics \u2014 it should be labelled inferred and confirmed empirically against a real org before revision identity is mapped onto iteration ids."
          },
          {
            "claim": "Votes bound to revisions: four documented 'When new changes are pushed' behaviours with the exact quoted wording; 'Require at least one approval on every iteration' is Azure DevOps Server 2022.1+; policy re-evaluation is event-driven; IdentityRefWithVote.vote is int16 with 10/5/0/-5/-10.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/branch-policies?view=azure-devops (applies to Azure DevOps Services | Azure DevOps Server | Azure DevOps Server 2022) has all four bullets verbatim, including 'The user's approval isn't counted against any previous unapproved iteration pushed by that user. As a result, another approval on the last iteration is required to be done by another user.' and the version note 'Require at least one approval on every iteration is available in Azure DevOps Server 2022.1 and higher.' and 'Reset all approval votes (does not reset votes to reject or wait)' and 'Reset all code reviewer votes'. Re-evaluation sentence verbatim: 'The server reevaluates branch policies when pull request owners push changes and when reviewers vote.' Vote enum verbatim from IdentityRefWithVote: 'Vote on a pull request: 10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected'. Both cited endpoints exist: PUT .../pullRequests/{pullRequestId}/reviewers/{reviewerId}?api-version=7.1 is 'Create Pull Request Reviewer \u2014 Add a reviewer to a pull request or cast a vote', with a sample body {\"vote\": 10, \"id\": ...}; Server 7.1 parity listed."
          },
          {
            "claim": "Authentication: managed identity > service principal > service connection > PAT ordering with 'Strongest option for Azure-hosted workloads'; 'Use personal access tokens sparingly, and only when Microsoft Entra ID isn't available'; the Services-vs-Server split; legacy Azure DevOps OAuth deprecation dates; token opacity; scope set; repo ACL internal names; Strict Vote Mode default On.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/authentication-guidance?view=azure-devops has verbatim: 'Use personal access tokens sparingly, and only when Microsoft Entra ID isn't available.', 'OAuth 2.0 and Microsoft Entra ID authentication are available for Azure DevOps Services only, not Azure DevOps Server.', 'For on-premises scenarios, use .NET client libraries, Windows authentication, or personal access tokens.', 'Strongest option for Azure-hosted workloads because tokens are short-lived and Azure manages the identity lifecycle', and 'Starting summer 2025, Azure DevOps is further encrypting authentication tokens, which means clients can't read token payloads.' https://learn.microsoft.com/en-us/azure/devops/integrate/get-started/authentication/oauth?view=azure-devops has verbatim: 'New app registrations are no longer accepted as of April 2025. The service is scheduled for full deprecation in 2026.' and 'Existing Azure DevOps OAuth apps stop working when the service is fully deprecated in 2026.' Its scope table gives vso.code_status = 'Grants the ability to read and write commit and pull-request status.' and vso.threads_full (name 'PR threads') = 'Grants the ability to read and write to pull request comment threads.', and confirms vso.code_manage/vso.code_full add repository create/manage. vso.code_write is the listed scope on Update-Refs, PR-Properties-Update, PR-Update and Create-Pull-Request-Reviewer \u2014 the claim's 'documented scope on every one of those endpoints' checks out. All repo ACL internal names exist verbatim in the Git Repositories namespace: Administer, GenericRead, GenericContribute, ForcePush, CreateBranch, CreateTag, ManageNote, PolicyExempt, CreateRepository, DeleteRepository, RenameRepository, EditPolicies, RemoveOthersLocks, ManagePermissions, PullRequestContribute, PullRequestBypassPolicy. 'Contribute to pull requests' = 'Can create, comment on, and vote on pull requests.' and the ForcePush description are verbatim. Strict Vote Mode is documented with default **On**: 'Enable Strict Vote Mode for the repository, which requires Contribute permission to vote on pull requests.'"
          },
          {
            "claim": "Auth sub-claims: PAT wire format is `Authorization: Basic base64(\":\" + PAT)` with an empty username; and the Entra resource/scope GUID 499b84ac-1321-427f-aa17-267ca6975798.",
            "status": "OVERSTATED",
            "evidence": "The empty-username Basic format is CORRECT but is not on the cited authentication-guidance page \u2014 it is on the REST index (https://learn.microsoft.com/en-us/rest/api/azure/devops/?view=azure-devops-rest-7.2), which shows `Authorization: Basic BASE64PATSTRING` and C# `string.Format(\"{0}:{1}\", \"\", personalaccesstoken)` \u2014 literally an empty username. Cite that page. The Entra resource GUID 499b84ac-1321-427f-aa17-267ca6975798 is UNVERIFIABLE in this pass \u2014 I could not locate it on any Learn page I read, and it appears in the claim's 'endpoints' array with no source attached. Do not ship it without a citation. Also note the OAuth scope table page carries moniker `azure-devops` only (header '**Azure DevOps Services**'), so the scope vocabulary is documented for Services and not for Server."
          },
          {
            "claim": "api-version 7.1 is GA and the right target; 'API version must be specified with every request'; format {major}.{minor}[-{stage}[.{resource-version}]]; Accept-header form; preview deprecated 12 weeks after GA; the versioning page's support matrix is STALE (stops at 7.0) while the REST index gives vNext\u21927.2, Server 2022.1\u21927.1 (builds >= 19.225.34309.2), 2022\u21927.0, 2020\u21926.0, 2019\u21925.0.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rest-api-versioning?view=azure-devops \u2014 verbatim 'API version **must** be specified with every request.', 'API versions are in the format {major}.{minor}[-{stage}[.{resource-version}]]', 'After an API is released (1.0, for example), its preview version (1.0-preview) is deprecated and can be deactivated after 12 weeks.', 'Once a preview API is deactivated, requests that specify a -preview version get rejected.' The stale-matrix catch is CORRECT and well spotted: page updated_at is 2026-07-23 and its Supported-versions table columns run 1.0\u20267.0 with no 7.1 or 7.2 row/column. The REST index (?view=azure-devops-rest-7.2) carries the authoritative mapping table exactly as claimed, including build 19.225.34309.2 for Server 2022.1\u21927.1, plus verbatim 'REST API versions are compatible with the Server version listed, as well as Server versions that are newer than the Server version listed.' Independently verified GA-not-preview for every endpoint in this claim set: refs, pushes, PR iterations, PR iteration changes, PR properties, PR update, PR reviewers and policy configurations all render 'API Version: 7.1' with no preview marker and all list azure-devops-server-rest-7.1."
          },
          {
            "claim": "Sub-claim: the Accept-header form is `Accept: application/json;api-version=7.1`.",
            "status": "OVERSTATED",
            "evidence": "The mechanism is documented but the doc's literal example is `Accept: application/json;api-version=1.0` \u2014 the claim silently substituted 7.1. Harmless in practice, but it is a value quoted from inference rather than from the page. Worth flagging a further doc defect the claim did NOT catch: the same page's 'Uri query parameter' example is itself malformed \u2014 it prints `GET https://dev.azure.com/v1.0/{organization}/_apis/{area}/{resource}?some-query=1000`, which is not a working Azure DevOps URL shape and contradicts the REST index's correct `VERB https://dev.azure.com/{organization}/_apis[/{area}]/{resource}?api-version={version}`. The versioning page is unreliable in two independent ways, not one."
          },
          {
            "claim": "Rate limiting: TSTU model, 10/100/200 TSTU figures, 200x-typical-user rule, per-pipeline 200 TSTU, Retry-After returning HTTP 200 (silent latency), delay range up to 30 seconds, indefinite delays, TF400733 with HTTP 429, the seven X-RateLimit/Retry-After headers and their descriptions, the X-RateLimit-Resource instability warning, the Basic + Test Plans escape hatch, and 'You can't calculate usage in TSTUs for an action with a formula'.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rate-limits?view=azure-devops reproduces every one of these verbatim, including 'One TSTU represents the average load generated by a typical Azure DevOps user over five minutes.', 'Normal user activity can generate spikes of 10 TSTUs or fewer per five minutes.', 'Larger but less frequent spikes can reach up to 100 TSTUs.', 'The global limit is 200 TSTUs within any sliding five-minute window.', 'Their personal usage exceeds 200 times the consumption of a typical user within a sliding five-minute window.', 'Honor the Retry-After header: If you receive it in a response, wait the specified time before sending another request. The response still returns HTTP 200, so retry logic isn't required.', 'Delays range from a few milliseconds per request up to 30 seconds.', 'If consumption stays high, delays can continue indefinitely to protect the resource.', the TF400733 string under 'HTTP code 429 (too many requests)', 'There's a 200 TSTU limit for each pipeline in a sliding 5-minute window.', 'Except for X-RateLimit-Delay, all these headers are sent before requests start getting delayed.', 'Threshold types and service names might vary over time and without warning. We recommend displaying this string to a human, but not relying on it for computation.', 'Only the Basic + Test Plans access level provides an increase to these limits.', and 'You can't calculate usage in TSTUs for an action with a formula'. All seven header descriptions match. The claim's adaptive-poller conclusion is well founded."
          },
          {
            "claim": "Rate limiting sub-claim: exists = 'native' (i.e. applies to the platform generally).",
            "status": "OVERSTATED",
            "evidence": "The rate-limits page carries moniker `azure-devops` ONLY and its product line reads '**Azure DevOps Services**'. The entire TSTU model, the 429/TF400733 behaviour and the X-RateLimit header contract are documented for Azure DevOps Services and NOT for Azure DevOps Server. Since this research must be explicit about Services-vs-Server, the entry should say so: an on-prem JetBridge deployment has no documented throttling contract at all, which means the adaptive poller keyed on X-RateLimit-Cost cannot be assumed to have anything to read against Server."
          },
          {
            "claim": "Push-side validation policies: six repository settings with their exact descriptions and Off defaults, Server 2020.1 notes for email/path validation, case enforcement tying to refNameConflict, hard path limits 32,766 / 4,096 with VS403729, 5 GB pushes, 250 GB / 10 GB repos, 100 MB files, and 'When you set any policy on a branch, the following policies are automatically enforced: Pull requests are required to update the branch. The branch can't be deleted.'",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/repository-settings?view=azure-devops has all six with default **Off** and verbatim text: 'Block pushes with a commit author email that doesn't match the specified patterns. This setting requires Azure DevOps Server 2020.1 or later version.'; 'Block pushes from introducing file paths that match the specified patterns. This setting requires Azure DevOps Server 2020.1 or later version.'; 'Avoid case-sensitivity conflicts by blocking pushes that change name casing on files, folders, branches, and tags.'; 'Block pushes that introduce files, folders, or branch names that include platform reserved names or incompatible characters.'; 'Block pushes that introduce paths that exceed the specified length.'; 'Block pushes that contain new or updated files larger than the selected limit.' The automatically-enforced pair is verbatim on the same page, in the Branch policies section \u2014 and the page adds a directly relevant warning the claim missed: 'Don't set branch policies on temporary branches that you plan to delete after you make a pull request. Adding branch policies to temporary branches causes automatic branch deletion to fail.' refNameConflict description is verbatim from Update-Refs. Git limits page confirms 32,766 / 4,096, both VS403729 messages, 'pushes are limited to 5 GB at a time', 'Repositories should be no larger than 250 GB', 'below 10 GB for optimal performance', 'Files should be no larger than 100 MB', and 'you can't disable or override it'."
          },
          {
            "claim": "Push-policy sub-claims: exists = 'native' for the hard limits, and the endpoint `GET .../_apis/policy/configurations?api-version=7.1` for discovering them.",
            "status": "OVERSTATED",
            "evidence": "Two corrections. (1) SERVER PARITY: the Git limits page is moniker `azure-devops` only, header '**Azure DevOps Services**' \u2014 the 32,766/4,096 hard limits, VS403729, the 5 GB push cap and the size guidance are documented for Services only, with no Server statement. The repository-settings policies (the six Off-by-default ones) DO cover Services | Server | Server 2022, so the two halves of this entry have different product coverage and should not be presented as one 'native' capability. (2) The cited endpoint exists and is GA at 7.1 (https://learn.microsoft.com/en-us/rest/api/azure/devops/policy/configurations/list?view=azure-devops-rest-7.1, project is a REQUIRED path param, Server 7.1 listed) \u2014 but the page itself warns against using it the way the claim implies: 'The scope parameter for this API should not be used, except for legacy compatability reasons. It returns specifically scoped policies and does not support heirarchical nesting. Instead, use the /_apis/git/policy/configurations API, which provides first class scope filtering support.' For repo/branch-scoped policy discovery JetBridge should call the git-scoped variant."
          },
          {
            "claim": "Multiple merge bases: hasMultipleMergeBases is a first-class PR field; the Files tab is a three-way comparison; 'in some cases, there's more than one true base... in large repositories with many active users, it can be common'; the three security consequences; and the over-reporting caveat vs git merge-base.",
            "status": "CONFIRMED",
            "evidence": "https://learn.microsoft.com/en-us/azure/devops/repos/git/merging-with-squash?view=azure-devops (Services | Server | Server 2022) carries all of it verbatim, including the three bullets word-for-word: 'A malicious user could abuse the UI algorithm to commit malicious changes that aren't present in the PR.', 'If changes proposed in the PR are already in the target branch, they're displayed in the Files tab, but they might not trigger branch policies that are mapped to folder changes.', 'Two sets of changes to the same files from multiple merge bases might not be present in the PR. That case might create treacherous logic gaps.' And verbatim: 'While Azure DevOps is running the detection of multiple merge bases, it doesn't check if potential merge base was already merged or not. Such check is done by git merge-base. This is why Azure DevOps might display the message even when git merge-base reports only one merge base.' hasMultipleMergeBases is confirmed present on GitPullRequest at 7.1. The claim's 'treat it as a hard gate on sealing a revision record' recommendation is well supported."
          },
          {
            "claim": "Multiple-merge-base sub-claims: that the Files-tab three-way comparison is 'against commonRefCommit', and that the REST boolean hasMultipleMergeBases is the same signal as the UI detection.",
            "status": "OVERSTATED",
            "evidence": "Both are reasonable but neither is stated. merging-with-squash never names commonRefCommit \u2014 it says the algorithm uses 'the last commit in the target branch, the last commit in the source branch, and their common merge base'. The identification with GitPullRequestIteration.commonRefCommit ('The first common Git commit of the source and target refs') is the reader's inference. Separately, hasMultipleMergeBases has exactly four words of documentation on the REST page \u2014 'Multiple mergebases warning' \u2014 and no page links it to the UI detection described in the conceptual article; the conceptual article names only the UI string 'Multiple merge bases detected. The list of commits displayed might be incomplete' and never mentions the REST field. The linkage is almost certainly right, but it is inferred, not documented, and the claim files the entry as confidence:'documented'."
          }
        ]
      },
      {
        "area": "git-and-clients",
        "claims_checked": [
          {
            "claim": "Refs POST accepts GitRefUpdate[] with compare-and-swap semantics; docs state the CAS contract explicitly; response is GitRefUpdateResult[] with a 16-value GitRefUpdateStatus enum including staleOldObjectId.",
            "status": "CONFIRMED",
            "evidence": "Fetched https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1 (api-version 7.1, GA, no preview marker; scope vso.code_write). Verbatim: \"Updating a ref means making it point at a different commit than it used to. You must specify both the old and new commit to avoid race conditions.\" All 16 enum values present exactly as claimed. staleOldObjectId verbatim: \"Indicates that the ref update request could not be completed because the old object ID presented in the request was not the object ID of the ref when the database attempted the update. The most likely scenario is that the caller lost a race to update the ref.\" GitRefUpdate body fields {isLocked,name,newObjectId,oldObjectId,repositoryId} and GitRefUpdateResult fields incl. rejectedBy \"Name of the plugin that rejected the updated\" confirmed verbatim."
          },
          {
            "claim": "This is strictly better than GitHub; on GitHub \"JetBridge cannot detect a lost race\" because PATCH /repos/{o}/{r}/git/refs has no expected-old-SHA.",
            "status": "OVERSTATED",
            "evidence": "True of GitHub's REST ref endpoint, but the conclusion does not follow. `git push --force-with-lease` sends the expected old-oid in the wire-protocol ref-update command and is enforced server-side by any conformant Git server, GitHub included \u2014 so a lost race IS detectable on GitHub via the git client path. Meanwhile the Azure side is the one with the weaker wire-level story: Microsoft's own Q&A on --atomic (https://learn.microsoft.com/en-us/answers/questions/2262768/) states Azure Repos runs a custom Git server that does not advertise --atomic, so ADO's git-protocol capability set is the non-standard one. Separately, the claim omits the operative trap: Update-Refs documents only \"200 OK | GitRefUpdateResult[] | successful operation\" and the sample returns per-element \"success\": true / \"updateStatus\". A partial or total failure (staleOldObjectId) still returns HTTP 200 with success:false per element. A Go client that checks only resp.StatusCode silently loses the race it was trying to detect. Net: Azure is better at the REST layer, not \"strictly better\", and the CAS win is forfeited by naive status-code handling."
          },
          {
            "claim": "Arbitrary ref namespaces (refs/jetbridge/changes/<id>/<n>) are absent \u2014 only branches, tags and notes are writable; test with POST /refs and expect invalidRefName or writePermissionRequired.",
            "status": "UNVERIFIABLE",
            "evidence": "No community report \u2014 Stack Overflow, GitHub issues on microsoft/azure-devops-*, or Developer Community \u2014 was found either creating or being refused a custom ref namespace on Azure Repos. The claim's own confidence (\"uncertain\") is the correct level and its recommended empirical test is the right call. One piece of counter-evidence in the direction of \"the backend can hold and serve them\": Azure Repos serves server-created `refs/pull/{id}/merge` refs, which Azure Pipelines fetches routinely for PR builds \u2014 so non-heads/non-tags refs are readable and fetchable. That still says nothing about whether a client-initiated POST /refs or push can create one. Do not promote this claim above \"uncertain\" without running the test."
          },
          {
            "claim": "Force-pushed / unreachable commits survive on Azure Repos; there is no documented GC/prune; \"There's no retention policy on deleted branches. You can restore a deleted Git branch at any time\"; Pushes API is a permanent server-side reflog.",
            "status": "CONFIRMED",
            "evidence": "Stronger than the claim asserted, and now documented rather than inferred at the object level. https://learn.microsoft.com/en-us/azure/devops/repos/git/restore-deleted-branch?view=azure-devops confirms verbatim: \"There's no retention policy on deleted branches. You can restore a deleted Git branch at any time, regardless of when it was deleted.\" and \"The branch gets recreated at the last commit to which it pointed. Branch policies and permissions do not get restored.\" and \"go to the Pushes page of the restored branch to see the entire history of the branch\" and \"You can go to a specific commit, then select New branch\". https://learn.microsoft.com/en-us/azure/devops/repos/git/remove-binaries?view=azure-devops states verbatim: \"The following steps remove the video from your branch history, but the file remains in your repo history when you clone your repo from Azure Repos.\" It links to an archived Microsoft VSTS-Git-team post (https://learn.microsoft.com/en-us/archive/blogs/congyiw/why-does-cloning-from-vsts-return-old-unreferenced-objects) which states verbatim: \"There is no equivalent to `git gc` on VSTS yet. Our server preserves the history of every ref/branch update to Git repos, including deleted branches. This is analogous to the 'reflog' in core Git. On VSTS, we expose the reflog via the REST API and the Branch Updates (i.e. pushes) tab in Web Access. Similarly to core Git, objects in the reflog are still considered to be referenced and will not be deleted by `git gc`. Core Git can eventually prune old reflog entries via `git prune` or `git gc`, but VSTS does not have that functionality yet.\" The 2017 update adds: \"We still don't have true object-level `git gc` on the server yet.\" Pushes-List (api-version 7.1, GA, scope vso.code) confirms searchCriteria.refName / includeRefUpdates (\"If true, include the list of refs that were updated by the push\") / pusherId / fromDate / toDate / $top / $skip, and the sample response shows per-update oldObjectId/newObjectId. No retention limit on push history is documented."
          },
          {
            "claim": "Therefore JetBridge can drop ghstack-style `orig` refs on Azure and reconstruct the revision series from Pushes history.",
            "status": "OVERSTATED",
            "evidence": "The ledger survives; the ability to MATERIALIZE an old revision by ordinary git does not. The same archived post's 2017 update says verbatim: \"We rolled out commit reachability bitmap indexes to VSTS and removed the clone cheat mentioned below. Cloning will no longer download unreachable objects!\" Its comment thread gives the Microsoft-authored repro: after deleting a branch, in a fresh clone \"git catfile -p abc123 should say that abc123 is not valid (unlike in the past when abc123 could get cloned even if NewBranch was deleted)\". So a dereferenced revision is retained server-side but is NOT in any clone, and fetch-by-unadvertised-SHA is not documented as supported on Azure Repos (uploadpack.allowReachableSHA1InWant/allowAnySHA1InWant are never mentioned for ADO; ADO's git server is custom and does not expose receive/upload-pack capability config). The documented recovery path is the one the branch-restore doc gives: create a ref at the commit (\"go to a specific commit, then select New branch\") \u2014 i.e. POST /refs. So dropping `orig` refs is safe for AUDIT (the Pushes ledger records the SHAs forever) but NOT for CHECKOUT: JetBridge must re-create a ref before it can fetch an old revision. Also note the retention evidence is a 2015 post updated 2017, marked archived/NOINDEX, and hedged with \"yet\" three times \u2014 it is a statement of past implementation, not a durability SLA."
          },
          {
            "claim": "Push options (git push -o), Gerrit refs/for, Forgejo agit are absent; Azure runs a custom Git server that does not expose receive-side capabilities.",
            "status": "CONFIRMED",
            "evidence": "Confirmed for the sibling capability and for the structural reason. Microsoft Q&A https://learn.microsoft.com/en-us/answers/questions/2262768/(ado)-(repo)-support-for-git-push-atomic states Azure Repos does not support `git push --atomic` because Azure DevOps uses a custom Git server that does not advertise support for it. No refs/for or agit equivalent exists anywhere in learn.microsoft.com. The push-options half specifically remains an argument from absence of documentation \u2014 correctly labelled \"inferred\" in the claim \u2014 and no community report was found of anyone successfully using `git push -o` against Azure Repos. The design consequence stands regardless: revision metadata must travel out-of-band."
          },
          {
            "claim": "Pull Request Properties is a native key/value bag for third-party state; primitives only; docs-vs-behaviour divergence where a JSON number comes back as System.String.",
            "status": "CONFIRMED",
            "evidence": "Fetched https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-properties/update?view=azure-devops-rest-7.1 (api-version 7.1, GA, media type \"application/json-patch+json\", scope vso.code_write). The add/replace/remove path semantics are confirmed verbatim. PropertiesCollection verbatim: \"Values of all primitive types (any type with a `TypeCode != TypeCode.Object`) except for `DBNull` are accepted. Values of type Byte[], Int32, Double, DateType and String preserve their type, other primitives are retuned as a String. Byte[] expected as base64 encoded string.\" The divergence is confirmed exactly: request sends \"value\": 8, response returns \"sampleId\": {\"$type\": \"System.String\", \"$value\": \"8\"} \u2014 and the replace sample sends 12 and likewise returns String. Microsoft.Git.PullRequest.SourceRefName / TargetRefName are pre-populated in both samples, confirming the namespacing warning. REFINEMENT the claim missed: the coercion is not universal \u2014 in the same sample \"startedDateTime\" round-trips as {\"$type\": \"System.DateTime\"}, so DateTime IS preserved while the documented Int32 preservation is what actually fails. \"Treat every read as a string\" is still the right rule, but the failure is Int32-specific, not blanket."
          },
          {
            "claim": "Iterations are ordered, immutable, individually addressable revisions each naming its own base \u2014 so JetBridge should not build ghstack ref triples on Azure.",
            "status": "OVERSTATED",
            "evidence": "Every structural field checks out; \"immutable\" does not. Fetched https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1 (api-version 7.1, GA, scope vso.code \u2014 NOT vso.code_write). Confirmed verbatim: \"Iterations are created as a result of creating and pushing updates to a pull request\"; commonRefCommit \"The first common Git commit of the source and target refs\"; sourceRefCommit / targetRefCommit; push (GitPushRef with pushedBy); IterationReason enum exactly {push, forcePush, create, rebase, unknown, retarget, resolveConflicts}; GitPullRequestChange.changeTrackingId \"ID used to track files through multiple changes\". supportsIterations confirmed verbatim on GitPullRequest. BUT: no Microsoft doc anywhere states an iteration is immutable, and GitPullRequestIteration carries a first-class `updatedDate` field (\"The updated date of the pull request iteration\") alongside createdDate \u2014 an object with a mutable-update timestamp is, on its face, not documented as immutable. Two further omissions that bite a revision-reconstruction design: `hasMoreCommits` \u2014 \"Indicates if the Commits property contains a truncated list of commits in this pull request iteration\" (so `commits` is not a reliable complete revision manifest and must be paged via the iteration-commits endpoint); and `newTargetRefName`/`oldTargetRefName`, which only populate when reason == retarget, meaning a retarget silently changes what the diff is against. Treat iteration identity as stable and iteration CONTENT as needing verification, and gate sealing on hasMoreCommits == false."
          },
          {
            "claim": "Approval votes are bound to a specific revision natively \u2014 \"Azure ties votes to iterations natively and with more granularity than GitHub\".",
            "status": "OVERSTATED",
            "evidence": "The POLICY behaviour is real; the DATA binding is not exposed. Fetched https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/list?view=azure-devops-rest-7.1 and the GitPullRequest definition. IdentityRefWithVote's complete field list is {_links, descriptor, directoryAlias, displayName, hasDeclined, id, imageUrl, inactive, isAadIdentity, isContainer, isDeletedInOrigin, isFlagged, isReapprove, isRequired, profileUrl, reviewerUrl, uniqueName, url, vote, votedFor}. There is NO iteration reference and NO timestamp on a vote. Vote values confirmed verbatim: \"10 - approved 5 - approved with suggestions 0 - no vote -5 - waiting for author -10 - rejected\". So from the reviewers API you can read the CURRENT vote but cannot tell which revision it was cast against \u2014 you must correlate out-of-band. Also newly surfaced and directly on point: `isReapprove` \u2014 \"Indicates if this approve vote should still be handled even though vote didn't change.\" The existence of that flag is evidence that setting a vote to its existing value does NOT normally produce a handled state change, which matters for any self-suppression or re-trigger logic built on votes."
          },
          {
            "claim": "(adversarial d) A vote change produces a distinct event.",
            "status": "CONFIRMED",
            "evidence": "Yes, via two channels the claim set did not name. (1) Subscription-level: the git.pullrequest.updated service hook has a `notificationType` FILTER with allowed values PushNotification, ReviewersUpdateNotification, StatusUpdateNotification, ReviewerVoteNotification \u2014 \"Include only events for pull requests with a specific change\" (https://learn.microsoft.com/en-us/azure/devops/service-hooks/events?view=azure-devops). Creating four separate subscriptions with distinct endpoint URLs is how you distinguish cause, because the payload does not carry the type. (2) Durable ledger: the Pull-Request-Threads List sample response (7.1) shows Azure writes SYSTEM comment threads for every state change, with commentType \"system\", author = \"Project Collection Service Accounts\", and typed properties. Confirmed thread types in the official sample: CodeReviewThreadType \"VoteUpdate\" with CodeReviewVotedByTfId / CodeReviewVotedByDisplayName / CodeReviewVoteResult (\"10\") and a publishedDate; \"RefUpdate\" with CodeReviewRefUpdatedByTfId / CodeReviewRefNewHeadCommit / CodeReviewRefNewCommits / CodeReviewRefNewCommitsCount; \"ReviewersUpdate\"; \"MergeAttempt\" with CodeReviewSourceCommit / CodeReviewTargetCommit / CodeReviewMergeCommit. This is a timestamped, attributed, pollable vote-and-push ledger \u2014 a materially better substrate for disposition-triggered review than anything the claim set identified, and it is the thing JetBridge should key the trigger on."
          },
          {
            "claim": "(adversarial c) Service hook payloads contain the identity needed for self-suppression.",
            "status": "REFUTED",
            "evidence": "For git.pullrequest.updated, no. The trigger is broad \u2014 \"A pull request is updated: the status, review list, or a reviewer vote changes, or a push updates the source branch\" \u2014 and I confirmed against both the rendered page and the source markdown (MicrosoftDocs/azure-devops-docs docs/service-hooks/events.md) that `notificationType` is a SUBSCRIPTION FILTER only and does NOT appear in the payload. The sample payload's only identity is resource.createdBy, which is the PR CREATOR, not the actor who caused the update. There is no pushedBy, no votedBy, no actor field. A JetBridge webhook handler therefore cannot self-suppress from the git.pullrequest.updated payload alone \u2014 it must either (a) run four notificationType-filtered subscriptions on distinct URLs, and/or (b) re-read GET /threads and use the system-thread properties (CodeReviewRefUpdatedByTfId, CodeReviewVotedByTfId) or GET /iterations (push.pushedBy) to identify the actor. By contrast, ms.vss-code.git-pullrequest-comment-event DOES carry resource.comment.author {id, displayName, uniqueName} \u2014 so comment-level self-suppression is clean, PR-level is not. This is the same class of self-trigger hazard the design already proved live on GitHub, just relocated."
          },
          {
            "claim": "(adversarial b) Comment anchors survive across iterations \u2014 \"comments left in one iteration will be tracked across future iterations\".",
            "status": "OVERSTATED",
            "evidence": "The quoted sentence is real (GitPullRequest.supportsIterations, verbatim), but Azure does NOT provide the Radicle-style immutable-OID pinning the borrowed model wants; it provides exactly the heuristic forwarding the model was chosen to avoid. From https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1: a thread anchors via CommentThreadContext {filePath, leftFileStart/End, rightFileStart/End} where CommentPosition is {line, offset} \u2014 a text position, not a commit OID. No commit SHA appears anywhere in the thread anchor. Tracking is explicitly conditional and can fail: GitPullRequestCommentThreadContext.trackingCriteria \u2014 \"The criteria used to track this thread. If this property is filled out when the thread is returned, then the thread has been tracked from its original location using the given criteria\"; and CommentTrackingCriteria.firstComparingIteration/secondComparingIteration \u2014 \"Threads were tracked if this is greater than 0.\" Worse, tracking is opt-in on READ: the $iteration and $baseIteration query params are documented as \"If specified, thread positions will be tracked using this iteration as the right side / left side of the diff\" \u2014 so a plain GET /threads with no params returns untracked positions. GOOD NEWS for the design: staleness is DETECTABLE (absent trackingCriteria, or comparing iterations == 0, or origFilePath != current filePath signalling a rename), so JetBridge can refuse to seal a review/v1 record whose anchors did not track. And changeTrackingId is the required cross-iteration handle \u2014 \"Used to track a comment across iterations... Must be set for pull requests with iteration support\" \u2014 meaning JetBridge MUST populate it when creating threads or its own comments will not track."
          },
          {
            "claim": "(adversarial, in-practice lens) Community/field reports contradicting the documented behaviour on iterations, comment anchoring, hook payloads, or votes.",
            "status": "UNVERIFIABLE",
            "evidence": "I could not substantiate the practice layer within this pass and will not confirm it on vibes. developercommunity.visualstudio.com ticket bodies are JS-rendered \u2014 fetching https://developercommunity.visualstudio.com/t/pull-request-comments-repeatedly-move-to-the-wrong/700623 returned only site chrome and footer navigation, no report text, no Microsoft status, no dates. Targeted searches on Stack Overflow and microsoft/azure-devops-* GitHub issues returned no substantive Azure-specific contradicting reports; the top hits for \"comments lost after rebase/force-push\" were GitLab issues (gitlab-org/gitlab-ce#24323, #28381) and a GitHub issue (isaacs/github#997), not Azure. Two Azure-adjacent leads exist but are unconfirmed: a Microsoft Q&A thread on commit-level comments not appearing in the PR overview (https://learn.microsoft.com/en-us/answers/questions/1602132/), and microsoft/azure-devops-python-api#166 on get_pull_request_iteration_changes returning only change_tracking_id. Recommendation: the four adversarial questions must be settled empirically against a real org (force-push a PR branch and re-read iterations + threads + Pushes; flip a vote and diff the hook deliveries), not from search. Every substantive finding above is doc-derived, and I have flagged which doc statements are current versus archived."
          },
          {
            "claim": "Target api-version=7.1 (GA); PR threads, iterations, properties, refs and pushes are all GA at 7.1.",
            "status": "CONFIRMED",
            "evidence": "Independently verified on all seven endpoints I fetched \u2014 Refs Update-Refs, Pushes List, PR Get, PR Iterations List, PR Reviewers List, PR Threads List, PR Properties Update. Every one carries \"API Version: 7.1\", the parameter text \"This should be set to '7.1' to use this version of the api\", and no preview marker. All seven also list `azure-devops-server-rest-7.1` among supported monikers, which corroborates the claim's Server 2022.1 \u2192 7.1 mapping. Incidental corroboration of the claim's OAuth-deprecation point: every one of these reference pages still renders its security block against the legacy `https://app.vssps.visualstudio.com/oauth2/authorize` flow, i.e. the docs continue to advertise the endpoint Microsoft says stops accepting new registrations."
          },
          {
            "claim": "Least-privilege scope set: vso.code_write covers reading PRs and PR properties; vso.threads_full for comments.",
            "status": "OVERSTATED",
            "evidence": "Right conclusion for writes, wrong mapping for reads \u2014 and over-scoping an agent identity is the exact failure this section exists to prevent. Observed per-endpoint scopes: PR Iterations List \u2192 `vso.code` only. PR Reviewers List \u2192 `vso.code` only. PR Get \u2192 `vso.code` only. Pushes List \u2192 `vso.code` only. Refs Update-Refs \u2192 `vso.code_write`. PR Properties Update \u2192 `vso.code_write`. PR Threads List \u2192 BOTH `vso.code` AND `vso.threads_full` (\"Grants the ability to read and write to pull request comment threads\"), settling the claim's open question: threads_full is listed as an alternative on the threads route, so the claim's \"test which is actually enforced\" is still the right instruction but the docs do list both. A read-only poller therefore needs only vso.code; vso.code_write is required strictly for the ref/property/PR-mutation paths."
          },
          {
            "claim": "Rate limiting is TSTU-based, applied first as silent latency with HTTP 200 + Retry-After; the listed X-RateLimit-* headers; Basic + Test Plans is the only escape hatch.",
            "status": "CONFIRMED",
            "evidence": "Fetched https://learn.microsoft.com/en-us/azure/devops/integrate/concepts/rate-limits?view=azure-devops (page updated 2026-07-31). Every quoted number and string verified verbatim: \"One TSTU represents the average load generated by a typical Azure DevOps user over five minutes\"; \"Normal user activity can generate spikes of 10 TSTUs or fewer per five minutes\"; \"Larger but less frequent spikes can reach up to 100 TSTUs\"; \"The global limit is 200 TSTUs within any sliding five-minute window\"; \"Their personal usage exceeds 200 times the consumption of a typical user within a sliding five-minute window\"; \"Delays range from a few milliseconds per request up to 30 seconds\"; \"If consumption stays high, delays can continue indefinitely to protect the resource\"; \"Honor the Retry-After header: If you receive it in a response, wait the specified time before sending another request. The response still returns HTTP 200, so retry logic isn't required\"; the TF400733 429 text; all six X-RateLimit-* headers with the stated descriptions; \"Threshold types and service names might vary over time and without warning. We recommend displaying this string to a human, but not relying on it for computation\"; \"You can't calculate usage in TSTUs for an action with a formula\"; \"There's a 200 TSTU limit for each pipeline in a sliding 5-minute window\"; \"Only the Basic + Test Plans access level provides an increase to these limits.\" The Services-only scope is confirmed by the page's own product banner: **Azure DevOps Services** (no Server moniker rendered). ONE REFINEMENT that weakens the proposed adaptive poller: the headers are not guaranteed. Docs say \"Monitor X-RateLimit headers: **If available**\" and X-RateLimit-Cost \"**If present**, indicates TSTUs consumed by this request\". A poller keyed on X-RateLimit-Remaining/Cost needs a defined fallback for when those headers are simply absent."
          },
          {
            "claim": "hasMultipleMergeBases is a first-class PR field and a correctness hazard; the docs describe over-reporting relative to git merge-base and three named security consequences.",
            "status": "CONFIRMED",
            "evidence": "Field confirmed present in GitPullRequest at api-version 7.1. Prose confirmed verbatim from https://learn.microsoft.com/en-us/azure/devops/repos/git/merging-with-squash?view=azure-devops: \"Unfortunately, in some cases, there's more than one true base. In most repositories this situation is rare, but in large repositories with many active users, it can be common.\"; \"While Azure DevOps is running the detection of multiple merge bases, it doesn't check if potential merge base was already merged or not. Such check is done by `git merge-base`. This is why Azure DevOps might display the message even when `git merge-base` reports only one merge base.\"; and all three risks verbatim \u2014 \"A malicious user could abuse the UI algorithm to commit malicious changes that aren't present in the PR.\", \"If changes proposed in the PR are already in the target branch, they're displayed in the Files tab, but they might not trigger branch policies that are mapped to folder changes.\", \"Two sets of changes to the same files from multiple merge bases might not be present in the PR. That case might create treacherous logic gaps.\" The docs add a line the claim did not cite that reinforces the recommendation: \"In case you have lost changes during a PR review, ensure that multiple merge bases aren't the root cause.\" Caveat on discoverability: the REST description for the field is three words \u2014 \"Multiple mergebases warning\" \u2014 so all operative semantics live on a conceptual page about squash merging, which is thin-docs territory."
          },
          {
            "claim": "Push-side validation policies (commit author email validation, file path validation, case enforcement, reserved names, path/file size limits, VS403729, the auto-enforced 'branch can't be deleted' side effect).",
            "status": "UNVERIFIABLE",
            "evidence": "Not examined in this pass \u2014 I did not fetch repository-settings or the Git limits page, and my searches surfaced no community contradiction of these specific policy behaviours. Flagging as unchecked rather than borrowing confidence from the adjacent claims. Two items in it deserve a dedicated verification because they are the ones that would actually block an agent identity: whether 'Commit author email validation' matches on the commit AUTHOR or COMMITTER trailer (the agent typically differs on the two), and whether attaching any policy to a branch really does auto-enable 'The branch can't be deleted', which would strand JetBridge's own revision branches."
          }
        ]
      }
    ]
  }
]
```