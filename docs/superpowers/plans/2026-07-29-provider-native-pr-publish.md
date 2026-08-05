# Provider-Native Pull Request Publish Implementation Plan

> **PARTIALLY SUPERSEDED (2026-08-05).** The Azure DevOps adapter (Task 13)
> and every Azure-specific policy, transport, and conformance concern described
> below were removed from the tree. GitHub is the only supported forge; the
> provider-neutral `Observer`/`Mutator` seam is retained. Read the Azure
> sections as historical record only.

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish exactly reviewed repository changes as provider-native pull
requests, monitor completed review batches by polling, revise and fully
revalidate safely, conditionally reapprove material changes, and support
GitHub live plus Azure DevOps REST 7.1 by contract.

**Architecture:** Preserve the accepted direct-Git publisher and add a
provider-native PR lane. A binding-owned ordinary Concourse source pipeline
polls a provider-neutral `forge-pr` resource, captures immutable
`pull-request/v1` observations, and launches serialized `pr-monitor-v3`
workflow runs through the existing source-admission machinery. All mutations
remain idempotent ATC publication operations with sealed expected heads and
typed evidence.

**Tech Stack:** Go 1.25, PostgreSQL migrations and squirrel-backed factories,
Concourse resource `check`/`in` protocol, schema-v3 agent workflows, Elm 0.19,
Helm, GitHub REST API, Azure DevOps Git REST API 7.1.

## Global Constraints

- Work only in
  `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-platform-rebase`
  on `codex/agentic-platform-rebase`.
- Preserve the accepted direct-Git branch/trunk implementation; do not widen
  its mutation-time head observation into PR authority.
- The reusable-node plan owns migrations `1773106149`–`1773106150`. This
  plan owns `1773106151`–`1773106154`, and no migration task may land until
  `1773106149` and `1773106150` exist in the same branch. Re-check the actual
  tail immediately before each migration; if the reservations changed,
  renumber this entire unpublished four-migration series atomically.
- Never edit migrations `1773106128`–`1773106148`.
- Agents never receive mutation-capable forge credentials.
- Read and write forge credentials are distinct destination-policy references.
- Every PR branch mutation carries sealed expected source and target heads.
- The forge, not Jetbridge, completes or abandons the PR.
- Polling defaults to 5 minutes; freshness defaults to 6 hours.
- Only submitted/completed review batches trigger agent revision work.
- One nonterminal mutation workflow run is allowed per PR binding.
- Full authoritative validation reruns after every content or target-head
  change.
- Deterministic policy and platform invariants can require reapproval; an
  agent can only escalate.
- Azure DevOps is pinned to REST API `7.1` and must be labeled
  `contract-tested, not live-validated`.
- Use focused tests during tasks. Run broad suites once at the final
  checkpoint, following the semantic-rebase session test budget.

## Recorded implementation decisions

- Model the initial source-branch authority as a sealed `missing`
  `pull-request/v1` observation; never discover its expected head inside the
  mutator.
- Leave direct-Git branch/merge behavior unchanged. PR branch updates use a
  separate caller-supplied exact-lease Git transport.
- Use one `agent_pr_bindings` coordination table. Reconstruct immutable audit
  history from ordinary workflow runs, snapshots, waits, outcomes, and
  publication occurrences; do not create a second PR-event table.
- Extend the existing ordinary resource-source pipeline registry with a
  binding instance key. Do not create a parallel scheduler or PR-specific
  pipeline lifecycle.
- Make `forge-pr` `in` verify the selected version and materialize the exact
  source and target repositories. A deterministic workflow function then
  seals them as `repository/v1`; this avoids a credentialed capture service.
- Feed acknowledged binding state back to `forge-pr` by re-rendering its
  protected server-owned source config. The binding row remains authoritative;
  every resource version carries the projected binding revision and the
  launch gate rejects stale-config admissions. No internal bearer endpoint or
  new scheduler is introduced.
- Split source-branch CAS, PR find/create, validation status, and review
  response into separate idempotent publication operations.
- Keep the accepted-review candidate as the approved `repository/v1`
  baseline, while branch/create operations carry the final
  `repository-change/v1` plus its exact post-rebase `validation/v1` and
  `publish-impact/v1`. Persist those final refs directly and include them in
  semantic operation identity.
- Require the target-head lease in the exact pre-update observation even when
  the source already equals the requested head. A target race after that
  observation remains safe and schedules later freshness work. PR-create
  recovery must return and compare provider-observed heads; review-reply
  recovery must match the authorized provider thread root.
- Carry the exact target ref on all four PR operation kinds, including status
  and response, so deployment policy can match the intended target without
  inferring it from provider state or forbidding multiple target rules.
- Give PR policy an explicit provider/repository/API/repository-URL identity
  plus distinct protected read and trusted write credential references.
  Direct-Git rules retain their current fields and cannot route PR actions.
- Materialize the verified nested Git payload of the exact change snapshot for
  one provider-neutral Git smart-HTTP ref-lease call; never pass the outer
  snapshot root as a worktree. Use that object-upload-plus-CAS path for both
  GitHub and Azure DevOps. Azure REST ref updates remain a contract-tested
  pre-existing-object seam and are not selected for a new local commit.
  Select Azure's OAuth Bearer Git authentication explicitly from the provider;
  never infer Basic/PAT behavior from token text or put a token in argv/URLs.
  Complete stale source/target leases as safe terminal reconciliation results
  rather than repeatedly reclaiming an obsolete pending operation.
- Treat the impact assessor as required authority in every policy mode. A
  missing, invalid, failed, or ambiguous assessment escalates; only an explicit
  valid non-escalating assessment can accompany a deterministic rules no-op.
- At both await and publish, resolve the deployment-owned policy identity and
  recompute deterministic impact from immutable inputs: accepted baseline and
  validation, final candidate and validation, current observation and response,
  binding ID, and action digest. Independently recover the agent assessment
  from verified workflow evidence rather than the body being checked. Require
  the complete derived decision to equal the sealed `publish-impact/v1`, and
  require its baseline to be the exact accepted-review candidate.
- Make the PR reapproval answer a conditional typed artifact selected by the
  exact server-opened impact record. When no answer is required, resolve the
  exact accepted `review/v1`, approved `repository/v1`, original
  `validation/v1`, accepted workflow run, and outcome revision, then hand that
  evidence to the same provider-native PR executor. Never route either branch
  through the legacy publisher.
- Bind both snapshot ID and digest for `pull-request-response/v1` in the
  server-synthesized approval context and require exact equality again at the
  publication handoff.
- Keep three planned migrations ordered after the reusable-node reservations;
  never deploy a higher PR migration before the two lower migrations exist.
- Treat `Observation.ReviewBatches` as a strict delta after the acknowledged
  input cursor. The core preserves but never decodes cursor structure; each
  provider conformance suite proves that acknowledged batches are not replayed.
- Give Azure DevOps one unambiguous policy decomposition:
  `api_base_url` is the organization URL, while the provider repository
  locator is exactly `project/repositoryID`. Production composition verifies
  that the credential-free Git URL names that same organization, project, and
  repository before constructing either REST or smart-HTTP transport.
- Require a nonempty cursor on active and terminal observations. The empty
  cursor is an input/pre-create sentinel, not a valid terminal progression.
- Derive the freshness action's canonical time bucket from its first due
  deadline (`LastReconciledAt + FreshnessInterval`). Keep that identity stable
  until acknowledged so an in-flight action is not redispatched at hour 12.
- Have each adapter select only the earliest completed review after its input
  cursor. Encode a strict versioned cursor with the selected batch digest and
  provider-state signature so identical re-observation is stable and later
  batches remain queued.
- Scope Day-1 observation and materialization to same-repository PRs. Render
  the provider API base URL and credential-free repository URL from trusted
  destination policy; never derive either from PR text or embed credentials.
- Keep `forge-pr` protocol logic in importable
  `agent/pullrequest/resource`; use `cmd/forge-pr-resource` only as the thin
  executable dispatcher. This preserves reuse by pipeline rendering and avoids
  mixing `package main` with the provider-neutral resource contract.
- Persist PR creation occurrence, original accepted-review authority, and the
  current approved baseline as three distinct facts. The approved baseline is
  mutable only through an atomic cursor/head/baseline acknowledgement after an
  exact successful human-authorized revision publication.
- Refuse to migrate any pre-authority PR binding row. The legacy coordination
  projection cannot prove accepted-review, exact `create_pr`, or approved
  baseline authority, so migration `1773106154` locks the table and fails
  instead of backfilling from partial publication JSON.
- Build initial PR publication as a separate binding-free coordinator. After
  find/create it must reobserve the provider PR, seal the active observation,
  and use that observation's opaque nonempty cursor when creating the binding.
- Model monitor effects by trigger: review batches require response authority;
  conflict and freshness carry typed semantic absence that provider response
  publication rejects; completed and abandoned observations bypass mutation
  workflows and advance terminal binding state from exact observation evidence.
- Keep `--agent-publisher-pull-requests-enabled` fail-closed until every
  production dependency is concrete: initial coordinator and created-PR
  observer, exact target renderer, revision executor, monitor-run inspector,
  impact resolver/evaluator, final repository baseline materializer, and atomic
  approved-baseline advancement. Partial composition must refuse startup.

---

## File and package map

New focused units:

- `agent/snapshot/contracts/pull_request.go`: normalized PR observation body
  and validator.
- `agent/snapshot/contracts/pull_request_response.go`: typed review-response
  body and validator.
- `agent/snapshot/contracts/publish_impact.go`: impact record and policy result
  validator.
