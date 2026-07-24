# Agentic Authority and Orphan Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the abandoned platform-MCP/checkpoint authority path, make workflow-run secrets model-token-only, and require scoped `cap1` principals for review and cost publishing.

**Architecture:** The only runtime authority for human interaction remains durable `await_snapshot` snapshots and workflow waits; no ticket-question service, checkpoint reconciler, notifier, or platform sidecar remains. Workflow admission resolves a model credential and attaches it directly to the main agent container's per-run secret, without creating a principal. Publishing stays behind the existing principal verifier, but its routes become strict and CI receives explicit scoped publisher credentials.

**Tech Stack:** Go, Ginkgo/Gomega, Kubernetes client-go fake client, PostgreSQL migrations, Concourse ATC/wrappa, Elm, Concourse pipeline YAML.

## Global Constraints

- Do not add a feature flag, fallback token, dual execution path, or replacement ticket-question API.
- `await_snapshot` is the only human-wait mechanism; its `question/v1` and `human-answer/v1` snapshots remain untouched.
- A workflow-run K8s secret contains exactly `anthropic-token`; only the agent main container receives it.
- Review and cost ingestion accepts only a verified scoped `cap1` principal (`reviews:write` or `costs:write`); missing or wrong credentials fail closed with 401/403.
- Preserve principal CRUD and explicit scoped publisher principals; remove only per-run principal creation/revocation and the `legacy-publish` sentinel.
- Use forward migration number **1773106122** only. Do not edit applied historical migrations, including `1773106010` and `1773106101`.
- This plan does not remove schema-v1/v2 workflow parsing, harvest, ticket outcome compatibility, or generic MCP. Those are owned by the sibling cleanup plans.
- Execute the numbered tasks in dependency order **2, 3, 4, 5, 1, 6, 7**.
  Remove every runtime reference to the retired constants before closing the
  principal vocabulary and applying migration `1773106122`.

---

## File Structure

| File or directory | Responsibility after this plan |
| --- | --- |
| `agent/api/principals/types.go` | Closed, publish-only scope vocabulary; no `questions:answer` or legacy sentinel. |
| `atc/db/migration/migrations/1773106122_remove_agentic_authority_orphans.*.sql` | Strip retired scope values and delete only the inert sentinel row on upgrade. |
| `agent/api/{reviews,costs}/handler.go` | Trust only the principal context installed by strict route auth. |
| `atc/api/auth/check_agent_principal_handler.go` and `atc/wrappa/api_auth_wrappa.go` | One strict principal wrapper for reviews, costs, and metrics. |
| `agent/credentials/*` and `agent/workflowrun/admission_adapters.go` | Model credential attachment and secret garbage collection only. |
| `agent/dispatch/{dispatch.go,dispatcher.go,reconcile.go}` | No per-run principal creation or dormant checkpoint/question reconciliation seam. |
| `agent/platformmcp/`, `cmd/platform-mcp/`, `agent/notify/` | Deleted orphan implementations and tests. |
| `ci-agent/`, `ci/tasks/`, `deploy/` | Explicit scoped review/cost credential names; no static publishing token. |

### Task 1: Retire question authority from the principal vocabulary and upgrade rows

