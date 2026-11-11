package jetbridge

// Re-admission: the step pod admission of a step pod or step Job that already
// exists when a step comes back to it. Under the exec step runtime that is a
// live pause pod reused (a repeated check on its container, a step a restarted
// web reattaches to); under the job step runtime, a running step Job resumed.
// Such a pod was admitted when it was made, under whatever privilege mode and
// step pod grants the web had then; it passes admitStepPod again, under the
// allowance its step resolves to now -- the same identity resolution, the same
// function, no second rule set -- before the step is handed it.
//
// A refused pod is never handed back, and no fresh pod replaces it: the
// command may already have begun in it, and a fresh pod would run it a second
// time. It is deleted, a step Job by its UID, and the step fails naming the
// grant it lacks. A Run-owned exact execution is refused the same way but
// never deleted: it may have started, and only its own path may stop it.
//
// Under the any privilege mode nothing is re-admitted: what comes back is
// handed back as it was before the privilege gate, and nothing more is looked
// up.

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readmitPod re-admits a live pause pod the exec runtime is about to reuse.
func (c *Container) readmitPod(ctx context.Context, pod *corev1.Pod, allowance privilegeAllowance) error {
	if !allowance.enforced {
		return nil
	}
	refusal := admitStepPod(pod, c.stepPodRoots(), allowance)
	if refusal == nil {
		return nil
	}
	if c.exactExecution() {
		return exactRefusal("pod", pod.Name, refusal)
	}
	opts := metav1.DeleteOptions{}
	if pod.UID != "" {
		opts.Preconditions = metav1.NewUIDPreconditions(string(pod.UID))
	}
	err := deleteStepPod(ctx, c.clientset, c.config.Namespace, pod.ObjectMeta, opts)
	if err != nil && !apierrors.IsNotFound(err) && !(pod.UID != "" && apierrors.IsConflict(err)) {
		return fmt.Errorf("step pod %q is not admitted now, and deleting it failed (%v): %w", pod.Name, err, refusal)
	}
	return deletedRefusal("pod", pod.Name, refusal)
}

// readmitStepJob re-admits a running step Job the job runtime is about to
// follow: its pod template is what its pod runs.
func (c *Container) readmitStepJob(ctx context.Context, job *batchv1.Job, allowance privilegeAllowance) error {
	if !allowance.enforced {
		return nil
	}
	template := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name, Namespace: job.Namespace,
			Labels: job.Spec.Template.Labels, Annotations: job.Spec.Template.Annotations},
		Spec: job.Spec.Template.Spec,
	}
	refusal := admitStepPod(template, c.stepPodRoots(), allowance)
	if refusal == nil {
		return nil
	}
	if c.exactExecution() {
		return exactRefusal("job", job.Name, refusal)
	}
	if err := deleteStepJob(ctx, c.clientset, c.config.Namespace, job.Name, job.UID); err != nil {
		return fmt.Errorf("step job %q is not admitted now, and deleting it failed (%v): %w", job.Name, err, refusal)
	}
	c.mu.Lock()
	c.jobUID, c.jobPodName = "", ""
	c.mu.Unlock()
	return deletedRefusal("job", job.Name, refusal)
}

// retireUnadmittedStepJob is re-admission for a step whose own composition
// was refused: a running Job of its name, made before, is re-admitted under
// the same allowance and deleted if refused. A finished Job is left; nothing
// would follow it.
func (c *Container) retireUnadmittedStepJob(ctx context.Context, allowance privilegeAllowance) error {
	if !allowance.enforced {
		return nil
	}
	existing, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the step job a refused step would come back to: %w", err)
	}
	finished, err := c.stepJobDone(ctx, existing)
	if err != nil || finished {
		return err
	}
	return c.readmitStepJob(ctx, existing, allowance)
}

// readmissionAllowance resolves the allowance for a step that comes back to
// what exists without composing a pod (an exact task's resumed Job). Under
// any it is the zero allowance, and nothing is looked up.
func (c *Container) readmissionAllowance() (privilegeAllowance, error) {
	if !stepPrivilegeEnforced(c.config.StepPrivilege) {
		return privilegeAllowance{}, nil
	}
	identity, err := c.identities.resolve(c.metadata, c.containerSpec)
	if err != nil {
		return privilegeAllowance{}, err
	}
	return c.privilegeAllowanceFor(identity), nil
}

func deletedRefusal(kind, name string, refusal error) error {
	return fmt.Errorf("step %s %q was made before this web's privilege gate admitted what it holds, so it was deleted "+
		"and the step fails; no fresh one replaces it, as its command may have begun: %w", kind, name, refusal)
}

func exactRefusal(kind, name string, refusal error) error {
	return fmt.Errorf("step %s %q is a Run's exact execution that the privilege gate no longer admits; it is not "+
		"handed back, and, as it may have started, it is neither deleted nor replaced here: %w", kind, name, refusal)
}
