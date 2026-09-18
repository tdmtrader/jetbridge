package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
)

// The runner owns the network namespace, the suite owns the API server, and
// this scenario owns its nodes, discovery record, credentials and daemons.
// There is no substitute peer-discovery or artifact-copy implementation.
func newArtifactCluster(res brine.Resources, rec *brine.Recorder, withPeer bool) (ArtifactCluster, error) {
	addresses := []string{os.Getenv("BRINE_PEER_ADDRESS_1")}
	if withPeer {
		addresses = append(addresses, os.Getenv("BRINE_PEER_ADDRESS_2"))
	}
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
			return ArtifactCluster{}, fmt.Errorf("real peer tests require scripts/run-private-network (missing valid private IPv4 address)")
		}
	}
	if withPeer && addresses[0] == addresses[1] {
		return ArtifactCluster{}, fmt.Errorf("real peers require distinct addresses")
	}
	api, ok := res.Get("real-cluster").(*realCluster)
	if !ok {
		return ArtifactCluster{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
	}
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return ArtifactCluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	ctx := context.Background()
	ns, err := api.Clientset.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "artifact-recording-"}}, metav1.CreateOptions{})
	if err != nil {
		return ArtifactCluster{}, err
	}
	registerNamespacePodCleanup(rec, api.Clientset, ns.Name)
	// Envtest has no namespace controller; the suite's API disposal removes
	// this namespace. Delete the actual scenario objects explicitly below.
	configPath, err := daemonKubeconfig(api, rec)
	if err != nil {
		return ArtifactCluster{}, err
	}
	port, err := freePort()
	if err != nil {
		return ArtifactCluster{}, err
	}
	names := []string{"node-1", "node-2"}[:len(addresses)]
	replicas := fmt.Sprint(len(addresses))
	var endpoints []discoveryv1.Endpoint
	var nodes []*realNode
	for i, name := range names {
		if _, err := createRealNode(ctx, api.Clientset, rec, name); err != nil {
			return ArtifactCluster{}, err
		}
		d, err := startConfiguredDaemon("http", http.DefaultClient,
			daemonOptions{Host: addresses[i], Port: port, Env: []string{"POD_IP=" + addresses[i]}},
			"--kubeconfig", configPath, "--namespace", ns.Name, "--node-name", name,
			"--mirror-replicas", replicas, "--mirror-timeout", "5s")
		if err != nil {
			return ArtifactCluster{}, err
		}
		rec.RegisterDisposer(func() {
			if err := d.stop(); err != nil {
				panic(err)
			}
		})
		nodes = append(nodes, &realNode{Root: d.Root, URL: d.URL, host: addresses[i], port: port})
		ready := true
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses: []string{addresses[i]}, NodeName: &names[i],
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	tcp, peerPort := corev1.ProtocolTCP, int32(port)
	slice, err := api.Clientset.DiscoveryV1().EndpointSlices(ns.Name).Create(ctx,
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Name: artifactDaemonService + "-brine", Namespace: ns.Name,
				Labels: map[string]string{discoveryv1.LabelServiceName: artifactDaemonService}},
			AddressType: discoveryv1.AddressTypeIPv4, Endpoints: endpoints,
			Ports: []discoveryv1.EndpointPort{{Port: &peerPort, Protocol: &tcp}},
		}, metav1.CreateOptions{})
	if err != nil {
		return ArtifactCluster{}, fmt.Errorf("publish real peer discovery: %w", err)
	}
	registerAPICleanup(rec, "peer EndpointSlice", func(ctx context.Context) error {
		return api.Clientset.DiscoveryV1().EndpointSlices(ns.Name).Delete(ctx, slice.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &slice.UID}})
	})
	row, err := database.PersistNamedWorker("artifact-recording-worker")
	if err != nil {
		return ArtifactCluster{}, err
	}
	var peer *realNode
	if withPeer {
		peer = nodes[1]
	}
	return wireArtifactCluster(ArtifactCluster{
		Ctx: ctx, Namespace: ns.Name, Clientset: api.Clientset,
		DB: database, WorkerRow: row, Node: nodes[0], Peer: peer,
		StoreRoot: nodes[0].Root, NodeName: names[0],
	}, jetbridge.NewSPDYExecutor(api.Clientset, api.RESTConfig))
}

