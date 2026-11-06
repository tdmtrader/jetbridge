package steps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Fixture maps describe input files only. Production daemon processes perform
// registration, tar serving and error handling.
type closingCacheRuntime struct {
	api       *realCluster
	rec       *brine.Recorder
	namespace string
	port      int
	addresses []string
	nodes     []string
	daemons   []*realDaemon
	client    *jetbridge.DaemonClient
	started   bool
}

func (p ClosingCachePlan) ensureCache() error {
	r := p.CacheRuntime
	if r.started {
		return nil
	}
	r.addresses = []string{os.Getenv("BRINE_PEER_ADDRESS_1")}
	if len(p.CachePeer) > 0 {
		r.addresses = append(r.addresses, os.Getenv("BRINE_PEER_ADDRESS_2"))
	}
	for _, address := range r.addresses {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
			return fmt.Errorf("cache tests require the private-network runner")
		}
	}
	if len(r.addresses) == 2 && r.addresses[0] == r.addresses[1] {
		return fmt.Errorf("cache peer needs a distinct address")
	}
	ns, err := r.api.Clientset.CoreV1().Namespaces().Create(p.CacheCtx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "local-cache-"}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	r.namespace = ns.Name
	// Discovery uses endpoint IPs directly. Standalone daemons do not label
	// nodes, so no Node object or reported Node address is needed here. The
	// port is free on the addresses the cache daemons actually bind, not on
	// loopback, and the whole group moves if one of them loses it.
	daemons, port, err := startSharedPortDaemons(r.addresses,
		func(_ int, host string, port int) (*realDaemon, error) {
			return startConfiguredDaemon("http", http.DefaultClient,
				daemonOptions{Host: host, Port: port})
		})
	if err != nil {
		return err
	}
	r.port = port
	for i, d := range daemons {
		name := fmt.Sprintf("cache-node-%d", i+1)
		TrackDisposer(r.rec, "the cache daemon on "+name, d.stop)
		r.nodes = append(r.nodes, name)
		r.daemons = append(r.daemons, d)
		files := p.CacheLocal
		if i > 0 {
			files = p.CachePeer
		}
		for key, content := range files {
			if !filepath.IsLocal(key) || filepath.Base(key) != key || key == "." {
				return fmt.Errorf("invalid cache alias: %q", key)
			}
			dir := filepath.Join(d.Root, "steps", key)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "cached.txt"), []byte(content), 0644); err != nil {
				return err
			}
			if i == 0 {
				if err := registerDaemonArtifact(p.CacheCtx, http.DefaultClient, d.URL, key, dir); err != nil {
					return err
				}
			}
		}
	}
	slice, err := r.api.Clientset.DiscoveryV1().EndpointSlices(r.namespace).Create(p.CacheCtx, p.cacheEndpoints(false), metav1.CreateOptions{})
	if err != nil {
		return err
	}
	registerAPICleanup(r.rec, "cache EndpointSlice", func(ctx context.Context) error {
		return r.api.Clientset.DiscoveryV1().EndpointSlices(r.namespace).Delete(ctx, slice.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &slice.UID}})
	})
	r.client = jetbridge.NewDaemonClient(lagertest.NewTestLogger("brine-real-cache"),
		r.api.Clientset, r.namespace, "artifact-daemon", r.port, nil)
	r.started = true
	return nil
}

func (p ClosingCachePlan) cacheEndpoints(withPeer bool) *discoveryv1.EndpointSlice {
	r := p.CacheRuntime
	count := 1
	if withPeer {
		count = len(r.addresses)
	}
	var endpoints []discoveryv1.Endpoint
	for i := range count {
		ready := p.CacheReady
		if i > 0 {
			ready = true
		}
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses: []string{r.addresses[i]}, NodeName: &r.nodes[i],
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	port, tcp := int32(r.port), corev1.ProtocolTCP
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "artifact-daemon-cache", Namespace: r.namespace,
			Labels: map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"}},
		AddressType: discoveryv1.AddressTypeIPv4, Endpoints: endpoints,
		Ports: []discoveryv1.EndpointPort{{Port: &port, Protocol: &tcp}},
	}
}

func (p ClosingCachePlan) closingConfig() jetbridge.Config {
	r := p.CacheRuntime
	return jetbridge.Config{Namespace: r.namespace, ArtifactDaemonService: "artifact-daemon",
		ArtifactDaemonPort: r.port, ArtifactDaemonHostPath: r.daemons[0].Root,
		ArtifactDaemonResolveCapabilityKey: brineResolveKey}
}

func (p ClosingCachePlan) reclaimCache(key string) error {
	if err := p.ensureCache(); err != nil {
		return err
	}
	if !filepath.IsLocal(key) || filepath.Base(key) != key || key == "." {
		return fmt.Errorf("invalid cache key to reclaim: %q", key)
	}
	d := p.CacheRuntime.daemons[0]
	req, err := http.NewRequestWithContext(p.CacheCtx, http.MethodDelete, d.URL+"/artifacts/steps/"+url.PathEscape(key), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reclaim cache: HTTP %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(d.Root, "steps", key)); !os.IsNotExist(err) {
		return fmt.Errorf("cache still exists after reclamation: %v", err)
	}
	return nil
}

func (p ClosingCachePlan) revealCachePeer() error {
	if len(p.CacheRuntime.daemons) != 2 {
		return fmt.Errorf("no real cache peer was described")
	}
	slices := p.CacheRuntime.api.Clientset.DiscoveryV1().EndpointSlices(p.CacheRuntime.namespace)
	next := p.cacheEndpoints(true)
	old, err := slices.Get(p.CacheCtx, next.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	next.ResourceVersion = old.ResourceVersion
	_, err = slices.Update(p.CacheCtx, next, metav1.UpdateOptions{})
	return err
}