- `agent/pullrequest/types.go`: provider-neutral observations, cursors,
  triggers, actions, and adapter interfaces.
- `agent/pullrequest/triggers.go`: pure actionable-version decision engine.
- `agent/pullrequest/github/`: GitHub REST observer and mutator.
- `agent/pullrequest/azuredevops/`: Azure DevOps REST 7.1 observer and mutator.
- `agent/pullrequest/resource/`: Concourse `check` and `in` protocol.
- `cmd/forge-pr-resource/`: resource executable entrypoint.
- `agent/pullrequest/pipeline.go`: binding-keyed standing monitor-pipeline
  renderer.
- `agent/pullrequest/coordinator.go`: initial binding, launch/ack, and terminal
  lifecycle orchestration.
- `atc/db/agent_pr_bindings_factory.go`: PostgreSQL coordination store.
- `agent/api/pullrequests/`: team-scoped read/control API.
- `web/elm/src/Concourse/AgentPullRequest.elm`: wire decoder.
- `web/elm/src/AgentPullRequest/AgentPullRequest.elm`: audit timeline page.

Existing units extended in place:

- snapshot contract registry, schema documents, histories, and parity tests;
- publisher request/evidence/policy/router and publication occurrence store;
- workflow resource-source pipeline ownership and launch reconciliation;
- ATC composition, routes, handler wiring, and Helm publisher configuration;
- workflow-run origin projection and UI links;
- migration head/preflight and agentic documentation.

---

### Task 1: Add the three sealed PR record contracts

**Files:**

- Create: `agent/snapshot/contracts/pull_request.go`
- Create: `agent/snapshot/contracts/pull_request_response.go`
- Create: `agent/snapshot/contracts/publish_impact.go`
- Create: `agent/snapshot/contracts/schemas/pull-request.v1.rev2.json`
- Create: `agent/snapshot/contracts/schemas/publish-impact.v1.rev2.json`
- Create:
  `agent/snapshot/contracts/schemas/pull-request-response.v1.rev2.json`
- Modify: `agent/snapshot/contracts/record.go`
- Modify: `agent/snapshot/contracts/record_prototypes.go`
- Modify: `agent/snapshot/contracts/record_schema.go`
- Modify: `agent/snapshot/contracts/raw_codec.go`
- Modify: `agent/snapshot/contracts/registry.go`
- Modify: `agent/snapshot/contracts/record_schema_history_test.go`
- Modify: `agent/snapshot/contracts/record_schema_bump_test.go`
- Modify: `agent/snapshot/contracts/schema_fixtures_internal_test.go`
- Modify: `agent/snapshot/contracts/schema_ports_internal_test.go`
- Test: `agent/snapshot/contracts/pull_request_test.go`
- Test: `agent/snapshot/contracts/pull_request_response_test.go`
- Test: `agent/snapshot/contracts/publish_impact_test.go`
- Test: `agent/snapshot/contracts/registry_test.go`
- Test: `agent/snapshot/contracts/schema_document_internal_test.go`

**Interfaces:**

- Consumes: existing `contracts.Record[T]`, `Subject`, schema revision history,
  and two-gate record validation.
- Produces:

```go
type PullRequestHeadExpectation struct {
	Exists bool   `json:"exists"`
	SHA    string `json:"sha,omitempty"`
}

type PullRequestBody struct {
	Provider      string                   `json:"provider"`
	Repository    string                   `json:"repository"`
	ExternalID    string                   `json:"external_id"`
	URL           string                   `json:"url"`
	State         PullRequestState         `json:"state"`
	Mergeability  PullRequestMergeability  `json:"mergeability"`
	SourceRef     string                   `json:"source_ref"`
	SourceSHA     string                   `json:"source_sha"`
	ExpectedSource *PullRequestHeadExpectation `json:"expected_source,omitempty"`
	TargetRef     string                   `json:"target_ref"`
	TargetSHA     string                   `json:"target_sha"`
	Iteration     string                   `json:"iteration"`
	Trigger       PullRequestTrigger       `json:"trigger"`
	ReviewBatches []PullRequestReviewBatch `json:"review_batches"`
	Threads       []PullRequestThread      `json:"threads"`
}

type PullRequestResponseBody struct {
	BatchID string                      `json:"batch_id"`
	Summary string                      `json:"summary"`
	Replies []PullRequestThreadResponse `json:"replies"`
}

type PublishImpactBody struct {
	BaselineDigest     string                `json:"baseline_digest"`
	CandidateDigest    string                `json:"candidate_digest"`
	ChangedFiles       []PublishChangedFile  `json:"changed_files"`
	ChangedLines       int                   `json:"changed_lines"`
	ConflictResolution bool                  `json:"conflict_resolution"`
	ValidationChanges []string              `json:"validation_changes"`
	RuleResults        []PublishImpactRule   `json:"rule_results"`
	AgentAssessment   *AgentImpactAssessment `json:"agent_assessment,omitempty"`
	ReapprovalRequired bool                  `json:"reapproval_required"`
	Reasons             []string             `json:"reasons"`
}
```

- [ ] **Step 1: Write failing contract tests**

Add table tests that reject unknown states, duplicate batch/thread IDs, a
response referencing a thread absent from its `pull-request/v1` subject, an
agent assessment that attempts to waive a failed deterministic rule, and
schema history/Go prototype drift.

```go
func TestPullRequestResponseRejectsThreadOutsideAuthorizedObservation(t *testing.T) {
	body := validPullRequestResponseBody()
	body.Replies[0].ThreadID = "thread-not-in-subject"
	if err := ValidatePullRequestResponseAgainst(
		body,
		pullRequestWithAuthorizedThreads("thread-1"),
	); err == nil {
		t.Fatal("accepted reply to an unauthorized thread")
	}
}
```

- [ ] **Step 2: Run the focused tests and confirm RED**

Run:

```bash
go test ./agent/snapshot/contracts -run 'PullRequest|PublishImpact|Registry|SchemaDocument' -count=1
```

Expected: compilation fails because the new record bodies and registrations do
not exist.

- [ ] **Step 3: Implement record bodies, validators, and schema documents**

Use exact enums:

```go
const (
	PullRequestActive    PullRequestState = "active"
	PullRequestMissing   PullRequestState = "missing"
	PullRequestCompleted PullRequestState = "completed"
	PullRequestAbandoned PullRequestState = "abandoned"

	PullRequestMergeable     PullRequestMergeability = "mergeable"
	PullRequestConflicted    PullRequestMergeability = "conflicted"
	PullRequestPolicyBlocked PullRequestMergeability = "policy_blocked"
	PullRequestUnknown       PullRequestMergeability = "unknown"
)
```

For `missing`, `ExternalID`, `URL`, and `SourceSHA` are empty; provider,
repository, source/target refs, target head, provider iteration/version, and
`ExpectedSource` are required. `ExpectedSource.Exists=false` requires an empty
SHA, while `Exists=true` requires a full object ID. Active and terminal
observations reject `ExpectedSource` and require external ID, URL, and exact
source/target heads. Stable batch, review, thread, and comment IDs,
iteration/commit identity, reviewer readiness, bounded markdown, anchors, and
deterministic unique ordering are all part of semantic validation.

Register all three types in `builtinTypeNames`, `builtinValidator`,
`recordPrototypes`, `recordSchemaHistories`, and the canonical schema-digest
golden. Treat `pull-request/v1` as platform-authored at workflow boundaries;
its generic read-time validator still validates exact schema and body shape.
Generic response validation enforces only intrinsic deterministic shape;
`ValidatePullRequestResponseAgainst` enforces exact batch/thread subset
authority after reopening the response's `pull-request/v1` subject.

- [ ] **Step 4: Run contract and snapshot suites**

Run:

```bash
go test ./agent/snapshot/contracts ./agent/snapshot/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/snapshot/contracts
git commit -m "feat(snapshot): add pull request publish records"
```

---

### Task 2: Define provider-neutral PR observations and trigger decisions

**Files:**

- Create: `agent/pullrequest/types.go`
- Create: `agent/pullrequest/triggers.go`
- Test: `agent/pullrequest/types_test.go`
- Test: `agent/pullrequest/triggers_test.go`

**Interfaces:**

- Consumes: contract enums from Task 1.
- Produces:

```go
type Provider string

const (
	ProviderGitHub       Provider = "github"
	ProviderAzureDevOps  Provider = "azure-devops"
)

type Locator struct {
	Provider   Provider
	Repository string
	ExternalID string
}

type Cursor string

type Observation struct {
	Locator
	Cursor        Cursor
	URL           string
	State         contracts.PullRequestState
	Mergeability  contracts.PullRequestMergeability
	SourceRef     string
	SourceSHA     string
	ExpectedSource *contracts.PullRequestHeadExpectation
	TargetRef     string
	TargetSHA     string
	Iteration     string
	ReviewBatches []ReviewBatch
	Threads       []Thread
}

type TriggerPolicy struct {
	Now                time.Time
	PollInterval       time.Duration
	FreshnessInterval  time.Duration
	LastCursor         Cursor
	LastTargetSHA      string
	LastReconciledAt   time.Time
	ActiveActionDigest string
}

func ActionFor(Observation, TriggerPolicy) (Action, bool, error)
```

`TriggerPolicy.LastCursor`, `LastTargetSHA`, `LastReconciledAt`, and
`ActiveActionDigest` always come from acknowledged binding state projected by
the server, never from the resource's previous version.

Provider seams:

