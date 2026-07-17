# /api/v1/agent/* route scope audit — owner: agent-identity

Status: authoritative per-route auth reference for the agentic platform.
Later workstreams adding a route MUST add a row here in the same change.
Source of truth for tiers: 00-shared-contracts.md §4.1/§4.2.

## Auth tiers

- **admin** — `auth.CheckAdminHandler`: Concourse admin user token only.
- **authorized viewer/member (main)** — `auth.CheckAgentAuthorizationHandler`:
  team-less agent routes authorize against the `main` team with the route's
  `DefaultRoles` entry (contracts decision 21). Every route in this tier MUST
  have an `atc/api/accessor/roles.go` DefaultRoles entry.
- **authorized viewer/member (:team_name)** — plain `auth.CheckAuthorizationHandler`
  for routes carrying a `:team_name` path param.
- **principal(<scope>)** — `auth.CheckAgentPrincipalHandlerFactory.HandlerFor`:
  `Authorization: Bearer cap1.<id>.<secret>` verified against `agent_principals`
  with the named scope required. Admin user tokens are also accepted (fly curl
  debugging). All verification failures (bad token, revoked, expired, missing
  scope) return 401.
- **authenticated (self only)** — `auth.CheckAuthenticationHandler`; the handler
  restricts rows to the token's own user.

## Scope vocabulary (closed set — adding one requires agent-identity sign-off)

`reviews:write`, `tickets:read`, `tickets:write`, `metrics:write`,
`costs:write`, `questions:answer`. Constants live in
`agent/api/principals/types.go`.

## Route table

