// Package credentials owns the per-user Anthropic credential vault: the
// domain types and Store contract (implemented by atc/db), the HTTP
// handler seam, and the platform-credential K8s secret that every agent pod
// mounts its model token from. Contract: docs/superpowers/plans/
// agentic-platform/00-shared-contracts.md §1.3, §2.6, §8.2, §1.13.
package credentials

import (
	"fmt"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

const (
	KindAnthropicOAuth  = "anthropic_oauth"
	KindAnthropicAPIKey = "anthropic_api_key"
)

// §8.2 platform-credential secret contract. The secret is the ONLY
// model-credential path into an agent pod: AgentStep wires both keys into the
// MAIN container as secretKeyRefs (sidecars never receive them).
//
// SecretKeyModelTokenKind is written by PlatformSecretSyncer beside the token
// and is OPTIONAL on read — a secret an operator created by hand carries only
// the token, and absent kind means anthropic_oauth.
const (
	SecretKeyAnthropicToken = "anthropic-token"
	SecretKeyModelTokenKind = "kind"

	// PlatformSecretName is the long-lived platform credential secret
	// (§8.2/§1.13), maintained by PlatformSecretSyncer. It is also the
	// default value of --agent-platform-token-secret.
	PlatformSecretName = "agent-platform-credential"
)

// The §1.13 platform service user, seeded by migration 1773106022. It
// never logs in; admins vault its credential via `fly agent auth
// --platform` (PutRequest.User = PlatformUserName). Its credential funds
// platform-initiated LLM work (judge, calibration).
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
	UserID    int    `json:"user_id"`
	UserName  string `json:"user_name"`
	Kind      string `json:"kind"` // anthropic_oauth | anthropic_api_key
	ExpiresAt int64  `json:"expires_at,omitempty"`

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

// Backend is what the HTTP handler and the platform-credential syncer
// need: the vault Store plus user resolution from token claims. The
// atc/db factory implements it. Additive to the frozen §2.6 Store.
type Backend interface {
	Store
	// UserBySub resolves a users row by its OIDC sub claim (users.sub is
	// UNIQUE; rows are created at login by skymarshal).
	UserBySub(sub string) (userID int, userName string, found bool, err error)
}

// PutRequest is the parsed PUT /api/v1/agent/user-credentials body.
type PutRequest struct {
	Kind      string `json:"kind"`
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix seconds; 0 = unknown
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
