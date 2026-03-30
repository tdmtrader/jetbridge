package jetbridge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	artifactLocator     *ArtifactLocator
	nodeIPResolver      *NodeIPResolver
	httpClient          *http.Client
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
		nodeIPResolver:      NewNodeIPResolver(clientset),
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

// SetArtifactLocator sets the ArtifactLocator for DaemonSet cleanup.
func (r *Reaper) SetArtifactLocator(locator *ArtifactLocator) {
	r.artifactLocator = locator
	r.httpClient = &http.Client{Timeout: 10 * time.Second}
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

	// Proactively delete completed pods (those with exit-status annotation).
	// This provides fast cleanup without waiting for the full DB GC cycle,
	// ensuring check pods and completed build pods are reaped promptly.
	var remainingPods []metav1.ObjectMeta
	for _, pod := range pods.Items {
		if _, hasExitStatus := pod.Annotations[exitStatusAnnotationKey]; hasExitStatus {
			if err := r.clientset.CoreV1().Pods(r.cfg.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error("failed-to-cleanup-completed-pod", err, lager.Data{"pod": pod.Name})
				}
			}
			continue
		}
		remainingPods = append(remainingPods, pod.ObjectMeta)
	}

	// Collect container handles from remaining (non-completed) pods.
	// When pods have a concourse.ci/handle label (readable pod names),
	// use that as the DB handle. Otherwise fall back to pod.Name for
	// backward compatibility.
	handles := make([]string, len(remainingPods))
	handleToPodName := make(map[string]string, len(remainingPods))
	activePodNames := make([]string, len(remainingPods))
	for i, podMeta := range remainingPods {
		handle := podMeta.Name
		if h, ok := podMeta.Labels[handleLabelKey]; ok && h != "" {
			handle = h
		}
		handles[i] = handle
		handleToPodName[handle] = podMeta.Name
		activePodNames[i] = podMeta.Name
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

	// Insert "destroying" records for pods that exist in K8s but have no
	// matching DB container record (orphaned pods). This happens when
	// in-memory check builds finish and their container records are
	// transitioned before the Reaper runs.
	_, err = r.containerRepository.DestroyUnknownContainers(workerName, handles)
	if err != nil {
		logger.Error("failed-to-destroy-unknown-containers", err)
		spanErr = err
		return fmt.Errorf("destroying unknown containers: %w", err)
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

	// Clean up artifact store entries for destroyed containers.
	r.cleanupArtifactStoreEntries(ctx, logger, destroying)

	return nil
}

// cleanupArtifactStoreEntries removes artifacts from the DaemonSet for
// destroyed containers. Best-effort — failures are logged but don't block GC.
func (r *Reaper) cleanupArtifactStoreEntries(ctx context.Context, logger lager.Logger, handles []string) {
	r.cleanupDaemonSetArtifacts(ctx, logger, handles)
}

// cleanupDaemonSetArtifacts sends HTTP DELETE requests to DaemonSet pods
// for destroyed container artifacts. Best-effort — failures are logged
// but don't block GC.
func (r *Reaper) cleanupDaemonSetArtifacts(ctx context.Context, logger lager.Logger, handles []string) {
	if len(handles) == 0 || r.artifactLocator == nil || r.httpClient == nil {
		return
	}

	port := r.cfg.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	for _, handle := range handles {
		if strings.HasPrefix(handle, "/") || strings.Contains(handle, "..") || handle == "" {
			continue
		}
		key := ArtifactKey(handle)
		sourceNode, found := r.artifactLocator.LocateNode(key)
		if !found {
			continue
		}

		if r.nodeIPResolver == nil {
			logger.Error("no-node-ip-resolver", nil, lager.Data{"handle": handle})
			continue
		}

		nodeIP, err := r.nodeIPResolver.Resolve(ctx, sourceNode)
		if err != nil {
			logger.Error("failed-to-resolve-node-ip", err, lager.Data{"node": sourceNode, "handle": handle})
			continue
		}

		// DELETE the step directory (not a tar file).
		url := fmt.Sprintf("http://%s:%d/artifacts/steps/%s",
			nodeIP, port, handle)

		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
		if err != nil {
			logger.Error("failed-to-create-delete-request", err, lager.Data{"handle": handle})
			continue
		}

		resp, err := r.httpClient.Do(req)
		if err != nil {
			logger.Error("failed-to-delete-artifact", err, lager.Data{"handle": handle, "node": sourceNode})
		} else {
			resp.Body.Close()
		}

		r.artifactLocator.Remove(key)
	}
}
