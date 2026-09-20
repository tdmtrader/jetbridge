package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// A suite-owned kube-apiserver and etcd, run as local binaries. Persistence,
// validation, selectors, resource versions and watches are Kubernetes's own
// implementations; fixtures must not reimplement them in a fake client.
//
// There is no kubelet or scheduler. Creating a pod proves API behavior, not
// container execution. Local watch cases observe metadata, deletion and
// cancellation; lifecycle transitions run against the live kubelet. Registrar
// and reaper scenarios need only the real objects and metadata.

type realCluster struct {
	lazy       *lazyResource[*realCluster]
	env        *envtest.Environment
	Clientset  kubernetes.Interface
	RESTConfig *rest.Config
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
			return &realCluster{lazy: &lazyResource[*realCluster]{start: startRealCluster}}, nil
		},
		Disposer: func(value any) error {
			rc, ok := value.(*realCluster)
			if !ok {
				return fmt.Errorf("real-cluster disposer got %T", value)
			}
			return rc.close()
		},
	}
}

func startRealCluster() (*realCluster, error) {
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
	// The isolated peer topology intentionally has no default route.
	// Its launcher supplies an address actually owned by that namespace.
	if address := os.Getenv("BRINE_PEER_ADDRESS_1"); address != "" {
		env.ControlPlane.GetAPIServer().Configure().Set("advertise-address", address)
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
	return &realCluster{env: env, Clientset: clientset, RESTConfig: cfg}, nil
}

// getRealCluster is the first-use boundary. Callers retain their concrete
// *realCluster and existing field accesses; merely acquiring the suite resource
// (including in the live tier) does not launch a local control plane.
func getRealCluster(resources brine.Resources) (*realCluster, error) {
	cluster, ok := resources.Get("real-cluster").(*realCluster)
	if !ok {
		return nil, fmt.Errorf("real-cluster resource is %T", resources.Get("real-cluster"))
	}
	if cluster.lazy != nil {
		return cluster.lazy.get()
	}
	return cluster, nil
}

func (r *realCluster) close() error {
	if r.lazy != nil {
		return r.lazy.close(func(ready *realCluster) error { return ready.close() })
	}
	return r.env.Stop()
}

// StartRealCluster warms the lazy resource before any local step deadline opens.
func StartRealCluster(resources brine.Resources) error {
	_, err := getRealCluster(resources)
	return err
}