```go
type Observer interface {
	Observe(context.Context, Locator, Cursor) (Observation, error)
}

type Mutator interface {
	CompareAndSwapBranch(context.Context, BranchMutation) (BranchResult, error)
	FindOrCreatePullRequest(context.Context, CreateRequest) (ExternalPullRequest, error)
	PublishValidationStatus(context.Context, StatusRequest) (ExternalResult, error)
	PublishReviewResponse(context.Context, ResponseRequest) (ExternalResult, error)
}
```

The initial locator may omit `ExternalID` only while observing the sealed
`missing` pre-create state. Provider cursors are opaque bounded canonical
bytes; provider-specific cursor structure never leaks into coordinator or API
contracts. Active and terminal observations require a nonempty cursor, and
`Observation.ReviewBatches` contains only provider-complete batches strictly
after the input cursor.

- [ ] **Step 1: Write trigger-table tests**

Cover unchanged state, GitHub submitted review, Azure ready-vote transition,
unchanged conflict, changed conflict signature, target movement before and
after six hours, an equivalent active action, completed state, and abandoned
state.

```go
func TestActionForWaitsSixHoursBeforeFreshness(t *testing.T) {
	policy := basePolicy()
	policy.LastTargetSHA = "old"
	policy.LastReconciledAt = policy.Now.Add(-5*time.Hour - 59*time.Minute)
	_, actionable, err := ActionFor(observationWithTarget("new"), policy)
	require.NoError(t, err)
	require.False(t, actionable)
}
```

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest -count=1
```

Expected: compilation fails because the package does not exist.

- [ ] **Step 3: Implement strict types and the pure trigger engine**

Action kinds are exactly:

```go
const (
	ActionReviewBatch ActionKind = "review_batch"
	ActionConflict    ActionKind = "conflict"
	ActionFreshness   ActionKind = "freshness"
	ActionCompleted   ActionKind = "completed"
	ActionAbandoned   ActionKind = "abandoned"
)
```

`pull-request/v1` additionally permits the platform-authored trigger token
`initial_publish` only with lifecycle `missing`; `ActionFor` never emits it
from the standing monitor.

Compute `Action.Digest` from canonical JSON over locator, exact heads, kind,
and provider cursor. Never include wall-clock time except the canonical
freshness due-deadline bucket selected from `LastReconciledAt` and
`FreshnessInterval`; that bucket remains stable until acknowledgement.

- [ ] **Step 4: Run tests**

```bash
go test ./agent/pullrequest -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest
git commit -m "feat(agent): define pull request trigger contract"
```

---

### Task 3: Persist PR bindings and the single-writer launch gate

**Files:**

- Create:
  `atc/db/migration/migrations/1773106151_create_agent_pr_bindings.up.sql`
- Create:
  `atc/db/migration/migrations/1773106151_create_agent_pr_bindings.down.sql`
- Create: `agent/pullrequest/store.go`
- Create: `atc/db/agent_pr_bindings_factory.go`
- Test: `atc/db/agent_pr_bindings_factory_test.go`
- Test: `atc/db/migration/pr_bindings_test.go`
- Modify: `atc/db/agent_workflow_resource_source_runtime_types.go`
- Modify: `atc/db/agent_workflow_resource_source_admissions_factory.go`
- Test: `atc/db/agent_workflow_resource_source_admissions_factory_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`

**Interfaces:**

- Consumes: `pullrequest.Locator`, `Cursor`, workflow definition/run IDs,
  pipeline IDs, and publication occurrence IDs.
- Produces:

```go
type BindingStore interface {
	Create(context.Context, CreateBinding) (Binding, bool, error)
	Get(context.Context, int, int64) (Binding, bool, error)
	GetByExternal(context.Context, int, Locator) (Binding, bool, error)
	ReserveLaunch(context.Context, ReserveLaunch) (LaunchReservation, bool, error)
	AttachRun(context.Context, AttachRun) (Binding, error)
	AcknowledgeAction(context.Context, AcknowledgeAction) (Binding, error)
	MarkAttention(context.Context, int, int64, string) (Binding, error)
	MarkTerminal(context.Context, TerminalBinding) (Binding, error)
	RequestObservation(context.Context, OperatorRequest) (Binding, error)
	Pause(context.Context, OperatorRequest) (Binding, error)
	Resume(context.Context, OperatorRequest) (Binding, error)
	Terminate(context.Context, OperatorRequest) (Binding, error)
	ListAudit(context.Context, int, int64, AuditFilter) ([]AuditEntry, error)
	ListActive(context.Context, int) ([]Binding, error)
}
```

`ReserveLaunch` includes binding ID, action digest, observation snapshot,
observed source/target heads, expected row revision, and a bounded expiry. It
returns the same reservation token for an idempotent replay and
`reserved=false` when another nonterminal action owns the binding. `AttachRun`
accepts only a same-team nonterminal run whose exact origin is
`pr-monitor/<binding-id>` and whose reservation token/action match.
Pause/resume/terminate are revision-CAS operator intents. Terminate drains
monitoring but cannot manufacture provider `completed` or `abandoned`
evidence.

- [ ] **Step 1: Write migration and concurrent-store tests**

Prove owner-team isolation, unique provider/repository/external ID, opaque
cursor round-trip, idempotent create, one active reservation, reservation
replay and expiry recovery, stale revision rejection, exact-run
acknowledgement, terminal immutability, binding-keyed source-pipeline
uniqueness, and `pipeline_id ON DELETE SET NULL`.

```go
go func() {
	_, reserved, err := factory.ReserveLaunch(ctx, reservationFor(actionA))
	results <- claimResult{reserved, err}
}()
go func() {
	_, reserved, err := factory.ReserveLaunch(ctx, reservationFor(actionB))
	results <- claimResult{reserved, err}
}()
```

Require exactly one `reserved=true`.

- [ ] **Step 2: Run migration/store focus and confirm RED**

```bash
ginkgo --focus='AgentPRBindingsFactory' ./atc/db/
ginkgo --focus='agent PR bindings|Legacy Database Upgrade' ./atc/db/migration/
```

Expected: new tests fail because migration `1773106151` and the factory are
absent.

- [ ] **Step 3: Implement migration and factory**

Before writing this migration, assert that migrations `1773106149` and
`1773106150` exist. The migration creates `agent_pr_bindings` with bounded
state checks and a partial unique active-action constraint. It also adds a
nullable `pr_binding_id` to
`agent_workflow_resource_source_pipelines`. PostgreSQL truncates the generated
75-byte uniqueness name, so the migration must not guess its identifier. Use a
catalog-safe block that requires exactly one unique constraint whose
`pg_get_constraintdef` is
`UNIQUE (team_id, workflow_definition_id)`, quote its discovered `conname`
with `format('%I', ...)`, and drop it. Also drop
`agent_workflow_resource_source_pipelines_active`, then create:

```sql
CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_definition
  ON agent_workflow_resource_source_pipelines (team_id, workflow_definition_id)
  WHERE pr_binding_id IS NULL;
CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_binding
  ON agent_workflow_resource_source_pipelines (team_id, pr_binding_id)
  WHERE pr_binding_id IS NOT NULL;
CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_active
  ON agent_workflow_resource_source_pipelines (team_id, workflow_name)
  WHERE state = 'active' AND pr_binding_id IS NULL;
```

The down migration refuses to proceed while a binding-owned row exists,
then uses `ALTER TABLE ... ADD UNIQUE (team_id, workflow_definition_id)` so
PostgreSQL regenerates the original catalog-safe name, and restores the exact
active index. Migration tests query `pg_constraint` to prove the intended
two-column constraint is found/dropped/restored, and prove two bindings can
use the same pinned `pr-monitor-v3` definition while two definition-owned rows
still cannot. Use a row
`revision BIGINT NOT NULL DEFAULT 1` and update with
`WHERE id=$1 AND revision=$2`. Store provider cursors as bounded JSONB but
compare them as opaque canonical bytes. Launch token, active run, terminal
evidence, provider lifecycle, pause, operator-terminated, and expiry columns
use mutually consistent CHECK constraints; all locator/ref/hash text is
bounded and trimmed.

- [ ] **Step 4: Advance migration gates and run serial DB tests**

Set `jetbridgeHeadMigration` and the preflight target to `1773106151`.

Run:

```bash
ginkgo --focus='agent PR bindings|Legacy Database Upgrade' ./atc/db/migration/
ginkgo --focus='AgentPRBindingsFactory' ./atc/db/
bash docs/migration/migrate-preflight_test.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add atc/db/migration atc/db/agent_pr_bindings_factory.go \
  atc/db/agent_pr_bindings_factory_test.go agent/pullrequest/store.go \
  docs/migration/migrate-preflight.sh
git commit -m "feat(db): persist pull request monitor bindings"
```

---

### Task 4: Generalize exact publication evidence for PRs

**Files:**

- Create: `agent/publisher/evidence.go`
- Create: `agent/publisher/review_evidence.go`
- Test: `agent/publisher/evidence_test.go`
- Test: `agent/publisher/review_evidence_test.go`
- Modify: `agent/publisher/types.go`
- Modify: `agent/publisher/approval.go`
- Modify: `atc/db/agent_publications_factory.go`
- Test: `atc/db/agent_publications_factory_test.go`
- Create:
  `atc/db/migration/migrations/1773106152_add_pr_publication_evidence.up.sql`
- Create:
  `atc/db/migration/migrations/1773106152_add_pr_publication_evidence.down.sql`
- Test: `atc/db/migration/pr_publication_evidence_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`

**Interfaces:**

- Consumes: exact accepted `review/v1`, authoritative `validation/v1`
  revision-3, existing durable merge `ApprovalEvidence`, and publication
  occurrences.
- Produces:

```go
type EvidenceKind string