**Files:**
- Create: `atc/db/migration/migrations/1773106122_remove_agentic_authority_orphans.up.sql`
- Create: `atc/db/migration/migrations/1773106122_remove_agentic_authority_orphans.down.sql`
- Create: `atc/db/migration/agentic_authority_orphans_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Modify: `agent/api/principals/types.go`
- Modify: `agent/api/principals/types_test.go`
- Modify: `atc/db/agent_principals_factory_test.go`
- Modify: `fly/commands/agent_principals.go`
- Modify: `web/elm/src/Agent/Agent.elm`

**Interfaces:**
- Produces: `principals.ValidScopes` containing exactly `reviews:write`, `tickets:read`, `tickets:write`, `metrics:write`, and `costs:write`.
- Produces: database state in which no row retains `questions:answer` and no row named `legacy-publish` with the historical blank token hash remains.
- Consumes: migration `1773106121` as the `beforeVersion` fixture.

- [ ] **Step 1: Write the failing vocabulary and migration tests**

  In `agent/api/principals/types_test.go`, make `questions:answer` an invalid `CreateSpec` case and assert its validation error is `unknown scope "questions:answer"`. Replace the factory's legacy-sentinel list assertion with an assertion that a normal minted `gateway` principal is listed and `legacy-publish` is absent.

  Create `atc/db/migration/agentic_authority_orphans_test.go` using the repository's migration fixture pattern. Migrate to 1773106121; insert one normal principal whose `scopes` is `ARRAY['reviews:write','questions:answer']` and rely on 1773106010's existing sentinel. Migrate to 1773106122 and assert:

  ```go
  Expect(database.QueryRow(`
      SELECT scopes FROM agent_principals WHERE name = $1
  `, keptName).Scan(&scopes)).To(Succeed())
  Expect(scopes).To(Equal([]string{"reviews:write"}))

  var sentinelCount int
  Expect(database.QueryRow(`
      SELECT count(*) FROM agent_principals
      WHERE name = 'legacy-publish' AND token_hash = ''
  `).Scan(&sentinelCount)).To(Succeed())
  Expect(sentinelCount).To(Equal(0))
  ```

- [ ] **Step 2: Run the focused tests and verify red**

  Run:

  ```bash
  go test ./agent/api/principals
  ginkgo ./atc/db/migration --focus='agentic authority orphan migration'
  ```

  Expected: the vocabulary test accepts `questions:answer`, and the migration suite cannot find 1773106122 assets.

- [ ] **Step 3: Implement the closed vocabulary and forward migration**

  Remove `ScopeQuestionsAnswer` and `LegacyPublishPrincipalName`; retain the other five constants and entries in `ValidScopes`. Change the Fly description and Elm list to exactly:

  ```go
  Scopes []string `long:"scope" required:"true" description:"Scope to grant; repeatable. One of: reviews:write, tickets:read, tickets:write, metrics:write, costs:write"`
  ```

  ```elm
  mintScopeVocabulary =
      [ "reviews:write"
      , "tickets:read"
      , "tickets:write"
      , "metrics:write"
      , "costs:write"
      ]
  ```

  Use this complete forward migration (the `token_hash` predicate guarantees that an operator-created principal coincidentally named `legacy-publish` is not deleted):

  ```sql
  UPDATE agent_principals
  SET scopes = array_remove(scopes, 'questions:answer')
  WHERE 'questions:answer' = ANY(scopes);

  DELETE FROM agent_principals
  WHERE name = 'legacy-publish' AND token_hash = '';
  ```

  Make the down migration intentionally non-authorizing and executable:

  ```sql
  -- This migration irreversibly retires authority. Do not recreate
  -- questions:answer grants or the legacy-publish bypass on downgrade.
  SELECT 1;
  ```

  Advance `jetbridgeHeadMigration` and `JETBRIDGE_VERSION` from
  `1773106121` to `1773106122`, and extend the legacy-to-head assertions to
  prove the retired scope/sentinel state.

- [ ] **Step 4: Run the focused tests and verify green**

  Run:

  ```bash
  go test ./agent/api/principals
  ginkgo ./atc/db/migration --focus='agentic authority orphan migration'
  ```

  Expected: PASS. PostgreSQL must be ready (`pg_isready`) before the migration command.

- [ ] **Step 5: Commit**

  ```bash
  git add agent/api/principals/types.go agent/api/principals/types_test.go atc/db/agent_principals_factory_test.go fly/commands/agent_principals.go web/elm/src/Agent/Agent.elm atc/db/migration/migrations/1773106122_remove_agentic_authority_orphans.up.sql atc/db/migration/migrations/1773106122_remove_agentic_authority_orphans.down.sql atc/db/migration/agentic_authority_orphans_test.go atc/db/migration/legacy_upgrade_test.go docs/migration/migrate-preflight.sh
  git commit -m "refactor: retire agent question authority"
  ```

### Task 2: Make review and cost publishing strict scoped-principal APIs

**Files:**
- Modify: `agent/api/reviews/handler.go`
- Modify: `agent/api/reviews/types.go`
- Modify: `agent/api/reviews/handler_test.go`
- Modify: `agent/api/costs/handler.go`
- Modify: `agent/api/costs/handler_test.go`
- Modify: `atc/api/auth/check_agent_principal_handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/api_auth_wrappa_test.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/integration/agent_reviews_test.go`
- Modify: `atc/integration/agent_principals_test.go`

**Interfaces:**
- Produces: `reviews.NewHandler(store, feedbackStore, lookup, projectionTeam)` with no publish-token argument.
- Produces: `costs.NewHandler(ledger, checker)` with no publish-token argument.
- Produces: `CheckAgentPrincipalHandlerFactory` with only `HandlerFor(delegate, rejector, scope)`.

- [ ] **Step 1: Write failing handler and wrapper tests**

  Replace static-token success tests with tokenless-context failures. In both handler test files, construct the handler without a token and assert a direct POST without `principals.NewContext` returns `http.StatusForbidden`; retain the existing context-backed success tests. Add a wrappa table for `SubmitAgentReview` and `SubmitAgentCostRecord`: a scoped token reaches the delegate, a missing token from an anonymous request is 401, and a token with the other scope is 401.

  Change `atc/integration/agent_reviews_test.go` to mint a `reviews:write` principal through the admin API and use its returned `cap1` token. In `agent_principals_test.go`, remove the `BeforeEach` static token setup and the legacy-publish submission/listing assertions; add a `costs:write` principal POST that succeeds and a `reviews:write` token cost POST that returns 401.

- [ ] **Step 2: Run focused tests and verify red**

  Run:

  ```bash
  go test ./agent/api/reviews ./agent/api/costs
  ginkgo ./atc/wrappa --focus='review|cost'
  ginkgo ./atc/integration --focus='Agent Reviews API|Agent Principals API'
  ```

  Expected: direct tokenless handlers still accept the configured static token, and the wrapper still delegates the legacy bypass.

- [ ] **Step 3: Remove every bypass and configuration ingress**

  Make reviews require a contextual principal and retain team matching:

  ```go
  p, ok := principals.FromContext(r.Context())
  if !ok {
      http.Error(w, "agent review publishing requires a scoped principal", http.StatusForbidden)
      return
  }
  submittedBy, principalTeam := p.Name, p.TeamName
  ```

  Make costs use the analogous context check before reading the body. Remove `crypto/subtle`, `strings`, `publishToken`, static-token comments, and every `LegacyPublishPrincipalName` branch. `StoredReview.SubmittedBy` documents only the verified principal name.

  Delete `HandlerForWithLegacyBypass` from the interface and implementation. In `atc/wrappa/api_auth_wrappa.go`, wrap both submission routes with strict `HandlerFor`:

  ```go
  case atc.SubmitAgentReview:
      newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
          handler, rejector, principals.ScopeReviewsWrite)
  case atc.SubmitAgentCostRecord:
      newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
          handler, rejector, principals.ScopeCostsWrite)
  ```

  Remove `agentReviewPublishToken` from `api.NewHandler`, all callers, and
  API-suite arguments. Remove `AgentReviewPublishToken` from `RunCommand` and
  delete its flag; remove the static-token setting from integration setup.
  `AgentRunTimeout` still belongs to the per-run principal path at this point
  and is removed only in Task 3.

- [ ] **Step 4: Run focused tests and verify green**

  Run:

  ```bash
  go test ./agent/api/reviews ./agent/api/costs ./atc/api/auth
  ginkgo ./atc/wrappa --focus='review|cost'
  ginkgo ./atc/integration --focus='Agent Reviews API|Agent Principals API'
  ```

  Expected: PASS; bare bearer values never reach either handler through production wrapping.

- [ ] **Step 5: Commit**

  ```bash
  git add agent/api/reviews agent/api/costs atc/api/auth/check_agent_principal_handler.go atc/wrappa/api_auth_wrappa.go atc/wrappa/api_auth_wrappa_test.go atc/api/handler.go atc/api/api_suite_test.go atc/atccmd/command.go atc/integration/agent_reviews_test.go atc/integration/agent_principals_test.go
  git commit -m "refactor: require scoped principals for agent publishing"
  ```

### Task 3: Change workflow-run secrets to model-token-only and retain secret cleanup

**Files:**
- Modify: `agent/credentials/types.go`
- Modify: `agent/credentials/secret_attacher.go`
- Modify: `agent/credentials/secret_attacher_test.go`
- Modify: `agent/credentials/secret_reaper.go`
- Modify: `agent/credentials/secret_reaper_test.go`
- Modify: `agent/credentials/credentialsfakes/fake_secret_attacher.go`
- Modify: `agent/workflowrun/admission_adapters.go`
- Modify: `agent/workflowrun/admission_adapters_test.go`
- Modify: `agent/dispatch/dispatch.go`
- Modify: `agent/dispatch/dispatch_test.go`
- Modify: `atc/db/agent_principals_factory.go`
- Modify: `atc/db/agent_principals_factory_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/agent_experiments.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `atc/worker/jetbridge/container_test.go`

