package jetbridge_test

import (
	"context"
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

func TestProbeResourceCache_Found(t *testing.T) {
	// Start a fake daemon that responds 200 with X-Node-Name.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/resource-caches/rc-42" {
			w.Header().Set("X-Node-Name", "node-1")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemon.Close()

	// Extract host:port from the test server URL.
	daemonAddr := daemon.Listener.Addr().String()
	// daemonAddr is "127.0.0.1:PORT" — extract the IP and port.
	host := daemonAddr[:len(daemonAddr)-len(":"+portFromAddr(daemonAddr))]
	port := portFromAddrInt(daemonAddr)

	clientset := fake.NewSimpleClientset(fakeEndpointSlice("cicd", "artifact-daemon", host))
	logger := lagertest.NewTestLogger("test")
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port)

	nodeName, found, err := client.ProbeResourceCache(context.Background(), "rc-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected cache to be found")
	}
	if nodeName != "node-1" {
		t.Errorf("expected node-1, got %q", nodeName)
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
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", port)

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
	client := jetbridge.NewDaemonClient(logger, clientset, "cicd", "artifact-daemon", 7780)

	_, found, err := client.ProbeResourceCache(context.Background(), "rc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected not found with no daemons")
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
