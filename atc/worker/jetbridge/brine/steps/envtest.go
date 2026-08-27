package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// A REAL Kubernetes API server, for scenarios whose subject is the API's own
// behaviour rather than the runtime's.
//
// The rest of this package drives client-go's fake.NewSimpleClientset, and for
// most scenarios that is fine: they set pod status by hand and assert what the
// runtime did with it, so the API is a store, not a participant.
//
// It is NOT fine when the API's own semantics are the thing under test. The
// fake does not honour FIELD SELECTORS, which is the entire content of PW-03 —
// "a step is never told about somebody else's pod". Covering that against the
// fake required WatchBus: a hand-written reimplementation of API-server
// selector filtering, buffering pre-connect events and applying the runtime's
// own selector at delivery. That is a double written to compensate for a
// weaker double, and it can only ever be as correct as my model of the API.
//
// envtest runs the real kube-apiserver and etcd as local binaries — no
// containers, so it fits this tier rather than the 23-minute K3s one. There is
// no kubelet and no scheduler, so pods never actually run; scenarios still
// drive status by hand exactly as they do today. What changes is that watch,
// field selectors, resourceVersion and validation are the real
// implementations.

type realCluster struct {
	env       *envtest.Environment
	Clientset kubernetes.Interface
}

// envtestAssets locates the kube-apiserver/etcd binaries setup-envtest placed.
// Returning "" means the assets are absent and the resource must say so
// plainly rather than hang trying to start a control plane that is not there.
func envtestAssets() string {
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(home, ".envtest", "k8s", "*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func RealClusterResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{
		Name:  "real-cluster",
		Scope: brine.ScopeSuite,
		Factory: func(map[string]any) (any, error) {
			assets := envtestAssets()
			if assets == "" {
				return nil, fmt.Errorf(
					"no envtest assets: run `setup-envtest use --bin-dir ~/.envtest` " +
						"or set KUBEBUILDER_ASSETS")
			}
			env := &envtest.Environment{
				BinaryAssetsDirectory:    assets,
				ControlPlaneStartTimeout: 60 * time.Second,
				ControlPlaneStopTimeout:  30 * time.Second,
			}
			cfg, err := env.Start()
			if err != nil {
				return nil, fmt.Errorf("start real control plane: %w", err)
			}
			clientset, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				_ = env.Stop()
				return nil, fmt.Errorf("build clientset for real control plane: %w", err)
			}
			return &realCluster{env: env, Clientset: clientset}, nil
		},
		Disposer: func(value any) error {
			rc, ok := value.(*realCluster)
			if !ok {
				return fmt.Errorf("real-cluster disposer got %T", value)
			}
			return rc.env.Stop()
		},
	}
}
