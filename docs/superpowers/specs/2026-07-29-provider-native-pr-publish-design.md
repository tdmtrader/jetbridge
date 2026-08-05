# Provider-Native Pull Request Publish and Monitoring

> **PARTIALLY SUPERSEDED (2026-08-05).** The Azure DevOps adapter and every
> Azure-specific policy, Git-transport, and conformance decision in this design
> were removed from the tree. GitHub is the only supported forge; the
> provider-neutral `Observer`/`Mutator` seam is retained. Read the Azure
> sections as historical record only.

**Date:** 2026-07-29
**Status:** Approved for implementation
**Approvals:** Product design approved section-by-section on 2026-07-27; on
2026-07-29 the owner explicitly authorized spec, plan, and implementation to
continue without another synchronous review gate.

## Objective

Complete Jetbridge's provider-native pull-request publication path without
building a direct-to-trunk delivery loop.

An accepted, exactly validated repository change can be published as a pull
request. A separate reusable monitor workflow then polls that pull request,
batches completed human reviews, refreshes it against trunk, invokes bounded
agent revision work when needed, reruns the complete authoritative validation
suite, and conditionally asks for human reapproval before updating the PR
branch. The forge remains responsible for final PR completion.

Azure DevOps is the Day-1 production target. GitHub is the live development and
integration-test provider. Azure DevOps is implemented deliberately against
the documented REST 7.1 contract but cannot be live-tested in the current
environment; the product must report that limitation honestly.

## Current state after the 2026-07-29 rebase

The implementation branch is based on `origin/jetbridge` at `ce2063bac3`.
The semantic-foundations work now provides several load-bearing pieces this
design reuses:

- ATC owns publication policy, credentials, snapshot reinspection,
  idempotency, and direct Git side effects.
- Repository changes are rebased before direct publication.
- Governed repository-change consumers require exact authoritative
  `validation/v1` revision-3 evidence.
- Publication operations and occurrences already distinguish semantic
  idempotency from the workflow run that authorized an occurrence.
- Standing workflow resource-source pipelines use ordinary Concourse version
  selection and durable source admissions.
- Workflow runs, retries, replays, snapshots, waits, and provenance are already
  durable.
- The external publisher gateway has been removed.

The current direct-Git adapter intentionally fails closed for pull-request
mode. `ModePullRequest` remains in the provider-neutral request model and
policy vocabulary, but no provider-native PR creation, observation, review
batching, status publication, or monitor lifecycle exists.

The accepted direct-Git branch writer is also not a safe implementation for PR
updates. It observes the remote source ref during the mutation and leases
against that newly observed value. A PR monitor instead needs to carry the
older server-authorized head from its sealed observation; otherwise an
intervening human or bot push could be overwritten. Provider-native PR writes
therefore use a new expected-source-head contract rather than widening the
accepted direct-Git backend.

### Rebase reconciliation decision

The earlier discussion used "PR-only" to simplify a design that also included
direct-trunk publication. Direct-trunk support landed before this spec was
written. This track therefore **preserves the accepted direct-Git path and
does not reopen it**. "PR-only" means that every new orchestration feature in
this track is scoped to pull requests; it does not mean deleting existing
direct publication.

## Product decisions

1. **Publish is PR-only for this track.** New work publishes and maintains a
   source branch and provider-native PR. It never writes trunk.
2. **The forge completes the PR.** Jetbridge does not call GitHub merge or
   Azure DevOps completion APIs. It observes completed or abandoned state and
   records the exact result.
3. **Initial approval already exists.** The accepted review of the exact
   candidate is the initial content approval. "Publish as PR" dispatches a
   governed operation; it is not an unconditional second approval.
4. **Reapproval is conditional.** Reconciliation and revision may require a
   new human answer. Deterministic policy and platform invariants can require
   it. An impact-assessment agent can only escalate.
5. **An explicit `agent-decides` policy is permitted.** It removes optional
   deterministic escalation rules, not platform integrity checks. Assessor
   failure or ambiguity still escalates to a human.
6. **Full validation always reruns after content or base changes.** Impact
   assessment decides approval, never which required tests may be skipped.
7. **Human and bot PR commits are adopted.** They become exact sealed input to
   the next iteration and are never overwritten blindly.
8. **Review feedback is batched.** Polling reacts to a completed review, not
   every individual comment.
9. **No webhooks.** The deployment is not externally reachable. Polling
   defaults to five minutes.