func waitForPeerFile(ctx context.Context, peer *realNode, path, want string) error {
	if peer.pod != nil {
		// A real exec reads the independent disk before any HTTP GET can cause
		// read-through. Mirror publication renames a completed extraction.
		content, err := peer.store.exec(ctx, peer.pod.Name,
			[]string{"sh", "-ec", "while [ ! -f \"$1\" ]; do sleep 0.1; done; cat \"$1\"", "wait-mirrored-file", path}, nil)
		if err != nil {
			return fmt.Errorf("peer file did not arrive: %w", err)
		}
		if content != want {
			return fmt.Errorf("peer file bytes: got %q, want %q", content, want)
		}
		return nil
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		content, err := os.ReadFile(path)
		if err == nil && string(content) == want {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("peer file did not arrive with expected bytes: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// daemonKubeconfig gives production daemons credentials for the owned real API.
// Artifact peers and warm-ownership use the same credential lifecycle.
func daemonKubeconfig(api *realCluster, rec *brine.Recorder) (string, error) {
	credentials, err := os.MkdirTemp("", "brine-peer-credentials-")
	if err != nil {
		return "", err
	}
	rec.RegisterDisposer(func() {
		if err := os.RemoveAll(credentials); err != nil {
			panic(err)
		}
	})
	configPath := filepath.Join(credentials, "kubeconfig")
	cfg := api.RESTConfig
	kc := clientapi.Config{
		Clusters:       map[string]*clientapi.Cluster{"peer": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
		AuthInfos:      map[string]*clientapi.AuthInfo{"peer": {ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}},
		Contexts:       map[string]*clientapi.Context{"peer": {Cluster: "peer", AuthInfo: "peer"}},
		CurrentContext: "peer",
	}
	if err := clientcmd.WriteToFile(kc, configPath); err != nil {
		return "", err
	}
	if err := os.Chmod(configPath, 0600); err != nil {
		return "", err
	}
	return configPath, nil
}

// peerResolveDaemon proves that resolving an artifact from a peer does not
// make an empty node a local cache holder. Both daemons use real discovery;
// the ATC's later probe publishes only the empty node in a separate namespace.
func peerResolveDaemon(res brine.Resources, rec *brine.Recorder, key, content string) (DaemonPlan, error) {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key {
		return DaemonPlan{}, fmt.Errorf("peer-only probe requires a single-component cache key")
	}
	cluster, err := newArtifactCluster(res, rec, true)
	if err != nil {
		return DaemonPlan{}, err
	}
	ctx, cancel := context.WithTimeout(cluster.Ctx, 30*time.Second)
	defer cancel()
	local, peer := cluster.Node, cluster.Peer
	if err := peer.write(filepath.Join("steps", key), content); err != nil {
		return DaemonPlan{}, err
	}
	if err := peer.registerAlias(ctx, key, filepath.Join(peer.Root, "steps", key)); err != nil {
		return DaemonPlan{}, err
	}
	status, _, err := peer.request(ctx, http.MethodHead, "/resource-caches/"+key, "")
	if err != nil || status != http.StatusOK {
		return DaemonPlan{}, fmt.Errorf("peer cache must exist: status %d, error %v", status, err)
	}
	localMiss := func() error {
		status, _, err := local.request(ctx, http.MethodHead, "/resource-caches/"+key, "")
		if err != nil || status != http.StatusNotFound {
			return fmt.Errorf("peer-only node must have no local cache: status %d, error %v", status, err)
		}
		return nil
	}
	if err := localMiss(); err != nil {
		return DaemonPlan{}, err
	}

	// Resolve into a volume under the owned storage root, never the daemon's
	// unswept /tmp. A 200 alone is insufficient: verify peer provenance and bytes.
	rel := "tmp/peer-probe/input"
	dest := filepath.Join(local.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return DaemonPlan{}, err
	}
	status, body, err := local.request(ctx, http.MethodPost, "/resolve",
		fmt.Sprintf(`{"key":%q,"dest":%q}`, key, dest))
	if err != nil || status != http.StatusOK {
		return DaemonPlan{}, fmt.Errorf("real peer resolve must succeed: status %d, error %v, body %s", status, err, abbrev(string(body)))
	}
	var resolved struct {
		Status string `json:"status"`
		Method string `json:"method"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(body, &resolved); err != nil {
		return DaemonPlan{}, err
	}
	if resolved.Status != "ok" || resolved.Method != "peer" || resolved.Source != peer.host {
		return DaemonPlan{}, fmt.Errorf("resolve must come from the actual peer %s: %s", peer.host, body)
	}
	got, err := os.ReadFile(filepath.Join(dest, stepOutputFileName))
	if err != nil || string(got) != content {
		return DaemonPlan{}, fmt.Errorf("resolved peer bytes: got %q, want %q, error %v", got, content, err)
	}

	// Reclaim the demonstration copy through the real API before the ATC
	// probe. The node must have neither that copy nor a steps/<key> cache.
	status, body, err = local.request(ctx, http.MethodDelete, "/artifacts/"+rel, "")
	if err != nil || status != http.StatusNoContent {
		return DaemonPlan{}, fmt.Errorf("reclaim peer-resolve copy: status %d, error %v, body %s", status, err, body)
	}
	for _, path := range []string{dest, filepath.Join(local.Root, "steps", key)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return DaemonPlan{}, fmt.Errorf("peer-only node still has %s: %v", path, err)
		}
	}
	if err := localMiss(); err != nil {
		return DaemonPlan{}, err
	}
	return DaemonPlan{
		Ctx: cluster.Ctx, Namespace: cluster.Namespace, Service: artifactDaemonService,
		Port: local.port, DaemonIP: local.host, Root: local.Root,
		IPs: []string{local.host},
	}, nil
}
