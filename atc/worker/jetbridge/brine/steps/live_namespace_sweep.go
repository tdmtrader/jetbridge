package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const namespaceRequestTimeout = time.Second
const namespaceCancellationBudget = 3 * time.Second
const liveNamespaceLabel = "app.kubernetes.io/managed-by=brine-runtime-tests"

type ownedLiveNamespace struct {
	name         string
	uid          types.UID
	client       kubernetes.Interface
	deleteLanded bool
}

// liveNamespaceRegistry defers controller latency to suite disposal. Each
// scenario owns a fresh namespace and deletion always uses its original UID.
// Failed requests remain registered for retry; the exit path is a backstop.
type liveNamespaceRegistry struct {
	staleOnce  sync.Once
	mu         sync.Mutex
	namespaces []*ownedLiveNamespace
	sweepMu    sync.Mutex
	deadline   time.Time
	waitCancel context.CancelFunc
}

var liveNamespaces liveNamespaceRegistry

func (r *liveNamespaceRegistry) dispose(client kubernetes.Interface, ns *corev1.Namespace) error {
	owned := &ownedLiveNamespace{name: ns.Name, uid: ns.UID, client: client}
	r.mu.Lock()
	r.namespaces = append(r.namespaces, owned)
	r.mu.Unlock()
	// Register BEFORE either network call: even a timed-out request may land.
	clean, cancel := context.WithTimeout(context.Background(), namespaceRequestTimeout)
	grace := int64(1)
	err := client.CoreV1().Pods(ns.Name).DeleteCollection(clean, metav1.DeleteOptions{GracePeriodSeconds: &grace}, metav1.ListOptions{})
	cancel()
	if err != nil && !apierrors.IsNotFound(err) {
		fmt.Fprintf(os.Stderr, "deleting pods in owned live namespace %s (%s): %v; continuing with namespace deletion\n", ns.Name, ns.UID, err)
	}
	clean, cancel = context.WithTimeout(context.Background(), namespaceRequestTimeout)
	defer cancel()
	_, err = deleteOwnedNamespace(clean, owned)
	r.mu.Lock()
	owned.deleteLanded = err == nil
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("delete owned namespace %s: %w", ns.Name, err)
	}
	fmt.Printf("deleting owned live namespace %s (%s)\n", ns.Name, ns.UID)
	return nil
}

func deleteOwnedNamespace(ctx context.Context, ns *ownedLiveNamespace) (bool, error) {
	err := ns.client.CoreV1().Namespaces().Delete(ctx, ns.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.uid}})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

// BoundLiveNamespaceSweep shortens even a sweep already running on the main
// goroutine. Repeated signals and the exit backstop share this deadline rather
// than each buying another three seconds before the engine's SIGKILL.
func BoundLiveNamespaceSweep() { liveNamespaces.boundWait(namespaceCancellationBudget) }

func (r *liveNamespaceRegistry) boundWait(budget time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.deadline.IsZero() {
		return
	}
	r.deadline = time.Now().Add(budget)
	if r.waitCancel != nil {
		time.AfterFunc(budget, r.waitCancel)
	}
}

// LiveNamespaceResourceDefinition is acquired first and disposed last, inside
// RunPlan before run_end. The pinned runner discards disposer errors; the
// adapter's protocol writer therefore also folds recorded failures into run_end.
func LiveNamespaceResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{Name: "live-namespace-sweep", Scope: brine.ScopeSuite,
		Factory:  func(map[string]any) (any, error) { return struct{}{}, nil },
		Disposer: func(any) error { return SweepLiveNamespaces() },
	}
}

func SweepLiveNamespaces() error {
	r := &liveNamespaces
	r.sweepMu.Lock()
	defer r.sweepMu.Unlock()
	r.mu.Lock()
	if len(r.namespaces) == 0 {
		r.mu.Unlock()
		return nil
	}
	deadline := r.deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(180 * time.Second)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	r.waitCancel = cancel
	r.mu.Unlock()
	defer func() { cancel(); r.mu.Lock(); r.waitCancel = nil; r.mu.Unlock() }()
	return r.sweep(ctx, 500*time.Millisecond, os.Stdout)
}

