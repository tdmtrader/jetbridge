package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// liveArtifactLinkDir holds one link per multi-node fixture run on each node,
// named after the run's owned namespace (linkName).
const liveArtifactLinkDir = "/var/lib/brine-artifacts"

// liveArtifactNodes is the live artifact fixture on every approved node at
// once: a store and a daemon per node, all serving one root path -- each
// node's link into its own anchor's emptyDir -- and one service publishing
// every daemon. Production pods over it may land on any of those nodes and
// fetch from any other, which is what a cluster's artifact daemons do.
type liveArtifactNodes struct {
	cluster liveKubernetes
	root    string
	port    int
	stores  []*liveArtifactStore
	daemons []*liveArtifactDaemon
}

// newLiveArtifactNodes needs at least two approved nodes that are up now; a
// run with fewer is an error, never a quiet single-node pass.
func newLiveArtifactNodes(ctx context.Context, rec *brine.Recorder) (*liveArtifactNodes, error) {
	port, bin, err := prepareLiveDaemon()
	if err != nil {
		return nil, err
	}
	defer bin.remove()
	cfg, err := liveKubernetesConfig()
	if err != nil {
		return nil, err
	}
	direct, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	nodes, err := approvedArtifactNodes(ctx, direct)
	if err != nil {
		return nil, err
	}
	if len(nodes) < 2 {
		return nil, fmt.Errorf("the cross-node artifact fixture needs two approved nodes up; found %s", nodeNames(nodes))
	}
	// Anchor, observer, linker and daemon on each node, plus a producer and a
	// consumer task at a time.
	cluster, err := newLiveArtifactCluster(ctx, rec, int64(4*len(nodes)+2), int64(len(nodes)))
	if err != nil {
		return nil, err
	}
	f := &liveArtifactNodes{cluster: cluster, root: filepath.Join(liveArtifactLinkDir, linkName(cluster)), port: port}
	for i, node := range nodes {
		s, err := newLiveArtifactStoreOn(ctx, rec, cluster, node.Name, fmt.Sprintf("-%d", i))
		if err != nil {
			return nil, fmt.Errorf("store on %s: %w", node.Name, err)
		}
		f.stores = append(f.stores, s)
		if err := s.link(ctx, f.root); err != nil {
			return nil, fmt.Errorf("link on %s: %w", node.Name, err)
		}
		d, err := launchLiveDaemon(ctx, rec, s, bin, port, false, true)
		if err != nil {
			return nil, fmt.Errorf("daemon on %s: %w", node.Name, err)
		}
		f.daemons = append(f.daemons, d)
	}
	if err := publishLiveDaemons(ctx, cluster, f.daemons...); err != nil {
		return nil, err
	}
	fmt.Printf("cross-node artifact fixture: root %s on %s\n", f.root, nodeNames(nodes))
	return f, nil
}

// runtimeConfig is a jetbridge config over the fixture whose pods must land on
// node: the root exists on the fixture's nodes alone, and a scenario that
// means to cross nodes says which.
func (f *liveArtifactNodes) runtimeConfig(node string) jetbridge.Config {
	cfg := jetbridge.NewConfig(f.cluster.Namespace, "")
	cfg.ArtifactDaemonService = liveArtifactDaemonService
	cfg.ArtifactDaemonHostPath, cfg.ArtifactDaemonPort = f.root, f.port
	if s, err := f.store(node); err == nil {
		s.pin(&cfg)
	} else {
		cfg.RequiredStepNode = &jetbridge.StepNodeLabel{Key: corev1.LabelHostname, Value: node}
	}
	return cfg
}

// store is the fixture's store on node.
func (f *liveArtifactNodes) store(node string) (*liveArtifactStore, error) {
	for _, s := range f.stores {
		if s.anchor.Spec.NodeName == node {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no fixture store on node %q", node)
}

func nodeNames(nodes []*corev1.Node) string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
