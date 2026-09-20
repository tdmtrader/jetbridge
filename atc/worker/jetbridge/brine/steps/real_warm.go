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
	"k8s.io/client-go/kubernetes"
)

// WarmRollPlan owns two production daemon processes and a real filesystem
// durable store. Kubernetes publishes their actual reachable addresses.
// Daemon replacement is explicit; envtest does not run a DaemonSet controller.
type WarmRollPlan struct {
	Ctx          context.Context
	Cluster      kubernetes.Interface
	Namespace    string
	Port         int
	Addresses    []string
	Nodes        []string
	Daemons      []*realDaemon
	Kubeconfig   string
	StorePath    string
	Expected     map[string]string
	Client       *jetbridge.DaemonClient
	Backend      *jetbridge.DaemonSetBackend
	Observed     []warmObservation
	ReclaimedKey string
}

type warmObservation struct {
	Served bool
	Node   string
}

func newWarmRollPlan(res brine.Resources, rec *brine.Recorder) (WarmRollPlan, error) {
	api, err := getRealCluster(res)
	if err != nil {
		return WarmRollPlan{}, err
	}
	p := WarmRollPlan{
		Ctx: context.Background(), Cluster: api.Clientset,
		Addresses: []string{os.Getenv("BRINE_PEER_ADDRESS_1"), os.Getenv("BRINE_PEER_ADDRESS_2")},
		Nodes:     []string{"warm-node-a", "warm-node-b"}, Expected: map[string]string{},
	}
	for _, address := range p.Addresses {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
			return p, fmt.Errorf("warm ownership requires the private-network runner")
		}
	}
	if p.Addresses[0] == p.Addresses[1] {
		return p, fmt.Errorf("warm ownership requires two distinct addresses")
	}
	ns, err := p.Cluster.CoreV1().Namespaces().Create(p.Ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "warm-ownership-"}}, metav1.CreateOptions{})
	if err != nil {
		return p, err
	}
	p.Namespace = ns.Name
	for _, name := range p.Nodes {
		if _, err := createRealNode(p.Ctx, p.Cluster, rec, name); err != nil {
			return p, err
		}
	}
	p.StorePath, err = AttributedTempDir("brine-warm-durable-")
	if err != nil {
		return p, err
	}
	storePath := p.StorePath
	TrackDisposer(rec, "the warm durable store "+storePath,
		func() error { return os.RemoveAll(storePath) })
	p.Kubeconfig, err = daemonKubeconfig(api, rec)
	if err != nil {
		return p, err
	}
	if err := p.startDaemons(rec); err != nil {
		return p, err
	}
	slice, err := p.Cluster.DiscoveryV1().EndpointSlices(p.Namespace).Create(p.Ctx, p.publish(), metav1.CreateOptions{})
	if err != nil {
		return p, fmt.Errorf("publish warm daemons: %w", err)
	}
	TrackDisposer(rec, "the warm endpoint slice "+slice.Name, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Not releasedIfGone: this site never forgave a missing slice, and a
		// disposer converted to report its failures is not the place to start
		// forgiving one.
		return p.Cluster.DiscoveryV1().EndpointSlices(p.Namespace).Delete(ctx, slice.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &slice.UID}})
	})
	p.wireClient()
	return p, nil
}

// wireClient points the production client and backend at the port the daemons
// are ACTUALLY on. startDaemons may move the group to a fresh port when one is
// taken from under it, and a client left on the old number would be talking to
// whatever else won the race.
func (p *WarmRollPlan) wireClient() {
	p.Client = jetbridge.NewDaemonClient(lagertest.NewTestLogger("brine-warm-roll"),
		p.Cluster, p.Namespace, "artifact-daemon", p.Port, nil)
	p.Backend = jetbridge.NewDaemonSetBackend(jetbridge.Config{
		Namespace: p.Namespace, ArtifactDaemonService: "artifact-daemon",
		ArtifactDaemonPort: p.Port, ArtifactDaemonHostPath: "/artifact-store",
		ArtifactDaemonWarmTimeout: 5 * time.Second,
	}, jetbridge.NewArtifactLocator(), nil)
	p.Backend.SetDaemonClient(p.Client)
}

