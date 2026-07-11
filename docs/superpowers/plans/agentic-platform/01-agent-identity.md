# Agent Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single static agent-review publish token with scoped, issuable/revocable per-agent principals (`agent_principals` table, `cap1.` tokens, a `principal(<scope>)` auth tier), cut the live theborg/cicd publisher over during a dual-accept window, and land the per-route scope audit and audit-attribution convention that every wave-2+ workstream codes against.

**Architecture:** A new `agent/api/principals` package owns the token wire format (`cap1.<id>.<43-char secret>`, sha256-at-rest, GitHub-PAT style), the domain types + `Store` interface, a 60s-cached `Verifier`, the request-context attribution helper, and the admin HTTP handler; `atc/db.NewAgentPrincipalsFactory` persists to `agent_principals` (migration `1773106010`). Two new auth tiers land in `atc/api/auth`: `CheckAgentPrincipalHandler` (principal token with admin fallback plus a temporary legacy-static-token bypass on `SubmitAgentReview`) and `CheckAgentAuthorizationHandler` (hardcoded main-team authorization for team-less `/api/v1/agent/*` routes, making today's dead `DefaultRoles` entries effective — contracts decision 21). The verified principal rides the request context and is recorded on written rows via `agent_reviews.submitted_by` (migration `1773106011`) — the audit-attribution convention.

**Tech Stack:** Go (net/http, crypto/rand, crypto/sha256, crypto/subtle), PostgreSQL via `atc/db` (squirrel `psql`, pgx `pgtype` for `TEXT[]`), embedded SQL migrations, rata routes, Ginkgo/Gomega for `atc/*` packages, plain `testing` for `agent/api/*`, theborg/cicd live cluster for the cutover.

---

## Context

**Charter** (workstreams.json id `agent-identity`, wave 1, size S, `depends_on: []`): coarse per-agent-role principals with an optional ticket/run claim (realized as ordinary rows named by convention with `expires_at`, per contracts §8.2 — no extra columns); scoped mint/verify/revoke replacing the `SubmitAgentReview` pass-through in `atc/wrappa/api_auth_wrappa.go`; dual-accept window keeping the static `--agent-review-publish-token` working behind a deprecation flag; live theborg/cicd publisher migration and static-token removal at window end; per-route scope documentation for every existing and planned `/api/v1/agent/*` route; the writing-principal attribution convention.

**Scope-in → task mapping:** principal model → Tasks 2–4; mint/verify/revoke + dual-accept → Tasks 3–5; theborg cutover + static removal → Task 8; per-route scope documentation → Task 1; attribution convention → Tasks 1 (doc), 2 (column), 3 (context helper), 5 (write-through). Contracts §4.2's decision-21 work (`CheckAgentAuthorizationHandler`, explicitly owned by agent-identity) → Task 6. End-to-end proof → Task 7.

**Scope-out (must NOT implement):** per-user credential vault / Anthropic tokens (credentials-and-budgets), K8s multi-namespace isolation, and the principal consumers themselves (platform-mcp, gateway, harvest adopt principals when they land). No fly subcommands — minting is an admin API operation (`fly curl` / curl).

**Prior waves:** none — this is wave 1. Wave-mates (credentials-and-budgets, pipeline-runs, dev-mcp, workflow-store) run in parallel and never touch `api_auth_wrappa.go`; the two workstreams that extend the auth switch (ticket-core, agent-step) land in wave 2 against what this plan builds.

**PRODUCES (contract surfaces in `00-shared-contracts.md`):**
- §1.2 `agent_principals` (owner: agent-identity) — table, token wire format, verification algorithm, `legacy-publish` backfill.
- §4.1 "Auth tiers and principal scopes" — the `principal(<scope>)` wrappa tier, the closed scope vocabulary, admin-token acceptance.
- §4.2 route table rows `CreateAgentPrincipal` / `ListAgentPrincipals` / `RevokeAgentPrincipal` and the closing "Team-less `/api/v1/agent/*` authorization" paragraph (`CheckAgentAuthorizationHandler`, decision 21).
- New (this plan, recorded as a §11 amendment in Task 1): `agent_reviews.submitted_by` attribution column, frozen Go surface names, and the per-route scope audit document `docs/superpowers/plans/agentic-platform/agent-route-scopes.md`.

**CONSUMES:** §1.1 migration-number allocation (block 1773106010–19), document-wide conventions (timestamps as `TIMESTAMPTZ` + epoch-seconds JSON, factory recipe from `atc/db/agent_reviews_factory.go`), and §4.2's tier definitions for routes owned by later workstreams (documented, not implemented, in the Task 1 audit).

**Verified code seams** (all opened and line-anchored on branch `jetbridge`):
- `atc/wrappa/api_auth_wrappa.go:112-113` — the `SubmitAgentReview` pass-through being generalized; `:169-176` — the five team-less agent feedback routes currently (and wrongly) in the `CheckAuthorizationHandler` case.
- `agent/api/reviews/handler.go:69-79` — the static-token carve-out inside `SubmitReview`.
- `atc/api/accessor/roles.go:102-114` — agent-route `DefaultRoles` entries (dead today, revived by Task 6).
- `atc/api/accessor/accessor.go:161-163` — `IsAuthorized(team)` reduces to `isAdmin` for team `""` (the decision-21 false-premise fix).
- `atc/api/handler.go:92,123-139,269-277`, `atc/atccmd/command.go:218,2239-2244,2296-2300`, `atc/api/api_suite_test.go:174,182,225-227` — wiring points.
- `atc/db/migration/legacy_upgrade_test.go:37` — `jetbridgeHeadMigration` constant that must track the new head.

---

### Task 1: Per-route scope audit + shared-contracts addendum

The audit document later workstreams code against, plus the cross-workstream sign-off note §11 requires for the one schema addition this plan makes beyond the frozen contracts.

**Files:**
- Create: `docs/superpowers/plans/agentic-platform/agent-route-scopes.md`
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:1463` (append to §11 Amendment log)

**Steps:**

- [ ] Write `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` with exactly this structure and content (the route table reproduces contracts §4.2 for `/api/v1/agent/*` routes — do not invent new tiers):

```markdown
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
| ListAgentTickets | GET /api/v1/agent/tickets | authorized viewer (main) | — | ticket-core | planned (wave 2) |
| CreateAgentTicket | POST /api/v1/agent/tickets | authorized member (main); also principal | tickets:write (origin: retrospective only) | ticket-core | planned (wave 2) |
| GetAgentTicket | GET /api/v1/agent/tickets/:ticket_id | authorized viewer (main); also principal | tickets:read | ticket-core | planned (wave 2) |
| UpdateAgentTicket | PUT /api/v1/agent/tickets/:ticket_id | authorized member (main) | — | ticket-core | planned (wave 2) |
| TransitionAgentTicket | PUT /api/v1/agent/tickets/:ticket_id/state | authorized member (main); also principal | tickets:write | ticket-core | planned (wave 2) |
| SubmitAgentTicketSpec / SubmitAgentTicketPlan | POST /api/v1/agent/tickets/:ticket_id/{spec,plan} | principal; also authorized member (main) | tickets:write | ticket-core | planned (wave 2) |
| UpdateAgentTicketTask | PUT /api/v1/agent/tickets/:ticket_id/tasks/:ordering | principal | tickets:write | ticket-core | planned (wave 2) |
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
```

- [ ] Append this entry to the `## 11. Amendment log` section at the end of `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (after the 2026-07-08 review-fixes entry):

```markdown
- 2026-07-08 (agent-identity planning addendum, cross-workstream sign-off note):
  - §1.2 / attribution convention (affects: harvest-step, ticket-core, delivery-outcomes): `agent_reviews` gains `submitted_by TEXT NOT NULL DEFAULT ''` via migration `1773106011` (inside agent-identity's 1773106010–19 block), recording the writing principal's name — the first demonstrator of the created_by/submitted_by audit-attribution convention. Orthogonal to harvest-step's planned ticket/run linkage columns (block 1773106080–89); no collision.
  - §4.1 Go surface names frozen by agent-identity: package `agent/api/principals` (types `Principal`, `CreateSpec`, `Store`, `Verifier`, `MemoryStore`, `Handler`; funcs `MintToken`, `ParseTokenID`, `HashToken`, `DisplayPrefix`, `NewContext`, `FromContext`; scope constants `ScopeReviewsWrite`, `ScopeTicketsRead`, `ScopeTicketsWrite`, `ScopeMetricsWrite`, `ScopeCostsWrite`, `ScopeQuestionsAnswer`; `LegacyPublishPrincipalName`; `TokenVersionPrefix = "cap1."`); `atc/db.AgentPrincipalsFactory` / `NewAgentPrincipalsFactory`; `atc/api/auth.CheckAgentPrincipalHandlerFactory` / `NewCheckAgentPrincipalHandlerFactory`; `atc/api/auth.CheckAgentAuthorizationHandler`. Verification failures on the principal tier are uniformly 401.
  - §4.2: the per-route scope audit (the document later workstreams add rows to) lives at `docs/superpowers/plans/agentic-platform/agent-route-scopes.md`.
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/ && git commit -m "docs(agent-identity): per-route scope audit + shared-contracts addendum"`

---

### Task 2: Migrations — `agent_principals` + `agent_reviews.submitted_by`

**Files:**
- Create: `atc/db/migration/migrations/1773106010_create_agent_principals.up.sql`
- Create: `atc/db/migration/migrations/1773106010_create_agent_principals.down.sql`
- Create: `atc/db/migration/migrations/1773106011_add_agent_reviews_submitted_by.up.sql`
- Create: `atc/db/migration/migrations/1773106011_add_agent_reviews_submitted_by.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration`)
- Test: `atc/db/migration/legacy_upgrade_test.go` (existing suite exercises the new head)

**Steps:**

- [ ] Write the failing test first — bump the head constant at `atc/db/migration/legacy_upgrade_test.go:37`:

```go
// JetBridge HEAD (last migration)
const jetbridgeHeadMigration = 1773106011
```

- [ ] Run `ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/` — expect FAIL: `Up()` reaches `1773105504`, so `ExpectDatabaseMigrationVersionToEqual(migrator, jetbridgeHeadMigration)` fails (version mismatch), proving the constant now demands the new migrations.

- [ ] Create `atc/db/migration/migrations/1773106010_create_agent_principals.up.sql` — DDL is verbatim contracts §1.2 plus the specified `legacy-publish` backfill:

```sql
CREATE TABLE agent_principals (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,                  -- e.g. 'harvest', 'gateway', 'ci-agent-review', 'agent-step'
    description  TEXT NOT NULL DEFAULT '',
    token_prefix TEXT NOT NULL,                  -- first 12 chars of the token, for display + O(1) lookup
    token_hash   TEXT NOT NULL,                  -- hex(sha256(full token)); raw token never stored
    scopes       TEXT[] NOT NULL DEFAULT '{}',   -- closed vocabulary, see 00-shared-contracts.md §4.1
    team_name    TEXT NOT NULL DEFAULT 'main',   -- join key to teams.name; '' = platform-global
    created_by   TEXT NOT NULL DEFAULT '',       -- concourse username that minted it
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,                    -- NULL = no expiry
    revoked_at   TIMESTAMPTZ,                    -- NULL = active
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX agent_principals_token_hash ON agent_principals (token_hash);
CREATE INDEX agent_principals_name ON agent_principals (name);

-- Dual-accept window attribution: rows written with the static
-- --agent-review-publish-token are attributed to this principal.
-- It has no token (empty hash can never match a 64-char sha256 hex).
INSERT INTO agent_principals (name, description, token_prefix, token_hash)
VALUES ('legacy-publish', 'static --agent-review-publish-token writes during the dual-accept window', '', '');
```

- [ ] Create `atc/db/migration/migrations/1773106010_create_agent_principals.down.sql`:

```sql
DROP TABLE agent_principals;
```

- [ ] Create `atc/db/migration/migrations/1773106011_add_agent_reviews_submitted_by.up.sql`:

```sql
ALTER TABLE agent_reviews
    ADD COLUMN submitted_by TEXT NOT NULL DEFAULT '';
```

- [ ] Create `atc/db/migration/migrations/1773106011_add_agent_reviews_submitted_by.down.sql`:

```sql
ALTER TABLE agent_reviews
    DROP COLUMN submitted_by;
```

- [ ] Run `ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/` — expect PASS (the `go:embed migrations` directive in `atc/db/migration/migration.go:153` picks the files up automatically; up/rollback/idempotency specs cover the new pair).

- [ ] Commit: `git add atc/db/migration/ && git commit -m "feat(db): agent_principals table + agent_reviews.submitted_by (migrations 1773106010-11)"`

---

### Task 3: `agent/api/principals` package — token format, types, context helper, verifier

Pure-Go domain package, no atc imports (same layering as `agent/api/reviews`). Plain `testing` tests, matching `agent/api/reviews/handler_test.go` style.

**Files:**
- Create: `agent/api/principals/types.go`
- Create: `agent/api/principals/token.go`
- Create: `agent/api/principals/context.go`
- Create: `agent/api/principals/verifier.go`
- Create: `agent/api/principals/memory_store.go`
- Test: `agent/api/principals/token_test.go`, `agent/api/principals/types_test.go`, `agent/api/principals/verifier_test.go`

**Steps:**

- [ ] Write the failing token tests in `agent/api/principals/token_test.go`:

```go
package principals_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func TestMintTokenShape(t *testing.T) {
	token, prefix, hash, err := principals.MintToken(42)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if !strings.HasPrefix(token, "cap1.42.") {
		t.Errorf("token = %q, want cap1.42. prefix", token)
	}
	secret := strings.TrimPrefix(token, "cap1.42.")
	if len(secret) != 43 {
		t.Errorf("secret length = %d, want 43 (32 bytes base64url raw)", len(secret))
	}
	if prefix != token[:12] {
		t.Errorf("prefix = %q, want first 12 chars %q", prefix, token[:12])
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(hash))
	}
	if hash != principals.HashToken(token) {
		t.Errorf("hash mismatch with HashToken")
	}
}

func TestMintTokenUnique(t *testing.T) {
	a, _, _, _ := principals.MintToken(1)
	b, _, _, _ := principals.MintToken(1)
	if a == b {
		t.Fatal("two mints produced the same token")
	}
}

func TestParseTokenID(t *testing.T) {
	token, _, _, _ := principals.MintToken(7)
	id, ok := principals.ParseTokenID(token)
	if !ok || id != 7 {
		t.Errorf("ParseTokenID(%q) = %d,%v want 7,true", token, id, ok)
	}

	for _, bad := range []string{
		"", "cap1.", "cap1.7", "cap1..secret", "cap1.abc.secret",
		"cap2.7.secret", "cap1.-1.secret", "cap1.0.secret",
		"Bearer cap1.7.secret", "eyJhbGciOi.something.jwt",
	} {
		if _, ok := principals.ParseTokenID(bad); ok {
			t.Errorf("ParseTokenID(%q) = ok, want reject", bad)
		}
	}
}
```

- [ ] Run `go test ./agent/api/principals/` — expect FAIL: package does not exist / undefined symbols.

- [ ] Create `agent/api/principals/token.go`:

```go
package principals

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// TokenVersionPrefix distinguishes agent principal tokens from Concourse
// JWTs at the auth seam. Wire format (00-shared-contracts.md §1.2):
// cap1.<id>.<43-char base64url secret> — cap = concourse agent principal,
// 1 = format version.
const TokenVersionPrefix = "cap1."

// tokenPrefixLen is how much of the token is stored for display
// (agent_principals.token_prefix).
const tokenPrefixLen = 12

// MintToken generates the wire token for principal id. It returns the
// full token (surfaced exactly once, never stored), the 12-char display
// prefix, and the hex sha256 hash stored at rest.
func MintToken(id int) (token, prefix, hash string, err error) {
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", "", "", fmt.Errorf("generate principal secret: %w", err)
	}
	token = fmt.Sprintf("cap1.%d.%s", id, base64.RawURLEncoding.EncodeToString(secret))
	return token, DisplayPrefix(token), HashToken(token), nil
}

// DisplayPrefix is the stored/displayed first 12 characters of a token.
func DisplayPrefix(token string) string {
	if len(token) < tokenPrefixLen {
		return token
	}
	return token[:tokenPrefixLen]
}

// HashToken is hex(sha256(full token)) — the at-rest form.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ParseTokenID extracts the principal id from a wire token. ok=false for
// anything that is not a well-formed cap1 token.
func ParseTokenID(token string) (int, bool) {
	if !strings.HasPrefix(token, TokenVersionPrefix) {
		return 0, false
	}
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 || parts[2] == "" {
		return 0, false
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
```

- [ ] Run `go test ./agent/api/principals/` — token tests still FAIL on missing types (`Store` etc. referenced next); proceed to types.

- [ ] Write the failing types tests in `agent/api/principals/types_test.go`:

```go
package principals_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func TestCreateSpecValidate(t *testing.T) {
	valid := principals.CreateSpec{Name: "ci-agent-review", Scopes: []string{principals.ScopeReviewsWrite}}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}

	cases := map[string]principals.CreateSpec{
		"missing name":  {Scopes: []string{principals.ScopeReviewsWrite}},
		"no scopes":     {Name: "x"},
		"unknown scope": {Name: "x", Scopes: []string{"reviews:read"}},
	}
	for name, spec := range cases {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestPrincipalHasScope(t *testing.T) {
	p := principals.Principal{Scopes: []string{principals.ScopeTicketsRead, principals.ScopeTicketsWrite}}
	if !p.HasScope(principals.ScopeTicketsRead) {
		t.Error("expected tickets:read")
	}
	if p.HasScope(principals.ScopeReviewsWrite) {
		t.Error("did not expect reviews:write")
	}
}

func TestTokenHashNeverSerializes(t *testing.T) {
	p := principals.Principal{ID: 1, Name: "x", TokenHash: "secret-hash"}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret-hash") {
		t.Errorf("TokenHash leaked into JSON: %s", out)
	}
}
```

- [ ] Create `agent/api/principals/types.go`:

```go
package principals

import (
	"fmt"
	"slices"
	"time"
)

// Scope vocabulary — closed set (00-shared-contracts.md §4.1). Adding a
// scope requires agent-identity sign-off; update
// docs/superpowers/plans/agentic-platform/agent-route-scopes.md in the
// same change.
const (
	ScopeReviewsWrite    = "reviews:write"
	ScopeTicketsRead     = "tickets:read"
	ScopeTicketsWrite    = "tickets:write"
	ScopeMetricsWrite    = "metrics:write"
	ScopeCostsWrite      = "costs:write"
	ScopeQuestionsAnswer = "questions:answer"
)

// ValidScopes is the closed scope set.
var ValidScopes = map[string]bool{
	ScopeReviewsWrite:    true,
	ScopeTicketsRead:     true,
	ScopeTicketsWrite:    true,
	ScopeMetricsWrite:    true,
	ScopeCostsWrite:      true,
	ScopeQuestionsAnswer: true,
}

// LegacyPublishPrincipalName attributes rows written with the static
// --agent-review-publish-token during the dual-accept window. The row is
// backfilled by migration 1773106010 with no token.
const LegacyPublishPrincipalName = "legacy-publish"

// Principal is one agent_principals row. Timestamps are Unix epoch
// seconds in JSON (repo convention, matching agent_reviews). TokenHash
// is needed for verification but must never serialize.
type Principal struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	TokenPrefix string   `json:"token_prefix"`
	TokenHash   string   `json:"-"`
	Scopes      []string `json:"scopes"`
	TeamName    string   `json:"team_name"`
	CreatedBy   string   `json:"created_by"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   *int64   `json:"expires_at,omitempty"`
	RevokedAt   *int64   `json:"revoked_at,omitempty"`
	LastUsedAt  *int64   `json:"last_used_at,omitempty"`
}

// HasScope reports whether the principal holds the scope.
func (p Principal) HasScope(scope string) bool {
	return slices.Contains(p.Scopes, scope)
}

// CreateSpec is the POST /api/v1/agent/principals request body.
// CreatedBy is filled server-side from the admin's username — never
// client-supplied.
type CreateSpec struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
	TeamName    string   `json:"team_name"`
	CreatedBy   string   `json:"-"`
	ExpiresAt   *int64   `json:"expires_at,omitempty"`
}

// Validate enforces the closed scope vocabulary and required fields.
func (s CreateSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(s.Scopes) == 0 {
		return fmt.Errorf("at least one scope is required")
	}
	for _, scope := range s.Scopes {
		if !ValidScopes[scope] {
			return fmt.Errorf("unknown scope %q", scope)
		}
	}
	return nil
}

// Store is the persistence interface, implemented by
// atc/db.NewAgentPrincipalsFactory (Postgres) and MemoryStore (tests).
type Store interface {
	// Create mints a new principal and returns it along with the full
	// wire token. The raw token is returned exactly once and never
	// stored.
	Create(spec CreateSpec) (Principal, string, error)
	// List returns all principals, active and revoked, ordered by id.
	List() ([]Principal, error)
	// Get returns the principal (including TokenHash) for verification.
	Get(id int) (Principal, bool, error)
	// Revoke sets revoked_at (idempotent, keeps the first revocation
	// time); found=false when no such principal exists.
	Revoke(id int) (bool, error)
	// RecordUse updates last_used_at. Best-effort: callers ignore errors.
	RecordUse(id int, usedAt time.Time) error
}
```

- [ ] Create `agent/api/principals/context.go`:

```go
package principals

import "context"

type contextKey struct{}

// NewContext records the verified writing principal on the request
// context. Handlers that persist agent-authored rows read it back with
// FromContext and store Principal.Name in their created_by/submitted_by
// column — the audit-attribution convention
// (docs/superpowers/plans/agentic-platform/agent-route-scopes.md).
func NewContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// FromContext returns the verified principal, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}
```

- [ ] Create `agent/api/principals/memory_store.go` (mirrors `agent/api/reviews/memory_store.go`'s role — used by unit tests and `atc/api/api_suite_test.go`):

```go
package principals

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	rows   map[int]Principal
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{nextID: 1, rows: map[int]Principal{}}
}

func (m *MemoryStore) Create(spec CreateSpec) (Principal, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.nextID
	m.nextID++

	token, prefix, hash, err := MintToken(id)
	if err != nil {
		return Principal{}, "", err
	}

	teamName := spec.TeamName
	if teamName == "" {
		teamName = "main"
	}

	p := Principal{
		ID:          id,
		Name:        spec.Name,
		Description: spec.Description,
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      append([]string{}, spec.Scopes...),
		TeamName:    teamName,
		CreatedBy:   spec.CreatedBy,
		CreatedAt:   time.Now().Unix(),
		ExpiresAt:   spec.ExpiresAt,
	}
	m.rows[id] = p
	return p, token, nil
}

func (m *MemoryStore) List() ([]Principal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Principal{}
	for id := 1; id < m.nextID; id++ {
		if p, ok := m.rows[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *MemoryStore) Get(id int) (Principal, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	return p, ok, nil
}

func (m *MemoryStore) Revoke(id int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	if !ok {
		return false, nil
	}
	if p.RevokedAt == nil {
		now := time.Now().Unix()
		p.RevokedAt = &now
		m.rows[id] = p
	}
	return true, nil
}

func (m *MemoryStore) RecordUse(id int, usedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.rows[id]; ok {
		epoch := usedAt.Unix()
		p.LastUsedAt = &epoch
		m.rows[id] = p
	}
	return nil
}
```

- [ ] Run `go test ./agent/api/principals/` — token + types tests PASS.

- [ ] Write the failing verifier tests in `agent/api/principals/verifier_test.go` (in-package so the test can inject the clock):

```go
package principals

import (
	"errors"
	"testing"
	"time"
)

func mustCreate(t *testing.T, store *MemoryStore, spec CreateSpec) (Principal, string) {
	t.Helper()
	p, token, err := store.Create(spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p, token
}

func TestVerifyHappyPath(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "reviewer", Scopes: []string{ScopeReviewsWrite}})

	v := NewVerifier(store)
	p, err := v.Verify(token, ScopeReviewsWrite)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.ID != created.ID || p.Name != "reviewer" {
		t.Errorf("verified principal = %+v", p)
	}

	got, _, _ := store.Get(created.ID)
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at recorded on first verification")
	}
}

func TestVerifyRejections(t *testing.T) {
	store := NewMemoryStore()
	_, token := mustCreate(t, store, CreateSpec{Name: "reviewer", Scopes: []string{ScopeReviewsWrite}})

	v := NewVerifier(store)

	if _, err := v.Verify("not-a-token", ScopeReviewsWrite); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("garbage token: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify("cap1.999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ScopeReviewsWrite); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("unknown id: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify(token+"x", ScopeReviewsWrite); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret: err = %v, want ErrInvalidToken", err)
	}
	if _, err := v.Verify(token, ScopeTicketsWrite); !errors.Is(err, ErrMissingScope) {
		t.Errorf("missing scope: err = %v, want ErrMissingScope", err)
	}
}

func TestVerifyRevokedBeforeFirstUse(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeReviewsWrite}})
	store.Revoke(created.ID)

	v := NewVerifier(store)
	if _, err := v.Verify(token, ScopeReviewsWrite); !errors.Is(err, ErrRevoked) {
		t.Errorf("err = %v, want ErrRevoked", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	store := NewMemoryStore()
	past := time.Now().Add(-time.Hour).Unix()
	_, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeReviewsWrite}, ExpiresAt: &past})

	v := NewVerifier(store)
	if _, err := v.Verify(token, ScopeReviewsWrite); !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
}

func TestVerifyCacheWindow(t *testing.T) {
	store := NewMemoryStore()
	created, token := mustCreate(t, store, CreateSpec{Name: "r", Scopes: []string{ScopeReviewsWrite}})

	current := time.Now()
	v := NewVerifier(store)
	v.now = func() time.Time { return current }

	if _, err := v.Verify(token, ScopeReviewsWrite); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	// Revocation lands after the row was cached: still accepted inside
	// the 60s window (documented staleness, contracts §1.2)...
	store.Revoke(created.ID)
	if _, err := v.Verify(token, ScopeReviewsWrite); err != nil {
		t.Errorf("within cache window: err = %v, want nil (60s staleness)", err)
	}

	// ...and rejected once the cache entry ages out.
	current = current.Add(61 * time.Second)
	if _, err := v.Verify(token, ScopeReviewsWrite); !errors.Is(err, ErrRevoked) {
		t.Errorf("after cache window: err = %v, want ErrRevoked", err)
	}
}
```

- [ ] Run `go test ./agent/api/principals/` — expect FAIL: `NewVerifier` undefined.

- [ ] Create `agent/api/principals/verifier.go`:

```go
package principals

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid agent principal token")
	ErrRevoked      = errors.New("agent principal is revoked")
	ErrExpired      = errors.New("agent principal token is expired")
	ErrMissingScope = errors.New("agent principal lacks required scope")
)

// cacheTTL: verification is one indexed SELECT with a 60s in-memory
// cache (00-shared-contracts.md §1.2 decision). Consequence: revocation
// may take up to 60s to be seen by a running web node.
const cacheTTL = 60 * time.Second

type cacheEntry struct {
	principal Principal
	fetchedAt time.Time
}

// Verifier checks cap1 wire tokens against the Store.
type Verifier struct {
	store Store
	now   func() time.Time

	mu    sync.Mutex
	cache map[int]cacheEntry
}

func NewVerifier(store Store) *Verifier {
	return &Verifier{store: store, now: time.Now, cache: map[int]cacheEntry{}}
}

// Verify parses the wire token, loads the principal (cached up to 60s),
// constant-time compares the hash, and checks revocation, expiry, and
// scope. All failures map to 401 at the HTTP layer.
func (v *Verifier) Verify(token, scope string) (Principal, error) {
	id, ok := ParseTokenID(token)
	if !ok {
		return Principal{}, ErrInvalidToken
	}

	p, found, err := v.lookup(id)
	if err != nil {
		return Principal{}, err
	}
	if !found {
		return Principal{}, ErrInvalidToken
	}

	if subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(p.TokenHash)) != 1 {
		return Principal{}, ErrInvalidToken
	}
	if p.RevokedAt != nil {
		return Principal{}, ErrRevoked
	}
	if p.ExpiresAt != nil && *p.ExpiresAt <= v.now().Unix() {
		return Principal{}, ErrExpired
	}
	if !p.HasScope(scope) {
		return Principal{}, ErrMissingScope
	}
	return p, nil
}

func (v *Verifier) lookup(id int) (Principal, bool, error) {
	v.mu.Lock()
	entry, ok := v.cache[id]
	v.mu.Unlock()
	if ok && v.now().Sub(entry.fetchedAt) < cacheTTL {
		return entry.principal, true, nil
	}

	p, found, err := v.store.Get(id)
	if err != nil || !found {
		return Principal{}, found, err
	}

	v.mu.Lock()
	v.cache[id] = cacheEntry{principal: p, fetchedAt: v.now()}
	v.mu.Unlock()

	// Best-effort usage stamp, at most once per cache fill.
	_ = v.store.RecordUse(id, v.now())

	return p, true, nil
}
```

- [ ] Run `go test ./agent/api/principals/` — all PASS.

- [ ] Commit: `git add agent/api/principals/ && git commit -m "feat(agent): principals package - cap1 token format, verifier, attribution context"`

---

### Task 4: DB factory + admin mint/list/revoke API

Follows the `atc/db/agent_reviews_factory.go` recipe (Store interface in `agent/api/`, factory in `atc/db/`, counterfeiter directive) and the `agent/api/reviews` handler-wiring precedent in `atc/api/handler.go`.

**Files:**
- Create: `atc/db/agent_principals_factory.go`
- Create: `agent/api/principals/handler.go`
- Modify: `atc/routes.go:129` (constants block, after `ListTeamAgentReviews`), `atc/routes.go:262` (routes list, after `ListTeamAgentReviews` entry)
- Modify: `atc/wrappa/api_auth_wrappa.go:126` (admin case, after `atc.ListSharedForResourceType`)
- Modify: `atc/wrappa/reject_archived_wrappa.go` — add the three new actions to the pass-through (leave-handler-as-is) admin case; this switch **panics** on any unlisted route (see `ci/dogfood/FINDINGS.md` add-a-route checklist)
- Modify: `atc/auditor/auditor.go` `ValidateAction` switch — add `atc.CreateAgentPrincipal`, `atc.ListAgentPrincipals`, `atc.RevokeAgentPrincipal` cases; this switch **panics on any unhandled action** and `atc/auditor` `TestAuditor` fails the full test suite otherwise (dogfood build 525330 failed here)
- Modify: `atc/api/handler.go:91-92` (params), `:139` (server construction), `:277` (handler map)
- Modify: `atc/api/api_suite_test.go:226` (new `NewHandler` arg)
- Modify: `atc/atccmd/command.go:2207` (factory construction), `:2298` (new `NewHandler` arg)
- Test: `atc/db/agent_principals_factory_test.go`, `agent/api/principals/handler_test.go`

**Steps:**

- [ ] Write the failing factory spec `atc/db/agent_principals_factory_test.go` (Ginkgo, style of `atc/db/agent_reviews_factory_test.go`):

```go
package db_test

import (
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPrincipalsFactory", func() {
	var factory db.AgentPrincipalsFactory

	BeforeEach(func() {
		factory = db.NewAgentPrincipalsFactory(dbConn)
	})

	It("mints a principal whose token round-trips through Get", func() {
		created, token, err := factory.Create(principals.CreateSpec{
			Name:        "ci-agent-review",
			Description: "theborg publisher",
			Scopes:      []string{principals.ScopeReviewsWrite},
			CreatedBy:   "admin",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(token).To(HavePrefix("cap1."))

		id, ok := principals.ParseTokenID(token)
		Expect(ok).To(BeTrue())
		Expect(id).To(Equal(created.ID))

		got, found, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Name).To(Equal("ci-agent-review"))
		Expect(got.Description).To(Equal("theborg publisher"))
		Expect(got.Scopes).To(Equal([]string{principals.ScopeReviewsWrite}))
		Expect(got.TeamName).To(Equal("main"))
		Expect(got.CreatedBy).To(Equal("admin"))
		Expect(got.TokenPrefix).To(Equal(token[:12]))
		Expect(got.TokenHash).To(Equal(principals.HashToken(token)))
		Expect(got.CreatedAt).To(BeNumerically(">", 0))
		Expect(got.RevokedAt).To(BeNil())
	})

	It("stores expiry when given", func() {
		expires := time.Now().Add(time.Hour).Unix()
		created, _, err := factory.Create(principals.CreateSpec{
			Name:      "run-42-platform",
			Scopes:    []string{principals.ScopeTicketsWrite},
			ExpiresAt: &expires,
		})
		Expect(err).NotTo(HaveOccurred())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ExpiresAt).NotTo(BeNil())
		Expect(*got.ExpiresAt).To(Equal(expires))
	})

	It("lists principals including the backfilled legacy-publish row", func() {
		_, _, err := factory.Create(principals.CreateSpec{
			Name: "gateway", Scopes: []string{principals.ScopeCostsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		list, err := factory.List()
		Expect(err).NotTo(HaveOccurred())

		names := []string{}
		for _, p := range list {
			names = append(names, p.Name)
		}
		Expect(names).To(ContainElement(principals.LegacyPublishPrincipalName))
		Expect(names).To(ContainElement("gateway"))
	})

	It("revokes idempotently and reports missing ids", func() {
		created, _, err := factory.Create(principals.CreateSpec{
			Name: "harvest", Scopes: []string{principals.ScopeTicketsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		found, err := factory.Revoke(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.RevokedAt).NotTo(BeNil())
		firstRevokedAt := *got.RevokedAt

		// second revoke keeps the original timestamp
		found, err = factory.Revoke(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		got, _, _ = factory.Get(created.ID)
		Expect(*got.RevokedAt).To(Equal(firstRevokedAt))

		found, err = factory.Revoke(999999)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("records usage", func() {
		created, _, err := factory.Create(principals.CreateSpec{
			Name: "agent-step", Scopes: []string{principals.ScopeMetricsWrite},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(factory.RecordUse(created.ID, time.Now())).To(Succeed())

		got, _, err := factory.Get(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.LastUsedAt).NotTo(BeNil())
	})
})
```

- [ ] Run `ginkgo --focus="AgentPrincipalsFactory" ./atc/db/` — expect FAIL: `db.AgentPrincipalsFactory` undefined. (PostgreSQL must be running: `pg_isready`. If `database "testdb_template" already exists`, another test process is running — wait or kill it.)

- [ ] Create `atc/db/agent_principals_factory.go`:

```go
package db

import (
	"database/sql"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/jackc/pgx/v5/pgtype"
)

//counterfeiter:generate . AgentPrincipalsFactory
type AgentPrincipalsFactory interface {
	principals.Store
}

func NewAgentPrincipalsFactory(conn DbConn) AgentPrincipalsFactory {
	return &agentPrincipalsFactory{conn: conn}
}

type agentPrincipalsFactory struct {
	conn DbConn
}

// Create mints a principal. The token embeds the row id
// (cap1.<id>.<secret>), so the id is drawn from the sequence first and
// the row is inserted with hash+prefix already computed. The raw token
// is returned exactly once and never stored.
func (f *agentPrincipalsFactory) Create(spec principals.CreateSpec) (principals.Principal, string, error) {
	var id int
	err := f.conn.QueryRow(
		`SELECT nextval(pg_get_serial_sequence('agent_principals', 'id'))`,
	).Scan(&id)
	if err != nil {
		return principals.Principal{}, "", err
	}

	token, prefix, hash, err := principals.MintToken(id)
	if err != nil {
		return principals.Principal{}, "", err
	}

	teamName := spec.TeamName
	if teamName == "" {
		teamName = "main"
	}

	var expiresAt *time.Time
	if spec.ExpiresAt != nil {
		t := time.Unix(*spec.ExpiresAt, 0)
		expiresAt = &t
	}

	var createdAt int64
	err = psql.Insert("agent_principals").
		Columns("id", "name", "description", "token_prefix", "token_hash",
			"scopes", "team_name", "created_by", "expires_at").
		Values(id, spec.Name, spec.Description, prefix, hash,
			spec.Scopes, teamName, spec.CreatedBy, expiresAt).
		Suffix("RETURNING EXTRACT(EPOCH FROM created_at)::bigint").
		RunWith(f.conn).
		QueryRow().
		Scan(&createdAt)
	if err != nil {
		return principals.Principal{}, "", err
	}

	return principals.Principal{
		ID:          id,
		Name:        spec.Name,
		Description: spec.Description,
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      append([]string{}, spec.Scopes...),
		TeamName:    teamName,
		CreatedBy:   spec.CreatedBy,
		CreatedAt:   createdAt,
		ExpiresAt:   spec.ExpiresAt,
	}, token, nil
}

const principalColumns = `id, name, description, token_prefix, token_hash,
	scopes, team_name, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint,
	EXTRACT(EPOCH FROM expires_at)::bigint,
	EXTRACT(EPOCH FROM revoked_at)::bigint,
	EXTRACT(EPOCH FROM last_used_at)::bigint`

func (f *agentPrincipalsFactory) List() ([]principals.Principal, error) {
	rows, err := f.conn.Query(
		`SELECT ` + principalColumns + ` FROM agent_principals ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []principals.Principal{}
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (f *agentPrincipalsFactory) Get(id int) (principals.Principal, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+principalColumns+` FROM agent_principals WHERE id = $1`, id,
	)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return principals.Principal{}, false, nil
	}
	if err != nil {
		return principals.Principal{}, false, err
	}
	return p, true, nil
}

func (f *agentPrincipalsFactory) Revoke(id int) (bool, error) {
	res, err := psql.Update("agent_principals").
		Set("revoked_at", sq.Expr("COALESCE(revoked_at, now())")).
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		Exec()
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (f *agentPrincipalsFactory) RecordUse(id int, usedAt time.Time) error {
	_, err := psql.Update("agent_principals").
		Set("last_used_at", usedAt).
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		Exec()
	return err
}

func scanPrincipal(row interface{ Scan(...any) error }) (principals.Principal, error) {
	var p principals.Principal
	var expiresAt, revokedAt, lastUsedAt sql.NullInt64
	m := pgtype.NewMap()
	err := row.Scan(
		&p.ID, &p.Name, &p.Description, &p.TokenPrefix, &p.TokenHash,
		m.SQLScanner(&p.Scopes), &p.TeamName, &p.CreatedBy, &p.CreatedAt,
		&expiresAt, &revokedAt, &lastUsedAt,
	)
	if err != nil {
		return principals.Principal{}, err
	}
	if expiresAt.Valid {
		p.ExpiresAt = &expiresAt.Int64
	}
	if revokedAt.Valid {
		p.RevokedAt = &revokedAt.Int64
	}
	if lastUsedAt.Valid {
		p.LastUsedAt = &lastUsedAt.Int64
	}
	return p, nil
}
```

- [ ] Run `ginkgo --focus="AgentPrincipalsFactory" ./atc/db/` — expect PASS.

- [ ] Write the failing handler tests `agent/api/principals/handler_test.go`:

```go
package principals_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func newHandler() (*principals.Handler, *principals.MemoryStore) {
	store := principals.NewMemoryStore()
	return principals.NewHandler(store, func(r *http.Request) string { return "test-admin" }), store
}

func TestCreatePrincipal(t *testing.T) {
	h, _ := newHandler()

	req := httptest.NewRequest("POST", "/api/v1/agent/principals",
		strings.NewReader(`{"name": "ci-agent-review", "description": "d", "scopes": ["reviews:write"]}`))
	rec := httptest.NewRecorder()
	h.CreatePrincipal(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	var resp struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Token       string `json:"token"`
		TokenPrefix string `json:"token_prefix"`
		CreatedBy   string `json:"created_by"`
		TeamName    string `json:"team_name"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Token, "cap1.") {
		t.Errorf("token = %q", resp.Token)
	}
	if resp.CreatedBy != "test-admin" {
		t.Errorf("created_by = %q, want server-derived test-admin", resp.CreatedBy)
	}
	if resp.TeamName != "main" {
		t.Errorf("team_name = %q, want default main", resp.TeamName)
	}
}

func TestCreatePrincipalRejectsBadSpecs(t *testing.T) {
	h, _ := newHandler()
	for name, body := range map[string]string{
		"bad json":      `{`,
		"missing name":  `{"scopes": ["reviews:write"]}`,
		"no scopes":     `{"name": "x"}`,
		"unknown scope": `{"name": "x", "scopes": ["reviews:read"]}`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/agent/principals", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreatePrincipal(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, rec.Code)
		}
	}
}

func TestListPrincipalsOmitsTokens(t *testing.T) {
	h, store := newHandler()
	_, token, err := store.Create(principals.CreateSpec{Name: "g", Scopes: []string{principals.ScopeCostsWrite}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/agent/principals", nil)
	rec := httptest.NewRecorder()
	h.ListPrincipals(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), principals.HashToken(token)) {
		t.Error("list response leaked token material")
	}
}

func TestRevokePrincipal(t *testing.T) {
	h, store := newHandler()
	created, _, err := store.Create(principals.CreateSpec{Name: "g", Scopes: []string{principals.ScopeCostsWrite}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/agent/principals/"+strconv.Itoa(created.ID), nil)
	req.Form = url.Values{":principal_id": {strconv.Itoa(created.ID)}}
	rec := httptest.NewRecorder()
	h.RevokePrincipal(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", rec.Code)
	}

	req = httptest.NewRequest("DELETE", "/api/v1/agent/principals/999", nil)
	req.Form = url.Values{":principal_id": {"999"}}
	rec = httptest.NewRecorder()
	h.RevokePrincipal(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id: code = %d, want 404", rec.Code)
	}
}
```

- [ ] Run `go test ./agent/api/principals/` — expect FAIL: `principals.NewHandler` undefined.

- [ ] Create `agent/api/principals/handler.go`:

```go
package principals

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// UserNameFunc derives the requesting admin's username (injected from
// the accessor by atc/api, mirroring reviews.BuildLookupFunc — this
// package never imports atc).
type UserNameFunc func(*http.Request) string

// Handler serves the admin principal-management API. Authentication and
// admin authorization are enforced by the wrappa layer (CheckAdminHandler,
// see atc/wrappa/api_auth_wrappa.go); the handler trusts the request.
type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// CreatedResponse is the POST response: the principal plus the full
// token, surfaced exactly once.
type CreatedResponse struct {
	Principal
	Token string `json:"token"`
}

// CreatePrincipal handles POST /api/v1/agent/principals.
func (h *Handler) CreatePrincipal(w http.ResponseWriter, r *http.Request) {
	var spec CreateSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if spec.TeamName == "" {
		spec.TeamName = "main"
	}
	spec.CreatedBy = h.userName(r)
	if err := spec.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p, token, err := h.store.Create(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreatedResponse{Principal: p, Token: token})
}

// ListPrincipals handles GET /api/v1/agent/principals.
func (h *Handler) ListPrincipals(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// RevokePrincipal handles DELETE /api/v1/agent/principals/:principal_id.
func (h *Handler) RevokePrincipal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.FormValue(":principal_id"))
	if err != nil {
		http.Error(w, "invalid principal_id", http.StatusBadRequest)
		return
	}

	found, err := h.store.Revoke(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "principal not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] Run `go test ./agent/api/principals/` — expect PASS.

- [ ] Add route constants in `atc/routes.go` after line 129 (`ListTeamAgentReviews = "ListTeamAgentReviews"`):

```go
	CreateAgentPrincipal = "CreateAgentPrincipal"
	ListAgentPrincipals  = "ListAgentPrincipals"
	RevokeAgentPrincipal = "RevokeAgentPrincipal"
```

and route entries after line 262 (`{Path: "/api/v1/teams/:team_name/agent-reviews", ...}`):

```go
	{Path: "/api/v1/agent/principals", Method: "POST", Name: CreateAgentPrincipal},
	{Path: "/api/v1/agent/principals", Method: "GET", Name: ListAgentPrincipals},
	{Path: "/api/v1/agent/principals/:principal_id", Method: "DELETE", Name: RevokeAgentPrincipal},
```

- [ ] Add the three routes to the admin case in `atc/wrappa/api_auth_wrappa.go:126` (after `atc.ListSharedForResourceType,`):

```go
		case atc.GetLogLevel,
			atc.DestroyTeam,
			atc.ListActiveUsersSince,
			atc.SetLogLevel,
			atc.GetInfoCreds,
			atc.SetWall,
			atc.ClearWall,
			atc.ClearResourceVersions,
			atc.ClearResourceTypeVersions,
			atc.ListSharedForResource,
			atc.ListSharedForResourceType,
			atc.CreateAgentPrincipal,
			atc.ListAgentPrincipals,
			atc.RevokeAgentPrincipal:
			newHandler = auth.CheckAdminHandler(handler, rejector)
```

- [ ] Add the three routes to the pass-through (leave-handler-as-is) case in `atc/wrappa/reject_archived_wrappa.go` after `atc.ListTeamAgentReviews,` (this switch panics `how do archived pipelines affect your endpoint?` on any unlisted route):

```go
			atc.ListTeamAgentReviews,
			atc.CreateAgentPrincipal,
			atc.ListAgentPrincipals,
			atc.RevokeAgentPrincipal,
```

- [ ] Add the three routes to the `EnableSystemAuditLog` case in `atc/auditor/auditor.go` `ValidateAction`, extending the case that ends with `atc.ListTeamAgentReviews:` (this switch panics `unhandled action: ...` on any unlisted route; `atc/auditor` `TestAuditor` fails the full suite otherwise):

```go
		atc.ListTeamAgentReviews,
		atc.CreateAgentPrincipal,
		atc.ListAgentPrincipals,
		atc.RevokeAgentPrincipal:
		return a.EnableSystemAuditLog
```

- [ ] Wire the server in `atc/api/handler.go`: add param after `reviewsStore reviewsapi.Store,` (line 91):

```go
	principalsStore principalsapi.Store,
```

with import `principalsapi "github.com/concourse/concourse/agent/api/principals"` and `"github.com/concourse/concourse/atc/api/accessor"`; construct after the `reviewsServer` block (line 139):

```go
	principalsServer := principalsapi.NewHandler(
		principalsStore,
		func(r *http.Request) string {
			return accessor.GetAccessor(r).Claims().UserName
		},
	)
```

and add handler-map entries after `atc.ListTeamAgentReviews` (line 277):

```go
		atc.CreateAgentPrincipal: http.HandlerFunc(principalsServer.CreatePrincipal),
		atc.ListAgentPrincipals:  http.HandlerFunc(principalsServer.ListPrincipals),
		atc.RevokeAgentPrincipal: http.HandlerFunc(principalsServer.RevokePrincipal),
```

- [ ] Update callers: in `atc/api/api_suite_test.go` add `principals.NewMemoryStore(),` between `reviews.NewMemoryStore(),` (line 226) and `"test-agent-review-publish-token",` (line 227), importing `"github.com/concourse/concourse/agent/api/principals"`. In `atc/atccmd/command.go` add `agentPrincipalsFactory := db.NewAgentPrincipalsFactory(dbConn)` next to the other factory constructions at line 2207, and pass `agentPrincipalsFactory,` between `db.NewAgentReviewsFactory(dbConn),` and `cmd.AgentReviewPublishToken,` (line 2298-2299).

- [ ] Run `go build ./... && ginkgo ./atc/wrappa/ ./atc/auditor/ && ginkgo ./atc/api/` — expect PASS (the wrappa "handles each route" spec exercises the new admin entries and panics `you missed a spot` on a missing auth case; the wrappa `RejectArchivedWrappa` and auditor `TestAuditor` specs iterate every route and panic on any unlisted one).

- [ ] Commit: `git add atc/db/agent_principals_factory.go atc/db/agent_principals_factory_test.go agent/api/principals/ atc/routes.go atc/wrappa/api_auth_wrappa.go atc/wrappa/reject_archived_wrappa.go atc/auditor/auditor.go atc/api/handler.go atc/api/api_suite_test.go atc/atccmd/command.go && git commit -m "feat(atc): agent principals factory + admin mint/list/revoke API"`

---

### Task 5: `principal(reviews:write)` auth tier with dual-accept on SubmitAgentReview

Replaces the wrappa pass-through at `atc/wrappa/api_auth_wrappa.go:112-113`. The static token keeps working through a legacy bypass (the reviews handler still validates it, exactly as today), attributed to `legacy-publish`. Principal-authenticated writes are attributed by name.

**Files:**
- Create: `atc/api/auth/check_agent_principal_handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go:11-30` (struct + constructor), `:112-113` (SubmitAgentReview case)
- Modify: `atc/wrappa/api_auth_wrappa_test.go:43` (constructor args)
- Modify: `agent/api/reviews/types.go:69-91` (StoredReview gains SubmittedBy), `:93-113` (comment only, no shape change)
- Modify: `agent/api/reviews/handler.go:69-79` (SubmitReview principal path)
- Modify: `atc/db/agent_reviews_factory.go:27-58` (Upsert), `:61-67` (reviewColumns), `:108-132` (scan)
- Modify: `atc/api/api_suite_test.go:174` (wrappa args), `atc/atccmd/command.go:218` (flag description), `:2239-2244` (wrappa args)
- Test: `atc/api/auth/check_agent_principal_handler_test.go`, `agent/api/reviews/handler_test.go`, `atc/db/agent_reviews_factory_test.go`

**Steps:**

- [ ] Write the failing auth-handler spec `atc/api/auth/check_agent_principal_handler_test.go` (Ginkgo, style of `check_admin_handler_test.go` — the server is wrapped in `accessor.NewHandler` so `GetAccessor` works):

```go
package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/auth/authfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAgentPrincipalHandler", func() {
	var (
		fakeRejector *authfakes.FakeRejector
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
		store        *principals.MemoryStore
		verifier     *principals.Verifier

		token       string
		seenName    string
		seenHasCtx  bool
		legacy      bool
		server      *httptest.Server
		client      *http.Client
	)

	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principals.FromContext(r.Context())
		seenHasCtx = ok
		seenName = p.Name
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "delegate")
	})

	BeforeEach(func() {
		fakeRejector = new(authfakes.FakeRejector)
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)
		seenName = ""
		seenHasCtx = false
		legacy = false

		fakeRejector.UnauthorizedStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusUnauthorized)
		}
		fakeRejector.ForbiddenStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "still nope", http.StatusForbidden)
		}

		store = principals.NewMemoryStore()
		verifier = principals.NewVerifier(store)

		var err error
		_, token, err = store.Create(principals.CreateSpec{
			Name: "itest-reviewer", Scopes: []string{principals.ScopeReviewsWrite},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		fakeAccessor.CreateReturns(fakeaccess, nil)

		factory := auth.NewCheckAgentPrincipalHandlerFactory(verifier)
		var inner http.Handler
		if legacy {
			inner = factory.HandlerForWithLegacyBypass(echoHandler, fakeRejector, principals.ScopeReviewsWrite)
		} else {
			inner = factory.HandlerFor(echoHandler, fakeRejector, principals.ScopeReviewsWrite)
		}

		server = httptest.NewServer(accessor.NewHandler(
			logger,
			"some-action",
			inner,
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		))
		client = &http.Client{Transport: &http.Transport{}}
	})

	AfterEach(func() {
		server.Close()
	})

	get := func(authorization string) *http.Response {
		req, err := http.NewRequest("GET", server.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("admits a valid principal token and places it in the context", func() {
		resp := get("Bearer " + token)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(seenHasCtx).To(BeTrue())
		Expect(seenName).To(Equal("itest-reviewer"))
	})

	It("rejects a tampered principal token with 401", func() {
		resp := get("Bearer " + token + "x")
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("rejects a valid token lacking the scope with 401", func() {
		_, wrongScope, err := store.Create(principals.CreateSpec{
			Name: "ticketer", Scopes: []string{principals.ScopeTicketsRead},
		})
		Expect(err).NotTo(HaveOccurred())
		resp := get("Bearer " + wrongScope)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	Context("without a principal token", func() {
		It("admits an admin user token without principal context", func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAdminReturns(true)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(seenHasCtx).To(BeFalse())
		})

		It("401s unauthenticated requests", func() {
			fakeaccess.IsAuthenticatedReturns(false)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("403s authenticated non-admins", func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAdminReturns(false)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})

		Context("with the legacy bypass (dual-accept window)", func() {
			BeforeEach(func() {
				legacy = true
			})

			It("passes non-cap1 bearer tokens through to the delegate", func() {
				fakeaccess.IsAuthenticatedReturns(false)
				resp := get("Bearer some-static-publish-token")
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				Expect(seenHasCtx).To(BeFalse())
			})

			It("still verifies cap1 tokens instead of bypassing", func() {
				resp := get("Bearer cap1.999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
				Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})
})
```

- [ ] Run `ginkgo ./atc/api/auth/` — expect FAIL: `auth.NewCheckAgentPrincipalHandlerFactory` undefined.

- [ ] Create `atc/api/auth/check_agent_principal_handler.go`:

```go
package auth

import (
	"net/http"
	"strings"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc/api/accessor"
)

// CheckAgentPrincipalHandlerFactory implements the principal(<scope>)
// auth tier (00-shared-contracts.md §4.1): a cap1.<id>.<secret> bearer
// token verified against agent_principals with a required scope. Admin
// user tokens are also accepted so fly curl debugging works. All
// principal verification failures are 401.
type CheckAgentPrincipalHandlerFactory interface {
	// HandlerFor is the strict tier: principal token or admin user.
	HandlerFor(delegate http.Handler, rejector Rejector, scope string) http.Handler
	// HandlerForWithLegacyBypass additionally passes requests without a
	// cap1 token through to the delegate, which validates the legacy
	// static publish token itself
	// (agent/api/reviews.Handler.SubmitReview). Dual-accept window
	// only — removed together with --agent-review-publish-token.
	HandlerForWithLegacyBypass(delegate http.Handler, rejector Rejector, scope string) http.Handler
}

func NewCheckAgentPrincipalHandlerFactory(verifier *principals.Verifier) CheckAgentPrincipalHandlerFactory {
	return &checkAgentPrincipalHandlerFactory{verifier: verifier}
}

type checkAgentPrincipalHandlerFactory struct {
	verifier *principals.Verifier
}

func (f *checkAgentPrincipalHandlerFactory) HandlerFor(delegate http.Handler, rejector Rejector, scope string) http.Handler {
	return checkAgentPrincipalHandler{
		verifier: f.verifier, delegate: delegate, rejector: rejector, scope: scope,
	}
}

func (f *checkAgentPrincipalHandlerFactory) HandlerForWithLegacyBypass(delegate http.Handler, rejector Rejector, scope string) http.Handler {
	return checkAgentPrincipalHandler{
		verifier: f.verifier, delegate: delegate, rejector: rejector, scope: scope,
		legacyBypass: true,
	}
}

type checkAgentPrincipalHandler struct {
	verifier     *principals.Verifier
	delegate     http.Handler
	rejector     Rejector
	scope        string
	legacyBypass bool
}

func (h checkAgentPrincipalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	if strings.HasPrefix(bearer, principals.TokenVersionPrefix) {
		p, err := h.verifier.Verify(bearer, h.scope)
		if err != nil {
			h.rejector.Unauthorized(w, r)
			return
		}
		h.delegate.ServeHTTP(w, r.WithContext(principals.NewContext(r.Context(), p)))
		return
	}

	acc := accessor.GetAccessor(r)
	if acc.IsAuthenticated() && acc.IsAdmin() {
		h.delegate.ServeHTTP(w, r)
		return
	}

	if h.legacyBypass {
		// Dual-accept window: the delegate validates the static publish
		// token itself and attributes the write to 'legacy-publish'.
		h.delegate.ServeHTTP(w, r)
		return
	}

	if !acc.IsAuthenticated() {
		h.rejector.Unauthorized(w, r)
	} else {
		h.rejector.Forbidden(w, r)
	}
}
```

- [ ] Run `ginkgo ./atc/api/auth/` — expect PASS.

- [ ] Write the failing reviews-handler tests — append to `agent/api/reviews/handler_test.go`:

```go
func TestSubmitWithPrincipalContextSkipsStaticToken(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	// no Authorization header at all — the wrappa already verified the principal
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{
		ID: 3, Name: "itest-reviewer",
	}))
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	recs, _ := store.GetByBuild(42)
	if len(recs) != 1 || recs[0].SubmittedBy != "itest-reviewer" {
		t.Errorf("submitted_by = %+v, want itest-reviewer", recs)
	}
}

func TestSubmitWithStaticTokenAttributesLegacyPublish(t *testing.T) {
	h, store, _ := newHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/agent/reviews", strings.NewReader(postBody()))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.SubmitReview(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	recs, _ := store.GetByBuild(42)
	if len(recs) != 1 || recs[0].SubmittedBy != principals.LegacyPublishPrincipalName {
		t.Errorf("submitted_by = %+v, want legacy-publish", recs)
	}
}
```

with `"github.com/concourse/concourse/agent/api/principals"` added to the test imports.

- [ ] Run `go test ./agent/api/reviews/` — expect FAIL: `SubmittedBy` undefined.

- [ ] Add the field to `StoredReview` in `agent/api/reviews/types.go` after `DurationSeconds` (line 85):

```go
	// SubmittedBy is the writing principal's name (audit-attribution
	// convention): the verified agent principal, or 'legacy-publish'
	// for static-token writes during the dual-accept window. Filled by
	// the handler, never by ToStoredReview.
	SubmittedBy string `json:"submitted_by"`
```

- [ ] Rework the auth block of `SubmitReview` in `agent/api/reviews/handler.go:69-79`:

```go
// SubmitReview handles POST /api/v1/agent/reviews.
//
// Auth: the wrappa wraps this route in principal(reviews:write) with a
// legacy bypass (atc/wrappa/api_auth_wrappa.go). A verified principal
// arrives via the request context; anything else falls back to the
// static publish token this handler has always validated (dual-accept
// window; removed with --agent-review-publish-token).
func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	submittedBy := ""
	if p, ok := principals.FromContext(r.Context()); ok {
		submittedBy = p.Name
	} else {
		if h.publishToken == "" {
			http.Error(w, "agent review publishing is not enabled", http.StatusForbidden)
			return
		}
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.publishToken)) != 1 {
			http.Error(w, "invalid publish token", http.StatusUnauthorized)
			return
		}
		submittedBy = principals.LegacyPublishPrincipalName
	}
```

and where the record is stored (currently `handler.go:107`):

```go
	rec := sub.ToStoredReview(buildCtx)
	rec.SubmittedBy = submittedBy
	if err := h.store.Upsert(rec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
```

Add `"github.com/concourse/concourse/agent/api/principals"` to the imports.

- [ ] Run `go test ./agent/api/reviews/` — expect PASS (existing static-token tests unchanged: no principal in context → static path).

- [ ] Persist the column — write the failing factory assertion first: in `atc/db/agent_reviews_factory_test.go`, set `SubmittedBy: "itest-reviewer",` inside the `rec` helper struct literal and add to the round-trip spec's expectations:

```go
		Expect(got[0].SubmittedBy).To(Equal("itest-reviewer"))
```

(Adapt to the existing spec that Upserts then reads back — add the assertion right after the existing field expectations.) Run `ginkgo --focus="AgentReviewsFactory" ./atc/db/` — expect FAIL (scan returns empty string).

- [ ] Update `atc/db/agent_reviews_factory.go`: add `"submitted_by"` to the `Upsert` Columns list (after `"duration_seconds"`, line 32) and `rec.SubmittedBy` to Values (after `rec.DurationSeconds`, line 38); add `submitted_by = EXCLUDED.submitted_by,` to the ON CONFLICT SET list (line 40-55); add `r.submitted_by` to `reviewColumns` after `r.duration_seconds` (line 64) and `&rec.SubmittedBy` after `&rec.DurationSeconds` in `scanReviewRows` (line 117). Run `ginkgo --focus="AgentReviewsFactory" ./atc/db/` — expect PASS.

- [ ] Rewire the wrappa. In `atc/wrappa/api_auth_wrappa.go`: extend the struct (line 11-16) and constructor (line 18-30) with a fifth field/param `checkAgentPrincipalHandlerFactory auth.CheckAgentPrincipalHandlerFactory`, and replace the pass-through case (lines 106-113):

```go
		// principal(reviews:write) — 00-shared-contracts.md §4.1. The
		// legacy static publish token is still accepted inside the
		// handler during the dual-accept window, so requests without a
		// cap1 token bypass to the delegate instead of being rejected.
		case atc.SubmitAgentReview:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerForWithLegacyBypass(
				handler, rejector, principals.ScopeReviewsWrite)
```

with import `"github.com/concourse/concourse/agent/api/principals"`.

- [ ] Update the three `NewAPIAuthWrappa` callers:
  - `atc/atccmd/command.go:2239-2244`: build `auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(agentPrincipalsFactory))` next to the other handler factories (line 2207-2211, reusing the `agentPrincipalsFactory` from Task 4 — move its construction above this block) and pass it as the fifth argument. Import `"github.com/concourse/concourse/agent/api/principals"`.
  - `atc/api/api_suite_test.go:174`: share one store — replace the inline `principals.NewMemoryStore()` NewHandler arg from Task 4 with a `principalsStore := principals.NewMemoryStore()` variable defined before the wrappa, pass `auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principalsStore))` to `wrappa.NewAPIAuthWrappa` and `principalsStore` to `api.NewHandler`.
  - `atc/wrappa/api_auth_wrappa_test.go:43`: pass `auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principals.NewMemoryStore()))` as the fifth argument, importing `"github.com/concourse/concourse/agent/api/principals"`.

- [ ] Mark the static-token flag deprecated in `atc/atccmd/command.go:218`:

```go
	AgentReviewPublishToken string `long:"agent-review-publish-token" description:"DEPRECATED: static bearer token accepted for publishing agent review results during the agent-principal dual-accept window. Mint a reviews:write agent principal instead (POST /api/v1/agent/principals). This flag will be removed at the end of the window."`
```

- [ ] Run `go build ./... && ginkgo ./atc/wrappa/ ./atc/api/auth/ && go test ./agent/api/reviews/ && ginkgo ./atc/api/` — expect PASS.

- [ ] Commit: `git add atc/api/auth/ atc/wrappa/ agent/api/reviews/ atc/db/agent_reviews_factory.go atc/db/agent_reviews_factory_test.go atc/api/ atc/atccmd/command.go && git commit -m "feat(atc): principal(reviews:write) auth tier with dual-accept for SubmitAgentReview"`

---

### Task 6: `CheckAgentAuthorizationHandler` — main-team authorization for team-less agent routes

Contracts §4.2 closing paragraph / decision 21, owned by agent-identity: `CheckAuthorizationHandler` reads the team from the `:team_name` URL param (`atc/api/auth/check_authorization_handler.go:32`); team-less paths yield `IsAuthorized("")` which reduces to `isAdmin` (`atc/api/accessor/accessor.go:161-163`), so the five agent feedback routes are silently admin-only and their `DefaultRoles` entries (`atc/api/accessor/roles.go:108-112`) are dead. Hardcode team `main`.

**Files:**
- Create: `atc/api/auth/check_agent_authorization_handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go:169-173` (move five routes out of the authorized case)
- Modify: `atc/api/accessor/roles.go:102-107` (comment update)
- Test: `atc/api/auth/check_agent_authorization_handler_test.go`

**Steps:**

- [ ] Write the failing spec `atc/api/auth/check_agent_authorization_handler_test.go`:

```go
package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/auth/authfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAgentAuthorizationHandler", func() {
	var (
		fakeRejector *authfakes.FakeRejector
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
		server       *httptest.Server
		client       *http.Client
	)

	simpleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "agent route")
	})

	BeforeEach(func() {
		fakeRejector = new(authfakes.FakeRejector)
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)

		fakeRejector.UnauthorizedStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusUnauthorized)
		}
		fakeRejector.ForbiddenStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "still nope", http.StatusForbidden)
		}

		server = httptest.NewServer(accessor.NewHandler(
			logger,
			"some-action",
			auth.CheckAgentAuthorizationHandler(simpleHandler, fakeRejector),
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		))
		client = &http.Client{Transport: &http.Transport{}}
	})

	AfterEach(func() {
		server.Close()
	})

	JustBeforeEach(func() {
		fakeAccessor.CreateReturns(fakeaccess, nil)
	})

	get := func() *http.Response {
		// team-less path — no :team_name param anywhere
		resp, err := client.Get(server.URL + "/api/v1/agent/feedback")
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("401s unauthenticated requests", func() {
		fakeaccess.IsAuthenticatedReturns(false)
		Expect(get().StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("authorizes against the main team, not the empty team", func() {
		fakeaccess.IsAuthenticatedReturns(true)
		fakeaccess.IsAuthorizedReturns(true)

		Expect(get().StatusCode).To(Equal(http.StatusOK))
		Expect(fakeaccess.IsAuthorizedCallCount()).To(Equal(1))
		Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal("main"))
	})

	It("403s main-team-unauthorized users", func() {
		fakeaccess.IsAuthenticatedReturns(true)
		fakeaccess.IsAuthorizedReturns(false)
		Expect(get().StatusCode).To(Equal(http.StatusForbidden))
	})
})
```

- [ ] Run `ginkgo ./atc/api/auth/` — expect FAIL: `auth.CheckAgentAuthorizationHandler` undefined.

- [ ] Create `atc/api/auth/check_agent_authorization_handler.go`:

```go
package auth

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
)

// CheckAgentAuthorizationHandler authorizes team-less /api/v1/agent/*
// routes against the main team (00-shared-contracts.md §4.2, decision
// 21). CheckAuthorizationHandler reads the team from the :team_name URL
// param; on team-less paths that yields IsAuthorized(""), which reduces
// to isAdmin — making such routes silently admin-only and their
// accessor DefaultRoles entries dead. Hardcoding atc.DefaultTeamName
// makes those entries effective.
func CheckAgentAuthorizationHandler(
	handler http.Handler,
	rejector Rejector,
) http.Handler {
	return checkAgentAuthorizationHandler{
		handler:  handler,
		rejector: rejector,
	}
}

type checkAgentAuthorizationHandler struct {
	handler  http.Handler
	rejector Rejector
}

func (h checkAgentAuthorizationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	acc := accessor.GetAccessor(r)

	if !acc.IsAuthenticated() {
		h.rejector.Unauthorized(w, r)
		return
	}

	if !acc.IsAuthorized(atc.DefaultTeamName) {
		h.rejector.Forbidden(w, r)
		return
	}

	h.handler.ServeHTTP(w, r)
}
```

- [ ] Run `ginkgo ./atc/api/auth/` — expect PASS.

- [ ] Move the five routes in `atc/wrappa/api_auth_wrappa.go`: delete `atc.SubmitAgentFeedback, atc.GetAgentFeedback, atc.GetAgentFeedbackSummary, atc.ClassifyAgentVerdict, atc.GetAgentReviewFindings,` from the `CheckAuthorizationHandler` case (lines 169-173) and add a new case immediately after it:

```go
		// team-less /api/v1/agent/* routes: authorized against the main
		// team via their accessor DefaultRoles entries (decision 21 in
		// docs/superpowers/plans/agentic-platform/00-shared-contracts.md)
		case atc.SubmitAgentFeedback,
			atc.GetAgentFeedback,
			atc.GetAgentFeedbackSummary,
			atc.ClassifyAgentVerdict,
			atc.GetAgentReviewFindings:
			newHandler = auth.CheckAgentAuthorizationHandler(handler, rejector)
```

(`atc.ListTeamAgentReviews` stays in the `CheckAuthorizationHandler` case — its path carries `:team_name`.)

- [ ] Update the comment above the agent entries in `atc/api/accessor/roles.go:102-107` to reflect reality:

```go
	// Agent review/feedback routes. Team-less /api/v1/agent/* routes are
	// wrapped in CheckAgentAuthorizationHandler, which authorizes against
	// the main team using these entries. Every route in that wrappa case
	// (and every :team_name-scoped route in CheckAuthorizationHandler)
	// needs an entry here: a missing entry resolves to requiredRole ""
	// and hasRequiredRole's default case, making the route admin-only.
```

- [ ] Run `ginkgo ./atc/wrappa/ ./atc/api/auth/ && ginkgo ./atc/api/` — expect PASS.

- [ ] Commit: `git add atc/api/auth/ atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go && git commit -m "fix(atc): authorize team-less agent routes against main team (contracts decision 21)"`

---

### Task 7: Integration spec — principal lifecycle end-to-end

Real ATC + real Postgres via the existing `atc/integration` suite (`make test-integration`). Note: revocation is asserted on a **never-used** principal — the verifier's 60s cache means a revoked-after-use token stays valid briefly (documented contract behavior), which a fast test must not race.

**Files:**
- Create: `atc/integration/agent_principals_test.go`
- Test: the file itself (reuses `postAgentReview` from `atc/integration/agent_reviews_test.go:136` and `login`/`setupTeam` from `integration_suite_test.go:105,123`)

**Steps:**

- [ ] Write the spec `atc/integration/agent_principals_test.go`:

```go
package integration_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const staticPublishTokenForPrincipalTest = "integration-static-token"

var _ = Describe("Agent Principals API", func() {
	BeforeEach(func() {
		cmd.AgentReviewPublishToken = staticPublishTokenForPrincipalTest
	})

	reviewBodyFor := func(buildID int, commit string) []byte {
		return []byte(`{
			"build_id": ` + strconv.Itoa(buildID) + `,
			"review": {
				"schema_version": "1.0.0",
				"metadata": {"repo": "itest", "commit": "` + commit + `"},
				"score": {"value": 8, "max": 10, "pass": true},
				"proven_issues": [],
				"observations": [],
				"summary": "clean"
			}
		}`)
	}

	mintPrincipal := func(httpClient *http.Client, body string) (*http.Response, map[string]any) {
		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/principals", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		var decoded map[string]any
		json.NewDecoder(resp.Body).Decode(&decoded)
		return resp, decoded
	}

	It("mints, uses, attributes, and revokes scoped principals", func() {
		client := login(atcURL, "test", "test")
		httpClient := client.HTTPClient()

		By("minting a reviews:write principal as admin")
		resp, created := mintPrincipal(httpClient,
			`{"name": "itest-reviewer", "description": "integration", "scopes": ["reviews:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		token, _ := created["token"].(string)
		Expect(token).To(HavePrefix("cap1."))
		Expect(created["token_prefix"]).To(Equal(token[:12]))

		By("publishing a review with the principal token")
		build, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		pub := postAgentReview(atcURL, token, reviewBodyFor(build.ID, "cafe0001"))
		Expect(pub.StatusCode).To(Equal(http.StatusCreated))

		By("recording the writing principal on the review row")
		req, err := http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())
		getResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer getResp.Body.Close()
		var reviews []map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(HaveLen(1))
		Expect(reviews[0]["submitted_by"]).To(Equal("itest-reviewer"))

		By("still accepting the static token during the dual-accept window, attributed to legacy-publish")
		build2, err := client.Team("main").CreateBuild(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		pub = postAgentReview(atcURL, staticPublishTokenForPrincipalTest, reviewBodyFor(build2.ID, "cafe0002"))
		Expect(pub.StatusCode).To(Equal(http.StatusCreated))

		req, err = http.NewRequest("GET", atcURL+"/api/v1/builds/"+strconv.Itoa(build2.ID)+"/agent-reviews", nil)
		Expect(err).NotTo(HaveOccurred())
		getResp2, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer getResp2.Body.Close()
		reviews = nil
		Expect(json.NewDecoder(getResp2.Body).Decode(&reviews)).To(Succeed())
		Expect(reviews).To(HaveLen(1))
		Expect(reviews[0]["submitted_by"]).To(Equal("legacy-publish"))

		By("rejecting a wrong-scope principal with 401")
		resp, wrongScope := mintPrincipal(httpClient,
			`{"name": "itest-ticketer", "scopes": ["tickets:read"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		pub = postAgentReview(atcURL, wrongScope["token"].(string), reviewBodyFor(build.ID, "cafe0003"))
		Expect(pub.StatusCode).To(Equal(http.StatusUnauthorized))

		By("rejecting a principal revoked before first use")
		resp, doomed := mintPrincipal(httpClient,
			`{"name": "itest-doomed", "scopes": ["reviews:write"]}`)
		Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		doomedID := strconv.Itoa(int(doomed["id"].(float64)))

		req, err = http.NewRequest("DELETE", atcURL+"/api/v1/agent/principals/"+doomedID, nil)
		Expect(err).NotTo(HaveOccurred())
		delResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		delResp.Body.Close()
		Expect(delResp.StatusCode).To(Equal(http.StatusNoContent))

		pub = postAgentReview(atcURL, doomed["token"].(string), reviewBodyFor(build.ID, "cafe0004"))
		Expect(pub.StatusCode).To(Equal(http.StatusUnauthorized))

		By("listing principals with revocation state and without token material")
		req, err = http.NewRequest("GET", atcURL+"/api/v1/agent/principals", nil)
		Expect(err).NotTo(HaveOccurred())
		listResp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer listResp.Body.Close()
		var list []map[string]any
		Expect(json.NewDecoder(listResp.Body).Decode(&list)).To(Succeed())

		byName := map[string]map[string]any{}
		for _, p := range list {
			byName[p["name"].(string)] = p
		}
		Expect(byName).To(HaveKey("legacy-publish"))
		Expect(byName["itest-doomed"]["revoked_at"]).NotTo(BeNil())
		Expect(byName["itest-reviewer"]["last_used_at"]).NotTo(BeNil())
		Expect(byName["itest-reviewer"]).NotTo(HaveKey("token"))
	})

	It("rejects principal minting by non-admins", func() {
		setupTeam(atcURL, atc.Team{
			Name: "some-team",
			Auth: atc.TeamAuth{
				"viewer": map[string][]string{
					"users":  []string{"local:v-user"},
					"groups": []string{},
				},
			},
		})
		nonAdmin := login(atcURL, "v-user", "v-user")

		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/principals",
			strings.NewReader(`{"name": "sneaky", "scopes": ["reviews:write"]}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		resp, err := nonAdmin.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})
})
```

- [ ] Run `make test-integration` — expect the two new specs to FAIL only if wiring from Tasks 4-5 is broken; otherwise PASS. If a spec fails, debug the wiring (do not weaken assertions).

- [ ] Run the full unit tier to catch cross-package fallout: `make test-quick` — expect PASS.

- [ ] Commit: `git add atc/integration/agent_principals_test.go && git commit -m "test(integration): agent principal mint/use/attribute/revoke lifecycle"`

---

### Task 8: theborg/cicd cutover + static-token removal

Two phases. Phase A is a live runbook (no repo changes except the deploy pipeline note); Phase B removes the static token from the codebase and is **gated on Phase A's dual-running verification** — do not start Phase B in the same sitting unless the human explicitly signs off.

The live publisher is the `agent-review` job in `deploy/concourse-pipeline.yml:66` (`AGENT_REVIEW_PUBLISH_TOKEN: ((agent-review-publish-token))` → `ci-agent publish` reads `AGENT_REVIEW_PUBLISH_TOKEN`, `ci-agent/cmd/ci-agent/publish.go:22`, and sends it as an opaque `Bearer` token). Because the token is opaque to ci-agent, the cutover is **pure config**: swap the secret value to a minted `cap1.` token. No ci-agent code change.

**Files:**
- Modify (Phase B): `atc/atccmd/command.go:218` (delete flag), `:2299` (drop arg)
- Modify (Phase B): `atc/api/handler.go:92,138` (drop `agentReviewPublishToken` param and pass-through)
- Modify (Phase B): `agent/api/reviews/handler.go:20-34,69-95` (drop `publishToken` field/param and the static path)
- Modify (Phase B): `agent/api/reviews/handler_test.go` (auth tests move to principal-context semantics)
- Modify (Phase B): `atc/api/auth/check_agent_principal_handler.go` (delete `HandlerForWithLegacyBypass` + `legacyBypass`), `atc/api/auth/check_agent_principal_handler_test.go` (delete bypass context)
- Modify (Phase B): `atc/wrappa/api_auth_wrappa.go` (`HandlerForWithLegacyBypass` → `HandlerFor`)
- Modify (Phase B): `atc/api/api_suite_test.go:227` (drop token arg)
- Modify (Phase B): `atc/integration/agent_reviews_test.go:15-51` (publish via minted principal instead of static token), `atc/integration/agent_principals_test.go` (drop the dual-accept `By` block and `cmd.AgentReviewPublishToken` lines)

**Steps — Phase A (live runbook, theborg kube-context, namespace `cicd`):**

- [ ] Confirm the running web image contains this workstream's commits (pattern from memory `reference_theborg_cicd_live_concourse.md`):

```bash
kubectl --context theborg -n cicd exec deploy/concourse-web -c concourse-web -- \
  sh -c "grep -a -o 'vcs.revision=[0-9a-f]*' /usr/local/concourse/bin/concourse | head -1"
git merge-base --is-ancestor <task-5-commit-sha> <build-commit> && echo OK
```

If not, wait for the jetbridge release pipeline to build/deploy first (`project_jetbridge_release_pipeline.md`).

- [ ] Mint the publisher principal as admin (`fly -t home login` first; creds are in the web deploy args — read them with `kubectl -n cicd get pod ... -o jsonpath`, never hardcode):

```bash
fly -t home curl /api/v1/agent/principals -- -X POST \
  -H 'Content-Type: application/json' \
  -d '{"name": "ci-agent-review", "description": "theborg cicd agent-review publisher", "scopes": ["reviews:write"]}'
```

Save the returned `token` (`cap1.<id>....`) — it is shown exactly once.

- [ ] Locate and update the secret backing `((agent-review-publish-token))` (per `project_agent_review_presentation.md` it lives where the in-cluster credential manager resolves main-team vars — verify, don't assume):

```bash
kubectl --context theborg get secret --all-namespaces | grep agent-review-publish-token
kubectl --context theborg -n <found-ns> create secret generic agent-review-publish-token \
  --from-literal=value='cap1.<id>.<secret>' --dry-run=client -o yaml | kubectl --context theborg apply -f -
```

Leave the web's `CONCOURSE_AGENT_REVIEW_PUBLISH_TOKEN` env **in place** — that is the dual-accept window.

- [ ] Trigger a review and verify the principal path end-to-end:

```bash
fly -t home trigger-job -j <pipeline>/agent-review -w   # find pipeline name via: fly -t home pipelines
fly -t home curl /api/v1/agent/principals               # ci-agent-review has last_used_at set
fly -t home curl /api/v1/teams/main/agent-reviews       # newest row present
fly -t home curl /api/v1/builds/<build-id>/agent-reviews  # submitted_by == "ci-agent-review"
```

- [ ] Dual-running verification period: leave both paths configured for at least several pushes/days; confirm no publish failures in agent-review job logs (`WARNING: failed to publish review to ATC` is the failure signature, `deploy/concourse-pipeline.yml:106`).

- [ ] **GATE: human sign-off that dual-running is verified and the static token may be removed.** Record the sign-off date in the commit message of Phase B.

**Steps — Phase B (static-token removal, only after the gate):**

- [ ] Write the failing test first — in `atc/integration/agent_reviews_test.go`: delete the `const agentReviewPublishTokenForTest` (line 15) and the `BeforeEach` that sets `cmd.AgentReviewPublishToken` (lines 18-20), then mint a principal at the top of the first spec and publish with it:

```go
	It("accepts a published review, rejects a bad token, and serves it back by build and by team", func() {
		client := login(atcURL, "test", "test")

		By("minting a reviews:write principal to publish with")
		req, err := http.NewRequest("POST", atcURL+"/api/v1/agent/principals",
			strings.NewReader(`{"name": "itest-publisher", "scopes": ["reviews:write"]}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		mintResp, err := client.HTTPClient().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer mintResp.Body.Close()
		Expect(mintResp.StatusCode).To(Equal(http.StatusCreated))
		var minted struct {
			Token string `json:"token"`
		}
		Expect(json.NewDecoder(mintResp.Body).Decode(&minted)).To(Succeed())
```

(add `"strings"` to the imports), replace the two `postAgentReview(atcURL, agentReviewPublishTokenForTest, ...)` calls with `postAgentReview(atcURL, minted.Token, ...)`, and change the "rejecting a submission with the wrong bearer token" block to use `postAgentReview(atcURL, "cap1.999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", reviewBody)` (still expects 401). In `atc/integration/agent_principals_test.go`, delete the `const staticPublishTokenForPrincipalTest`, the `BeforeEach`, and the "still accepting the static token during the dual-accept window" `By` block (including its build2 setup and assertions).

- [ ] Run `make test-integration` — expect FAIL: `cmd.AgentReviewPublishToken` still exists and non-cap1 bearer tokens still bypass to the handler (the static path still answers). This proves the removal is observable.

- [ ] Remove the static path from `agent/api/reviews/handler.go`: delete the `publishToken` struct field (line 24) and constructor param (line 27-34, signature becomes `NewHandler(store Store, feedbackStore feedback.Store, lookup BuildLookupFunc) *Handler`); replace `SubmitReview`'s auth block with:

```go
// SubmitReview handles POST /api/v1/agent/reviews.
//
// Auth is enforced by the wrappa principal(reviews:write) tier
// (atc/wrappa/api_auth_wrappa.go). A verified principal arrives via the
// request context; its absence means an admin user token (the tier's
// debug path), attributed as the admin has no principal name.
func (h *Handler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	submittedBy := ""
	if p, ok := principals.FromContext(r.Context()); ok {
		submittedBy = p.Name
	}
```

Drop the now-unused `crypto/subtle` and `strings` imports.

- [ ] Update `agent/api/reviews/handler_test.go`: `newHandler` drops the token arg; delete `TestSubmitRequiresToken` and `TestSubmitWithStaticTokenAttributesLegacyPublish`; keep `TestSubmitWithPrincipalContextSkipsStaticToken` (rename to `TestSubmitRecordsPrincipalName`). Run `go test ./agent/api/reviews/` — PASS.

- [ ] Remove the bypass tier: in `atc/api/auth/check_agent_principal_handler.go` delete `HandlerForWithLegacyBypass` from the interface and implementation and the `legacyBypass` field/branch; in `atc/wrappa/api_auth_wrappa.go` change the `SubmitAgentReview` case to `HandlerFor(handler, rejector, principals.ScopeReviewsWrite)` and update its comment (static token gone); delete the legacy-bypass `Context` from `check_agent_principal_handler_test.go`. Run `ginkgo ./atc/api/auth/ ./atc/wrappa/` — PASS.

- [ ] Remove the flag and plumbing: delete `AgentReviewPublishToken` from `atc/atccmd/command.go:218` and its arg at `:2299`; delete the `agentReviewPublishToken string` param from `atc/api/handler.go:92` and its use at `:138`; delete `"test-agent-review-publish-token",` from `atc/api/api_suite_test.go:227`. Run `go build ./... && ginkgo ./atc/api/` — PASS.

- [ ] Run `make test-quick && make test-integration` — expect PASS.

- [ ] Commit: `git add -A && git commit -m "feat(atc)!: remove static agent-review publish token (dual-accept window closed <sign-off date>)"`

- [ ] Live cleanup on theborg: remove the `CONCOURSE_AGENT_REVIEW_PUBLISH_TOKEN` env from the `concourse-web` deployment (edit the deploy manifest source, or `kubectl --context theborg -n cicd edit deploy concourse-web`), wait for rollout, and confirm the next agent-review job still publishes (the `((agent-review-publish-token))` secret now carries the `cap1.` token and keeps working untouched). Trigger one review job and re-verify `submitted_by == "ci-agent-review"`.

---

## Execution notes

**Full workstream test suite (in dependency order):**

```bash
pg_isready                                   # PostgreSQL required for db/migration/integration tiers
go test ./agent/api/principals/ ./agent/api/reviews/
ginkgo ./atc/api/auth/
ginkgo ./atc/wrappa/
ginkgo --focus="AgentPrincipalsFactory" ./atc/db/     # full suite: ginkgo ./atc/db/ (~90s, template DB)
ginkgo --focus="Legacy Database Upgrade" ./atc/db/migration/
ginkgo ./atc/api/
make test-integration                        # ~12s, includes the new agent_principals specs
make test-quick                              # final gate before any cutover step
```

Never use `--race` (parallel compilation failures per CLAUDE.md). If `atc/db` reports `database "testdb_template" already exists`, another test process is running — wait or kill it.

**Live-test requirements (Task 8 Phase A):** theborg kube-context, namespace `cicd` (the live deployment — do NOT create throwaway namespaces for this task; the cutover is against the real publisher). Follow `reference_theborg_cicd_live_concourse.md`: verify the web binary's `vcs.revision` is an ancestor-descendant match before trusting behavior; fly target `home` (`http://concourse.home`); read admin creds from the deploy args. The dual-accept window requires real elapsed time — Phase B is explicitly gated on human sign-off.

**Merge-conflict expectation:** every wave-1 workstream adding migrations bumps `jetbridgeHeadMigration` in `atc/db/migration/legacy_upgrade_test.go:37`. On conflict, resolve to the highest migration number present in `atc/db/migration/migrations/`.

**Rollback notes for the risky diffs:**
- *Migrations:* both have exact down migrations (`DROP TABLE agent_principals` / `DROP COLUMN submitted_by`). The `legacy-publish` backfill row is data, dropped with the table.
- *Task 5 (SubmitAgentReview tier change):* behavior-preserving for static-token clients by construction — the legacy bypass delegates to the exact same handler-side check that exists today. If the live publisher breaks anyway, revert the wrappa case to the pass-through (`case atc.SubmitAgentReview:` no-op) — the handler still validates the static token until Phase B, so a one-line wrappa revert fully restores the old path.
- *Task 6 (feedback-route authorization):* this *loosens* access from silently-admin-only to main-team DefaultRoles (`viewer`/`member`) — the documented intent of decision 21. If that is ever unwanted, revert the wrappa case move; the handler itself is additive.
- *Task 8 Phase B:* revert the single removal commit to restore dual-accept; the theborg secret keeps the `cap1.` token either way, so reverting code never breaks the live publisher.
- *Revocation latency:* the verifier's 60s cache means a revoked principal may be accepted for up to 60s per web node. For emergency revocation, restart the web deployment after revoking (`kubectl -n cicd rollout restart deploy/concourse-web`).
