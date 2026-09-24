package jetbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestStepHostPath_RoundTripsUnderAnyRootSpelling(t *testing.T) {
	for _, root := range []string{
		"/artifact-store",
		"/artifact-store/",
		"/var//artifact-store/./",
		"/var/lib/../artifact-store",
	} {
		for _, key := range []string{"handle/dir", "rc-42", "handle/result/nested"} {
			hostPath := stepHostPath(root, key)
			got, ok := stepKeyFromHostPath(root, hostPath)
			if !ok || got != key {
				t.Errorf("root %q key %q: stepHostPath gave %q, inverse gave (%q, %v)", root, key, hostPath, got, ok)
			}
		}
	}
}

func TestStepKeyFromHostPath_RefusesPathsNotUnderSteps(t *testing.T) {
	for _, hostPath := range []string{
		"/artifact-store/steps",           // the steps directory itself
		"/artifact-store/steps/",          // same, trailing slash
		"/artifact-store/caches/rc-1",     // a sibling tree
		"/artifact-store/steps-evil/x",    // a lookalike prefix
		"/artifact-store/steps/../caches", // climbs out
		"/elsewhere/steps/handle/dir",     // another root
		"artifact-store/steps/handle/dir", // relative
	} {
		if key, ok := stepKeyFromHostPath("/artifact-store/", hostPath); ok {
			t.Errorf("expected %q to be refused, got key %q", hostPath, key)
		}
	}

	// A key that climbs out of steps/ is not a key the inverse gives back.
	if key, ok := stepKeyFromHostPath("/artifact-store", stepHostPath("/artifact-store", "../caches/rc-1")); ok {
		t.Errorf("expected an escaping key to be refused, got %q", key)
	}
}

// The artifact daemon host path is an operator flag passed through verbatim.
// A trailing slash or a doubled separator is an ordinary spelling of the same
// directory, and must not change which artifact key a resource cache's
// mirror is triggered for.
func TestDaemonSetBackend_RegisterResourceCache_MirrorsUnderAnyRootSpelling(t *testing.T) {
	for _, root := range []string{
		"/artifact-store",
		"/artifact-store/",
		"/var//artifact-store/./",
	} {
		t.Run(root, func(t *testing.T) {
			var (
				mu         sync.Mutex
				mirrorKeys []string
				registered int
			)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/register":
					mu.Lock()
					registered++
					mu.Unlock()
					w.WriteHeader(http.StatusCreated)
				case "/mirror":
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					mirrorKeys = append(mirrorKeys, body["key"])
					mu.Unlock()
					w.WriteHeader(http.StatusAccepted)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ts.Close()

			addr := strings.TrimPrefix(ts.URL, "http://")
			colonIdx := strings.LastIndex(addr, ":")
			host := addr[:colonIdx]
			port, _ := strconv.Atoi(addr[colonIdx+1:])

			clientset := fake.NewSimpleClientset(
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
					Status: corev1.NodeStatus{
						Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
					},
				},
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "artifact-daemon-fixture",
						Namespace: "test-ns",
						Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
					},
					Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
				},
			)

			cfg := testDaemonConfig()
			cfg.ArtifactDaemonHostPath = root
			cfg.ArtifactDaemonPort = port

			b := NewDaemonSetBackend(cfg, NewArtifactLocator(), NewNodeIPResolver(clientset),
				NewDaemonClient(lagertest.NewTestLogger("test"), clientset, "test-ns", "artifact-daemon", port, nil))

			if err := b.RegisterResourceCache(context.Background(), "rc-42", "", "container-handle-dir", "node-1"); err != nil {
				t.Fatalf("RegisterResourceCache: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if registered == 0 {
				t.Fatalf("expected the alias to be registered")
			}
			if len(mirrorKeys) != 1 || mirrorKeys[0] != "container-handle/dir" {
				t.Errorf("expected one mirror of %q, got %v", "container-handle/dir", mirrorKeys)
			}
		})
	}
}
