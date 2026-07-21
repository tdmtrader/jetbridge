// Package credentials owns the per-user Anthropic credential vault: the
// domain types and Store contract (implemented by atc/db), the HTTP
// handler seam, and the K8s secret helpers (ephemeral per-run secret and
// long-lived platform-credential secret) that dispatch and the gateway
// consume. Contract: docs/superpowers/plans/agentic-platform/
// 00-shared-contracts.md §1.3, §2.6, §8.2, §1.13.
package credentials

import (
	"context"
	"fmt"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

const (
	KindAnthropicOAuth  = "anthropic_oauth"
	KindAnthropicAPIKey = "anthropic_api_key"
)

// The §1.13 platform service user, seeded by migration 1773106022. It
// never logs in; admins vault its credential via `fly agent auth
// --platform` (PutRequest.User = PlatformUserName). Its credential funds
// platform-initiated LLM work (harvest judge, retrospective, calibration).
const (
	PlatformUserSub  = "agent-platform"
	PlatformUserName = "platform"
)

// ValidKind reports whether kind is accepted by the
// agent_user_credentials CHECK constraint.
func ValidKind(kind string) bool {
	return kind == KindAnthropicOAuth || kind == KindAnthropicAPIKey
}

// Credential never carries the decrypted token in API responses;
// Token is populated only by Store.Resolve for dispatch/secret-attach.
type Credential struct {
	UserID         int    `json:"user_id"`
	UserName       string `json:"user_name"`
	Kind           string `json:"kind"` // anthropic_oauth | anthropic_api_key
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	LastVerifiedAt int64  `json:"last_verified_at,omitempty"`
	JiraAccountID  string `json:"jira_account_id,omitempty"`

	Token string `json:"-"` // decrypted; in-memory only
}

//counterfeiter:generate . Store
type Store interface {
	Put(userID int, userName, kind, token string, expiresAt time.Time) error
	Status(userID int) ([]Credential, error)                    // no tokens
	Resolve(userID int, kind string) (*Credential, bool, error) // decrypts
	ExpiringWithin(d time.Duration) ([]Credential, error)       // nag list
	Delete(userID int, kind string) error
}

// SecretAttacher is the ephemeral K8s secret helper (§8.2). Implemented once
// here; dispatch and the gateway use it, nobody re-implements secret lifecycle.
//
//counterfeiter:generate . SecretAttacher
type SecretAttacher interface {
	// Attach creates secret agent-run-<runID> in the worker namespace with
	// the §8.2 keys and returns its name. Idempotent per runID.
	Attach(ctx context.Context, runID int, cred *Credential, principalToken string) (secretName string, err error)
	// Cleanup deletes the secret. Called by the pipeline-run lifecycle
	// component on run completion (and best-effort by dispatch on error).
	Cleanup(ctx context.Context, runID int) error
}

// Backend is what the HTTP handler and the platform-credential syncer
// need: the vault Store plus user resolution from token claims. The
// atc/db factory implements it. Additive to the frozen §2.6 Store.
type Backend interface {
	Store
	// UserBySub resolves a users row by its OIDC sub claim (users.sub is
	// UNIQUE; rows are created at login by skymarshal).
	UserBySub(sub string) (userID int, userName string, found bool, err error)
	// SetJiraAccountID records the phase-2 Jira mapping seam value on all
	// of the user's credential rows.
	SetJiraAccountID(userID int, jiraAccountID string) error
}

// PutRequest is the parsed PUT /api/v1/agent/user-credentials body.
type PutRequest struct {
	Kind          string `json:"kind"`
	Token         string `json:"token"`
	ExpiresAt     int64  `json:"expires_at,omitempty"` // unix seconds; 0 = unknown
	JiraAccountID string `json:"jira_account_id,omitempty"`
	// User is empty for the normal self-scoped write. The ONLY other value
	// is PlatformUserName ("platform"), accepted from admins only: it
	// vaults the credential onto the §1.13 service user's row (the service
	// user never logs in, so no self-scoped path can reach it).
	User string `json:"user,omitempty"`
}

func (r *PutRequest) Validate() error {
	if !ValidKind(r.Kind) {
		return fmt.Errorf("kind must be %s or %s", KindAnthropicOAuth, KindAnthropicAPIKey)
	}
	if r.Token == "" {
		return fmt.Errorf("token is required")
	}
	if r.User != "" && r.User != PlatformUserName {
		return fmt.Errorf("user must be omitted or %q", PlatformUserName)
	}
	return nil
}
