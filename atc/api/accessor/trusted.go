package accessor

import (
	"context"
	"maps"
	"net/http"
)

type trustedClaimsKey struct{}

// WithTrustedClaims carries an identity already verified by an in-process
// authentication adapter. No HTTP header can populate this private context key.
// Authorization still runs through the normal access factory and team policies.
func WithTrustedClaims(ctx context.Context, claims map[string]any) context.Context {
	return context.WithValue(ctx, trustedClaimsKey{}, maps.Clone(claims))
}

type trustedTokenVerifier struct{ delegate TokenVerifier }

func NewTrustedTokenVerifier(delegate TokenVerifier) TokenVerifier {
	return trustedTokenVerifier{delegate: delegate}
}

func (v trustedTokenVerifier) Verify(r *http.Request) (map[string]any, error) {
	if claims, ok := r.Context().Value(trustedClaimsKey{}).(map[string]any); ok {
		return maps.Clone(claims), nil
	}
	return v.delegate.Verify(r)
}
