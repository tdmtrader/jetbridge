package mcpauth

import (
	"context"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// NewOIDCVerifier validates upstream identity tokens, including their signature,
// issuer, audience and expiry. Keys are loaded lazily: the embedded issuer does
// not need to be listening while the application's handlers are constructed.
func NewOIDCVerifier(issuer, clientID string, client *http.Client) func(context.Context, string) (map[string]any, error) {
	bounded := *client
	bounded.Timeout = 15 * time.Second
	ctx := oidc.ClientContext(context.Background(), &bounded)
	verifier := oidc.NewVerifier(issuer, oidc.NewRemoteKeySet(ctx, issuer+"/keys"), &oidc.Config{ClientID: clientID})
	return func(ctx context.Context, raw string) (map[string]any, error) {
		token, err := verifier.Verify(ctx, raw)
		if err != nil {
			return nil, err
		}
		var claims map[string]any
		if err := token.Claims(&claims); err != nil {
			return nil, err
		}
		return claims, nil
	}
}
