package jetbridge

import (
	"net/http"
	"testing"

	"github.com/concourse/concourse/artifactwire"
)

// plainWire is a plaintext wire client on port whose every request goes
// through rt, for tests that route host:port pairs to test servers.
func plainWire(t *testing.T, port int, rt http.RoundTripper) *artifactwire.Client {
	t.Helper()
	client, err := artifactwire.NewClient(port, artifactwire.TLS{})
	if err != nil {
		t.Fatalf("artifactwire.NewClient: %v", err)
	}
	return client.WithTransport(rt)
}
