package principals

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Scope vocabulary — closed set (00-shared-contracts.md §4.1). Adding a
// scope requires agent-identity sign-off; update
// docs/superpowers/plans/agentic-platform/agent-route-scopes.md in the
// same change.
// reviews:write, metrics:write and costs:write were removed with the HTTP
// publishing routes they guarded (POST /api/v1/agent/{reviews,metrics,costs}):
// reviews, metrics and ledger rows are written in-process by the agent step,
// so no scope can authorize a write that no longer has a door.
const (
	ScopeTicketsRead  = "tickets:read"
	ScopeTicketsWrite = "tickets:write"
)

// ValidScopes is the closed scope set.
var ValidScopes = map[string]bool{
	ScopeTicketsRead:  true,
	ScopeTicketsWrite: true,
}

// Principal kinds (ticket #44) distinguish operator-managed principals
// from the dispatcher's ephemeral per-run credentials on list surfaces
// (web /agent Principals table, `fly agent principals list`). Kind is
// derived read-side by DeriveKind and is never persisted — the
// agent_principals table has no kind/metadata column, and the mint path
// (agent/dispatch.attachRunSecret) is intentionally left unchanged.
const (
	KindOperator = "operator"
	KindRun      = "run"
)

// runPrincipalName matches the dispatcher's per-run principal naming
// convention, "agent-run-<runID>" (digits only). This is the documented
// agent-run-<digits> prefix match (ticket #44) used to derive Kind when only
// the name is available. Principals are unrelated to model credentials, which
// have no per-run secret any more.
var runPrincipalName = regexp.MustCompile(`^agent-run-\d+$`)

// DeriveKind classifies a principal by name: KindRun for the dispatcher's
// ephemeral per-run principals (agent-run-<id>, identical scope sets, 6h
// expiry), KindOperator for everything else.
func DeriveKind(name string) string {
	if runPrincipalName.MatchString(name) {
		return KindRun
	}
	return KindOperator
}

// Principal is one agent_principals row. Timestamps are Unix epoch
// seconds in JSON (repo convention, matching agent_reviews). TokenHash
// is needed for verification but must never serialize. Kind is derived
// read-side (see DeriveKind) rather than stored.
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
	Kind        string   `json:"kind,omitempty"`
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