const (
	EvidenceAcceptedReview EvidenceKind = "accepted_review"
	EvidenceHumanWait      EvidenceKind = "human_wait"
)

type AcceptedReviewEvidence struct {
	Review              snapshot.SnapshotRef
	Candidate           snapshot.SnapshotRef
	Validation          snapshot.SnapshotRef
	ReviewWorkflowRunID snapshot.WorkflowRunID
	OutcomeRevision     int64
	AcceptedBy          string
	AcceptedAt          time.Time
}

type PublicationEvidence struct {
	Kind           EvidenceKind
	AcceptedReview *AcceptedReviewEvidence
	HumanWait      *ApprovalEvidence
}

type EvidenceVerifier interface {
	Verify(context.Context, EvidenceRequest) (PublicationEvidence, error)
}
```

- [ ] **Step 1: Write failing evidence tests**

Reject a review with a different candidate subject, a non-accept verdict,
mutable projection-only claims, validation bound to another candidate, a
human wait for another PR intent, both evidence variants at once, and neither
variant.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/publisher -run 'Evidence|Review' -count=1
ginkgo --focus='AgentPublicationsFactory.*evidence' ./atc/db/
```

- [ ] **Step 3: Implement the exact verifier and additive persistence**

Reopen and rehash sealed review and validation bytes through existing snapshot
inspectors. Resolve the accepted review subject through the exact primary
`repository/v1` input binding of the `code-review-v3` run; do not infer a
snapshot ID from type or digest. Require the exact accepted outcome output and
revision. Add
`agent_publication_inputs(publication_id, team_id, role, snapshot_id)` for
observation, validation, impact, and response refs, plus one-to-one,
same-team
`agent_publication_approval_evidence` keyed by publication occurrence with
mutually exclusive accepted-review and human-wait CHECK shapes. Keep legacy
merge approval columns readable and unchanged; backfill/project them into
`EvidenceHumanWait` for existing operations.

- [ ] **Step 4: Run publisher, DB, and migration focus**

Advance the migration head to `1773106152`.

```bash
go test ./agent/publisher -run 'Evidence|Publication' -count=1
ginkgo --focus='PR publication evidence|Legacy Database Upgrade' ./atc/db/migration/
ginkgo --focus='AgentPublicationsFactory.*evidence' ./atc/db/
bash docs/migration/migrate-preflight_test.sh
```

Expected: PASS, including existing direct-merge approval tests.

- [ ] **Step 5: Commit**

```bash
git add agent/publisher atc/db docs/migration/migrate-preflight.sh
git commit -m "feat(publisher): authorize pull requests with exact review evidence"
```

---

### Task 5: Implement the GitHub observation adapter

**Files:**

- Create: `agent/pullrequest/httpclient.go`
- Create: `agent/pullrequest/github/client.go`
- Create: `agent/pullrequest/github/observe.go`
- Create: `agent/pullrequest/github/wire.go`
- Test: `agent/pullrequest/github/observe_test.go`
- Test: `agent/pullrequest/github/testdata/pull_request_active.json`
- Test: `agent/pullrequest/github/testdata/pull_request_merged.json`
- Test: `agent/pullrequest/github/testdata/pull_request_closed.json`
- Test: `agent/pullrequest/github/testdata/reviews_page_1.json`
- Test: `agent/pullrequest/github/testdata/reviews_page_2.json`
- Test: `agent/pullrequest/github/testdata/review_comments_page_1.json`

**Interfaces:**

- Consumes: `pullrequest.Observer`, locator/cursor/observation types.
- Produces:

```go
func NewObserver(baseURL string, token TokenSource, client *http.Client) (*Observer, error)

func (o *Observer) Observe(
	context.Context,
	pullrequest.Locator,
	pullrequest.Cursor,
) (pullrequest.Observation, error)
```

- [ ] **Step 1: Write HTTP fixture tests**

Cover active/merged/closed PRs, exact heads, mergeability unknown,
chronological submitted reviews, pending-review exclusion, review comment
grouping by `pull_request_review_id`, pagination, 403 rate limit, 429
`Retry-After`, malformed JSON, oversized body, and URL host mismatch.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest/github -count=1
```

- [ ] **Step 3: Implement bounded GitHub REST reads**

Use `X-GitHub-Api-Version: 2022-11-28`, bounded pagination, a fixed user agent,
and authorization header redaction. Normalize only reviews with non-nil
`submitted_at`. Preserve review ID and `commit_id` as the batch marker. Select
only the earliest submitted review after the acknowledged watermark; sort by
parsed submission time and numeric ID. Use a strict versioned canonical cursor
that binds the watermark, selected batch digest, lifecycle, mergeability,
exact heads, and iteration. Reject malformed nonempty cursors, cross-host
pagination, redirects, over-limit pages/bodies/collections, and ordinary
authorization failures distinctly from header-proven `403`/`429` rate limits.
Preserve a nonempty overall review body as a deterministic context-only
unanchored thread whose ID is excluded from reply authority.

- [ ] **Step 4: Run adapter and trigger suites**

```bash
go test ./agent/pullrequest/... -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest
git commit -m "feat(publisher): observe GitHub pull requests"
```

---

### Task 6: Add provider-native idempotent mutation operations

**Files:**

- Create: `agent/publisher/pr_actions.go`
- Create: `agent/publisher/pr_service.go`
- Test: `agent/publisher/pr_service_test.go`
- Create: `agent/publisher/gittransport/ref_lease.go`
- Test: `agent/publisher/gittransport/ref_lease_test.go`
- Create: `agent/pullrequest/github/mutate.go`
- Test: `agent/pullrequest/github/mutate_test.go`
- Modify: `agent/publisher/types.go`
- Modify: `agent/publisher/policy.go`
- Modify: `agent/publisher/store.go`
- Modify: `atc/db/agent_publications_factory.go`
- Modify: `atc/atccmd/agent_publisher.go`
- Test: `atc/atccmd/agent_publisher_internal_test.go`
- Create:
  `atc/db/migration/migrations/1773106153_add_publication_operation_kind.up.sql`
- Create:
  `atc/db/migration/migrations/1773106153_add_publication_operation_kind.down.sql`
- Test: `atc/db/migration/publication_operation_kind_test.go`

**Interfaces:**

- Consumes: existing publication acquire/lease/occurrence store, provider
  policy, exact evidence, and `pullrequest.Mutator`.
- Produces:

```go
type OperationKind string

const (
	OperationPublishPRBranch OperationKind = "publish_pr_branch"
	OperationCreatePR        OperationKind = "create_pr"
	OperationPublishPRStatus OperationKind = "publish_pr_status"
	OperationRespondToReview OperationKind = "respond_to_review"
)

type HeadExpectation = contracts.PullRequestHeadExpectation

type PRService interface {
	PublishBranch(context.Context, BranchPublicationRequest) (Publication, error)
	FindOrCreate(context.Context, PullRequestPublicationRequest) (Publication, error)
	PublishStatus(context.Context, StatusPublicationRequest) (Publication, error)
	PublishResponse(context.Context, ResponsePublicationRequest) (Publication, error)
}
```

Every `BranchPublicationRequest` contains `ExpectedSource HeadExpectation`,
`ExpectedTargetSHA`, and `NewSourceSHA`. `Exists=false` is the only expected
absence representation and requires an empty SHA; `Exists=true` requires an
exact full object ID. Every request kind carries the exact target ref used by
policy matching.

- [ ] **Step 1: Write failing operation and recovery tests**

Prove operation-key separation, exact-source stale refusal, exact-target stale
refusal, branch success followed by PR-create timeout/recovery, duplicate
status recovery, response thread authorization, and unchanged direct-Git
operation keys.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/publisher ./agent/pullrequest/github \
  ./atc/atccmd -run 'PR|OperationKind|DirectGit' -count=1
```

- [ ] **Step 3: Implement operation kinds and GitHub mutations**

GitHub and Azure DevOps branch mutation use `git push
--force-with-lease=<ref>:<caller-expected-head>` (or exact expected absence)
through a new caller-sealed Git transport. It must not use the current
direct-Git backend's observe-then-lease behavior or GitHub's unconditional ref
PATCH. PR find/create uses source ref, target ref, and a bounded
`Jetbridge-Operation` marker in the body. A retry lists matching open and
closed PRs before creating. Status and response requests bind exact head/batch
and use independent semantic keys. The marker is exact machine-authored
recovery metadata, not an operation identity inferred from arbitrary mutable
human text. A missing, altered, or ambiguous marker fails closed and requires
operator attention instead of creating a duplicate.

Policy resolution binds PR actions to an exact provider, repository, target
branch, API base URL, credential-free repository URL, read credential
reference, and write credential reference. The GitHub branch mutator reopens
and verifies the nested Git payload from the exact candidate snapshot into
bounded scratch storage before invoking the ref lease; Azure production
composition uses the same transport because REST ref update does not upload a
locally produced Git object. Provider-neutral stale
source/target errors complete the operation as requiring fresh reconciliation;
they are not left pending for lease reclaim.

- [ ] **Step 4: Compose the GitHub adapter without changing direct Git**

Add `AdapterGitHub AdapterKind = "github"` and route only PR operation kinds to
the new service. Keep `direct-git` restricted to `ModeBranch` and `ModeMerge`.
Advance migration gates to `1773106153`.

Run:

```bash
go test ./agent/publisher/... ./agent/pullrequest/github ./atc/atccmd -count=1
ginkgo --focus='publication operation kind|Legacy Database Upgrade' ./atc/db/migration/
bash docs/migration/migrate-preflight_test.sh
```

