package k8sruntime

import (
	"context"
	"fmt"
	"path/filepath"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/tracing"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Reaper implements the GC sweep loop for K8s-backed containers and volumes.
// It reports active pods to the DB, deletes pods that the DB has marked as
// "destroying", and cleans up PVC cache subdirectories for destroying volumes.
type Reaper struct {
	logger              lager.Logger
	clientset           kubernetes.Interface
	cfg                 Config
	containerRepository db.ContainerRepository
	destroyer           gc.Destroyer
	volumeRepository    db.VolumeRepository
	executor            PodExecutor
}

// NewReaper creates a Reaper that will manage pod lifecycle using the given
// K8s clientset, config, container repository, and destroyer.
func NewReaper(
	logger lager.Logger,
	clientset kubernetes.Interface,
	cfg Config,
	containerRepository db.ContainerRepository,
	destroyer gc.Destroyer,
) *Reaper {
	return &Reaper{
		logger:              logger,
		clientset:           clientset,
		cfg:                 cfg,
		containerRepository: containerRepository,
		destroyer:           destroyer,
	}
}

// SetVolumeRepo sets the VolumeRepository used for cache volume cleanup.
func (r *Reaper) SetVolumeRepo(repo db.VolumeRepository) {
	r.volumeRepository = repo
}

// SetExecutor sets the PodExecutor used for cache directory cleanup.
func (r *Reaper) SetExecutor(executor PodExecutor) {
	r.executor = executor
}

// Run implements component.Runnable. It reports active pods to the DB,
// deletes pods that the DB has marked for destruction, and cleans up
// PVC cache subdirectories for destroying volumes.
func (r *Reaper) Run(ctx context.Context) error {
	logger := r.logger.Session("run")

	workerName := fmt.Sprintf("k8s-%s", r.cfg.Namespace)

	ctx, span := tracing.StartSpan(ctx, "k8s.reaper.run", tracing.Attrs{
		"worker-name": workerName,
		"namespace":   r.cfg.Namespace,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// List all pods belonging to this worker.
	labelSelector := fmt.Sprintf("%s=%s", workerLabelKey, workerName)
	pods, err := r.clientset.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		logger.Error("failed-to-list-pods", err)
		spanErr = err
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
		logger.Error("failed-to-update-containers-missing-since", err)
		spanErr = err
		return fmt.Errorf("updating containers missing since: %w", err)
	}

	// Trigger DB-side container destruction for containers whose runtime
	// resource (pod) no longer exists.
	err = r.destroyer.DestroyContainers(workerName, handles)
	if err != nil {
		logger.Error("failed-to-destroy-containers", err)
		spanErr = err
		return fmt.Errorf("destroying containers: %w", err)
	}

	// Find containers the DB has marked for destruction and delete their pods.
	destroying, err := r.containerRepository.FindDestroyingContainers(workerName)
	if err != nil {
		logger.Error("failed-to-find-destroying-containers", err)
		spanErr = err
		return fmt.Errorf("finding destroying containers: %w", err)
	}

	for _, handle := range destroying {
		err := r.clientset.CoreV1().Pods(r.cfg.Namespace).Delete(ctx, handle, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			logger.Error("failed-to-delete-pod", err, lager.Data{"handle": handle})
			spanErr = err
			return fmt.Errorf("deleting pod %s: %w", handle, err)
		}
	}

	// Clean up PVC cache subdirectories for destroying volumes.
	err = r.cleanupCacheVolumes(ctx, logger, workerName, handles)
	if err != nil {
		logger.Error("failed-to-cleanup-cache-volumes", err)
		spanErr = err
		return fmt.Errorf("cleaning up cache volumes: %w", err)
	}

	return nil
}

// cleanupCacheVolumes removes PVC subdirectories for volumes in "destroying"
// state by exec-ing rm -rf inside a running pod. Volumes whose directories
// are successfully removed are then deleted from the DB.
func (r *Reaper) cleanupCacheVolumes(ctx context.Context, logger lager.Logger, workerName string, activePods []string) error {
	if r.cfg.CacheVolumeClaim == "" || r.volumeRepository == nil {
		return nil
	}

	destroyingVolumes, err := r.volumeRepository.GetDestroyingVolumes(workerName)
	if err != nil {
		return fmt.Errorf("finding destroying volumes: %w", err)
	}

	if len(destroyingVolumes) == 0 {
		return nil
	}

	if len(activePods) == 0 || r.executor == nil {
		logger.Info("skipping-volume-cleanup-no-active-pods")
		return nil
	}

	// Use the first active pod as the exec target for cache cleanup.
	podName := activePods[0]
	var failedHandles []string

	for _, handle := range destroyingVolumes {
		cachePath := filepath.Join(CacheBasePath, handle)
		cmd := []string{"rm", "-rf", cachePath}
		err := r.executor.ExecInPod(ctx, r.cfg.Namespace, podName, mainContainerName, cmd, nil, nil, nil, false)
		if err != nil {
			logger.Error("failed-to-cleanup-cache-volume", err, lager.Data{"handle": handle})
			failedHandles = append(failedHandles, handle)
		}
	}

	_, err = r.volumeRepository.RemoveDestroyingVolumes(workerName, failedHandles)
	if err != nil {
		return fmt.Errorf("removing cleaned volumes from db: %w", err)
	}

	return nil
}