**Interfaces:**
- Produces: `credentials.SecretAttacher.Attach(context.Context, int, *Credential) (string, error)`.
- Produces: `workflowrun.NewVaultedRunSecretPreparer(users CreatorUserResolver, vault RunCredentialVault, attacher credentials.SecretAttacher) (*VaultedRunSecretPreparer, error)`.
- Produces: `credentials.NewRunSecretReaper(logger, client, namespace, runs)`; it deletes terminal/orphan model secrets but never revokes principals.

- [ ] **Step 1: Write the failing model-only tests**

  Change `TestAttachCreatesLabeledSecret` to call `Attach(ctx, 42, cred)` and assert the sole `StringData` key is `anthropic-token`. Change the idempotency and cleanup calls similarly.

  Replace `TestVaultedRunSecretPreparerResolvesCreatorAndDefersMintUntilAllocatedRun` with `TestVaultedRunSecretPreparerAttachesOnlyResolvedCredentialAfterAllocation`: its expected call order is `find-user`, `resolve-user`, `attach-secret`; its attacher must receive no token argument. Retain retry-on-the-same-pipeline-run and different-run rejection assertions. Remove principal-store stubs and revocation-failure cases.

  In reaper tests, remove `fakeRevoker`, principal token seed data, and all revocation retry assertions; add an assertion that a finished labeled secret is deleted even when its data map only has `anthropic-token`.

  Replace `TestAttachMintsContractShapedPrincipal` in dispatch tests with an assertion that dispatch attaches a credential and leaves the supplied `principals.MemoryStore` empty. In `atc/exec/agent_step_test.go`, preserve the existing proof that `CLAUDE_CODE_OAUTH_TOKEN` is a main-container SecretRef and assert `spec.SidecarSecretEnv` is empty for the same run.

