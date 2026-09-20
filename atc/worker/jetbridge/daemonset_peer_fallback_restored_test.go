package jetbridge

// RESTORED Go 2026-09-18 (round 2) — live-only replacement.
//
// `TestDaemonSetMode_StreamOut_FallsBackToPeer_AfterProducerDeath` (PE-08)
// comes back from `daemonset_integration_test.go` at core `b294dafc49`. The
// status-writer sweep retired it against an "existing live stopped-producer /
// present-peer row", which lives under `features/live/peer-read.feature` and
// is tagged `@live-kubernetes`: it runs only against a real cluster and never
// under `make test-unit`. With this test gone, no unit-tier test asserted that
// a DaemonSetVolume whose producing node refuses the connection falls through
// to a ready peer from the EndpointSlice and still returns the payload.
//
// Its three helpers (`integrationRoutingTransport`, `itoa`, `tarDirOrFile`)
// were deleted with it as unused; they come back here with it, verbatim.

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDaemonSetMode_StreamOut_FallsBackToPeer_AfterProducerDeath(t *testing.T) {
	// Peer's hostPath: pre-populated with the mirrored step output.
	storageB := t.TempDir()
	bData := filepath.Join(storageB, "steps", "build-handle", "result")
	if err := os.MkdirAll(bData, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bData, "out.txt"), []byte("peer-payload"), 0644); err != nil {
		t.Fatal(err)
	}

	// Live peer's HTTP server: serves HEAD /artifacts/steps/{key} (probe)
	// and GET /artifacts/steps/{key} (tar fetch) by reading from storageB.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/artifacts/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		path := filepath.Join(storageB, key)
		info, err := os.Stat(path)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if info.IsDir() {
			tarDirOrFile(w, path)
			return
		}
		f, _ := os.Open(path)
		defer f.Close()
		io.Copy(w, f)
	}))
	defer peer.Close()

	const (
		// Synthetic peer hostnames keyed by the routing transport.
		producerHost = "node-a-dead.svc"
		peerHost     = "node-b-live.svc"
		port         = 7780
	)
	transport := &integrationRoutingTransport{routes: map[string]string{
		producerHost + ":" + itoa(port): "", // refuse — simulates dead producer
		peerHost + ":" + itoa(port):     peer.URL,
	}}

	// ATC state: Node maps node-a → producerHost; EndpointSlice has only
	// the live peer.
	ready := true
	atcClientset := fake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: producerHost}},
			},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "artifact-daemon-slice",
				Namespace: "concourse",
				Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{peerHost}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
			},
		},
	)

	cfg := Config{
		Namespace:              "concourse",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     port,
		ArtifactDaemonService:  "artifact-daemon",
	}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * 1e9} // 5s

	logger := lagertest.NewTestLogger("atc")
	resolver := NewNodeIPResolver(atcClientset)
	// White-box DaemonClient construction so we can inject the routing
	// transport (NewDaemonClient builds its own client).
	dc := &DaemonClient{
		logger:    logger,
		clientset: atcClientset,
		namespace: "concourse",
		service:   "artifact-daemon",
		port:      port,
		client:    httpClient,
		scheme:    "http",
	}

	// Volume keyed by daemonKey ("build-handle/result"). This matches what
	// the daemon's filesystem has at storage/steps/build-handle/result on
	// the peer post-mirror — and what /resolve (init container) uses.
	vol := NewDaemonSetVolume("build-handle/result", "build-handle/result", "worker-x", nil, "node-a", cfg, resolver)
	vol.httpClient = httpClient
	vol.daemonClient = dc

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut should fall back to peer: %v", err)
	}
	defer reader.Close()

	body, _ := io.ReadAll(reader)
	if !strings.Contains(string(body), "peer-payload") {
		t.Errorf("expected peer-payload in body, got: %q", string(body))
	}
}

// integrationRoutingTransport routes by URL host:port to a target server
// URL. "" simulates a refused / unreachable host (returns a synthetic
// error without actually attempting a TCP dial).
type integrationRoutingTransport struct {
	routes map[string]string
}

func (t *integrationRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, ok := t.routes[req.URL.Host]
	if !ok || target == "" {
		return nil, fmt.Errorf("connection refused: %s", req.URL.Host)
	}
	if strings.HasPrefix(target, "http://") {
		target = strings.TrimPrefix(target, "http://")
	}
	req.URL.Scheme = "http"
	req.URL.Host = target
	return http.DefaultTransport.RoundTrip(req)
}

// itoa is just strconv.Itoa, inlined so the test reads cleanly.
func itoa(n int) string { return strconv.Itoa(n) }

// tarDirOrFile mirrors the daemon's directory-tar-on-the-fly behavior
// closely enough to satisfy DaemonSetVolume.StreamOut. Used only by the
// peer's httptest server in TestDaemonSetMode_StreamOut_FallsBackToPeer_*.
func tarDirOrFile(w http.ResponseWriter, path string) {
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	defer tw.Close()
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		tw.WriteHeader(&tar.Header{
			Name:     rel,
			Mode:     int64(info.Mode().Perm()),
			Size:     info.Size(),
			Typeflag: tar.TypeReg,
		})
		f, _ := os.Open(p)
		defer f.Close()
		io.Copy(tw, f)
		return nil
	})
}