- [ ] **Step 5: Commit**

```bash
git add agent/publisher agent/pullrequest/github atc/atccmd atc/db \
  docs/migration/migrate-preflight.sh
git commit -m "feat(publisher): add provider-native PR operations"
```

---

### Task 7: Build initial `publish-pr-v3` orchestration

**Files:**

- Create: `agent/workflow/seeds/publish-pr-v3/workflow.yaml`
- Create: `agent/pullrequest/coordinator.go`
- Test: `agent/pullrequest/coordinator_test.go`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/workflow/seed_contract_registration_test.go`
- Modify: `atc/atccmd/command.go`
- Test: `atc/atccmd/agent_publisher_command_test.go`

**Interfaces:**

- Consumes: exact accepted-review evidence, existing repository rebase and
  authoritative validation functions, PR service, and binding store.
- Produces:

```go
type InitialPublishCoordinator interface {
	Begin(context.Context, InitialPublishRequest) (Binding, error)
	Reconcile(context.Context, int, int64) (Binding, error)
}

type InitialPublishRequest struct {
	TeamID          int
	WorkflowRunID   snapshot.WorkflowRunID
	Review          snapshot.SnapshotRef
	Candidate       snapshot.SnapshotRef
	Validation      snapshot.SnapshotRef
	Provider        string
	Repository      string
	SourceRef       string
	TargetRef       string
	ExpectedSource  contracts.PullRequestHeadExpectation
	MonitorWorkflowDefinitionID int
	MonitorWorkflowName         string
	MonitorWorkflowVersion      int
	MonitorWorkflowContentHash  string
}
```

- [ ] **Step 1: Write coordinator and seed contract tests**

Cover exact-review mismatch, sealed missing-state authority, target
capture/rebase/validation ordering, branch-success/PR-create recovery,
idempotent binding creation, and refusal to create a binding before a PR
external ID exists. The composition test must fail unless the concrete Task 12
impact decider and durable-wait verifier are wired.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest ./agent/workflow ./atc/atccmd \
  -run 'InitialPublish|PublishPR' -count=1
```

- [ ] **Step 3: Implement the linear initial flow**

The seed inputs are exact `candidate`, `review`, `validation`, and captured
`target`; outputs are `rebased-change`, `validation`, `publish-impact`,
and `pull-request`. Keep reapproval optional and server-selected; authored
workflow YAML cannot assert that it is unnecessary. The coordinator depends on
the concrete `ImpactDecider` and durable-wait verifier from Task 12. Unit tests
may use a strict fake returning explicit authority, but ATC composition and
the integration test in this task construct the real implementation; there is
no production fallback or permissive fake.

- [ ] **Step 4: Run workflow and publisher integration focus**

```bash
go test ./agent/workflow ./agent/pullrequest ./agent/publisher ./atc/atccmd -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/seeds/publish-pr-v3 agent/workflow \
  agent/pullrequest atc/atccmd
git commit -m "feat(workflow): publish reviewed changes as pull requests"
```

---

### Task 8: Implement the `forge-pr` polling resource

**Files:**

- Create: `agent/pullrequest/resource/protocol.go`
- Create: `agent/pullrequest/resource/check.go`
- Create: `agent/pullrequest/resource/in.go`
- Create: `agent/pullrequest/resource/out.go`
- Test: `agent/pullrequest/resource/check_test.go`
- Test: `agent/pullrequest/resource/in_test.go`
- Test: `agent/pullrequest/resource/out_test.go`
- Create: `cmd/forge-pr-resource/main.go`
- Test: `cmd/forge-pr-resource/main_test.go`
- Create: `deploy/forge-pr-resource.Dockerfile`
- Test: `deploy/forge_pr_resource_image_test.go`

**Interfaces:**

- Consumes: provider observer, pure `ActionFor`, Concourse resource source
  containing the server-projected acknowledged binding state, and
  previous-version JSON. The projected state is:

```go
type MonitorCheckState struct {
	BindingID            int64     `json:"binding_id"`
	BindingRevision      int64     `json:"binding_revision"`
	AcknowledgedCursor   string    `json:"acknowledged_cursor"`
	LastReconciledTarget string    `json:"last_reconciled_target"`
	LastReconciledAt     time.Time `json:"last_reconciled_at"`
	ActiveActionDigest   string    `json:"active_action_digest"`
	AttentionRequired    bool      `json:"attention_required"`
	Paused               bool      `json:"paused"`
	OperatorTerminated   bool      `json:"operator_terminated"`
}
```

The binding row is the authority for these values. `check` uses
`previous-version` only for Concourse ordering/deduplication; it never treats
an unacknowledged previous version as a processed review cursor or successful
reconciliation.

- Produces `check` versions:

```go
type Version struct {
	Provider     string `json:"provider"`
	ExternalID   string `json:"external_id"`
	SourceSHA    string `json:"source_sha"`
	TargetSHA    string `json:"target_sha"`
	ActionKind   string `json:"action_kind"`
	ActionDigest string `json:"action_digest"`
	Cursor       string `json:"cursor"`
	BindingRevision string `json:"binding_revision"`
}
```

`BindingRevision` is the canonical decimal encoding of the positive source
revision; leading zeros, signs, floats, and exponent forms are rejected.

`in` re-observes the provider and rejects a mismatch in source head, target
head, cursor, or action digest before materialization. It writes normalized
`pull-request/v1` `record.json`, an exact clean checkout under
`source-repository/`, and an exact clean target checkout under
`target-repository/`. A deterministic Task 11 function verifies and seals
those directories as `repository/v1`; this avoids a second credentialed ATC
capture controller. No credential-bearing file is written.

Active actions fetch the observed source/target branch refs and verify their
exact object IDs. Completed and abandoned actions instead fetch both exact
observed object IDs, because forge-native completion may delete the source
branch. Install children inside the caller-owned Concourse destination mount;
never rename over or replace the mount, remove partial children on failure,
and publish `record.json` last. Bound every checkout, including its contained
Git database, by `snapshot.DefaultMaxSnapshotEntries` and
`snapshot.DefaultMaxSnapshotContentBytes`.

The protected source includes policy-resolved `api_base_url` and
`repository_url` values. The latter is credential-free and identifies the
single same-repository source/target repository; `in` supplies the resolved
read secret out of band to the controlled Git runner and removes remotes
before returning.

- [ ] **Step 1: Write protocol tests**

Drive `check`, `in`, and `out` through byte buffers. Prove unchanged polls emit
the prior version only, an unacknowledged previous version does not advance
the projected cursor or reconciliation clock, attention/paused/terminated
state emits no new version, pending comments emit no new version, submitted
review batches emit one version, freshness buckets do not fire more often than
six hours after the projected successful reconciliation, stale binding
revision or observation materialization is refused, exact clean repositories
are materialized, `out` fails before network access, and
stdout/stderr/errors, versions, and output trees contain no source token.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest/resource ./cmd/forge-pr-resource ./deploy \
  -run 'ForgePR|Test' -count=1
```

- [ ] **Step 3: Implement bounded `check` and `in`**

Select provider from a strict enum; resolve the read token from resource
source secret interpolation; never echo source JSON in an error. Dispatch by
`path.Base(os.Args[0])` for `/opt/resource/check`, `/opt/resource/in`, and
`/opt/resource/out`. Bound pages, response bodies, comments, repository size,
redirects, and timeouts.

Keep protocol/check/in/out in the importable
`agent/pullrequest/resource` package. The command is a thin executable adapter
under `cmd/forge-pr-resource`; do not turn the protocol package into
`package main`.

- [ ] **Step 4: Run resource and contract suites**

```bash
go test ./agent/pullrequest/resource ./cmd/forge-pr-resource \
  ./agent/pullrequest/... \
  ./agent/snapshot/contracts -count=1
```

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest/resource cmd/forge-pr-resource \
  deploy/forge-pr-resource.Dockerfile \
  deploy/forge_pr_resource_image_test.go
git commit -m "feat(resource): poll provider-native pull requests"
```

---

### Task 9: Render and own one standing monitor pipeline per binding

**Files:**

- Create: `agent/pullrequest/pipeline.go`
- Test: `agent/pullrequest/pipeline_test.go`
- Modify: `agent/workflow/resource_source_render.go`
- Modify: `agent/workflow/resource_source_test.go`
- Modify: `atc/db/agent_workflow_resource_source_runtime_types.go`
- Modify: `atc/db/agent_workflow_resource_source_admissions_factory.go`
- Test: `atc/db/agent_workflow_resource_source_admissions_factory_test.go`
- Modify: `atc/db/agent_pr_bindings_factory.go`
- Modify: `atc/atccmd/workflow_resource_sources.go`
- Test: `atc/atccmd/workflow_resource_sources_test.go`

**Interfaces:**

- Consumes: binding locator, pinned monitor workflow revision, read credential
  reference, provider resource type, and existing pipeline save/config
  primitives.
- Produces:

```go
type MonitorPipelineTarget struct {
	TeamID             int
	BindingID          int64
	WorkflowDefinition int
	WorkflowVersion    int
	Provider           string
	Repository         string
	ExternalID         string
	APIBaseURL         string
	RepositoryURL      string
	ReadCredential     string
	PollInterval       time.Duration
	FreshnessInterval  time.Duration
	CheckState         MonitorCheckState
}

func RenderMonitorPipeline(MonitorPipelineTarget) (RenderedMonitorPipeline, error)
```

- [ ] **Step 1: Write renderer and ownership tests**