10. **Freshness is deliberately slower.** A PR refreshes when it becomes
    conflicted and when trunk has advanced for six hours since the last
    successful reconciliation.
11. **One writer per PR.** Revision runs serialize. Newly completed review
    batches remain queued and run later against freshly observed state.
12. **GitHub proves the contract live.** Azure DevOps must pass the same
    conformance suite but is labeled `contract-tested, not live-validated`.
13. **The provider cursor is first-class opaque evidence.** Each normalized
    observation carries the exact bounded cursor returned by its adapter. Core
    scheduling and digest logic preserves those bytes but never decodes
    provider-specific cursor structure.
14. **Observed review batches are cursor deltas.** Given an acknowledged input
    cursor, an adapter returns only completed batches after that cursor.
    GitHub and Azure DevOps conformance tests must prove that replaying the
    same provider state returns no previously acknowledged batch.
15. **Freshness identity is stable until acknowledgement.** Its canonical time
    bucket is derived from the first due deadline
    (`last_reconciled_at + freshness_interval`), so a long-running equivalent
    action does not become new work at the next interval boundary.
16. **Adapters drain completed reviews one batch at a time.** An observation
    selects the earliest completed batch after the acknowledged cursor. The
    cursor advances only through that batch, leaving later batches queued for
    subsequent serialized workflow instantiations.
17. **Day-1 PRs are same-repository.** Source and target refs must belong to
    the binding's policy-authorized repository. Fork PRs fail closed until a
    distinct source-repository authority is designed.
18. **Checkout locations are protected configuration.** The provider API base
    URL and credential-free repository URL are resolved from destination
    policy and rendered into the protected monitor source. They are never
    accepted from mutable PR text, and credentials are never embedded in URLs.
19. **Mutation authority names both the approved baseline and final
    revision.** Accepted-review evidence keeps the human-approved
    `repository/v1` baseline. Every branch/create action separately binds the
    final `repository-change/v1`, its post-rebase `validation/v1`, and its
    `publish-impact/v1`; all three participate in operation identity and
    durable publication inputs.
20. **Branch publication must transport the exact Git object.** A ref-update
    API can move a ref only to an object the forge already has; it cannot
    publish Jetbridge's locally produced commit bytes. Day 1 therefore uses
    the same verified Git smart-HTTP `--force-with-lease` object upload and
    ref CAS for GitHub and Azure DevOps. Azure's REST 7.1 ref-update adapter
    remains a contract-tested pre-existing-object seam, but production
    composition does not select it for a new `repository-change/v1`.
21. **PR creation, original review, and the approved baseline are separate
    authorities.** The creation occurrence proves which exact provider PR was
    created. The original accepted review explains why initial publication was
    allowed. The mutable approved baseline names the latest exact human-approved
    repository and validation plus the publication occurrence that authorized
    it. One occurrence field may not stand in for all three.
22. **Initial publication is a distinct coordinator.** It starts without a PR
    binding, publishes the exact source branch, finds or creates the PR,
    reobserves that PR through the provider adapter to obtain authoritative
    state and cursor, seals that observation, and only then creates a binding.
    A create response is not allowed to fabricate monitor cursor authority.
23. **Monitor effects are trigger-specific.** `review_batch` requires an exact
    authorized response. Conflict and freshness runs carry an explicit semantic
    absence and can never enter response publication. Completed and abandoned
    observations record terminal state without launching a mutation workflow.
24. **Production enablement remains fail-closed until the authority spine is
    complete.** Exposing configuration, adapters, contracts, or a reusable
    workflow is not sufficient. Startup refuses PR enablement until
    deployment-owned impact evaluation, action-bound initial publication
    composition, final baseline materialization and atomic approved-baseline
    advancement, and the owned monitor lifecycle are all composed.
25. **Legacy binding rows are not upgraded into authority.** The pre-authority
    binding projection cannot prove an accepted review, the exact succeeded
    `create_pr`, or a human-approved repository baseline. The forward migration
    therefore takes an exclusive binding lock and refuses any preexisting row;
    it never infers security facts from partial historical operation JSON.
26. **Initial retry recovery precedes mutable provider observation.** A retry
    first resolves an already-created exact binding and its immutable
    publication occurrences. Only a genuinely incomplete initial operation may
    reobserve mutable forge state and continue creation.
