package jetbridge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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

	// Collect container handles from pods. When pods have a
	// concourse.ci/handle label (readable pod names), use that as the DB
	// handle. Otherwise fall back to pod.Name for backward compatibility.
	handles := make([]string, len(pods.Items))
	handleToPodName := make(map[string]string, len(pods.Items))
	activePodNames := make([]string, len(pods.Items))
	for i, pod := range pods.Items {
		handle := pod.Name
		if h, ok := pod.Labels[handleLabelKey]; ok && h != "" {
			handle = h
		}
		handles[i] = handle
		handleToPodName[handle] = pod.Name
		activePodNames[i] = pod.Name
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
		// Look up the actual pod name from the handle→podName map.
		// If the handle isn't in the map (pod already gone), try
		// deleting by handle directly for backward compatibility.
		podName := handle
		if name, ok := handleToPodName[handle]; ok {
			podName = name
		}
		err := r.clientset.CoreV1().Pods(r.cfg.Namespace).Delete(ctx, podName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			logger.Error("failed-to-delete-pod", err, lager.Data{"handle": handle, "pod": podName})
			spanErr = err
			return fmt.Errorf("deleting pod %s: %w", podName, err)
		}
	}

	// Clean up PVC cache subdirectories for destroying volumes.
	err = r.cleanupCacheVolumes(ctx, logger, workerName, activePodNames)
	if err != nil {
		logger.Error("failed-to-cleanup-cache-volumes", err)
		spanErr = err
		return fmt.Errorf("cleaning up cache volumes: %w", err)
	}

	// Clean up artifact store entries for destroyed containers.
	r.cleanupArtifactStoreEntries(ctx, logger, destroying, activePodNames)

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
		// Validate handle to prevent path traversal (e.g. "../../etc").
		if strings.Contains(handle, "/") || strings.Contains(handle, "..") || handle == "" {
			logger.Info("skipping-invalid-volume-handle", lager.Data{"handle": handle})
			failedHandles = append(failedHandles, handle)
			continue
		}
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

// cleanupArtifactStoreEntries removes artifact tar files from the artifact
// store PVC for destroyed containers. Execs rm in the artifact-helper sidecar
// of an active pod. Best-effort — failures are logged but don't block GC.
func (r *Reaper) cleanupArtifactStoreEntries(ctx context.Context, logger lager.Logger, handles []string, activePods []string) {
	if r.cfg.ArtifactStoreClaim == "" || r.executor == nil {
		return
	}
	if len(handles) == 0 || len(activePods) == 0 {
		return
	}

	podName := activePods[0]

	for _, handle := range handles {
		if strings.Contains(handle, "/") || strings.Contains(handle, "..") || handle == "" {
			continue
		}
		artifactPath := filepath.Join(ArtifactMountPath, ArtifactKey(handle))
		cmd := []string{"rm", "-f", artifactPath}
		err := r.executor.ExecInPod(ctx, r.cfg.Namespace, podName, artifactHelperContainerName, cmd, nil, nil, nil, false)
		if err != nil {
			logger.Error("failed-to-cleanup-artifact", err, lager.Data{"handle": handle})
		}
	}
}
