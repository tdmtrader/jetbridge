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
	ScopeReviewsWrite = "reviews:write"
	ScopeTicketsRead  = "tickets:read"
	ScopeTicketsWrite = "tickets:write"
	ScopeMetricsWrite = "metrics:write"
	ScopeCostsWrite   = "costs:write"
)

// ValidScopes is the closed scope set.
var ValidScopes = map[string]bool{
	ScopeReviewsWrite: true,
	ScopeTicketsRead:  true,
	ScopeTicketsWrite: true,
	ScopeMetricsWrite: true,
	ScopeCostsWrite:   true,
}

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