| Route | Method Path | Tier | Scope | Owner | Status |
|---|---|---|---|---|---|
| CreateAgentPrincipal | POST /api/v1/agent/principals | admin | — | agent-identity | live (this plan) |
| ListAgentPrincipals | GET /api/v1/agent/principals | admin | — | agent-identity | live (this plan) |
| RevokeAgentPrincipal | DELETE /api/v1/agent/principals/:principal_id | admin | — | agent-identity | live (this plan) |
| SubmitAgentReview | POST /api/v1/agent/reviews | principal | reviews:write | agent-identity (was: static-token pass-through) | live (this plan; static token dual-accepted until window end) |
| SubmitAgentFeedback | POST /api/v1/agent/feedback | authorized member (main) | — | existing | live (this plan moves it onto CheckAgentAuthorizationHandler) |
| GetAgentFeedback | GET /api/v1/agent/feedback | authorized viewer (main) | — | existing | live (same move) |
| GetAgentFeedbackSummary | GET /api/v1/agent/feedback/summary | authorized viewer (main) | — | existing | live (same move) |
| ClassifyAgentVerdict | POST /api/v1/agent/feedback/classify | authorized member (main) | — | existing | live (same move) |
| GetAgentReviewFindings | GET /api/v1/agent/reviews/:commit/findings | authorized viewer (main) | — | existing | live (same move) |
| GetBuildAgentReviews | GET /api/v1/builds/:build_id/agent-reviews | build-read access | — | existing | live (unchanged) |
| ListTeamAgentReviews | GET /api/v1/teams/:team_name/agent-reviews | authorized viewer (:team_name) | — | existing | live (unchanged) |
| SetAgentUserCredential | PUT /api/v1/agent/user-credentials | authenticated (self only) | — | credentials-and-budgets | planned (wave 1) |
| GetAgentUserCredentialStatus | GET /api/v1/agent/user-credentials | authenticated (self only) | — | credentials-and-budgets | planned (wave 1) |
| DeleteAgentUserCredential | DELETE /api/v1/agent/user-credentials/:kind | authenticated (self only) | — | credentials-and-budgets | planned (wave 1) |
| GetAgentCostRollup | GET /api/v1/agent/costs | authorized viewer (main) | — | credentials-and-budgets | planned (wave 1) |
| SubmitAgentCostRecord | POST /api/v1/agent/costs | principal | costs:write | credentials-and-budgets | planned (wave 1) |
| ListAgentWorkflows / ListAgentWorkflowVersions / GetAgentWorkflowVersion | GET /api/v1/agent/workflows[...] | authorized viewer (main) | — | workflow-store | planned (wave 1) |
| CreateAgentWorkflowVersion / PromoteAgentWorkflowVersion | POST/PUT /api/v1/agent/workflows[...] | authorized member (main) | — | workflow-store | planned (wave 1) |
| ListAgentTickets | GET /api/v1/agent/tickets | authorized viewer (main) | — | ticket-core | live (ticket-core) |
| CreateAgentTicket | POST /api/v1/agent/tickets | authorized member (main); also principal | tickets:write (origin: retrospective only) | ticket-core | live (ticket-core) |
| GetAgentTicket | GET /api/v1/agent/tickets/:ticket_id | authorized viewer (main); also principal | tickets:read | ticket-core | live (ticket-core) |
| UpdateAgentTicket | PUT /api/v1/agent/tickets/:ticket_id | authorized member (main) | — | ticket-core | live (ticket-core) |
| TransitionAgentTicket | PUT /api/v1/agent/tickets/:ticket_id/state | authorized member (main); also principal | tickets:write | ticket-core | live (ticket-core) |
| SubmitAgentTicketSpec / SubmitAgentTicketPlan | POST /api/v1/agent/tickets/:ticket_id/{spec,plan} | principal; also authorized member (main) | tickets:write | ticket-core | live (ticket-core) |
| UpdateAgentTicketTask | PUT /api/v1/agent/tickets/:ticket_id/tasks/:ordering | principal | tickets:write | ticket-core | live (ticket-core) |
| SubmitAgentRunMetrics | POST /api/v1/agent/metrics | principal | metrics:write | agent-step | planned (wave 2) |
| ListAgentRunMetrics | GET /api/v1/agent/tickets/:ticket_id/metrics | authorized viewer (main) | — | agent-step | planned (wave 2) |
| AskAgentQuestion | POST /api/v1/agent/tickets/:ticket_id/questions | principal | tickets:write | platform-mcp-hitl | planned (wave 3) |
| GetAgentQuestion | GET /api/v1/agent/tickets/:ticket_id/questions/:question_id | principal; also authorized viewer (main) | tickets:read | platform-mcp-hitl | planned (wave 3) |
| AnswerAgentQuestion | PUT /api/v1/agent/tickets/:ticket_id/questions/:question_id/answer | authorized member (main); also principal | questions:answer (timeout resolution only) | platform-mcp-hitl | planned (wave 3) |
| SetAgentTicketDisposition | PUT /api/v1/agent/tickets/:ticket_id/disposition | authorized member (main) | — | delivery-outcomes | planned (wave 4) |
| GetAgentTicketOutcome | GET /api/v1/agent/tickets/:ticket_id/outcome | authorized viewer (main) | — | delivery-outcomes | planned (wave 4) |
| GetAgentWorkflowScorecard | GET /api/v1/agent/workflows/:workflow_name/scorecard | authorized viewer (main) | — | scorecards | planned (wave 4) |
| ListAgentBenchmarkCases / CreateAgentBenchmarkCase | GET/POST /api/v1/agent/benchmarks | authorized viewer/member (main) | — | process-intel-experiments | planned (wave 5) |
| CreateAgentExperiment / GetAgentExperiment | POST/GET /api/v1/agent/experiments[...] | authorized member/viewer (main) | — | process-intel-experiments | planned (wave 5) |

## Audit-attribution convention (writing principal on agent-authored rows)

- Every table written by a principal-authed route carries a
  `TEXT NOT NULL DEFAULT ''` column named `submitted_by` (row written by an
  agent submission) or `created_by` (row created on someone's behalf),
  holding the **principal name** (or the human username for
  human-authenticated writes).
- Handlers obtain it via `principals.FromContext(r.Context())` — the
  `CheckAgentPrincipalHandler` tier places the verified principal into the
  request context before delegating. Never trust a client-supplied name.
- Writes performed with the legacy static publish token during the
  dual-accept window are attributed to the backfilled principal
  `legacy-publish` (`principals.LegacyPublishPrincipalName`).
- First demonstrator: `agent_reviews.submitted_by` (migration 1773106011).
  ticket-core's `agent_tickets.created_by` / `agent_ticket_specs.submitted_by`
  (contracts §1.7) follow the same convention.

## Per-run principals (the "optional ticket/run claim")

The principal model stays coarse: one row per agent role (`ci-agent-review`,
`platform-mcp`, `gateway`, `harvest`, ...). A run-scoped identity is an
ordinary row minted by dispatch (wave 4) with:
- `name` = `run-<pipeline-run-id>-<role>` (e.g. `run-42-platform`),
- `expires_at` = now + run timeout,
- exactly the scopes the role needs for that run.
Revoking the run principal (or letting it expire) kills the sidecar's access.
No schema extension is required for run claims in v1.