Assert one ordinary non-template pipeline created paused, `check_every: 5m`,
one `serial: true` job, triggered `get` with `version: every`, binding ID in
the domain-separated config hash, no literal credential, Lidar visibility,
and refusal of public config/pause/archive/manual-build mutations. Changing
only acknowledged cursor, reconciliation time/head, active action, or operator
state must change the config hash while keeping the same owned pipeline
identity.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest ./agent/workflow ./atc/atccmd \
  -run 'MonitorPipeline|PRMonitorOwnership|ResourceSource' -count=1
ginkgo --focus='resource source.*binding|PR monitor source' ./atc/db/
```

- [ ] **Step 3: Implement binding-keyed rendering and atomic ownership**

Use a generated name:

```go
name := fmt.Sprintf("agent-pr-monitor-%d-%s", target.BindingID, hash[:12])
```

Save the paused pipeline and bind its ID in one DB transaction, then activate
only after the binding is durable. Do not reuse the
definition-owned uniqueness key. Extend the existing resource-source pipeline
registry with a binding-scoped instance key and use its ordinary
save/activate/drain lifecycle. Existing definition-owned rendering and
pipeline hashes must remain byte-for-byte unchanged. Add a
`MonitorPipelineReconciler` to the existing resource-source lifecycle
component in `newWorkflowResourceSourceComposition`; it reads binding rows and
converges protected pipeline config to `Binding.Revision`. This is part of the
existing reconciliation tick, not a new periodic component.

- [ ] **Step 4: Run focused DB and scheduler tests**

```bash
go test ./agent/pullrequest ./agent/workflow \
  -run 'MonitorPipeline|ResourceSourcePipeline|PRMonitor' -count=1
ginkgo --focus='resource source.*binding|PR monitor source' ./atc/db/
```

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest agent/workflow atc/db
git commit -m "feat(workflow): own binding-scoped PR monitor pipelines"
```

---

### Task 10: Launch, serialize, acknowledge, and archive monitor runs

**Files:**

- Create: `agent/pullrequest/monitor.go`
- Test: `agent/pullrequest/monitor_test.go`
- Modify: `agent/workflowrun/source_build_reconciler.go`
- Test: `agent/workflowrun/source_build_reconciler_test.go`
- Modify: `agent/workflowrun/binder.go`
- Test: `agent/workflowrun/binder_resource_source_test.go`
- Modify: `agent/workflowrun/source_pipeline_lifecycle.go`
- Test: `agent/workflowrun/source_pipeline_lifecycle_test.go`
- Modify: `atc/db/agent_workflow_runs_factory.go`
- Test: `atc/db/agent_workflow_runs_factory_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/workflow_resource_sources.go`
- Test: `atc/atccmd/workflow_resource_sources_test.go`

**Interfaces:**

- Consumes: successful monitor source builds, captured `pull-request/v1`,
  binding launch reservations, workflow binder/launcher, workflow terminal
  reconciliation, and owned pipeline lifecycle.
- Produces:

```go
type MonitorCoordinator interface {
	ReserveAndLaunch(context.Context, MonitorSourceBuild) (snapshot.WorkflowRunID, bool, error)
	Acknowledge(context.Context, MonitorRunResult) (Binding, error)
	ReconcileTerminal(context.Context, int, int64) (Binding, error)
}
```

- [ ] **Step 1: Write race and lifecycle tests**

Prove two captured admissions for one PR launch one run, the second remains
claimable after acknowledgement, a failed run does not advance the cursor, a
validated no-op does, completed/abandoned state archives the pipeline, and
`attention_required` pauses without discarding history. Also prove a source
build selected from an older projected binding revision is failed as stale,
not launched or acknowledged.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest ./agent/workflowrun \
  -run 'Monitor|ReserveAndLaunch|Acknowledge' -count=1
ginkgo --focus='PR monitor launch|AgentWorkflowRunsFactory' ./atc/db/
```

- [ ] **Step 3: Implement the binding-aware launch gate**

Set workflow-run origin to:

```go
OriginKind:      "pr-monitor",
OriginReference: strconv.FormatInt(binding.ID, 10),
```

Reserve before constructing the execution envelope. Workflow-run creation
locks the binding, verifies the oldest unlaunched source admission and exact
reservation, inserts the run, and attaches its ID in the same transaction. A
busy binding defers the admission and stops later builds for that binding. On
launch failure, release only the exact unattached reservation. A terminal run
acknowledges only its own run/action digest. Successful publish, validated
no-op, and terminal observation may advance the cursor; failed, errored,
aborted, stale, and ambiguous runs never do. Every source version carries the
projected binding revision. The launch transaction compares it, the action
digest, and cursor against the current binding row; a version selected during
the brief acknowledgement-to-config-sync window fails closed.

After safe acknowledgement, update the binding cursor/heads/time and revision
first. The existing resource-source lifecycle component then re-renders the
owned pipeline from that row. On attention, it pauses the pipeline; on
operator resume it syncs and unpauses; on terminal or operator termination it
drains. Reconciliation is idempotent so a crash between the binding commit and
pipeline update converges without treating the resource's previous version as
acknowledged.

- [ ] **Step 4: Compose the periodic reconciler and run focus**

```bash
go test ./agent/pullrequest ./agent/workflowrun ./atc/atccmd -count=1
ginkgo --focus='PR monitor launch|PR monitor.*archive' ./atc/db/
```

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest agent/workflowrun atc/atccmd
git commit -m "feat(workflow): reconcile serialized PR monitor runs"
```

---

### Task 11: Implement `pr-monitor-v3` revision and response workflow

**Files:**

- Create: `agent/workflow/seeds/pr-monitor-v3/workflow.yaml`
- Create: `agent/workflow/seeds/pr-monitor-v3/prompts/respond.md`
- Create: `agent/functions/pullrequestresponse/runner.go`
- Test: `agent/functions/pullrequestresponse/runner_test.go`
- Create: `agent/functions/prmonitor/materialize.go`
- Test: `agent/functions/prmonitor/materialize_test.go`
- Modify: `cmd/function-runner/main.go`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/workflow/seed_contract_registration_test.go`

**Interfaces:**

- Consumes: exact PR observation, current PR repository, target repository,
  completed review batch, rebase function, validation profile, and managed
  typed-output builder.
- Produces: revised `repository-change/v1`,
  `pull-request-response/v1`, `validation/v1`, and `publish-impact/v1`.

- [ ] **Step 1: Write seed and response-authority tests**

Assert the agent receives only authorized batch threads, existing external
commits remain in its repository input, response replies are a subset of
batch threads, rebase precedes authoritative validation, validation precedes
publication, and no forge credential reaches the agent task.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/workflow ./agent/functions/pullrequestresponse \
  ./cmd/function-runner -run 'PRMonitor|PullRequestResponse' -count=1
```

- [ ] **Step 3: Implement the seed and deterministic response validator**

The first deterministic task verifies the resource observation against both
materialized repositories, enforces clean exact heads, and seals them as
`repository/v1`. Use the managed output builder for the response record. The
deterministic response function reopens the PR observation, builds the
authorized thread set for the exact completed batch, and rejects any reply
outside it before final sealing.

- [ ] **Step 4: Run workflow/function suites**

```bash
go test ./agent/workflow ./agent/functions/pullrequestresponse \
  ./agent/functions/prmonitor \
  ./cmd/function-runner -count=1
```

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/seeds/pr-monitor-v3 \
  agent/functions/pullrequestresponse agent/functions/prmonitor \
  cmd/function-runner
git commit -m "feat(workflow): revise pull requests from review batches"
```

---

### Task 12: Enforce impact policy and conditional reapproval

**Files:**

- Create: `agent/pullrequest/impact.go`
- Test: `agent/pullrequest/impact_test.go`
- Create: `agent/pullrequest/impact_verifier.go`
- Create: `agent/publisher/pr_impact.go`
- Create: `agent/publisher/pr_approval.go`
- Test: `agent/publisher/pr_approval_test.go`
- Modify: `agent/workflow/parse.go`
- Modify: `agent/workflow/typecheck.go`
- Modify: `atc/exec/await_snapshot_step.go`
- Modify: `atc/exec/publish_snapshot_step.go`
- Modify: `atc/engine/step_factory.go`
- Test: `atc/exec/publish_snapshot_step_test.go`

**Interfaces:**

- Consumes: `publish-impact/v1`, accepted review evidence, durable
  `question/v1`/`human-answer/v1`, and exact PR action intent.
- Produces:

```go
type ImpactPolicy struct {
	Mode           ImpactMode
	SensitivePaths []string
	MaxChangedFiles int
	MaxChangedLines int
	ConflictRequiresApproval bool
	ValidationChangeRequiresApproval bool
}

func DecideImpact(
	ImpactPolicy,
	DeterministicImpact,
	*contracts.AgentImpactAssessment,
) (contracts.PublishImpactBody, error)
```

- [ ] **Step 1: Write policy and exact-intent tests**

Cover `always`, `rules`, and `agent-decides`; deterministic escalation that an
agent tries to waive; assessor error/ambiguity; conflict and sensitive path;
no-op; question context binding binding ID, action digest, exact candidate,
source/target heads, response digest, policy version, and validation.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/pullrequest ./agent/publisher ./atc/exec \
  -run 'Impact|PRApproval|Reapproval' -count=1
```

- [ ] **Step 3: Implement decision and generalized wait verification**

