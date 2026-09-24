package jetbridge

import (
	"context"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The artifact daemon's Service, its EndpointSlices and its certificate live
// in the daemon's namespace, which a deployment may keep apart from the
// namespace step pods run in. A web node that looked for the daemon in the
// step namespace found no daemon at all: every probe missed, every alias
// registration and stream-in had nowhere to go.
func TestDaemonClientFromConfigListsDaemonsInTheDaemonNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		daemonSlice("steps", "10.0.0.1"),
		daemonSlice("daemons", "10.0.0.2"),
	)

	cfg := testDaemonConfig()
	cfg.Namespace = "steps"
	cfg.ArtifactDaemonNamespace = "daemons"
	cfg.ArtifactDaemonService = "artifact-daemon"

	ips, err := NewDaemonClientFromConfig(lagertest.NewTestLogger("test"), clientset, cfg).daemonIPs(context.Background())
	if err != nil {
		t.Fatalf("daemonIPs: %v", err)
	}
	if len(ips) != 1 || ips[0] != "10.0.0.2" {
		t.Errorf("daemonIPs = %v, want only the daemon namespace's [10.0.0.2]", ips)
	}
}

// Unset, the daemon is where the step pods are: the colocated install keeps
// working without the new flag.
func TestDaemonClientFromConfigFallsBackToTheStepNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		daemonSlice("steps", "10.0.0.1"),
		daemonSlice("daemons", "10.0.0.2"),
	)

	cfg := testDaemonConfig()
	cfg.Namespace = "steps"
	cfg.ArtifactDaemonNamespace = ""
	cfg.ArtifactDaemonService = "artifact-daemon"

	ips, err := NewDaemonClientFromConfig(lagertest.NewTestLogger("test"), clientset, cfg).daemonIPs(context.Background())
	if err != nil {
		t.Fatalf("daemonIPs: %v", err)
	}
	if len(ips) != 1 || ips[0] != "10.0.0.1" {
		t.Errorf("daemonIPs = %v, want only the step namespace's [10.0.0.1]", ips)
	}
}

// Discovery and verification name the same daemon: the namespace the client
// lists slices in is the one in the name it verifies certificates against.
func TestDaemonDiscoveryAndVerificationAgreeOnTheNamespace(t *testing.T) {
	for _, tc := range []struct {
		name, override, want string
	}{
		{name: "override", override: "daemons", want: "daemons"},
		{name: "fallback", override: "", want: "steps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tlsDaemonConfig(t)
			cfg.Namespace = "steps"
			cfg.ArtifactDaemonNamespace = tc.override
			cfg.ArtifactDaemonService = "artifact-daemon"

			client := NewDaemonClientFromConfig(lagertest.NewTestLogger("test"), fake.NewSimpleClientset(), cfg)
			if client.namespace != tc.want {
				t.Errorf("client lists slices in %q, want %q", client.namespace, tc.want)
			}
			if got, want := daemonTLSServerName(cfg), "artifact-daemon."+tc.want+".svc"; got != want {
				t.Errorf("daemonTLSServerName = %q, want %q", got, want)
			}
			if got := client.wire.Scheme(); got != "https" {
				t.Errorf("a TLS config built a %s client", got)
			}
		})
	}
}

func daemonSlice(namespace, ip string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-" + namespace,
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{ip}}},
	}
}