27. **The successful initial branch publication is the binding origin.** It
    carries the accepted evidence that authorized the published content. The
    PR-creation occurrence remains a separate immutable fact, and a
    pre-supplied accepted-review occurrence is only pre-mutation authority.
28. **Original accepted review and current approved baseline are verified
    separately.** Revision execution always reopens the immutable original
    review, then resolves the binding-scoped current repository and validation
    triple. A later baseline must be human-wait-authorized; a same-team baseline
    from another binding is not substitutable.
29. **Forge terminal state bypasses mutation workflows.** Completed and
    abandoned observations are reopened as exact sealed evidence and applied
    through a row-locked direct binding transition. They do not reserve or
    launch a revision run, invoke a forge mutator, or invent publication
    evidence. Owned-pipeline disposal is a later lifecycle consequence.
30. **Initial binding authority accepts exact occurrence aliases.** Publication
    operation identity is semantic, while each retrying workflow run receives
    its own independently authorized occurrence. Recovery after a completed
    provider side effect but before binding creation therefore accepts a
    succeeded same-run occurrence alias; it does not require that occurrence to
    be the operation's lease owner. The alias must still reopen as the exact
    `publish_pr_branch` or `create_pr` action and match the complete action,
    result, snapshots, accepted-review evidence, refs, heads, destination, and
    policy before it can establish binding authority.

## Implementation checkpoint

As of 2026-07-29, the durable authority split, exact initial coordinator,
monitor target authority, exact revision executor, exact monitor-run
inspection, binding-scoped approved-baseline contract, and direct
completed/abandoned transition are implemented and independently reviewed.

Production enablement remains intentionally blocked. The remaining authority
work is a concrete deployment-owned impact resolver/evaluator, action-bound
composition of the initial observer/sealer/handoff, an immutable relation from
the authorizing publication to its materialized final `repository/v1`, a
database resolver for later approved baselines, atomic cursor/head/baseline
advancement, and complete monitor-pipeline lifecycle wiring.

GitHub live proof was not run because its explicit environment prerequisites
were unavailable. Azure DevOps adapter: contract-tested against REST 7.1; not
live-validated.

## Invariants

- Agents never receive mutation-capable forge credentials.
- A publication or response can address only the PR and thread identifiers
  present in server-authorized, sealed inputs.
- Every branch mutation is compare-and-swap against the exact observed source
  head. A mismatch is a stale observation, never permission to overwrite.
- The exact expected target head is checked in the pre-update observation,
  including before accepting an already-published source head. A target race
  after that check is safe and is handled by the next freshness iteration.
- Trunk is rechecked immediately before a branch update. Because this track
  updates only a PR branch, a trunk race after that read is safe: the next poll
  schedules another freshness iteration and forge branch policy controls
  completion.
- Approval binds immutable content, not a mutable branch.
- Publication inputs and approval evidence use composite same-team database
  references. Cross-team evidence is rejected by storage constraints, not
  merely by an application-layer authorization check.
- Authoritative validation binds the exact candidate, base, profile,
  configuration, image, toolchain, workflow revision, and attestation.
- External mutations are destination-policy checked, idempotent, auditable,
  and recoverable after uncertain responses.
- Provider-specific ambiguity maps to `unknown` and fails closed; adapters do
  not guess a favorable semantic state.

## Architecture

The feature has two workflows, one small coordination record, and three
focused sealed record types.

### `publish-pr-v3`

The initial workflow:

1. resolves the exact accepted review and reviewed candidate;
2. captures the current target branch as an immutable `repository/v1`;
3. deterministically rebases the candidate onto that target;
4. runs the complete authoritative validation suite;
5. computes impact relative to the accepted candidate;
6. creates a durable reapproval wait only when policy requires one;
7. asks ATC to conditionally create/update the source branch;
8. finds or creates the provider-native PR idempotently;
9. seals the normalized PR observation and creates its monitor binding.

The result is a PR external ID and URL, exact source and target heads, initial
provider state, publication audit, and monitor identity.

### `pr-monitor-v3`

Monitoring is a separate reusable workflow. Each active PR binding owns one
server-generated standing Concourse pipeline. It is an **ordinary owned
pipeline**, not a template pipeline: ordinary periodic checks are required,
and current Concourse scheduling deliberately excludes template pipelines
from Lidar checks.

The pipeline has one provider-neutral PR resource and one serial monitor job:

- `check` polls every five minutes and emits only actionable semantic versions;
- `get` materializes the normalized provider observation;
- the monitor job captures and seals that observation, then invokes the exact
  pinned `pr-monitor-v3` workflow revision;