At both await and publish, the server reopens accepted-review evidence, uses
its candidate as the only valid impact baseline, resolves the deployment-owned
policy version, and recomputes impact from the exact final validation,
observation, response, binding, action, baseline, and candidate. The evaluator
independently recovers the authoritative agent assessment and assessor status;
it must not copy them from the untrusted body. The complete derived decision
must equal the sealed impact record. For required
reapproval, build a strict `PRApprovalContext` and reuse durable wait
resolution. For no reapproval, persist the exact accepted-review evidence and
impact record that justified continuation.

- [ ] **Step 4: Run affected suites**

```bash
go test ./agent/pullrequest ./agent/publisher ./agent/workflow ./atc/exec -count=1
```

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest agent/publisher agent/workflow atc/exec
git commit -m "feat(publisher): conditionally reapprove PR revisions"
```

---

### Task 13: Implement Azure DevOps REST 7.1 adapter and conformance

**Files:**

- Create: `agent/pullrequest/conformance/suite.go`
- Create: `agent/pullrequest/conformance/suite_test.go`
- Create: `agent/pullrequest/azuredevops/client.go`
- Create: `agent/pullrequest/azuredevops/wire.go`
- Create: `agent/pullrequest/azuredevops/observe.go`
- Create: `agent/pullrequest/azuredevops/mutate.go`
- Test: `agent/pullrequest/azuredevops/adapter_test.go`
- Test: `agent/pullrequest/azuredevops/testdata/README.md`
- Test: `agent/pullrequest/azuredevops/testdata/pull_request_active.json`
- Test: `agent/pullrequest/azuredevops/testdata/pull_request_completed.json`
- Test: `agent/pullrequest/azuredevops/testdata/pull_request_abandoned.json`
- Test: `agent/pullrequest/azuredevops/testdata/iterations_page_1.json`
- Test: `agent/pullrequest/azuredevops/testdata/threads_page_1.json`
- Test: `agent/pullrequest/azuredevops/testdata/reviewers.json`
- Test: `agent/pullrequest/azuredevops/testdata/ref_update_succeeded.json`
- Test: `agent/pullrequest/azuredevops/testdata/ref_update_stale.json`
- Modify: `agent/pullrequest/github/observe_test.go`
- Modify: `agent/pullrequest/github/mutate_test.go`

**Interfaces:**

- Consumes: the Task 2 Observer/Mutator interfaces and shared conformance
  scenarios.
- Produces:

```go
func New(
	organizationURL string,
	project string,
	repositoryID string,
	token TokenSource,
	client *http.Client,
) (*Adapter, error)
```

- [ ] **Step 1: Extract the shared adapter conformance suite**

The suite executes normalization, pagination, review batching, exact CAS,
idempotent find/create/status/response, throttling, malformed responses,
unknown enums, terminal states, and log redaction against an `httptest.Server`.
Make the existing GitHub adapter pass first.

- [ ] **Step 2: Add Azure official-contract fixtures and confirm RED**

Fixtures record the official REST endpoint shape and retrieval date in
`testdata/README.md`. They cover PR GET, iterations, threads, reviewers with
votes `10`, `5`,
`0`, `-5`, `-10`, statuses, ref update `succeeded` and
`staleOldObjectId`, completion, abandonment, conflict, and
`rejectedByPolicy`.

Use OAuth bearer tokens for Day 1; do not infer PAT/Basic authentication from
token contents. Derive the human URL from the configured organization,
project, repository, and PR identity. Detect `-5` readiness transitions from
strictly decoded system `VoteUpdate` threads and use the reviewer list only to
corroborate identity/current state, so `-5 -> 0 -> -5` rearms even when a
non-action poll does not advance the binding's acknowledged cursor. Azure
comment/status retries use exact bounded operation markers; thread replies use
`parentCommentId: 0` within the sealed batch's authorized thread IDs.

Run:

```bash
go test ./agent/pullrequest/azuredevops ./agent/pullrequest/conformance -count=1
```

- [ ] **Step 3: Implement strict Azure REST 7.1 mapping**

Every request includes `api-version=7.1`. Treat unknown merge status as
`unknown`; never map it to mergeable. Create refs with zero old object ID and
update with the sealed expected object ID. Validate returned repository,
source, target, and PR IDs before accepting success.

- [ ] **Step 4: Run both adapters through conformance**

```bash
go test ./agent/pullrequest/github ./agent/pullrequest/azuredevops \
  ./agent/pullrequest/conformance -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/pullrequest
git commit -m "feat(publisher): add Azure DevOps PR adapter"
```

---

### Task 14: Add team-scoped PR APIs and audit UI

**Files:**

- Create: `agent/api/pullrequests/types.go`
- Create: `agent/api/pullrequests/handler.go`
- Test: `agent/api/pullrequests/handler_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Test: `atc/integration/agent_pull_requests_test.go`
- Create: `web/elm/src/Concourse/AgentPullRequest.elm`
- Create: `web/elm/src/AgentPullRequest/AgentPullRequest.elm`
- Modify: `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Routes.elm`
- Modify: `web/elm/src/Main.elm`
- Modify: `web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm`
- Test: `web/elm/tests/AgentPullRequestTest.elm`

**Interfaces:**

- Consumes: safe binding projection, PR-origin workflow runs, publication
  occurrences, snapshots, and team authorization.
- Produces:

```text
GET  /api/v1/teams/:team/agent/pull-requests/:id
GET  /api/v1/teams/:team/agent/pull-requests/:id/runs
POST /api/v1/teams/:team/agent/pull-requests/:id/refresh
POST /api/v1/teams/:team/agent/pull-requests/:id/pause
POST /api/v1/teams/:team/agent/pull-requests/:id/resume
POST /api/v1/teams/:team/agent/pull-requests/:id/terminate
```

No route accepts caller-authored heads, cursors, evidence, or provider URLs.
The timeline is a read-only projection over the originating run and
`origin_kind=pr-monitor` runs plus their snapshot bindings, waits, outcomes,
and safe publication occurrence fields. Do not add a PR event/history table
or expose raw publication detail, provider cursors, launch tokens, policy
references, or credential references.

- [ ] **Step 1: Write API authorization and Elm decoder/view tests**

Prove team isolation, safe URL projection, member-only controls, revision-CAS
conflicts, pause/resume/terminate semantics, no mutation fields in requests,
timeline ordering, exact-head display, external link,
validation/impact/reapproval links, and Azure validation badge.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./agent/api/pullrequests -run 'PullRequest' -count=1
ginkgo --focus='agent pull request' ./atc/integration/
cd web/elm && elm-test tests/AgentPullRequestTest.elm
```

- [ ] **Step 3: Implement handlers, routes, decoder, and page**

Use existing workflow-run page components for run links and statuses. The PR
page is an audit timeline, not a forge diff/review replacement. Extend the
workflow-run publication row to link the safe PR binding when present.

- [ ] **Step 4: Compile and run focused tests**

```bash
go test ./agent/api/pullrequests -run 'PullRequest' -count=1
ginkgo --focus='agent pull request' ./atc/integration/
cd web/elm && elm make --output /dev/null src/Main.elm
cd web/elm && elm-test tests/AgentPullRequestTest.elm
```

- [ ] **Step 5: Commit**

```bash
git add agent/api/pullrequests atc web/elm
git commit -m "feat(web): add pull request publish audit timeline"
```

---

### Task 15: Wire deployment configuration and operational documentation

**Files:**

- Modify: `atc/atccmd/agent_publisher.go`
- Modify: `atc/atccmd/command.go`
- Modify: `deploy/chart/values.yaml`
- Modify: `deploy/chart/templates/web-deployment.yaml`
- Modify: `deploy/chart/templates/secret.yaml`
- Modify: `deploy/chart/templates/networkpolicy.yaml`
- Modify: `deploy/chart/README.md`
- Modify: `deploy/concourse-pipeline.yml`
- Test: `deploy/chart/tests/agent_publisher_test.go`
- Test: `deploy/chart/tests/networkpolicy_test.go`
- Test: `deploy/forge_pr_resource_image_test.go`
- Modify: `docs/agentic/README.md`
- Create: `docs/agentic/PR_PUBLISH_OPERATIONS.md`

**Interfaces:**

- Consumes: GitHub/Azure adapter constructors, distinct read/write credential
  references, provider policy, resource image, polling/freshness defaults.
- Produces chart configuration shaped as:

```yaml
agentPublisher:
  pullRequests:
    enabled: true
    # Test-fixture digest; operators must set the digest built by their release.
    resourceImage: ghcr.io/concourse/forge-pr-resource@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    pollInterval: 5m
    freshnessInterval: 6h
    providers:
      - name: engineering-github
        type: github
        apiBaseURL: https://api.github.com
        readCredentialReference: widget-github-read
        writeCredentialReference: widget-github-write
