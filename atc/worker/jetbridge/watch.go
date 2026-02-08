package jetbridge

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

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
// a single Get() call when watch re-establishment fails consecutively.
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

			pod, isPod := event.Object.(*corev1.Pod)
			if !isPod {
				// Skip non-pod events (e.g., Status objects on error).
				continue
			}

			// Track resourceVersion for reconnection.
			pw.mu.Lock()
			pw.lastResourceVersion = pod.ResourceVersion
			pw.mu.Unlock()
			return pod, nil
		}
	}
}

// getPod does a single Get() call to retrieve the current pod state. This
// is the fallback when watch re-establishment fails.
func (pw *PodWatcher) getPod(ctx context.Context) (*corev1.Pod, error) {
	pod, err := pw.clientset.CoreV1().Pods(pw.namespace).Get(ctx, pw.podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("fallback Get() failed: %w", err)
	}
	pw.mu.Lock()
	pw.lastResourceVersion = pod.ResourceVersion
	// Reset watcher so the next call to Next() tries to re-establish.
	pw.watcher = nil
	pw.mu.Unlock()
	return pod, nil
}