- the job processes versions in order with one active mutation run per PR.

`serial: true` prevents overlapping source-pipeline jobs but is not the
mutation lock: capture completes before a potentially long monitor workflow
run. The binding row therefore has a row-locked launch/acknowledgement gate
that permits only one nonterminal monitor workflow run and advances its cursor
only after safe completion.

The protected resource source also carries policy-resolved provider API and
credential-free repository URLs. `in` authenticates through the resolved read
secret and verifies both exact heads from that one same-repository URL. Active
actions fetch the observed branch refs so branch movement fails closed.
Completed and abandoned actions fetch the exact observed source and target
objects by object ID instead, because forge-native completion may delete the
source branch. Provider capability diagnostics surface an unsupported
exact-object fetch rather than silently substituting another head.

`in` writes inside Concourse's caller-owned mounted destination; it never
renames over or replaces that mount. Repositories are installed within the
mount, temporary children are removed on failure, and `record.json` is
published last. Each materialized repository, including its contained Git
database, is bounded by the platform snapshot entry and content limits.
Remotes and credential configuration are absent before `in` returns.

Acknowledged binding state is fed back into polling by re-rendering the
protected server-owned resource source after binding revision changes. The
resource check derives review cursor and freshness only from that projected
state, never from an unacknowledged previous resource version. Each emitted
version carries the binding revision; the launch gate rejects a version
selected from stale pipeline config. The existing source-pipeline lifecycle
reconciler converges config after crashes, so this requires neither an
internal bearer endpoint nor a new scheduler.

Terminal PR state pauses and archives the owned monitor pipeline through the
existing workflow-owned source-pipeline lifecycle. The archived binding and
run history remain as provenance. Template-pipeline garbage collection does
not apply to these ordinary standing pipelines; physical deletion requires a
PR-binding retention collector with an explicit retention policy and is not
part of terminal correctness. This feature introduces neither a long-lived
agent nor a new scheduler.

### `agent_pr_bindings`

One new durable table coordinates an external PR with existing workflow and
publication records. It stores:

- owning team;
- provider and repository locator;
- external PR ID and URL;
- source and target refs;
- originating publication and workflow run;
- exact monitor workflow definition and revision;
- owned standing-pipeline identity;
- last successfully processed review cursor;
- last reconciled source and target heads and timestamp;
- lifecycle state: `active`, `attention_required`, `completed`, or
  `abandoned`;
- timestamps and terminal provider evidence.

The row is coordination, not evidence. Exact observations, candidates,
validation, impact reports, questions, answers, responses, and publication
results remain immutable workflow snapshots and publication occurrences.
Monitor workflow runs use an explicit PR-monitor origin whose reference is the
binding ID, so the audit timeline is reconstructable without a second mutable
history table.

The three new record types are:

- `pull-request/v1`: platform-authored normalized provider observation and
  actionable trigger. Its lifecycle includes a sealed `missing` pre-create
  observation: provider, repository, source ref, expected source head or
  expected absence, target ref, target head, and provider version are present,
  while external PR ID and URL are absent. This is the authority for the
  initial exact-lease branch mutation; it is not inferred from a
  mutation-time re-observation;
- `publish-impact/v1`: deterministic and agent impact evidence plus the
  server-derived reapproval decision;
- `pull-request-response/v1`: agent-authored overall summary and thread replies
  whose thread IDs must be a subset of its `pull-request/v1` subject.

The response record is intentionally typed. Dynamic response text cannot be
smuggled through static workflow parameters, and an untyped blob would not let
ATC enforce thread authority before an external side effect.

## Polling and trigger semantics

A resource version identity includes provider, PR ID, source head, target head,
action kind, and an action cursor or canonical action digest. Repeated polls of
unchanged state produce no new version.

Actionable versions are:

### Completed review batch

- **GitHub:** a submitted review ID after the binding cursor. Pending reviews
  are invisible. The batch includes the submitted review, its commit ID, and
  all review comments belonging to it.
- **Azure DevOps:** a reviewer transitions to vote `-5` ("waiting for author").
  The batch contains the canonical digest of all included threads and their
  iteration contexts. That reviewer cannot produce another batch until their
  vote leaves and later returns to `-5`.

Draft/pending comments do not trigger a run. A batch is processed at most once
semantically even if a provider returns it on every poll.

### Conflict

