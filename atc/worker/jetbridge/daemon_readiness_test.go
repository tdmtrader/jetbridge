package jetbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Every operation that picks an artifact daemon from discovery must pick
// only from ready ones. A not-ready or terminating daemon pod still has an
// address in its EndpointSlice, and one that still answers HTTP would win a
// step-artifact probe, take an alias registration, or receive a source-less
// stream in (`fly execute` input upload) -- only to be gone moments later.
func TestDaemonClient_DiscoveryUsesReadyDaemonsOnly(t *testing.T) {
	no, yes := false, true

	for _, tc := range []struct {
		name       string
		conditions discoveryv1.EndpointConditions
		usable     bool
	}{
		{"no opinion is ready", discoveryv1.EndpointConditions{}, true},
		{"ready", discoveryv1.EndpointConditions{Ready: &yes}, true},
		{"not ready", discoveryv1.EndpointConditions{Ready: &no}, false},
		{"terminating", discoveryv1.EndpointConditions{Ready: &no, Serving: &yes, Terminating: &yes}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				hits []string
			)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits = append(hits, r.Method+" "+r.URL.Path)
				mu.Unlock()
				switch {
				case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/artifacts/steps/"):
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodPost && r.URL.Path == "/register":
					w.WriteHeader(http.StatusCreated)
				case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/stream-in/"):
					w.WriteHeader(http.StatusCreated)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ts.Close()

			addr := strings.TrimPrefix(ts.URL, "http://")
			colonIdx := strings.LastIndex(addr, ":")
			host := addr[:colonIdx]
			port, _ := strconv.Atoi(addr[colonIdx+1:])

			clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "artifact-daemon-fixture",
					Namespace: "test-ns",
					Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
				},
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}, Conditions: tc.conditions}},
			})
			client := NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "test-ns", "artifact-daemon", port, nil)
			ctx := context.Background()

			ip, found, err := client.ProbeStepArtifact(ctx, "handle/dir")
			if err != nil {
				t.Fatalf("ProbeStepArtifact: %v", err)
			}
			if found != tc.usable || (found && ip != host) {
				t.Errorf("ProbeStepArtifact: expected found=%v, got (%q, %v)", tc.usable, ip, found)
			}

			err = client.RegisterAlias(ctx, "rc-1", "/artifact-store/steps/handle/dir", "")
			if tc.usable && err != nil {
				t.Errorf("RegisterAlias: expected success, got %v", err)
			}
			if !tc.usable && err == nil {
				t.Errorf("RegisterAlias: expected no daemon to register with")
			}

			cfg := testDaemonConfig()
			cfg.ArtifactDaemonPort = port
			vol := NewDaemonSetVolume("upload-key", "upload-handle", "worker", nil, "", cfg, nil)
			vol.SetDaemonClient(client)
			err = vol.StreamIn(ctx, ".", nil, 0, strings.NewReader("tar"))
			if tc.usable && err != nil {
				t.Errorf("StreamIn: expected success, got %v", err)
			}
			if !tc.usable && err == nil {
				t.Errorf("StreamIn: expected no daemon to stream into")
			}

			mu.Lock()
			defer mu.Unlock()
			if !tc.usable && len(hits) != 0 {
				t.Errorf("expected no request to reach an unready daemon, got %v", hits)
			}
		})
	}
}
