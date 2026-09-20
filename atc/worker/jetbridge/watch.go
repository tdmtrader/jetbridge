package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// ErrPodDeleted is returned by PodWatcher.Next() when a watch.Deleted event
// is received, indicating the pod was removed externally (eviction, node
// failure, manual deletion, etc.).
var ErrPodDeleted = errors.New("pod was deleted")

// WatchPod starts a Kubernetes Watch on a specific pod identified by name
// within the given namespace. The watch uses a field selector
// (metadata.name=<podName>) to receive events only for that pod. If
// resourceVersion is non-empty, the watch resumes from that version to avoid
// missing events after a reconnection.
func WatchPod(ctx context.Context, clientset kubernetes.Interface, namespace, podName, resourceVersion string) (watch.Interface, error) {
	opts := metav1.ListOptions{
		FieldSelector:   fmt.Sprintf("metadata.name=%s", podName),
		ResourceVersion: resourceVersion,
	}
	return clientset.CoreV1().Pods(namespace).Watch(ctx, opts)
}

// PodWatcher wraps the Kubernetes Watch API for a single pod, providing
// automatic reconnection when the watch channel closes and fallback to
// a single Get() call when history expires or watch re-establishment fails
// consecutively.
type PodWatcher struct {
	mu                  sync.Mutex
	clientset           kubernetes.Interface
	namespace           string
	podName             string
	lastResourceVersion string
	watcher             watch.Interface
	stopped             bool
	initialPod          *corev1.Pod // Cached initial state from first Get()
}

// NewPodWatcher creates a PodWatcher for the given pod. The watch is lazily
// established on the first call to Next().
func NewPodWatcher(clientset kubernetes.Interface, namespace, podName string) *PodWatcher {
	return &PodWatcher{
		clientset: clientset,
		namespace: namespace,
		podName:   podName,
	}
}

// Stop stops the underlying watch. After Stop(), Next() must not be called.
func (pw *PodWatcher) Stop() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.stopped = true
	if pw.watcher != nil {
		pw.watcher.Stop()
		pw.watcher = nil
	}
}

// Next blocks until the next pod event is received and returns the pod. It
// transparently handles watch reconnection: if the watch channel closes, it
// re-establishes the watch using the last observed resourceVersion. If watch
// re-establishment fails consecutively (up to maxConsecutiveAPIErrors), it
// falls back to a single Get() to retrieve the current pod state.
//
// On the first call, Next() does a Get() to retrieve the current pod state
// and returns it immediately. This ensures we don't miss state changes that
// occurred before the watch was established.
func (pw *PodWatcher) Next(ctx context.Context) (*corev1.Pod, error) {
	pw.mu.Lock()
	needsInitialSync := pw.initialPod == nil && pw.watcher == nil && pw.lastResourceVersion == ""
	pw.mu.Unlock()

	// On first call, do a Get() to sync current state. This handles the case
	// where the pod already completed before we started watching.
	if needsInitialSync {
		consecutiveErrors := 0
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			pod, err := pw.clientset.CoreV1().Pods(pw.namespace).Get(ctx, pw.podName, metav1.GetOptions{})
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveAPIErrors {
					return nil, fmt.Errorf("%d consecutive API errors during initial sync: %w", consecutiveErrors, err)
				}
				continue
			}
			pw.mu.Lock()
			pw.initialPod = pod
			pw.lastResourceVersion = pod.ResourceVersion
			pw.mu.Unlock()
			return pod, nil
		}
	}

	consecutiveWatchErrors := 0

	for {
		// Check context before any operation.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Establish watch if needed.
		pw.mu.Lock()
		if pw.watcher == nil {
			w, err := WatchPod(ctx, pw.clientset, pw.namespace, pw.podName, pw.lastResourceVersion)
			if err != nil {
				pw.mu.Unlock()
				if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
					return pw.getPod(ctx)
				}
				consecutiveWatchErrors++
				if consecutiveWatchErrors >= maxConsecutiveAPIErrors {
					// Fall back to a single Get().
					return pw.getPod(ctx)
				}
				continue
			}
			pw.watcher = w
			consecutiveWatchErrors = 0
		}
		ch := pw.watcher.ResultChan()
		pw.mu.Unlock()

		// Read from the watch channel.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case event, ok := <-ch:
			if !ok {
				// Channel closed — watch disconnected. Clean up and retry.
				pw.mu.Lock()
				pw.watcher = nil
				pw.mu.Unlock()
				continue
			}

			if event.Type == watch.Error {
				err := apierrors.FromObject(event.Object)
				if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
					// An expired stream is terminal. Reconnecting at the same
					// version can never recover; refresh before watching again.
					pw.mu.Lock()
					if pw.watcher != nil {
						pw.watcher.Stop()
						pw.watcher = nil
					}
					pw.mu.Unlock()
					return pw.getPod(ctx)
				}
			}

			pod, isPod := event.Object.(*corev1.Pod)
			if !isPod {
				// Skip non-pod events (e.g., Status objects on error).
				continue
			}

			// Track resourceVersion for reconnection.
			pw.mu.Lock()
			pw.lastResourceVersion = pod.ResourceVersion
			pw.mu.Unlock()

			// A Deleted event means the pod was removed externally
			// (eviction, node failure, manual deletion). Signal this
			// to the caller so it can fail the build gracefully.
			if event.Type == watch.Deleted {
				return pod, ErrPodDeleted
			}

			return pod, nil
		}
	}
}

// getPod reads the current pod state once. This is the fallback when the
// watch history has expired or watch re-establishment keeps failing.
//
// If the pod's resourceVersion advanced, the next watch resumes from it to
// replay events from the checkpoint we just observed. If it is unchanged,
// it is the version the apiserver just called expired, so the next watch
// omits resourceVersion to resume from most recent. An advanced version that
// is itself already expired costs one more 410 round trip; an unchanged
// recovery read then clears it, preventing a loop. The read itself stays a
// GET of the named pod: that is the request shape the runtime's RBAC is
// granted for and the one the live scenarios inject faults against.
func (pw *PodWatcher) getPod(ctx context.Context) (*corev1.Pod, error) {
	pod, err := pw.clientset.CoreV1().Pods(pw.namespace).Get(ctx, pw.podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fallback Get() failed: %w", err)
	}
	pw.mu.Lock()
	if pod.ResourceVersion != pw.lastResourceVersion {
		pw.lastResourceVersion = pod.ResourceVersion
	} else {
		pw.lastResourceVersion = ""
	}
	// Reset watcher so the next call to Next() tries to re-establish.
	pw.watcher = nil
	pw.mu.Unlock()
	return pod, nil
}