A version is emitted when mergeability transitions into `conflicted`, or when
the canonical conflict signature changes. An unchanged conflict does not
launch a run every five minutes.

### Scheduled freshness

A version is emitted only when:

- target head differs from the last successfully reconciled target;
- at least the configured freshness interval has elapsed; and
- an equivalent freshness action is not already queued or active.

The default freshness interval is six hours.

### Terminal state

Completion or abandonment emits one final version. The terminal run seals the
provider result, updates the binding, and archives monitoring.

## Provider adapter boundary

The core depends on a deliberately small split interface:

```text
ObservePullRequest(locator, cursor) -> observation
CompareAndSwapBranch(ref, expectedHead, newHead) -> result
FindOrCreatePullRequest(operationKey, source, target, metadata) -> PR
PublishValidationStatus(PR, exactHead, status) -> result
PublishReviewResponse(PR, authorizedBatch, response) -> result
```

Observation is read-only and can execute in the resource check/get container
with a narrowly scoped read credential resolved by Concourse. That container
is not an agent workload, and its credential is never emitted in resource
bytes, snapshots, logs, or workflow parameters.

Every mutation executes through ATC's existing trusted publication boundary
with a separately mapped write credential. Provider adapters normalize
transport details; they do not decide scheduling, validation, impact, or
approval policy.

Provider-native mutation is a family of independent idempotent publication
operations, not one monolithic PR side effect. Source-branch CAS, PR
find/create, status publication, and review response each have their own
operation kind and semantic key. This permits recovery when a branch update
succeeds before PR creation or a response succeeds after the branch update.
Every operation carries the exact target ref used for deployment-policy
selection; status and review response do not infer it from mutable provider
state or rely on a one-target-per-repository restriction.

Provider policy is explicit and asymmetric. A PR rule binds team, destination,
provider, repository locator, target branch, API base URL, credential-free
repository URL, read-credential reference, and write-credential reference.
The protected monitor source receives only the read authority it needs; ATC
resolves the write credential only inside the trusted mutation boundary.
Direct Git retains its existing policy shape and cannot select a PR adapter.

GitHub branch CAS materializes and verifies the nested Git payload of the exact
`repository-change/v1` snapshot into bounded server-owned scratch storage for
the lifetime of one mutation. The outer snapshot directory is never treated
as a Git worktree. A stale source or target lease is a safe terminal operation
result (`rebase_required`/fresh reconciliation), not a retryable pending lease;
this prevents permanent reclaim loops for obsolete authority.

The normalized observation contains:

- the exact opaque provider cursor used to acknowledge polling progress;
- active, completed, or abandoned lifecycle;
- exact source and target refs and heads;
- mergeability: `mergeable`, `conflicted`, `policy_blocked`, or `unknown`;
- provider iteration/version identity;
- completed review batches;
- stable thread/comment IDs and source anchors;
- reviewer identity and readiness state;
- platform-published status for the exact current head.

Active and terminal observations require a nonempty cursor. The empty cursor
is reserved for an initial caller position and, where needed, a sealed
pre-create `missing` observation.

### GitHub

GitHub is the live reference adapter. Submitted review IDs and their
`commit_id` values define batches. Branch mutation uses an exact expected-head
lease. PR creation and lookup use a stable operation marker plus source/target
identity. Validation uses the native check/status surface available to the
configured credential.

Its opaque cursor is a strict versioned canonical token containing the
submitted-review watermark, the selected normalized batch digest, and a
provider-state signature over lifecycle, mergeability, exact heads, and
iteration. Given the same acknowledged cursor and provider state, observation
returns the same earliest batch and exact cursor. After acknowledgement, that
batch is absent and the next completed review can advance the watermark.
Malformed nonempty GitHub cursors fail closed.

A nonempty submitted review body is preserved as a deterministic,
context-only unanchored thread. Its synthetic ID is not included in the
batch's reply-authority `thread_ids`; agents answer it through the typed
overall response rather than a forged provider-thread reply.

### Azure DevOps

Azure DevOps is pinned to REST API `7.1`. The adapter maps:

- PR iterations and their source commits;
- threaded comments and iteration contexts;
- reviewer votes, including `-5` waiting-for-author;
- PR/iteration statuses;
- ref updates with explicit `oldObjectId` and `newObjectId` when the exact
  object already exists in Azure Repos;
- active, completed, abandoned, conflict, and policy states.

