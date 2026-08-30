package steps

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Can two real daemons discover each other through a real API server?
func TestTwoRealDaemonsCanSeeEachOther(t *testing.T) {
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

	// Write a kubeconfig the daemon can be pointed at.
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	api := clientcmdapi.NewConfig()
	api.Clusters["e"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData, InsecureSkipTLSVerify: cfg.CAData == nil}
	api.AuthInfos["e"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	api.Contexts["e"] = &clientcmdapi.Context{Cluster: "e", AuthInfo: "e", Namespace: "default"}
	api.CurrentContext = "e"
	if err := clientcmd.WriteToFile(*api, kc); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	ctx := context.Background()

	// Two daemons, each labelling its own node.
	var ds []*realDaemon
	for i, node := range []string{"node-a", "node-b"} {
		if _, err := cs.CoreV1().Nodes().Create(ctx, &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: node},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create node: %v", err)
		}
		d, err := startRealDaemon("--kubeconfig", kc, "--node-name", node, "--namespace", "default")
		if err != nil {
			t.Fatalf("daemon %d: %v", i, err)
		}
		defer func() { _ = d.stop() }()
		ds = append(ds, d)
	}

	// The production change is already proven at this point: both daemons
	// started with --kubeconfig --node-name, and startup labels the node and
	// os.Exit(1)s if it cannot. Assert it explicitly before going further.
	for _, node := range []string{"node-a", "node-b"} {
		n, err := cs.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", node, err)
		}
		if _, ok := n.Labels["concourse.dev/artifact-cache"]; !ok {
			t.Fatalf("daemon on %s did not label its node — the kubeconfig path is not working: %v", node, n.Labels)
		}
	}
	fmt.Println("   both daemons reached the API server and labelled their nodes")

	// Now peers. A REAL API server refuses loopback addresses in an
	// EndpointSlice — validation the fake clientset never performed, and the
	// first thing envtest caught here. The daemons bind all interfaces, so a
	// non-loopback address on this host reaches them.
	host := nonLoopbackIPv4()
	if host == "" {
		t.Skip("no non-loopback IPv4 on this host; peer discovery needs a routable address")
	}
	var eps []discoveryv1.Endpoint
	for range ds {
		eps = append(eps, discoveryv1.Endpoint{Addresses: []string{host}})
	}
	if _, err := cs.DiscoveryV1().EndpointSlices("default").Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "artifact-daemon-x", Namespace: "default",
			Labels: map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   eps,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("publish endpoints: %v", err)
	}

	fmt.Println("   both daemons published at", host, "— peer discovery is now expressible")
}

// nonLoopbackIPv4 returns an address an EndpointSlice will accept.
func nonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}
