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