func (r *liveNamespaceRegistry) sweep(ctx context.Context, interval time.Duration, out io.Writer) error {
	r.mu.Lock()
	pending := r.namespaces
	r.namespaces = nil
	r.mu.Unlock()
	lastErrors := make(map[*ownedLiveNamespace]error)
	err := wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		remaining := pending[:0]
		for _, ns := range pending {
			r.mu.Lock()
			landed := ns.deleteLanded
			r.mu.Unlock()
			if !landed {
				request, cancel := context.WithTimeout(ctx, namespaceRequestTimeout)
				done, err := deleteOwnedNamespace(request, ns)
				r.mu.Lock()
				ns.deleteLanded = err == nil
				r.mu.Unlock()
				cancel()
				if done {
					fmt.Fprintf(out, "removed owned live namespace %s (%s)\n", ns.name, ns.uid)
					continue
				}
				if err != nil {
					lastErrors[ns] = err
					remaining = append(remaining, ns)
					continue
				}
			}
			request, cancel := context.WithTimeout(ctx, namespaceRequestTimeout)
			current, err := ns.client.CoreV1().Namespaces().Get(request, ns.name, metav1.GetOptions{})
			cancel()
			if apierrors.IsNotFound(err) || (err == nil && current.UID != ns.uid) {
				fmt.Fprintf(out, "removed owned live namespace %s (%s)\n", ns.name, ns.uid)
				delete(lastErrors, ns)
				continue
			}
			if ctx.Err() == nil {
				lastErrors[ns] = err
			}
			remaining = append(remaining, ns)
		}
		pending = remaining
		return len(pending) == 0, nil
	})
	var failures []error
	for _, ns := range pending {
		failure := fmt.Errorf("namespace still present or unverified after cleanup sweep: %w", err)
		if lastErr := lastErrors[ns]; lastErr != nil {
			failure = fmt.Errorf("%w (last request: %v)", failure, lastErr)
		}
		failure = fmt.Errorf("owned live namespace %s (%s): %w", ns.name, ns.uid, failure)
		fmt.Fprintln(out, failure)
		RecordDisposalFailure("namespace sweep", failure)
		failures = append(failures, failure)
	}
	r.mu.Lock()
	r.namespaces = append(r.namespaces, pending...)
	r.mu.Unlock()
	return errors.Join(failures...)
}

// sweepStaleOnce recovers namespaces left by SIGKILL only after live namespace
// creation has proven API access. Failure is diagnostic, never a run verdict.
func (r *liveNamespaceRegistry) sweepStaleOnce(ctx context.Context, client kubernetes.Interface) {
	r.staleOnce.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := sweepStaleLiveNamespaces(ctx, client, time.Now(), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "stale live namespace sweep skipped: %s\n", strings.ReplaceAll(err.Error(), "\n", "; "))
		}
	})
}

// sweepStaleLiveNamespaces recovers namespaces left by SIGKILL. Attribution is
// mandatory: only the explicitly selected cluster and the runtime-test label
// qualify. The 30-minute age floor excludes concurrent runs' young namespaces;
// names alone never establish ownership. UID preconditions protect replacements.
// This age lease assumes a live scenario (including a held one) lasts less than
// 30 minutes. No deletion wait is charged to the new run.
func sweepStaleLiveNamespaces(ctx context.Context, client kubernetes.Interface, now time.Time, out io.Writer) error {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: liveNamespaceLabel})
	if err != nil {
		return fmt.Errorf("list stale live namespaces: %w", err)
	}
	var failures []error
	for _, ns := range list.Items {
		if ns.Labels["app.kubernetes.io/managed-by"] != "brine-runtime-tests" || ns.CreationTimestamp.IsZero() || !ns.CreationTimestamp.Time.Before(now.Add(-30*time.Minute)) {
			continue
		}
		request, cancel := context.WithTimeout(ctx, namespaceRequestTimeout)
		_, err := deleteOwnedNamespace(request, &ownedLiveNamespace{name: ns.Name, uid: ns.UID, client: client})
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("delete stale namespace %s: %w", ns.Name, err))
		} else {
			fmt.Fprintf(out, "deleting stale owned live namespace %s (%s)\n", ns.Name, ns.UID)
		}
	}
	return errors.Join(failures...)
}
