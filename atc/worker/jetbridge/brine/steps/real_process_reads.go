package steps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// deniedReadTransport forwards every response unchanged. Observing the chosen
// real denial triggers a Role update, and authorization is verified before that
// response is released to the runtime. Counts order the infrastructure fault;
// assertions examine the process outcome, never a collaborator's call history.
type deniedReadTransport struct {
	next         http.RoundTripper
	path         string
	restoreAfter int
	restore      func(context.Context) error
	mu           sync.Mutex
	armed        bool
	denials      int
	restoreErr   error
}

func (t *deniedReadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err != nil || response == nil || req.Method != http.MethodGet || req.URL.Path != t.path || response.StatusCode != http.StatusForbidden {
		return response, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed {
		t.denials++
		fmt.Printf("real process read denial: path %s HTTP %d ordinal %d\n", req.URL.Path, response.StatusCode, t.denials)
		if t.restoreAfter > 0 && t.denials == t.restoreAfter {
			t.restoreErr = t.restore(req.Context())
		}
	}
	return response, err
}

func (t *deniedReadTransport) arm() { t.mu.Lock(); defer t.mu.Unlock(); t.armed = true }
func (t *deniedReadTransport) restorationError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restoreErr
}

// The probe uses the same real identity without the observation transport.
// Before pod creation an authorized NotFound also proves Get permission.
func awaitPodReadAccess(ctx context.Context, client kubernetes.Interface, namespace, handle string, allowed bool) error {
	for {
		_, err := client.CoreV1().Pods(namespace).Get(ctx, handle, metav1.GetOptions{})
		if allowed && (err == nil || apierrors.IsNotFound(err)) {
			return nil
		}
		if !allowed && apierrors.IsForbidden(err) {
			return nil
		}
		if err != nil && !apierrors.IsForbidden(err) {
			return fmt.Errorf("verify pod read permission: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pod read permission did not become allowed=%t: %w", allowed, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// observeInitialReadFailures exercises compatibility Process.Wait against a
// real API pod and real authorization failures. Positive restoreAfter completes
// a real kubelet-backed pod before revoking reads, then restores access after
// that many denials. No completion status is manufactured.
// Zero leaves the pod Pending and the denial in place until the runtime stops.
func observeInitialReadFailures(in WorkerReady, rec *brine.Recorder, base *rest.Config, handle string, restoreAfter int) (ProcessOutcome, error) {
	ctx, cancel := context.WithTimeout(in.Ctx, 15*time.Second)
	defer cancel()
	admin := in.Clientset
	access, err := newPodAccess(ctx, rec, admin, base, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "process-reader", Namespace: in.Namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create", "delete", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, ResourceNames: []string{handle}, Verbs: []string{"get"}},
		},
	}, "brine-process-"+in.Namespace)
	if err != nil {
		return ProcessOutcome{}, err
	}
	if err := awaitPodReadAccess(ctx, access.client, in.Namespace, handle, true); err != nil {
		return ProcessOutcome{}, err
	}
	grantedRules := access.role.DeepCopy().Rules
	observer := &deniedReadTransport{path: "/api/v1/namespaces/" + in.Namespace + "/pods/" + handle, restoreAfter: restoreAfter}
	observer.restore = func(ctx context.Context) error {
		restored := access.role.DeepCopy()
		restored.Rules = grantedRules
		updated, err := admin.RbacV1().Roles(in.Namespace).Update(ctx, restored, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("restore pod read permission: %w", err)
		}
		access.role = updated
		return awaitPodReadAccess(ctx, access.client, in.Namespace, handle, true)
	}
	config := rest.CopyConfig(access.config)
	config.Wrap(func(next http.RoundTripper) http.RoundTripper { observer.next = next; return observer })
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ProcessOutcome{}, err
	}
	in.Clientset, in.Executor = client, nil
	in = in.rebuild()
	container, _, err := in.Worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, runtime.ContainerSpec{TeamID: in.TeamID, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("create read-fault container: %w", err)
	}
	stderr := new(bytes.Buffer)
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("create read-fault pod: %w", err)
	}
	if _, ok := process.(*jetbridge.Process); !ok {
		return ProcessOutcome{}, fmt.Errorf("expected direct compatibility Process, got %T", process)
	}
	pods := admin.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if pod.UID == "" || pod.ResourceVersion == "" {
		return ProcessOutcome{}, fmt.Errorf("read-fault pod has no API identity")
	}
	if restoreAfter > 0 {
		originalUID := pod.UID
		for {
			pod, err = pods.Get(ctx, handle, metav1.GetOptions{})
			if err != nil {
				return ProcessOutcome{}, err
			}
			if pod.UID != originalUID {
				return ProcessOutcome{}, fmt.Errorf("read-retry completion pod was replaced")
			}
			if pod.Status.Phase == corev1.PodSucceeded {
				completed := false
				for _, status := range pod.Status.ContainerStatuses {
					if status.Name != "main" {
						continue
					}
					dead := status.State.Terminated
					if dead == nil || dead.ExitCode != 0 || dead.ContainerID == "" || dead.StartedAt.IsZero() || dead.FinishedAt.IsZero() ||
						status.RestartCount != 0 || status.LastTerminationState.Terminated != nil || pod.Spec.NodeName == "" {
						return ProcessOutcome{}, fmt.Errorf("kubelet did not report a real successful main: %+v", pod.Status)
					}
					fmt.Printf("real read-retry completion: pod %s/%s UID %s node %s phase Succeeded main exit 0 container %s; no restart history; before revoking reads\n",
						pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, dead.ContainerID)
					completed = true
				}
				if !completed {
					return ProcessOutcome{}, fmt.Errorf("successful pod has no main container status")
				}
				break
			}
			if pod.Status.Phase == corev1.PodFailed {
				return ProcessOutcome{}, fmt.Errorf("real read-retry task failed: %+v", pod.Status)
			}
			select {
			case <-ctx.Done():
				return ProcessOutcome{}, fmt.Errorf("wait for real read-retry completion: %w", ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	denied := access.role.DeepCopy()
	denied.Rules = denied.Rules[:1] // Keep lifecycle/watch access, remove Get.
	access.role, err = admin.RbacV1().Roles(in.Namespace).Update(ctx, denied, metav1.UpdateOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if err := awaitPodReadAccess(ctx, access.client, in.Namespace, handle, false); err != nil {
		return ProcessOutcome{}, err
	}
	observer.arm()
	result, waitErr := process.Wait(ctx)
	if err := observer.restorationError(); err != nil {
		return ProcessOutcome{}, err
	}
	state := StepRunning{Namespace: in.Namespace, Clientset: admin, Ctx: in.Ctx, Handle: handle, Process: process, Stderr: stderr}
	return state.report(result, waitErr), nil
}
