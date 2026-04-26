package jetbridge_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func fakeEndpointSlice(namespace, service string, ips ...string) *discoveryv1.EndpointSlice {
	var endpoints []discoveryv1.Endpoint
	for _, ip := range ips {
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses: []string{ip},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service + "-abc",
			Namespace: namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: service,
			},
		},
		Endpoints: endpoints,
	}
}

func TestProbeResourceCache_FoundViaNewEndpoint(t *testing.T) {
	// Daemon with the new HEAD /resource-caches/ endpoint.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/resource-caches/rc-42" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	daemonAddr := daemon.Listener.Addr().String()
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeResourceCache(context.Background(), "rc-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected cache to be found")
	}
	if daemonIP != host {
		t.Errorf("expected daemon IP %q, got %q", host, daemonIP)
	}
}

func TestProbeResourceCache_FoundViaResolveFallback(t *testing.T) {
	// Older daemon without HEAD /resource-caches/ — falls back to /resolve.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/resolve" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok","method":"registry"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	daemonAddr := daemon.Listener.Addr().String()
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeResourceCache(context.Background(), "rc-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected cache to be found via resolve fallback")
	}
	if daemonIP != host {
		t.Errorf("expected daemon IP %q, got %q", host, daemonIP)
	}
}

func TestProbeResourceCache_NotFound(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	daemonAddr := daemon.Listener.Addr().String()
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	_, found, err := client.ProbeResourceCache(context.Background(), "rc-999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected cache to not be found")
	}
}

func TestProbeResourceCache_NoDaemons(t *testing.T) {
	// Empty EndpointSlice — no daemon pods.
	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon"))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", 7780, nil)

	_, found, err := client.ProbeResourceCache(context.Background(), "rc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected not found with no daemons")
	}
}

// ---------------------------------------------------------------------------
// ProbeStepArtifact
// ---------------------------------------------------------------------------

func TestProbeStepArtifact_OneDaemonHit(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/artifacts/steps/handle/output" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	daemonAddr := daemon.Listener.Addr().String()
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeStepArtifact(context.Background(), "handle/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected step artifact to be found")
	}
	if daemonIP != host {
		t.Errorf("expected daemon IP %q, got %q", host, daemonIP)
	}
}

func TestProbeStepArtifact_OneDaemonMiss(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	daemonAddr := daemon.Listener.Addr().String()
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeStepArtifact(context.Background(), "handle/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected step artifact to NOT be found, got daemonIP=%q", daemonIP)
	}
	if daemonIP != "" {
		t.Errorf("expected empty daemon IP on miss, got %q", daemonIP)
	}
}

func TestProbeStepArtifact_NoDaemons(t *testing.T) {
	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon"))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", 7780, nil)

	daemonIP, found, err := client.ProbeStepArtifact(context.Background(), "handle/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected not found with no daemons")
	}
	if daemonIP != "" {
		t.Errorf("expected empty daemon IP, got %q", daemonIP)
	}
}

func TestProbeStepArtifact_AllMiss(t *testing.T) {
	// One httptest server returning 404 for everything, registered TWICE
	// in the EndpointSlice (simulating two peer daemons that both miss).
	miss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer miss.Close()

	addr := miss.Listener.Addr().String()
	host := addr[:len(addr)-len(":"+portFromAddr(addr))]
	port := portFromAddrInt(addr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host, host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeStepArtifact(context.Background(), "handle/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected not found across all-miss daemons, got daemonIP=%q", daemonIP)
	}
}

func TestProbeStepArtifact_OneHitOneError(t *testing.T) {
	// Two daemon entries: one returns 200, the other (a TEST-NET-3
	// reserved IP) refuses connections. Probe must succeed via the hit.
	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/artifacts/steps/handle/output" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer hit.Close()

	addr := hit.Listener.Addr().String()
	hitHost := addr[:len(addr)-len(":"+portFromAddr(addr))]
	port := portFromAddrInt(addr)

	// Both endpoints share the configured port. 203.0.113.99 is a TEST-NET-3
	// reserved address — connections will fail, exactly like a dead peer.
	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", hitHost, "203.0.113.99"))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port, nil)

	daemonIP, found, err := client.ProbeStepArtifact(context.Background(), "handle/output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected hit daemon to win despite unreachable peer")
	}
	if daemonIP != hitHost {
		t.Errorf("expected daemon IP %q, got %q", hitHost, daemonIP)
	}
}