```

- [ ] **Step 1: Write command/chart failure tests**

Reject unpinned resource images, same read/write path, unknown provider,
non-HTTPS API base, missing Azure project/repository, PR policy with no
adapter, retired gateway values, and mounting write credentials outside web.

- [ ] **Step 2: Run and confirm RED**

```bash
go test ./atc/atccmd ./deploy/chart/tests -run 'Publisher|PullRequest|Azure|GitHub' -count=1
helm lint deploy/chart
```

- [ ] **Step 3: Implement composition and chart wiring**

Build the resource image from `deploy/forge-pr-resource.Dockerfile` in the
existing release pipeline. Mount write credentials only into web. Supply read
credential references to the server-owned resource pipeline through ordinary
secret interpolation; never render token bytes into pipeline config. Add
egress rules only for configured provider hosts when the chart's optional
egress policy is enabled.

- [ ] **Step 4: Write runbook and run focused verification**

Document credential scopes, provider capability diagnostics,
pause/resume/attention recovery, polling/rate-limit sizing, terminal archive,
GitHub live proof, and the exact Azure message:

```text
Azure DevOps adapter: contract-tested against REST 7.1; not live-validated.
```

Run:

```bash
go test ./atc/atccmd ./deploy/chart/tests -count=1
helm lint deploy/chart
```

- [ ] **Step 5: Commit**

```bash
git add atc/atccmd deploy/chart docs/agentic
git commit -m "feat(deploy): configure provider-native PR publishing"
```

---

### Task 16: Prove GitHub live behavior and run milestone verification

**Files:**

- Create: `agent/pullrequest/github/live_integration_test.go`
- Create: `docs/agentic/PR_PUBLISH_LIVE_PROOF.md`
- Modify:
  `docs/superpowers/plans/2026-07-29-provider-native-pr-publish.md`

**Interfaces:**

- Consumes: an explicitly authorized GitHub test repository and scoped
  credentials supplied through environment variables.
- Produces: environment-gated live proof and final verification evidence.

- [ ] **Step 1: Add an environment-gated live test**

Require:

```text
JETBRIDGE_GITHUB_PR_TEST_REPOSITORY
JETBRIDGE_GITHUB_PR_TEST_READ_TOKEN
JETBRIDGE_GITHUB_PR_TEST_WRITE_TOKEN
```

Without all three, skip with the exact missing prerequisite. Never create a
repository or broaden token permissions.

- [ ] **Step 2: Run the live matrix when prerequisites exist**

The test must prove create/retry, pending-review suppression, submitted review
batch, adopted external commit, stale source lease, target refresh, conflict
transition, exact validation status, reapproval, idempotent replies, and
forge-native terminal observation. Record created branch/PR IDs and clean them
up only within the authorized test repository.

- [ ] **Step 3: Run focused final suites**

```bash
go test ./agent/pullrequest/... ./agent/publisher/... \
  ./agent/snapshot/contracts ./agent/workflow ./agent/workflowrun \
  ./agent/api/pullrequests ./atc/atccmd -count=1
ginkgo ./atc/db/migration/
ginkgo --focus='AgentPR|Publication' ./atc/db/
bash docs/migration/migrate-preflight_test.sh
cd web/elm && elm make --output /dev/null src/Main.elm
helm lint deploy/chart
```

- [ ] **Step 4: Run broad milestone suites once**

```bash
make test-unit
make test-fly-integration
make test-integration
helm lint deploy/chart
```

Record environment-blocked live or fixed-port tests once; do not loop on
unchanged infrastructure failures.

- [ ] **Step 5: Run residue and diff hygiene checks**

```bash
git diff --check origin/jetbridge...HEAD
rg -n 'agent-publisher-gateway|cap1|agent_principals' agent atc deploy/chart docs/agentic
```

Expected: only intentional migration/history/runbook tombstones, no live
gateway or principal runtime.

- [ ] **Step 6: Request one bounded blocking-only review**

Review only this feature range for correctness, security boundaries, data
loss/corruption, migration hazards, and required acceptance failures. Follow
the session review budget: at most three rounds and stop on a passing round.

- [ ] **Step 7: Commit final evidence**

```bash
git add agent/pullrequest/github/live_integration_test.go \
  docs/agentic/PR_PUBLISH_LIVE_PROOF.md \
  docs/superpowers/plans/2026-07-29-provider-native-pr-publish.md
git commit -m "test(agent): prove provider-native PR publish"
```

---

### Task 17: Complete and compose the production authority spine

This task is an integration correction discovered while composing Tasks
10–16. It is not optional cleanup: PR enablement must remain fail-closed until
every step below is complete.

**Files:**

- Modify: `agent/pullrequest/store.go`
- Modify: `atc/db/agent_pr_bindings_factory.go`
- Create: next forward-only PR binding authority migration and migration tests
- Create: `agent/pullrequest/initial_coordinator.go`
- Create: `agent/pullrequest/revision_executor.go`
- Create: `agent/pullrequest/monitor_run_inspector.go`
- Create: exact provider-created PR observer and approved-baseline
  materializer/advancer units
- Modify: `agent/pullrequest/monitor.go`
- Modify: `agent/workflow/render.go`
- Modify: `agent/workflowrun/binder.go`
- Modify: `agent/workflow/seeds/pr-monitor-v3/workflow.yaml`
- Modify: `atc/atccmd/agent_publisher.go`
- Modify: `atc/atccmd/command.go`
- Test: focused unit, DB migration/factory, workflow rendering, execution,
  provider recovery, and composition suites

- [x] **Step 1: Split and persist durable authorities**

Persist immutable PR-creation occurrence, immutable original accepted-review
authority, and mutable approved baseline (repository snapshot, validation
snapshot, and authorizing publication occurrence) as distinct same-team
foreign-keyed facts. Binding creation must reopen and match the exact
successful `create_pr` occurrence and its exact result, action, observation,
heads, refs, destination, and policy. Treat an independently authorized
succeeded occurrence alias as exact recovery evidence; do not require it to be
the semantic operation's original lease owner.

- [x] **Step 2: Implement initial publication and provider reobservation**

Use a binding-free coordinator for the accepted initial review. Publish the
exact branch and PR idempotently, then reobserve the created PR through the
provider adapter. Seal its active `pull-request/v1` observation and nonempty
opaque cursor before creating the binding. Never fabricate an observation or
cursor from the create response.

- [x] **Step 3: Inject exact monitor publication target authority**

Extend the protected monitor launch/render envelope with destination,
approval-policy version, source ref, and target ref resolved from the exact
creation action plus current deployment policy. Replace only explicit reusable
workflow sentinels and include the result in the canonical render identity.

- [x] **Step 4: Implement exact revision execution**

Reopen the binding, active reservation, observation, candidate, validation,
impact, and trigger-required response. Reopen the exact team-authorized
`repository-change/v1` to obtain its result commit before constructing the
branch operation. Execute durable branch and status operations, and response
only for `review_batch`. A `rebase_required` branch result is a safe stale
outcome; terminal observations never enter this executor.

- [x] **Step 5: Implement exact monitor-run inspection**

Project one team/run's immutable publication occurrences and classify only
complete matching evidence. Runtime success alone is insufficient. Missing,
extra, mismatched, or conflicting proof is `ambiguous`; exact branch
`rebase_required` is `stale`; exact terminal observation is completed or
abandoned; exact branch/status/required-response evidence is published or
validated-noop.

- [ ] **Step 6: Materialize and atomically advance the approved baseline**

Materialize the final published revision as an exact sealed `repository/v1`.
After a successful human-authorized publication, advance cursor, heads, and
the approved baseline in one binding transaction. Rejection, stale authority,
failure, ambiguous evidence, or a no-reapproval run cannot advance the human
baseline.

The provider-neutral approved-baseline authority is implemented and now binds
the exact binding, publication occurrence, repository, and validation. The
remaining work is the immutable publication-to-materialization relation, the
database-backed resolver for later human-wait baselines, and same-transaction
cursor/head/baseline advancement.

- [ ] **Step 7: Compose trusted impact policy and trigger-specific paths**

Install a deployment-owned impact-policy resolver and authoritative evaluator;
no permissive fallback is allowed. `review_batch` requires response authority,
conflict/freshness carry non-publishable semantic absence, and
completed/abandoned observations take the terminal coordinator path without a
mutation workflow.

The direct completed/abandoned coordinator and its row-locked, idempotent
binding transition are implemented. Concrete deployment-owned impact
resolution/evaluation and the terminal pipeline lifecycle hook remain
uncomposed.

- [ ] **Step 8: Wire production and remove the intentional startup refusal**

Compose the initial coordinator, created-PR observer, target renderer, revision
executor, evidence verifier, impact verifier, monitor inspector/coordinator,
standing-pipeline reconciler, and lifecycle hooks. Only after focused and
migration verification passes may
`--agent-publisher-pull-requests-enabled` stop returning the explicit
incomplete-authority error.

**Implementation checkpoint (2026-07-29):** Steps 1–5 are implemented and
reviewed. Step 7's direct terminal subpath is implemented and reviewed. The
production startup refusal remains intentional for the open work recorded in
Steps 6–8. GitHub live proof was not run because the required environment was
not available, and Azure DevOps remains contract-tested against REST 7.1 but
not live-validated.

---

## Execution order and safe parallelism

The dependency spine is:

```text
Task 1 → Task 2 → Task 5 → Task 8
[migration gate] → Task 3 → Task 4 → Task 6
Task 4 → Task 12
Task 6 + Task 12 → Task 7
Task 3 + Task 8 → Task 9 → Task 10 → Task 11
Task 11 requires Task 12
Task 5 + Task 6 → Task 13
Tasks 3–13 → Task 14 → Task 15 → Task 17 → Task 16
```

Tasks 3, 4, and 6 are migration-bearing and start only after reusable-node
migrations `1773106149` and `1773106150` are present. Until that gate clears,
Tasks 1, 2, 5, and 8 remain independently executable. Execute Task 12 after
Task 4 and before Task 7; its location later in this document groups monitor
workflow material, not dependency order.

- Binding pipeline rendering (Task 9) starts after both Task 3 persistence and
  Task 8 protocol types exist.
- Azure DevOps (Task 13) starts only after the shared GitHub adapter contract
  and mutation semantics are stable.
- UI work (Task 14) starts after API projections and binding store semantics
  are stable.

One worker owns each task's listed files. Workers must not revert concurrent
changes and must adapt to already-landed neighboring interfaces.
