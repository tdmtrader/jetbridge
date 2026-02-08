package k8sruntime

import (
	"context"
	"fmt"
	"time"

	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// heartbeatTTL is the time-to-live for a K8s worker registration.
	// The worker must heartbeat before this expires or it will be reaped.
	heartbeatTTL = 30 * time.Second

	// workerLabelKey is the Pod label used to identify Pods managed by a
	// particular Concourse K8s worker.
	workerLabelKey = "concourse.ci/worker"
)

// Registrar handles registering and heartbeating a K8s-backed worker in the
// Concourse database. Unlike Garden workers, K8s workers do not use TSA SSH
// tunnels; instead they write directly to the DB via the WorkerFactory.
type Registrar struct {
	clientset     kubernetes.Interface
	cfg           Config
	workerFactory db.WorkerFactory
}

// NewRegistrar creates a Registrar that will register a K8s worker using the
// given clientset, config, and worker factory.
func NewRegistrar(clientset kubernetes.Interface, cfg Config, workerFactory db.WorkerFactory) *Registrar {
	return &Registrar{
		clientset:     clientset,
		cfg:           cfg,
		workerFactory: workerFactory,
	}
}

// WorkerName returns the deterministic name for this K8s worker, derived from
// the configured namespace.
func (r *Registrar) WorkerName() string {
	return fmt.Sprintf("k8s-%s", r.cfg.Namespace)
}

// Register saves the K8s worker to the database. It counts active Pods in the
// namespace to report the current container count.
func (r *Registrar) Register(ctx context.Context) error {
	activeContainers, err := r.countActivePods(ctx)
	if err != nil {
		return fmt.Errorf("counting active pods: %w", err)
	}

	worker := atc.Worker{
		Name:             r.WorkerName(),
		Platform:         "linux",
		State:            "running",
		Version:          concourse.WorkerVersion,
		GardenAddr:       "",
		BaggageclaimURL:  fmt.Sprintf("kubernetes://%s", r.cfg.Namespace),
		ActiveContainers: activeContainers,
		ResourceTypes:    r.resourceTypes(),
	}

	_, err = r.workerFactory.SaveWorker(worker, heartbeatTTL)
	if err != nil {
		return fmt.Errorf("saving worker: %w", err)
	}

	return nil
}

// Run implements component.Runnable. It registers the worker and refreshes
// its TTL on each invocation.
func (r *Registrar) Run(ctx context.Context) error {
	return r.Register(ctx)
}

// Heartbeat refreshes the worker's TTL in the database by re-saving it.
func (r *Registrar) Heartbeat(ctx context.Context) error {
	return r.Register(ctx)
}

// resourceTypes builds the list of base resource types that this K8s worker
// supports. Each entry maps a Concourse resource type name to its Docker image.
func (r *Registrar) resourceTypes() []atc.WorkerResourceType {
	images := r.cfg.ResourceTypeImages
	if images == nil {
		images = DefaultResourceTypeImages
	}

	var types []atc.WorkerResourceType
	for typeName, image := range images {
		types = append(types, atc.WorkerResourceType{
			Type:    typeName,
			Image:   image,
			Version: "1.0.0",
		})
	}
	return types
}

// countActivePods returns the number of Pods in the namespace that are
// labelled as belonging to this worker.
func (r *Registrar) countActivePods(ctx context.Context) (int, error) {
	labelSelector := fmt.Sprintf("%s=%s", workerLabelKey, r.WorkerName())
	pods, err := r.clientset.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return 0, fmt.Errorf("listing pods: %w", err)
	}
	return len(pods.Items), nil
}