- [ ] **Step 2: Run focused tests and verify red**

  Run:

  ```bash
  go test ./agent/credentials ./agent/workflowrun ./agent/dispatch
  ginkgo ./atc/db --focus='AgentPrincipalsFactory'
  ginkgo ./atc/exec --focus='runtime seams'
  ```

  Expected: compile failures from the changed `Attach` signature and failures proving principal creation/revocation still occurs.

- [ ] **Step 3: Remove principal creation, token delivery, and revocation**

  Delete `SecretKeyPrincipalToken`; reduce the `SecretAttacher` interface and K8s implementation to:

  ```go
  Attach(ctx context.Context, runID int, cred *Credential) (secretName string, err error)
  ```

  and create `StringData: map[string]string{SecretKeyAnthropicToken: cred.Token}` only.

  Remove `RunPrincipalStore`, `defaultRunPrincipalTimeout`, the principal store/timeout fields and constructor parameters from `VaultedRunSecretPreparer`. `vaultedPreparedRunSecret.Attach` validates the allocated run and credential expiry, calls `p.attacher.Attach(ctx, pipelineRunID, &p.credential)`, validates the returned deterministic name, then records `attached` and `pipelineRunID`. On attach failure it retains the existing best-effort secret cleanup but never performs principal rollback.

  Remove `PrincipalRevoker` and the revoker parameter/branch from `RunSecretReaper`. Remove `RevokeByName` from `db.AgentPrincipalsFactory` and its tests. Keep the normal `Revoke(id)` API for explicit publisher principals.

  In legacy dispatch, retain credential selection and attachment but remove `Deps.Principals`, `Deps.RunTimeout`, all `principals` imports, and the mint block in `attachRunSecret`. Update all four binder constructions in `atc/atccmd/command.go` and the experiment construction in `atc/atccmd/agent_experiments.go` to pass only user lookup, credential factory, and `cmd.agentRunSecrets()`. Remove the reaper's database principal-factory argument and delete `--agent-run-timeout` wiring. Update `lazySecretAttacher.Attach` to the new three-argument signature.

  Keep generic Jetbridge secret-ref capability tests, but rename the artificial `platform` sidecar to `auxiliary` and use `AUXILIARY_SECRET` pointing at `unrelated-secret/token`; this proves the runtime seam without retaining an agent platform-principal fixture.

