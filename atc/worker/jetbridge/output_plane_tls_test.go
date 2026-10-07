package jetbridge

import (
	"strings"
	"testing"
)

// The output plane has ONE supported transport, so a missing client credential
// is a startup refusal and not a fallback.
//
// The artifact daemon has two honest modes, and its validation accepts "none of
// the three" for that reason. The output plane it serves has one: its off-node
// routes refuse every operation whose request carries no verified peer
// certificate. Accepting "none of the three" with the output plane on would be
// an ATC that starts, reports healthy, and is refused by every control call it
// ever makes -- at the first capture, in production.
func TestAnOutputPlaneWithNoClientCredentialIsRefusedAtStartup(t *testing.T) {
	for _, missing := range []struct {
		name             string
		cert, key, caCrt string
		wants            []string
	}{
		{
			name:  "nothing configured",
			wants: []string{"tls-cert", "tls-key", "tls-ca-cert"},
		},
		{
			name: "no CA", cert: "/c", key: "/k",
			wants: []string{"tls-ca-cert"},
		},
		{
			name: "certificate only", cert: "/c",
			wants: []string{"tls-key", "tls-ca-cert"},
		},
	} {
		t.Run(missing.name, func(t *testing.T) {
			err := ValidateOutputPlaneTLS(missing.cert, missing.key, missing.caCrt)
			if err == nil {
				t.Fatalf("an output plane with cert=%q key=%q ca=%q was accepted; every "+
					"control call it makes is refused for want of a verified peer certificate",
					missing.cert, missing.key, missing.caCrt)
			}
			for _, want := range missing.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name the artifact daemon's %s: %v",
						want, err)
				}
			}
		})
	}

	if err := ValidateOutputPlaneTLS("/c", "/k", "/ca"); err != nil {
		t.Errorf("a complete triple was refused: %v", err)
	}

	// The artifact daemon's own predicate still accepts the empty case: it is
	// the output plane that requires TLS, not every deployment.
	if err := ValidateDaemonTLSFlags("", "", ""); err != nil {
		t.Errorf("the artifact daemon's plaintext mode is no longer accepted: %v", err)
	}
}
