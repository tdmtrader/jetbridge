package steps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The live tier has one approved node, so its independent peer runs on the
// producer's node. The web takes a ready endpoint on a node for that node's
// daemon, so the discovery the fixture hands the web must not make the peer
// speak for the producer's node: a running producer is still read from the
// node, through the production resolver and a real API server.
func TestWebPeerDiscoveryLeavesTheProducerNodeToItsDaemon(t *testing.T) {
	assets := envtestAssets()
	if assets == "" {
		t.Skip("no envtest assets")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets, ControlPlaneStartTimeout: 60 * time.Second}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start control plane: %v", err)
	}
	defer func() { _ = env.Stop() }()
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The producer: a daemon reached at its node's InternalIP.
	var mu sync.Mutex
	var producerRequests []string
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		producerRequests = append(producerRequests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, "producer-content")
	}))
	defer producer.Close()
	address, err := url.Parse(producer.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(address.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	const nodeName = "producer-node"
	node, err := cs.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}}
	if _, err := cs.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("publish node address: %v", err)
	}

	// The peer as the live fixture sees it: scheduled on the producer's node,
	// at a pod address of its own. Nothing listens there in this test, so a
	// web that took the peer for the node's daemon cannot read the artifact.
	peer := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: livePeerService, Namespace: "default", UID: types.UID("peer-uid")},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{PodIP: "192.0.2.10"},
	}
	if err := publishPeerForWeb(ctx, cs, "default", peer, uint16(port)); err != nil {
		t.Fatalf("publish the peer for the web: %v", err)
	}

	volume := jetbridge.NewDaemonSetVolume("h/o", "h", "brine", nil, nodeName,
		jetbridge.Config{Namespace: "default", ArtifactDaemonPort: port, ArtifactDaemonResolveCapabilityKey: brineResolveKey},
		jetbridge.NewNodeIPResolver(cs))
	volume.SetDaemonClient(jetbridge.NewDaemonClient(lagertest.NewTestLogger("web-peer"), cs, "default", livePeerWebService, port, nil))

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	stream, err := volume.StreamOut(readCtx, ".", nil)
	if err != nil {
		t.Fatalf("a running producer must be read from its node, not from the peer: %v", err)
	}
	body, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "producer-content" {
		t.Fatalf("read %q, want the producer's bytes", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(producerRequests) != 1 || !strings.HasSuffix(producerRequests[0], "/artifacts/h/o") {
		t.Fatalf("producer requests %v, want one read of the artifact", producerRequests)
	}
}

func TestDaemonResolutionVerdict(t *testing.T) {
	const (
		namespace = "brine-runtime-x"
		service   = "brine-artifact-daemon"
		node      = "theborg"
	)
	slices := "/apis/discovery.k8s.io/v1/namespaces/" + namespace + "/endpointslices"
	list := func(selector string, status int) volumeExecRequest {
		return volumeExecRequest{method: http.MethodGet, path: slices, query: url.Values{"labelSelector": {selector}}, status: status}
	}
	nodeRead := volumeExecRequest{method: http.MethodGet, path: "/api/v1/nodes/" + node, status: http.StatusOK}
	ours := "kubernetes.io/service-name=" + service

	for _, tc := range []struct {
		name      string
		service   string
		published bool
		requests  []volumeExecRequest
		want      string
	}{
		{"no discovery reads the node once", "", false, []volumeExecRequest{nodeRead}, ""},
		{"no discovery that never read the node", "", false, nil, "expected one real node read, got 0"},
		{"unpublished discovery falls back to one node read", service, false, []volumeExecRequest{list(ours, 200), nodeRead}, ""},
		{"unpublished discovery that never read the node", service, false, []volumeExecRequest{list(ours, 200)}, "expected one real node read, got 0"},
		{"published discovery answers from the endpoint", service, true, []volumeExecRequest{list(ours, 200), list(ours, 200)}, ""},
		{"published discovery that read the node anyway", service, true, []volumeExecRequest{list(ours, 200), nodeRead}, "read node theborg's address although discovery published its daemon"},
		{"published discovery that was never asked", service, true, []volumeExecRequest{list("kubernetes.io/service-name=other", 200)}, "never listed brine-artifact-daemon's endpoints"},
		{"published discovery whose list failed", service, true, []volumeExecRequest{list(ours, 500)}, "endpoint list for brine-artifact-daemon failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := daemonResolutionVerdict(tc.requests, namespace, tc.service, node, tc.published)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
