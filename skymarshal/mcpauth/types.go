// Package mcpauth owns browser consent and resource-bound OAuth credentials for
// JetBridge's MCP endpoint. A grant restricts an identity; it never adds roles.
package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"
)

const (
	ScopeRead      = "read"
	ScopePipelines = "pipelines:write"
	ScopeBuilds    = "builds:write"
	ScopeHijack    = "hijack"
	ScopeAdmin     = "admin"
)

type Scope struct{ Name, Label, Description string }

var Scopes = []Scope{
	{ScopeRead, "Read", "Read pipelines, builds and their output."},
	{ScopePipelines, "Manage pipelines", "Set pipeline configuration, manage pipelines and create parameterized pipeline runs."},
	{ScopeBuilds, "Operate builds", "Trigger or stop configured jobs and operate resource checks."},
	{ScopeHijack, "Hijack containers", "Run interactive commands inside build containers."},
	{ScopeAdmin, "Administer JetBridge", "Perform administrative operations already allowed to your account."},
}

var (
	ErrNotFound     = errors.New("MCP authorization record not found")
	ErrInvalidToken = errors.New("invalid or expired MCP access token")
	ErrInvalidGrant = errors.New("invalid or expired MCP authorization grant")
)

// Client is a pre-registered public OAuth client. Redirects match exactly.
type Client struct {
	ID           string   `json:"client_id"`
	Name         string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

type Identity struct {
	Claims       map[string]any `json:"claims"`
	RefreshToken string         `json:"refresh_token"`
}

// Provider authenticates the user independently of the client asking for MCP
// access. Implementations must verify identity tokens, including issuer/audience.
type Provider interface {
	AuthorizationURL(state, verifier string) string
	Exchange(context.Context, string, string) (Identity, error)
	Refresh(context.Context, string) (Identity, error)
}

// Store must commit all operations atomically and serialize Get/Put on a key,
// including missing keys. SQLStore uses transaction-scoped advisory locks.
type Store interface {
	WithTx(context.Context, func(Tx) error) error
}
type Tx interface {
	Lock(key string, shared bool) error
	Get(kind, key string, value any) error
	Put(kind, key string, value any, expires time.Time) error
	Delete(kind, key string) error
	List(kind string) ([]Record, error)
}
type Record struct {
	Key  string
	Data []byte
}

type Config struct {
	Issuer           string
	Resource         string
	Clients          []Client
	Store            Store
	Provider         Provider
	SecureCookies    bool
	AccessLifetime   time.Duration
	IdleLifetime     time.Duration
	AbsoluteLifetime time.Duration
	// Now is injectable for expiry tests; production defaults to time.Now.
	Now func() time.Time
}

type Principal struct {
	GrantID  string
	ClientID string
	Scopes   []string
	Claims   map[string]any
}

func (p Principal) HasScope(scope string) bool { return slices.Contains(p.Scopes, scope) }

// RequireScope writes the OAuth insufficient-scope challenge. A tool's normal
// team/role authorization is still required after this succeeds.
func (s *Server) RequireScope(w http.ResponseWriter, p Principal, scope string) bool {
	if p.HasScope(scope) {
		return true
	}
	s.challenge(w, http.StatusForbidden, "insufficient_scope", scope)
	return false
}

func scopeNames() []string {
	names := make([]string, 0, len(Scopes))
	for _, scope := range Scopes {
		names = append(names, scope.Name)
	}
	return names
}