- [ ] **Step 4: Regenerate the one affected fake and run focused tests**

  Run:

  ```bash
  go generate ./agent/credentials
  gofmt -w agent/credentials agent/workflowrun agent/dispatch atc/db/agent_principals_factory.go atc/atccmd/command.go atc/atccmd/agent_experiments.go
  go test ./agent/credentials ./agent/workflowrun ./agent/dispatch
  ginkgo ./atc/db --focus='AgentPrincipalsFactory'
  ginkgo ./atc/exec --focus='runtime seams'
  ```

  Expected: PASS. A generated fake must expose `Attach(context.Context, int, *credentials.Credential)` and no raw principal-token parameter.

- [ ] **Step 5: Commit**

  ```bash
  git add agent/credentials agent/workflowrun/admission_adapters.go agent/workflowrun/admission_adapters_test.go agent/dispatch/dispatch.go agent/dispatch/dispatch_test.go atc/db/agent_principals_factory.go atc/db/agent_principals_factory_test.go atc/atccmd/command.go atc/atccmd/agent_experiments.go atc/exec/agent_step_test.go atc/worker/jetbridge/container_test.go
  git commit -m "refactor: make workflow run secrets model-only"
  ```

### Task 4: Remove dormant checkpoint/question reconciliation authority

**Files:**
- Modify: `agent/dispatch/dispatcher.go`
- Modify: `agent/dispatch/reconcile.go`
- Modify: `agent/dispatch/reconcile_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `agent/schema/event_payloads.go`
- Modify: `agent/schema/SCHEMA.md`
- Modify: `agent/api/tickets/handler.go`
- Modify: `agent/api/tickets/types.go`
- Modify: `agent/api/tickets/memory_store.go`
- Modify: `agent/api/tickets/types_test.go`
- Modify: `atc/api/auth/agent_principal_or_main_team_handler.go`
- Delete: `atc/api/mcpserver/live_cli_park_test.go`

**Interfaces:**
- Produces: `dispatch.LoopConfig{Mode func() string, RunReader RunReader, MaxAttempts int}` with no question store.
- Produces: `Dispatcher.reconcileOne` that projects any terminal subordinate pipeline run to `needs_review`; generic workflow outcomes and waits remain the canonical v3 mechanism.

- [ ] **Step 1: Write the failing reconciler test for the simplified terminal rule**

  Replace the checkpoint test section in `agent/dispatch/reconcile_test.go` with one table covering `failed`, `errored`, and `aborted`. Each case must install a running ticket with a pipeline run and assert exactly one transition to `tickets.StateNeedsReview` with empty `TransitionMeta`. Do not instantiate a question fake.

- [ ] **Step 2: Run the dispatch test and verify red**

  Run:

  ```bash
  go test ./agent/dispatch -run 'TestReconcile'
  ```

  Expected: the old `LoopConfig.Questions`/`reconcileCheckpoints` path remains referenced by the test and source.

- [ ] **Step 3: Delete the dormant authority surface**

  Delete `QuestionLister` and `CheckpointRow` from `dispatcher.go`; delete `LoopConfig.Questions`. In `reconcile.go`, remove the question-listing branch, `reconcileCheckpoints`, and `onRejectFor`, plus their `sort`/`strings` imports. Terminal non-success runs must fall directly through to:

  ```go
  transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
  ```

  Remove `Questions: nil` from ATC component wiring. Remove platform/checkpoint-only event constants and structs (`EventHumanAsk`, `EventHumanAnswer`, `EventCheckpointWait`, `EventCheckpointRelease`, `HumanAskData`, `HumanAnswerData`, `CheckpointWaitData`, `CheckpointReleaseData`) and their schema documentation. Update ticket comments/tests so a queued-to-running retry is no longer described as checkpoint re-dispatch. Remove the stale platform-MCP comment in `AgentPrincipalOrMainTeamHandler` and delete the live CLI parking test, which only validates the retired ask-human architecture.

- [ ] **Step 4: Run focused tests and verify green**

  Run:

  ```bash
  go test ./agent/dispatch ./agent/api/tickets ./agent/schema
  go test ./atc/api/auth
  ```

  Expected: PASS; no production type or test can name `QuestionLister`, `CheckpointRow`, or a dispatcher question-answer operation.

- [ ] **Step 5: Commit**

  ```bash
  git add agent/dispatch/dispatcher.go agent/dispatch/reconcile.go agent/dispatch/reconcile_test.go atc/atccmd/command.go agent/schema/event_payloads.go agent/schema/SCHEMA.md agent/api/tickets/handler.go agent/api/tickets/types.go agent/api/tickets/memory_store.go agent/api/tickets/types_test.go atc/api/auth/agent_principal_or_main_team_handler.go atc/api/mcpserver/live_cli_park_test.go
  git commit -m "refactor: remove checkpoint reconciliation authority"
  ```

### Task 5: Delete platform-MCP and notification orphan packages

**Files:**
- Delete: `agent/platformmcp/`
- Delete: `cmd/platform-mcp/`
- Delete: `agent/notify/`
- Modify: `deploy/MCP_IMAGES.md`

**Interfaces:**
- Produces: no importable `github.com/concourse/concourse/agent/platformmcp` package and no `platform-mcp` command.
- Retains: `atc/api/mcpserver`, `ci-agent/devmcp`, `agent/functions/gates`, `agent/functions/judge`, and generic named workflow capabilities.

- [ ] **Step 1: Record the exact removed surface in the deletion review**

  Verify the directories contain only the retired implementation and its contract tests:

  ```bash
  find agent/platformmcp cmd/platform-mcp agent/notify -type f | sort
  ```

  Expected list includes the platform server/client/checkpoint/ask-human files and their tests, the four command files, and `agent/notify/notifier.go` plus `notifier_test.go`; there must be no retained generic MCP import in those directories.

- [ ] **Step 2: Delete the packages and remove the deployment documentation claim**

  Delete all files under the three exact directories. In `deploy/MCP_IMAGES.md`, remove the platform-MCP consumer and contract-kit prose, leaving the gateway-MCP material only. Do not remove `dev-mcp` documentation elsewhere.

- [ ] **Step 3: Verify removal and repository compilation**

  Run:

  ```bash
  test ! -d agent/platformmcp
  test ! -d cmd/platform-mcp
  test ! -d agent/notify
  ! rg -n 'github.com/concourse/concourse/agent/platformmcp|agent/notify|cmd/platform-mcp' --glob '!docs/**' --glob '!forge/**'
  go test ./atc/api/mcpserver ./ci-agent/devmcp
  ```

  Expected: the first four commands succeed because the retired packages and imports are absent; both retained generic MCP package tests pass.

- [ ] **Step 4: Commit**

  ```bash
  git add -A agent/platformmcp cmd/platform-mcp agent/notify deploy/MCP_IMAGES.md
  git commit -m "refactor: remove platform mcp and notifier"
  ```

### Task 6: Migrate CI publishing configuration to explicit scoped tokens

**Files:**
- Modify: `ci-agent/publish/publish.go`
- Modify: `ci-agent/publish/publish_test.go`
- Modify: `ci-agent/cmd/ci-agent/publish.go`
- Modify: `ci/tasks/ci-agent-review.yml`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `deploy/dogfood-pipeline.yml`

**Interfaces:**
- Produces: `publish.Options{Token string}` supplied from `AGENT_REVIEW_PRINCIPAL_TOKEN`.
- Produces: `publish.CostsOptions{Token string}` supplied from `AGENT_COST_PRINCIPAL_TOKEN`.
- Permits: an operator to deliberately set the same scoped `cap1` value for both variables when the publisher deployment cannot yet split credentials.

- [ ] **Step 1: Write failing publisher environment tests**

  In `ci-agent/publish/publish_test.go`, update missing-token assertions to expect `AGENT_REVIEW_PRINCIPAL_TOKEN is not set` for `Publish` and `AGENT_COST_PRINCIPAL_TOKEN is not set` for `PublishCosts`. Add requests that assert the respective options token is emitted exactly as `Authorization: Bearer cap1.17.review` and `Authorization: Bearer cap1.18.cost`; retain the existing retry and status-code assertions.

- [ ] **Step 2: Run publisher tests and verify red**

  Run:

  ```bash
  cd ci-agent && go test ./publish ./cmd/ci-agent
  ```

  Expected: failures still mention `AGENT_REVIEW_PUBLISH_TOKEN` and command wiring reads the retired variable for both calls.

- [ ] **Step 3: Implement explicit scoped environment wiring**

  In `ci-agent/cmd/ci-agent/publish.go`, pass:

  ```go
  Token: os.Getenv("AGENT_REVIEW_PRINCIPAL_TOKEN"),
  ```

  to `publish.Options`, and:

  ```go
  Token: os.Getenv("AGENT_COST_PRINCIPAL_TOKEN"),
  ```

  to `publish.CostsOptions`. Update only the corresponding missing-token messages in `publish.go`; HTTP authorization remains `Bearer ` plus the passed `cap1` token.

  In `ci/tasks/ci-agent-review.yml`, declare both empty params, require the review token before `ci-agent publish`, and pass `--costs` only when the cost token is present; the warning must name the missing principal token. In `deploy/dogfood-pipeline.yml`, replace the old parameter and shell gate with `AGENT_REVIEW_PRINCIPAL_TOKEN: ((agent-review-principal-token))` and `AGENT_COST_PRINCIPAL_TOKEN: ((agent-cost-principal-token))`. In `deploy/concourse-pipeline.yml`, rename its review publishing parameter and secret reference to `AGENT_REVIEW_PRINCIPAL_TOKEN` / `agent-review-principal-token` while retaining the same curl bearer-header form.

- [ ] **Step 4: Run focused CI-agent verification and static token scan**

  Run:

  ```bash
  cd ci-agent && go test ./publish ./cmd/ci-agent
  cd .. && ! rg -n 'AGENT_REVIEW_PUBLISH_TOKEN|agent-review-publish-token|--agent-review-publish-token' ci-agent ci deploy atc agent --glob '!docs/**'
  ```

  Expected: tests pass and the scan succeeds. The only remaining `legacy-publish` text may be historical migration SQL or migration fixtures, never active application, deployment, or CI code.

- [ ] **Step 5: Commit**

  ```bash
  git add ci-agent/publish ci-agent/cmd/ci-agent/publish.go ci/tasks/ci-agent-review.yml deploy/concourse-pipeline.yml deploy/dogfood-pipeline.yml
  git commit -m "ci: use scoped credentials for agent publishing"
  ```

### Task 7: Run authority-slice regression verification

**Files:**
- Modify only if a command exposes a compile failure caused by the preceding tasks; do not broaden into v3, harvest, outcome, metrics, or UI cleanup work.

**Interfaces:**
- Verifies the frozen boundary: scoped external publishers remain, model secrets are reaped, generic MCP remains, and no retired authority is wired.

- [ ] **Step 1: Confirm PostgreSQL availability**

  Run:

  ```bash
  pg_isready
  ```

  Expected: `accepting connections`; start local PostgreSQL before continuing if it is not.

- [ ] **Step 2: Run all focused authority tests**

  Run:

  ```bash
  go test ./agent/api/principals ./agent/api/reviews ./agent/api/costs ./agent/credentials ./agent/workflowrun ./agent/dispatch ./agent/schema
  ginkgo ./atc/db --focus='AgentPrincipalsFactory'
  ginkgo ./atc/db/migration --focus='agentic authority orphan migration'
  ginkgo ./atc/wrappa --focus='review|cost'
  ginkgo ./atc/exec --focus='runtime seams'
  ```

  Expected: PASS.

- [ ] **Step 3: Run consumer and integration checks**

  Run:

  ```bash
  make test-ci-agent
  ginkgo ./atc/integration --focus='Agent Reviews API|Agent Principals API'
  go test ./atc/api/mcpserver ./ci-agent/devmcp
  ```

  Expected: PASS. The retained generic MCP and independent CI agent continue to compile and test.

- [ ] **Step 4: Run the required absence audit**

  Run:

  ```bash
  ! rg -n 'questions:answer|principal-token|AGENT_PRINCIPAL_TOKEN|legacy-publish|agent-review-publish-token|AGENT_REVIEW_PUBLISH_TOKEN|platform-mcp|platformmcp|agent/notify' --glob '!docs/**' --glob '!forge/**' --glob '!atc/db/migration/migrations/1773106010_create_agent_principals.up.sql'
  ```

  Expected: success. If it reports legacy workflow/checkpoint prose owned by a sibling plan, do not suppress it with a compatibility path; hand the exact file and match to that plan's owner.

- [ ] **Step 5: Verify the completed task commits are clean**

  Run:

  ```bash
  git status --short
  git diff --check HEAD
  ```

  Expected: no uncommitted implementation changes and no whitespace errors. Stop and return any new failure to the task that owns its exact source file; do not introduce a cross-plan compatibility shim.

## Self-Review

- Spec coverage: Tasks 1 and 4 remove `questions:answer`, the sentinel, checkpoint/question reconciliation, and question authority; Task 5 deletes platform-MCP and notifications; Task 3 removes per-run principals and keeps model-secret reaping; Tasks 2 and 6 remove static review/cost bypasses and migrate CI to scoped credentials. Generic MCP, `await_snapshot`, scoped publisher CRUD, model credential cleanup, and `dev-mcp` are explicitly retained.
- Placeholder scan: no deferred behavior, unnamed files, or unspecified test commands remain. Task 7 is verification-only because every implementation task has its own commit.
- Type consistency: all secret attachment callers use `Attach(context.Context, int, *Credential)`; all workflow preparer callers use `(CreatorUserResolver, RunCredentialVault, SecretAttacher)`; all publisher handlers take no token configuration and consume the principal context.

Execution uses subagent-driven development with a fresh implementation agent
and task review at each dependency boundary.
