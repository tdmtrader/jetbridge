package jetbridge

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Reaper implements the GC sweep loop for K8s-backed containers and volumes.
// It reports active pods to the DB, deletes pods that the DB has marked as
// "destroying", and cleans up PVC cache subdirectories for destroying volumes.
// RunningBuildLookup reports the builds the ATC still considers in flight.
//
// The reaper needs it to tell a completed step whose build is over from one
// whose build is still running. db.BuildFactory satisfies it.
type RunningBuildLookup interface {
	GetAllStartedBuilds() ([]db.Build, error)
}

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
	buildLookup         RunningBuildLookup
}

// SetBuildLookup sets the source of truth for which builds are still running.
// Without it the reaper cannot prove a completed pod is finished with, so it
// retains every such pod and leaves them to the DB-driven destroying path.
func (r *Reaper) SetBuildLookup(lookup RunningBuildLookup) {
	r.buildLookup = lookup
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
	r.httpClient = newDaemonHTTPClient(r.cfg, 10*time.Second)
}

// Run implements component.Runnable. It reports active pods to the DB,
// deletes pods that the DB has marked for destruction, and cleans up
// PVC cache subdirectories for destroying volumes.
func (r *Reaper) Run(ctx context.Context) error {
	logger := r.logger.Session("run")

	workerName := fmt.Sprintf("k8s-%s", r.cfg.Namespace)

	// List all pods belonging to this worker.
	labelSelector := fmt.Sprintf("%s=%s", workerLabelKey, workerName)
	pods, err := r.clientset.CoreV1().Pods(r.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		logger.Error("failed-to-list-pods", err)
		return fmt.Errorf("listing pods: %w", err)
	}

	// Proactively delete completed pods (those with exit-status annotation)
	// so check pods and finished builds' pods are reaped without waiting for
	// the full DB GC cycle -- but only once nothing can still need to read
	// that annotation. It is the only durable record of a step's result:
	// engineBuild.Run deliberately leaves job builds "started" on drain so
	// the next web re-attaches, and Container.Attach recovers the result from
	// this annotation. Deleting it while the build is still in flight makes
	// the resumed plan re-execute an already completed step.
	var remainingPods, completedPods []metav1.ObjectMeta
	for _, pod := range pods.Items {
		if _, hasExitStatus := pod.Annotations[exitStatusAnnotationKey]; hasExitStatus {
			completedPods = append(completedPods, pod.ObjectMeta)
			continue
		}
		remainingPods = append(remainingPods, pod.ObjectMeta)
	}

	reapNow, retained := r.splitCompletedPods(logger, completedPods)
	for _, podMeta := range reapNow {
		if err := r.clientset.CoreV1().Pods(r.cfg.Namespace).Delete(ctx, podMeta.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error("failed-to-cleanup-completed-pod", err, lager.Data{"pod": podMeta.Name})
			}
		}
	}
	// A retained pod is still present in K8s, so it has to be reported as
	// active below. Otherwise UpdateContainersMissingSince marks its
	// container missing and RemoveDestroyingContainers drops the DB row
	// while the pod -- and the annotation -- are still there.
	remainingPods = append(remainingPods, retained...)

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
		return fmt.Errorf("updating containers missing since: %w", err)
	}

	// Trigger DB-side container destruction for containers whose runtime
	// resource (pod) no longer exists.
	err = r.destroyer.DestroyContainers(workerName, handles)
	if err != nil {
		logger.Error("failed-to-destroy-containers", err)
		return fmt.Errorf("destroying containers: %w", err)
	}

	// Insert "destroying" records for pods that exist in K8s but have no
	// matching DB container record (orphaned pods). This happens when
	// in-memory check builds finish and their container records are
	// transitioned before the Reaper runs.
	_, err = r.containerRepository.DestroyUnknownContainers(workerName, handles)
	if err != nil {
		logger.Error("failed-to-destroy-unknown-containers", err)
		return fmt.Errorf("destroying unknown containers: %w", err)
	}

	// Find containers the DB has marked for destruction and delete their pods.
	destroying, err := r.containerRepository.FindDestroyingContainers(workerName)
	if err != nil {
		logger.Error("failed-to-find-destroying-containers", err)
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
		url := fmt.Sprintf("%s://%s:%d/artifacts/steps/%s",
			daemonURLScheme(r.cfg), nodeIP, port, handle)

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

// splitCompletedPods divides pods carrying the exit-status annotation into
// those that can be deleted now and those that must survive this sweep.
//
// A completed pod is safe to delete once its owning build is no longer
// running: nothing will re-attach to it, so the annotation has no reader
// left. Pods with no owning build at all -- in-memory check builds, whose
// builds are finished on drain and never resumed, and the resource-type
// get/put steps they spawn -- carry no build-id label and are always safe.
//
// When the running-build set cannot be determined the split is fail-closed:
// every completed pod is retained and falls through to the DB-driven
// destroying path, which still reaps it once GC marks its container. Erring
// toward a pod that lingers is cheap; erring toward a deleted annotation
// costs a re-executed step.
func (r *Reaper) splitCompletedPods(logger lager.Logger, completed []metav1.ObjectMeta) (reap, retain []metav1.ObjectMeta) {
	if len(completed) == 0 {
		return nil, nil
	}

	ownedByBuild := false
	for _, podMeta := range completed {
		if podMeta.Labels[buildIDLabelKey] != "" {
			ownedByBuild = true
			break
		}
	}

	running := map[string]bool{}
	if ownedByBuild {
		if r.buildLookup == nil {
			logger.Debug("retaining-completed-pods-without-build-lookup", lager.Data{"pods": len(completed)})
			return nil, completed
		}
		builds, err := r.buildLookup.GetAllStartedBuilds()
		if err != nil {
			logger.Error("failed-to-list-started-builds", err)
			return nil, completed
		}
		for _, build := range builds {
			running[strconv.Itoa(build.ID())] = true
		}
	}

	for _, podMeta := range completed {
		// A missing label yields "", which is never a running build id.
		if running[podMeta.Labels[buildIDLabelKey]] {
			retain = append(retain, podMeta)
			continue
		}
		reap = append(reap, podMeta)
	}
	return reap, retain
}