The REST ref-update contract does not transport a locally produced Git object.
Initial and revised branch publication therefore materializes the exact sealed
Git payload and uses the policy-authorized, credential-free Azure repository
URL with smart HTTP `--force-with-lease`. That one Git operation uploads the
object and applies the caller-sealed source lease atomically. The Azure REST
adapter remains covered for pre-existing-object ref updates, while the Day-1
production bridge selects the verified Git transport for branch publication.
The transport selects an explicit Azure Bearer-header mode from provider
policy; it never infers PAT/Basic behavior from token text. Bearer material is
passed through private scrubbed Git configuration, never argv or a
credential-bearing URL, and credential-bearing redirects are disabled.

Ready-for-author review batches are derived from Azure's durable system
`VoteUpdate` threads, ordered by published time and thread ID, rather than
from the current reviewer list alone. The adapter recognizes only the
documented sample property tuple
`CodeReviewThreadType.$value=VoteUpdate`,
`CodeReviewVotedByTfId.$value`, and `CodeReviewVoteResult.$value`. Its opaque
cursor carries the event watermark and vote state needed to suppress repeated
`-5` votes while allowing a later `-5 -> 0 -> -5` transition to rearm. The
reviewer endpoint corroborates the identity and current vote; missing or
malformed vote-event properties fail closed.

Day 1 uses an OAuth access token sent with the `Bearer` authorization scheme.
PAT/Basic authentication is not inferred from token text and remains disabled
until the credential contract carries an explicit authentication mode. Human
PR URLs are derived from the configured organization, project, repository,
and PR identity; REST `url` and `remoteUrl` fields are not trusted as browser
destinations.

Azure policy has one strict decomposition: `api_base_url` is the organization
URL, and the provider repository locator is exactly
`project/repositoryID`. The credential-free Git repository URL must name that
same organization, project, and repository. Production composition verifies
all three before constructing either the REST adapter or the smart-HTTP object
transport; it never guesses a project by trimming an arbitrary URL.

Azure response recovery uses exact, bounded, machine-authored operation
markers because the comment API has no documented idempotency key. A reply
creates a root comment (`parentCommentId: 0`) only within a thread ID
authorized by the sealed batch; the overall response creates a separate
PR-level thread. Missing, altered, or ambiguous recovery markers fail closed
rather than creating a duplicate response.

The implementation uses strict bounded requests, pagination, throttling and
retry classification, unknown-enum handling, and fixture-backed decoding.
Deployment diagnostics and documentation must say:

> Azure DevOps adapter: contract-tested against REST 7.1; not live-validated.

Relevant normative documentation:

- [Azure DevOps pull requests REST 7.1](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests?view=azure-devops-rest-7.1)
- [Azure DevOps conditional ref updates](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1)
- [Azure DevOps Git pushes](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/create?view=azure-devops-rest-7.1)
- [Azure DevOps PR iterations](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1)
- [Azure DevOps PR threads](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1)
- [Azure DevOps PR statuses](https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-statuses?view=azure-devops-rest-7.1)
- [GitHub pull-request reviews](https://docs.github.com/en/rest/pulls/reviews)

## Reconciliation and revision

Every monitor run pins:

- current sealed PR observation;
- exact current PR repository snapshot;
- exact current target repository snapshot;
- most recent human-approved candidate;
- completed review batch, when applicable;
- exact workflow, policy, adapter, and validation revisions.

Human and bot commits already on the PR branch are adopted as input. A
feedback agent receives only the current PR repository, the authorized
completed review batch, and declared workflow inputs. It produces a revised
repository change and a structured response candidate. The response may
reference only thread IDs in that review batch.

Reconciliation always rebases; it never creates a merge commit. A clean
deterministic rebase produces a new candidate. A conflict produces durable
validation evidence. Policy may route that conflict to a bounded agent repair
step; the repaired candidate is rebased again before validation.

## Validation and impact assessment

The complete declared authoritative validation suite runs after every content
or base change. A prior result is reusable only when candidate digest, target
head, validation definition, workflow revision, toolchain, and relevant
environment identity all match exactly.

Impact compares the final candidate with the exact `repository/v1` candidate
named by reopened accepted-review evidence, not merely the previous PR head or
an authored baseline claim. The new `publish-impact/v1` record contains:

- changed paths, files, and lines;
- semantic summary;
- conflict-resolution evidence;
- review-requested versus incidental changes;
- validation-result differences;
- deterministic rule results;
- agent assessment and rationale;
- final `reapproval_required` decision and reasons.

Supported policy modes:

- `always`: every content change after approval requires reapproval;
- `rules`: configured deterministic rules require reapproval and an agent may
  escalate further;
- `agent-decides`: no optional deterministic rules; the agent may escalate.

Platform invariants apply in all modes. Missing or mismatched approval
evidence, changed approval-policy identity, invalid lineage, ambiguous impact,
assessor failure, an invalid assessment, or an unavailable assessment requires
human review. A rules-mode no-op therefore requires both passing deterministic
rules and an explicit valid non-escalating assessment; silence is never
interpreted as a waiver. The agent never waives a deterministic rule or
platform invariant. At both the conditional-wait boundary and the provider
mutation boundary, the web node resolves the deployment-owned policy version,
recomputes deterministic impact from the immutable baseline and candidate, and
requires the complete server-derived decision to equal the sealed impact
record. The recomputation request also binds the accepted and final validation,
current PR observation and response, binding ID, and action digest. The agent
assessment used by policy is independently recovered by the server-owned
evaluator from verified workflow evidence; it is never copied from the
untrusted impact body being checked. A shape-valid authored assessment or
`reapproval_required` value is never authority.

Initial publication authority is a new typed evidence envelope over the exact
accepted sealed `review/v1`, its reviewed candidate subject, authoritative
validation, and server-owned accepted disposition. The mutable review
projection alone is never authority, and the accepted evidence candidate must
equal the impact baseline exactly. Conditional reapproval uses the existing
durable question/answer wait machinery through the same generalized evidence
interface; the merge-only approval context is not reused as though it already
described PR semantics. The server-derived impact boolean selects the branch:
the approval step produces no answer artifact when reapproval is unnecessary,
and the publication step resolves exact accepted-review evidence instead. Both
branches use the provider-native typed PR executor; neither can fall through to
legacy direct Git. A human-wait context binds the response snapshot ID and
digest in addition to the observation, candidate, validation, impact, heads,
destination, action, policy, workflow, and build identities.

The reapproval question shows the exact delta since the previous human
approval, validation results, and escalation reasons. Approval advances the
approved baseline to that exact candidate. Rejection publishes nothing and
waits for a later completed review batch or explicit retry.

## Mutation order and idempotency

Initial publication and monitor updates use the same recoverable ordering:

1. reopen and revalidate every exact sealed input;
2. resolve destination policy and credential;
3. reconcile any prior provider result by operation key;
4. reread source and target heads;
5. refuse stale expected heads;
6. compare-and-swap the source branch;
7. find/create the PR or publish the authorized response/status;
8. record the durable publication result;
9. advance the PR binding cursor only after the intended safe terminal state.

Branch update, PR creation, status, and review response are separate idempotent
operations. A retry after an uncertain response observes provider state before
issuing another mutation. Provider metadata must carry a bounded stable
operation marker where the API permits it; otherwise exact source/target/head
identity is the recovery key.

Every branch operation receives the expected source head (or explicit expected
absence) and expected target head from sealed, server-authorized observation.
The provider adapter may not replace either value with a head it observed
during the mutation. Recovery of an uncertain PR create requires the provider's
current source and target heads to equal those exact sealed heads; returned
evidence is provider-observed rather than synthesized from the request.
Recovery of a review reply additionally binds the operation marker to the
authorized provider thread root.

## Failure behavior

- **Stale source head:** publish nothing; request a fresh observation.
- **Target moved before update:** publish nothing; reconcile again.
- **Rebase conflict:** seal failed reconciliation evidence and invoke only the
  configured repair path.
- **Authoritative validation failure:** leave the branch unchanged and publish
  a failed status for the exact observed head.
- **Provider timeout or rate limit:** bounded exponential backoff, followed by
  observation-based reconciliation.
- **Authentication, policy, unsupported API state, or malformed response:**
  fail closed and mark the binding `attention_required`.
- **Partial external success:** recover each sub-operation idempotently before
  retry.
- **PR completed or abandoned:** seal the final observation, persist terminal
  evidence, and archive monitoring.

Failed runs retain exact inputs for retry. The processed-review cursor advances
only after successful branch/response publication, an explicit validated
no-op, or terminal observation.

## User experience

An accepted review exposes **Publish as PR** with:

- provider and repository;
- target branch;
- validation workflow;
- impact policy;
- monitor workflow revision;
- polling and freshness settings.

After publication, Concourse shows the external PR link, exact heads,
validation and impact results, approval evidence, and linked monitor.

The internal PR page is an audit timeline, not a replacement forge UI. It
shows initial publication, completed review batches, adopted external commits,
reconciliation and validation runs, impact decisions, reapprovals, branch
updates, and terminal provider state.

Each validation status is attached to the exact PR head. Old status cannot
satisfy a newer iteration. Operators may retry pinned inputs, request a fresh
observation, pause/resume monitoring, or terminate monitoring. They cannot
edit stored heads, cursors, approvals, or impact evidence.

Pause and operator termination are monitoring controls separate from provider
lifecycle. Termination drains the standing monitor but never manufactures a
provider `completed` or `abandoned` observation.

## Security

- Read and write credentials are distinct policy references and can have
  different scopes.
- Mutation credentials remain mounted only in `concourse-web`.
- Read credentials are available only to the server-owned PR resource
  check/get container.
- Provider URLs come from startup-loaded destination policy, never workflow or
  agent output.
- Response thread IDs are intersected with the sealed authorized batch.
- All provider bodies, pages, thread counts, comment sizes, and response sizes
  are bounded.
- Provider markdown is data; it is never interpreted as workflow
  configuration or trusted instructions.
- Logs redact authorization headers, credential-bearing remotes, and provider
  response bodies that may echo secrets.

## Verification and acceptance

### Shared adapter conformance

Both adapters run the same behavioral suite:

- normalization and pagination;
- unchanged poll deduplication;
- completed-review batching;
- conflict and freshness triggers;
- exact expected-head success and stale failure;
- idempotent PR creation, status, branch update, and response recovery;
- rate-limit and retry classification;
- malformed, truncated, oversized, and unknown-enum responses;
- terminal completion and abandonment;
- credential and log non-disclosure.

Azure additionally uses official-schema/documentation fixtures and golden
normalized observations. Its lack of live validation is not waived or hidden.

### Live GitHub proof

Work is not complete until a supervised live GitHub repository demonstrates:

1. accepted candidate publishes one PR;
2. retry creates no duplicate PR;
3. pending comments do not trigger;
4. one submitted review triggers one monitor run;
5. external human/bot branch commits are adopted and never overwritten;
6. a source-head race fails stale;
7. clean trunk advancement refreshes after the configured interval;
8. conflict transition triggers repair/attention behavior once;
9. full validation is bound to the updated exact head;
10. deterministic and agent escalation request exact reapproval;
11. thread responses and summary are idempotent;
12. forge-native merge is observed and terminates monitoring.

### Repository verification

- fresh and previous-head database migrations;
- focused publisher, workflow, resource, DB, and UI suites;
- full unit, integration, Fly, and Helm checkpoints once at the milestone;
- migration pointer/preflight agreement;
- no reintroduction of the gateway or agent principals;
- no regression to existing direct-Git publication.

New migrations append after the actual migration head and advance the embedded
migration head, legacy-upgrade coverage, and `migrate-preflight` target
together. The concurrently planned reusable-node slice reserves
`1773106149`–`1773106150`, so PR-publish migrations begin at `1773106151` only
after those lower migrations exist in the same ordered series. A deployment
must never apply `1773106151` and later introduce a lower-numbered migration.
Frozen historical migrations are never edited.

## Rollout

Expected implementation duration is 8–12 weeks:

1. core records, PR binding, and approval generalization;
2. GitHub observation/publication adapter and initial PR workflow;
3. standing monitor pipeline and resource check/get;
4. revision, validation, impact, reapproval, response, and lifecycle loop;
5. Azure DevOps REST 7.1 adapter and conformance fixtures;
6. UI/operations, live GitHub matrix, and hardening.

Provider rollout is capability-gated. Existing direct-Git publication remains
available. PR publication is disabled until a destination policy names a
configured provider adapter and both read/write credential mappings.

## Deferred and non-goals

- a new direct-to-trunk revalidation loop;
- deleting or redesigning the accepted direct-Git adapter;
- platform-driven PR completion;
- webhooks;
- selective omission of required validation;
- fork-based PRs;
- comment-by-comment triggers;
- a persistent conversational agent;
- live Azure DevOps validation without an authorized environment;
- general-purpose provider plugins beyond the GitHub/Azure PR contract;
- cross-team PR bindings or sharing.
- physical deletion of archived PR monitor pipelines before a retention policy
  exists.