// ---------------------------------------------------------------------------
// TriggerMirror — fire-and-forget POST /mirror to a specific daemon IP.
// ---------------------------------------------------------------------------

func TestTriggerMirror_PostsCorrectBody(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
		hits      int
	)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		hits++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer daemon.Close()

	addr := daemon.Listener.Addr().String()
	host := addr[:len(addr)-len(":"+portFromAddr(addr))]
	port := portFromAddrInt(addr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("ns", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "ns", "artifact-daemon", port, nil)

	err := client.TriggerMirror(context.Background(), host, "handle/output")
	if err != nil {
		t.Fatalf("TriggerMirror returned error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/mirror" {
		t.Errorf("expected /mirror, got %s", gotPath)
	}
	if string(gotBody) != `{"key":"handle/output"}` && string(gotBody) != `{"key":"handle/output"}`+"\n" {
		t.Errorf("unexpected body: %q", string(gotBody))
	}
	if hits != 1 {
		t.Errorf("expected 1 hit, got %d", hits)
	}
}

func TestTriggerMirror_BestEffort_OnUnreachable(t *testing.T) {
	// 203.0.113.99 is TEST-NET-3 — connections will fail. The method must
	// not return an error; failures are logged so they show up in
	// observability without failing the producing step.
	clientset := fake.NewSimpleClientset(fakeEndpointSlice("ns", "artifact-daemon", "203.0.113.99"))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "ns", "artifact-daemon", 7780, nil)

	err := client.TriggerMirror(context.Background(), "203.0.113.99", "handle/output")
	if err != nil {
		t.Errorf("TriggerMirror should be best-effort but returned: %v", err)
	}
}

// TestTriggerMirror_EmptyDaemonIP verifies the method doesn't construct
// a malformed URL when daemonIP is empty (defensive — callers should
// never pass empty, but a typo or null could happen). Should return
// nil (best-effort) without panicking.
func TestTriggerMirror_EmptyDaemonIP(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "ns", "artifact-daemon", 7780, nil)

	// Must not panic. The actual HTTP request will fail (URL like
	// http://:7780/mirror is malformed) but TriggerMirror swallows
	// errors per its best-effort contract.
	err := client.TriggerMirror(context.Background(), "", "handle/output")
	if err != nil {
		t.Errorf("TriggerMirror must be best-effort even with empty daemonIP, got: %v", err)
	}
}

// TestTriggerMirror_ContextCancelled verifies that a cancelled context
// doesn't cause the method to leak the request or surface a context
// error to the caller — best-effort means transport problems don't
// fail the producing step.
func TestTriggerMirror_ContextCancelled(t *testing.T) {
	clientset := fake.NewSimpleClientset(fakeEndpointSlice("ns", "artifact-daemon", "1.2.3.4"))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "ns", "artifact-daemon", 7780, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := client.TriggerMirror(ctx, "1.2.3.4", "handle/output")
	if err != nil {
		t.Errorf("TriggerMirror with pre-cancelled ctx must still return nil, got: %v", err)
	}
}

func TestTriggerMirror_BestEffort_OnNon202(t *testing.T) {
	// Daemon returns 500 — should not propagate as an error.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer daemon.Close()

	addr := daemon.Listener.Addr().String()
	host := addr[:len(addr)-len(":"+portFromAddr(addr))]
	port := portFromAddrInt(addr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("ns", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "ns", "artifact-daemon", port, nil)

	err := client.TriggerMirror(context.Background(), host, "handle/output")
	if err != nil {
		t.Errorf("TriggerMirror should be best-effort on 5xx but returned: %v", err)
	}
}

func portFromAddr(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return ""
}

func portFromAddrInt(addr string) int {
	portStr := portFromAddr(addr)
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return port
}
