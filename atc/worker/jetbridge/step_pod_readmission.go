package jetbridge

// Re-admission: the privilege gate applied again to a step pod or step Job
// that already exists when a step comes back to it. Under the exec step
// runtime that is a live pause pod reused (a repeated check on its container,
// a step a restarted web reattaches to); under the job step runtime, a running
// step Job resumed. Such a pod was admitted when it was made, under whatever
// privilege mode and step pod grants the web had then; before the step is
// handed it, it passes the privilege mode's rule again
// (privilegeAllowance.admitPrivileged, the rule creation applies), under the
// allowance its step resolves to now through the same identity resolution.
//
// Re-admission judges privilege only. The capability, host-namespace and
// host-path rules of admitStepPod judged the pod the web composed, at
// creation; the object the API server holds may carry what a mutating webhook
// added (an istio-init or linkerd-init container adding NET_ADMIN, a host path
// of its own), and the web's host paths may have moved across a restart.
// Neither is a reason to delete a pod that is already running a step.
//
// A refused pod is never handed back, and no fresh pod replaces it: the
// command may already have begun in it, and a fresh pod would run it a second
// time. It is deleted, a step Job by its UID, and the step fails naming the
// grant it lacks. A Run-owned exact execution is refused the same way but
// never deleted: it may have started, and only its own path may stop it.
//
// Under the any privilege mode nothing is re-admitted: what comes back is
// handed back as it was before the privilege gate, and nothing more is looked
// up. A finished step's result is read back with no admission (attachJob,
// recoverExecProcess): nothing runs.

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
	refusal := c.readmissionRefusal(pod, allowance)
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
		return fmt.Errorf("step pod %q holds privilege this web no longer admits, and deleting it failed (%v): %w", pod.Name, err, refusal)
	}
	return deletedRefusal("pod", pod.Name, refusal)
}

// readmitStepJob re-admits a running step Job the job runtime is about to
// follow: its pod template is what its pod runs.
func (c *Container) readmitStepJob(ctx context.Context, job *batchv1.Job, allowance privilegeAllowance) error {
	template := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name, Namespace: job.Namespace,
			Labels: job.Spec.Template.Labels, Annotations: job.Spec.Template.Annotations},
		Spec: job.Spec.Template.Spec,
	}
	refusal := c.readmissionRefusal(template, allowance)
	if refusal == nil {
		return nil
	}
	if c.exactExecution() {
		return exactRefusal("job", job.Name, refusal)
	}
	if err := deleteStepJob(ctx, c.clientset, c.config.Namespace, job.Name, job.UID); err != nil {
		return fmt.Errorf("step job %q holds privilege this web no longer admits, and deleting it failed (%v): %w", job.Name, err, refusal)
	}
	c.mu.Lock()
	c.jobUID, c.jobPodName = "", ""
	c.mu.Unlock()
	return deletedRefusal("job", job.Name, refusal)
}

// readmissionRefusal is the privilege mode's rule on an existing pod, or nil.
// A pod that holds privilege its step no longer asks for is told so: the
// allowance's own reason (a check's owner unresolved, "drop privileged:") is
// written for a step that asks.
func (c *Container) readmissionRefusal(pod *corev1.Pod, allowance privilegeAllowance) error {
	if !allowance.enforced {
		return nil
	}
	refusal := allowance.admitPrivileged(pod)
	if refusal == nil || c.containerSpec.ImageSpec.Privileged {
		return refusal
	}
	return fmt.Errorf("refusing step pod %q: %s, a privilege its step no longer asks for: it was made while the "+
		"step was privileged, and this web admits privilege only under a step pod grant that declares privileged=true "+
		"(--kubernetes-step-privilege=granted)", pod.Name, privilegedContainer(pod))
}

// retireUnadmittedStepJob is re-admission for a step whose own composition
// failed: a running Job of its name, made before, is re-admitted under the
// same allowance and deleted if the privilege rule refuses it. A Job the rule
// admits is left, and the step fails with its composition's error. A finished
// Job is left; nothing would follow it.
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
	return fmt.Errorf("step %s %q holds privilege this web's privilege gate no longer admits, so it was deleted "+
		"and the step fails; no fresh one replaces it, as its command may have begun: %w", kind, name, refusal)
}

func exactRefusal(kind, name string, refusal error) error {
	return fmt.Errorf("step %s %q is a Run's exact execution holding privilege the privilege gate no longer admits; "+
		"it is not handed back, and, as it may have started, it is neither deleted nor replaced here: %w", kind, name, refusal)
}