// startDaemons brings up one daemon per address at one shared DaemonSet port,
// free on the addresses the warm daemons actually bind rather than on loopback.
// It owns the port because the group may have to move to a new one: see
// startSharedPortDaemons.
func (p *WarmRollPlan) startDaemons(rec *brine.Recorder) error {
	p.Daemons = nil
	daemons, port, err := startSharedPortDaemons(p.Addresses,
		func(i int, host string, port int) (*realDaemon, error) {
			return startConfiguredDaemon("http", http.DefaultClient,
				daemonOptions{Host: host, Port: port},
				"--kubeconfig", p.Kubeconfig, "--namespace", p.Namespace,
				"--node-name", p.Nodes[i], "--durable-store=filesystem",
				"--durable-path", p.StorePath)
		})
	if err != nil {
		return err
	}
	p.Port = port
	for i, d := range daemons {
		name := p.Nodes[i]
		TrackDisposer(rec, "the warm daemon on "+name, d.stop)
		p.Daemons = append(p.Daemons, d)
	}
	return nil
}

func (p WarmRollPlan) publish() *discoveryv1.EndpointSlice {
	ready, tcp, port := true, corev1.ProtocolTCP, int32(p.Port)
	var endpoints []discoveryv1.Endpoint
	for i, address := range p.Addresses {
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses: []string{address}, NodeName: &p.Nodes[i],
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "artifact-daemon-warm", Namespace: p.Namespace,
			Labels: map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"}},
		AddressType: discoveryv1.AddressTypeIPv4, Endpoints: endpoints,
		Ports: []discoveryv1.EndpointPort{{Port: &port, Protocol: &tcp}},
	}
}

