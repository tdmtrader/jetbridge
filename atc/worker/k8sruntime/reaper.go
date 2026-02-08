package k8sruntime

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// Reaper implements the GC sweep loop for K8s-backed containers.
// It reports active pods to the DB and deletes pods that the DB has
// marked as "destroying".
type Reaper struct {
	clientset           kubernetes.Interface
	cfg                 Config
	containerRepository db.ContainerRepository
	destroyer           gc.Destroyer
}

// NewReaper creates a Reaper that will manage pod lifecycle using the given
// K8s clientset, config, container repository, and destroyer.
func NewReaper(
	clientset kubernetes.Interface,
	cfg Config,
	containerRepository db.ContainerRepository,
	destroyer gc.Destroyer,
) *Reaper {
	return &Reaper{
		clientset:           clientset,
		cfg:                 cfg,
		containerRepository: containerRepository,
		destroyer:           destroyer,
	}
}

// Run implements component.Runnable. It reports active pods to the DB and
// deletes pods that the DB has marked for destruction.
func (r *Reaper) Run(ctx context.Context) error {
	workerName := fmt.Sprintf("k8s-%s", r.cfg.Namespace)

	// List all pods belonging to this worker.
	labelSelector := fmt.Sprintf("%s=%s", workerLabelKey, workerName)
	pods, err := r.clientset.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("listing pods: %w", err)
	}

	// Collect pod names as container handles.
	handles := make([]string, len(pods.Items))
	for i, pod := range pods.Items {
		handles[i] = pod.Name
	}

	// Report active containers to the DB. This marks containers not in
	// the list as "missing since now" — the first step toward GC.
	err = r.containerRepository.UpdateContainersMissingSince(workerName, handles)
	if err != nil {
		return fmt.Errorf("updating containers missing since: %w", err)
	}

	// Trigger DB-side container destruction for containers whose runtime
	// resource (pod) no longer exists.
	err = r.destroyer.DestroyContainers(workerName, handles)
	if err != nil {
		return fmt.Errorf("destroying containers: %w", err)
	}

	// Find containers the DB has marked for destruction and delete their pods.
	destroying, err := r.containerRepository.FindDestroyingContainers(workerName)
	if err != nil {
		return fmt.Errorf("finding destroying containers: %w", err)
	}

	for _, handle := range destroying {
		err := r.clientset.CoreV1().Pods(r.cfg.Namespace).Delete(ctx, handle, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting pod %s: %w", handle, err)
		}
	}

	return nil
}