// Ownership is checked twice: the production probe names an owner, and that
// owner's actual daemon storage must contain the exact durable payload.
func (p WarmRollPlan) warm(cacheKey, durableKey string) (WarmRollPlan, error) {
	want, described := p.Expected[durableKey]
	if !described {
		return p, fmt.Errorf("no durable object %q was described", durableKey)
	}
	_, found := p.Backend.FindResourceCache(p.Ctx, cacheKey, durableKey, "k8s-worker-1")
	obs := warmObservation{Served: found}
	if found {
		probe, hit := p.Client.ProbeResourceCache(p.Ctx, cacheKey)
		if hit {
			obs.Node = probe.Node
		}
		var owners []string
		for i, d := range p.Daemons {
			body, err := os.ReadFile(filepath.Join(d.Root, "steps", cacheKey, "payload.txt"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return p, fmt.Errorf("read warmed payload: %w", err)
			}
			if string(body) != want {
				return p, fmt.Errorf("node %s warmed %q, want %q", p.Nodes[i], body, want)
			}
			owners = append(owners, p.Nodes[i])
		}
		if len(owners) != 1 || owners[0] != obs.Node {
			return p, fmt.Errorf("warm probe named %q, actual payload owners are %v", obs.Node, owners)
		}
	}
	p.Observed = append(append([]warmObservation{}, p.Observed...), obs)
	return p, nil
}

func (p WarmRollPlan) reclaim(key string) error {
	if !filepath.IsLocal(key) || filepath.Base(key) != key || key == "." {
		return fmt.Errorf("invalid cache key to reclaim: %q", key)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if len(p.Daemons) == 0 {
		return fmt.Errorf("no daemons to reclaim")
	}
	for _, d := range p.Daemons {
		req, err := http.NewRequestWithContext(p.Ctx, http.MethodDelete, d.URL+"/artifacts/steps/"+url.PathEscape(key), nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("reclaim cache: HTTP %d", resp.StatusCode)
		}
		if _, err := os.Stat(filepath.Join(d.Root, "steps", key)); !os.IsNotExist(err) {
			return fmt.Errorf("cache %q was not reclaimed: %v", key, err)
		}
	}
	return nil
}

func daemonWarmDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, WarmRollPlan](
			"artifact daemons on two nodes with one durable store behind them",
			[]string{"real-cluster"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (WarmRollPlan, error) {
				return newWarmRollPlan(res, rec)
			}),
		Transform[WarmRollPlan, WarmRollPlan]("only the durable store holds the object {string} containing {string}",
			func(in WarmRollPlan, a Args) (WarmRollPlan, error) {
				key := a.String(0)
				if !filepath.IsLocal(key) {
					return in, fmt.Errorf("durable fixture key escapes its owned store: %q", key)
				}
				body, err := plainTarOfOneFile("payload.txt", a.String(1))
				if err != nil {
					return in, err
				}
				// Seed a real FS-store object; the daemon performs validation and restore.
				path := filepath.Join(in.StorePath, key)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return in, err
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					return in, err
				}
				in.Expected[a.String(0)] = a.String(1)
				return in, nil
			}),
		Transform[WarmRollPlan, WarmRollPlan]("a get step warms the resource cache {string} under content key {string}",
			func(in WarmRollPlan, a Args) (WarmRollPlan, error) { return in.warm(a.String(0), a.String(1)) }),
		Transform[WarmRollPlan, WarmRollPlan]("every node's local copy of {string} is reclaimed",
			func(in WarmRollPlan, a Args) (WarmRollPlan, error) {
				if err := in.reclaim(a.String(0)); err != nil {
					return in, err
				}
				in.ReclaimedKey = a.String(0)
				return in, nil
			}),
		brine.DefineMap[WarmRollPlan, WarmRollPlan](
			"the DaemonSet rolls and every pod comes back answering on a different address",
			func(in WarmRollPlan, _ brine.Params, rec *brine.Recorder) (WarmRollPlan, error) {
				if in.ReclaimedKey == "" {
					return in, fmt.Errorf("reclaim the local cache before rolling its daemons")
				}
				for _, d := range in.Daemons {
					if _, err := os.Stat(filepath.Join(d.Root, "steps", in.ReclaimedKey)); !os.IsNotExist(err) {
						return in, fmt.Errorf("local cache still exists before roll: %v", err)
					}
					if err := d.stop(); err != nil {
						return in, err
					}
				}
				in.Addresses = []string{in.Addresses[1], in.Addresses[0]}
				if err := in.startDaemons(rec); err != nil {
					return in, err
				}
				// The rolled group may have come back on a different port.
				in.wireClient()
				slices := in.Cluster.DiscoveryV1().EndpointSlices(in.Namespace)
				old, err := slices.Get(in.Ctx, in.publish().Name, metav1.GetOptions{})
				if err != nil {
					return in, err
				}
				next := in.publish()
				next.ResourceVersion = old.ResourceVersion
				if _, err := slices.Update(in.Ctx, next, metav1.UpdateOptions{}); err != nil {
					return in, err
				}
				return in, nil
			}),
		CheckThat[WarmRollPlan]("every warm was served",
			func(in WarmRollPlan) error {
				if len(in.Observed) == 0 {
					return fmt.Errorf("no warm was attempted")
				}
				for i, obs := range in.Observed {
					if !obs.Served {
						return fmt.Errorf("warm %d was not served from the durable store", i+1)
					}
				}
				return nil
			}),
		CheckThat[WarmRollPlan]("both warms left the cache on the same node",
			func(in WarmRollPlan) error {
				if len(in.Observed) != 2 {
					return fmt.Errorf("expected two warms to compare, got %d", len(in.Observed))
				}
				for i, obs := range in.Observed {
					if obs.Node == "" {
						return fmt.Errorf("warm %d left the cache on no node the cluster can name, so ownership is unobservable", i+1)
					}
				}
				if in.Observed[0].Node != in.Observed[1].Node {
					return fmt.Errorf("the cache moved from %s to %s across a pod-address roll; every warmed copy in the cluster moves with it",
						in.Observed[0].Node, in.Observed[1].Node)
				}
				return nil
			}),
	}
}
