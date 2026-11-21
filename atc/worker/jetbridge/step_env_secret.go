package jetbridge

// The step params Secret: where a task step's params live instead of its pod
// spec.
//
// A task's params are whatever its config interpolated, credentials among
// them, and a literal env value is stored in the Pod (and, under the job step
// runtime, in the step Job's template) where anyone who can read pods sees
// it. So every param goes in a Secret of the step's own, step-env-<handle>,
// and the main container reads each through a secretKeyRef under a key of
// ours (p0, p1, ... in param order). The env entries stay where the literals
// were, after the step's own env and before the process's, so precedence is
// unchanged; the kubelet expands no $(VAR) in a value read from a Secret, so
// the value is stored as written.
//
// The name is a function of the handle alone so the reaper, which may know
// nothing else of a container, can delete it by name; web is granted create,
// patch and delete on Secrets and never get, list or watch. Every creation
// deletes the name first: a handle's pod is replaced (pause-pod replacement,
// relocation, a terminal pod replaced at Run, a repeated step Job), and the
// Secret is immutable.
//
// Ownership:
//
//	exec   Secret (no owner) -> pod -> the Secret's ownerReference patched to
//	       the pod. A pod create that fails deletes the Secret; a web that
//	       dies in between leaves it to the reaper, which deletes the Secret
//	       of every task container leaving the database
//	       (collectStepEnvSecrets).
//	job    the suspended Job -> the Secret, owned by the Job at creation ->
//	       unsuspend. A Secret that cannot be made deletes the Job, which
//	       never ran.
//
// There is no path on which a param becomes a literal value: a Secret that
// cannot be made fails the step.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// stepEnvSecretAnnotation names, on a step pod (and a step Job's template),
// the Secret its params are read from. The reaper reads it to skip the
// delete for a pod that has none.
const stepEnvSecretAnnotation = "concourse.ci/step-env-secret"

// maxStepEnvSecretBytes is the API server's limit on a Secret: k8s.io/api
// (corev1.MaxSecretSize) states it as the total bytes of the values in its
// Data, keys not counted, and the API server refuses a Secret whose values
// total more. The check below counts exactly that: the params' values, in
// bytes, and nothing else. (The validation itself lives in k8s.io/kubernetes,
// which this module does not vendor; the constant and its contract are
// k8s.io/api's.)
const maxStepEnvSecretBytes = corev1.MaxSecretSize

// stepEnvSecretName is the name of the Secret that holds a step's params.
func stepEnvSecretName(handle string) string {
	return "step-env-" + handle
}

// stepParamKey is the Secret key the i'th param is stored under. The keys are
// ours, not the params' names: a Secret key allows fewer characters than an
// environment variable's name does.
func stepParamKey(i int) string {
	return "p" + strconv.Itoa(i)
}

// stepEnvSecretData is a step's params as the Secret's data, or an error
// naming the size when they cannot fit one Secret.
func stepEnvSecretData(params []runtime.Param) (map[string][]byte, error) {
	data := make(map[string][]byte, len(params))
	total := 0
	for i, param := range params {
		key := stepParamKey(i)
		data[key] = []byte(param.Value)
		total += len(param.Value)
	}
	if total > maxStepEnvSecretBytes {
		return nil, fmt.Errorf("the task's param values total %d bytes: a step's param values must fit one Kubernetes Secret (%d bytes)", total, maxStepEnvSecretBytes)
	}
	return data, nil
}

// stepParamEnv is the main container's env entries for a step's params: each
// read from the step's Secret, none a literal.
func stepParamEnv(handle string, params []runtime.Param) []corev1.EnvVar {
	if len(params) == 0 {
		return nil
	}
	name := stepEnvSecretName(handle)
	env := make([]corev1.EnvVar, len(params))
	for i, param := range params {
		env[i] = corev1.EnvVar{
			Name: param.Name,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: name},
				Key:                  stepParamKey(i),
			}},
		}
	}
	return env
}

// hasStepEnvSecret reports whether this container's pod reads a Secret.
func (c *Container) hasStepEnvSecret() bool {
	return len(c.containerSpec.Params) > 0
}

// buildStepEnvSecret is the step's params Secret, owned by owner when one is
// given.
func (c *Container) buildStepEnvSecret(owner *metav1.OwnerReference) (*corev1.Secret, error) {
	data, err := stepEnvSecretData(c.containerSpec.Params)
	if err != nil {
		return nil, err
	}
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stepEnvSecretName(c.handle),
			Namespace: c.config.Namespace,
			Labels:    c.buildPodLabels(),
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: &immutable,
		Data:      data,
	}
	if owner != nil {
		secret.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return secret, nil
}

// replaceStepEnvSecret makes the step's params Secret afresh: whatever of
// that name a previous pod on this handle left is deleted first. A refusal
// names what the Role lacks.
func (c *Container) replaceStepEnvSecret(ctx context.Context, owner *metav1.OwnerReference) error {
	secret, err := c.buildStepEnvSecret(owner)
	if err != nil {
		return err
	}
	// A container its worker had lost (a pod gone while no web watched it)
	// is departing, and the reaper takes the Secret of a departing task
	// container. It stops departing before its Secret is made, so no pass
	// that reads the departing set from here on takes the new one.
	if c.dbContainer != nil {
		if err := c.dbContainer.MarkPresent(); err != nil {
			return fmt.Errorf("marking container %q present before making its params Secret: %w", c.handle, err)
		}
	}
	if err := deleteStepEnvSecret(ctx, c.clientset, c.config.Namespace, c.handle); err != nil {
		return stepEnvSecretError("replace", secret, err)
	}
	if _, err := c.clientset.CoreV1().Secrets(c.config.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return stepEnvSecretError("create", secret, err)
	}
	return nil
}

// stepEnvSecretError says what became of the Secret, and for a refusal which
// rule the web's Role is missing.
func stepEnvSecretError(verb string, secret *corev1.Secret, err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("cannot %s the step's params Secret %q in namespace %q: the web's Role lacks "+
			`"secrets: create, patch, delete" (is the chart older than this web?): %w`, verb, secret.Name, secret.Namespace, err)
	}
	return fmt.Errorf("%s the step's params Secret %q: %w", verb, secret.Name, err)
}

// ownStepEnvSecret points the step's params Secret at the pod that reads it,
// so the garbage collector deletes it with the pod. A failure only costs the
// Secret's lifetime: the reaper deletes it with the container. It is logged
// and the step goes on.
func (c *Container) ownStepEnvSecret(ctx context.Context, pod *corev1.Pod) {
	name := stepEnvSecretName(c.handle)
	// A pod that reads no Secret of this name (one an older web made, with
	// its params as literals) has nothing to own.
	if !c.hasStepEnvSecret() || pod == nil || pod.UID == "" || pod.Annotations[stepEnvSecretAnnotation] != name {
		return
	}
	owner := metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": []metav1.OwnerReference{owner}}})
	if err != nil {
		return
	}
	if _, err := c.clientset.CoreV1().Secrets(c.config.Namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		lagerctx.FromContext(ctx).Info("step-env-secret-not-owned", lager.Data{
			"secret": name, "pod": pod.Name, "error": err.Error(),
			"reason": "the reaper deletes it with the container",
		})
	}
}

// createStepPod creates an exec-runtime step pod and, for a task with params,
// the Secret it reads them from: the Secret first, since a container whose
// Secret is missing fails to start; then the pod; then the Secret's owner.
func (c *Container) createStepPod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	if !c.hasStepEnvSecret() {
		return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	}
	if err := c.replaceStepEnvSecret(ctx, nil); err != nil {
		return nil, err
	}
	created, err := c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		// A pod of this name is there after all, and it may be reading this
		// Secret: it is left alone, and so is the Secret.
		if apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if delErr := deleteStepEnvSecret(ctx, c.clientset, c.config.Namespace, c.handle); delErr != nil {
			lagerctx.FromContext(ctx).Info("step-env-secret-left-for-the-reaper", lager.Data{
				"secret": stepEnvSecretName(c.handle), "error": delErr.Error(),
			})
		}
		return nil, err
	}
	c.ownStepEnvSecret(ctx, created)
	return created, nil
}

// stepJobOwner is the owner reference a step Job's Secret carries.
func stepJobOwner(name string, uid types.UID) *metav1.OwnerReference {
	return &metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: uid}
}

// ensureStepJobEnvSecret makes the params Secret of a step Job that has not
// been unsuspended, owned by that Job.
func (c *Container) ensureStepJobEnvSecret(ctx context.Context, name string, uid types.UID) error {
	if !c.hasStepEnvSecret() {
		return nil
	}
	return c.replaceStepEnvSecret(ctx, stepJobOwner(name, uid))
}

// refuseLiteralParams refuses a step pod whose main container would carry one
// of the task's params as a literal value. Both step runtimes pass it once,
// next to the step pod admission, so no later change to the pod's
// composition can put a param back in the spec unnoticed.
func (c *Container) refuseLiteralParams(pod *corev1.Pod) error {
	if !c.hasStepEnvSecret() || len(pod.Spec.Containers) == 0 {
		return nil
	}
	params := make(map[string]bool, len(c.containerSpec.Params))
	for _, param := range c.containerSpec.Params {
		params[param.Name] = true
	}
	// The step's own variable of a param's name (a param may override
	// BUILD_ID) is a literal, before the param that overrides it; anything
	// else of a param's name must be read from the Secret.
	own := make(map[string]bool, len(c.containerSpec.Env))
	for _, entry := range envVars(c.containerSpec.Env) {
		own[entry.Name] = true
	}
	read := make(map[string]bool, len(params))
	for _, env := range pod.Spec.Containers[0].Env {
		if !params[env.Name] {
			continue
		}
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == stepEnvSecretName(c.handle) {
			read[env.Name] = true
			continue
		}
		if !own[env.Name] || read[env.Name] {
			return fmt.Errorf("refusing step pod %q: the task param %q would be a literal value in its spec rather than read from its Secret", pod.Name, env.Name)
		}
	}
	for name := range params {
		if !read[name] {
			return fmt.Errorf("refusing step pod %q: the task param %q is not read from its Secret", pod.Name, name)
		}
	}
	return nil
}

// stepEnvSecretSyncGrace is how long after its creation a step pod may wait
// on the kubelet reading its own params Secret before that counts as the
// terminal CreateContainerConfigError it is reported as. The Secret exists
// before the pod (or before its step Job is unsuspended); what the kubelet
// reports in that time is its own lag -- its secret cache not synced, or the
// node authorizer not yet relating the node to the Secret through the pod --
// which it retries.
const stepEnvSecretSyncGrace = 30 * time.Second

// stepEnvSecretSyncPending reports whether the main container is the only
// container of pod waiting in a terminal state, and is waiting only for the
// kubelet to read the step's own params Secret, while the pod is younger than
// stepEnvSecretSyncGrace; and how much of the grace is left. Anything else --
// another reason, another container, another Secret, a pod with no params
// Secret, an older pod -- is not pending and is judged as before.
//
// The kubelet names the Secret when it cannot find it (secret "<name>" not
// found), but not when its secret cache has not synced ("failed to sync
// secret cache: ..."; the cache-based manager's error carries the resource
// and the cause, not the object -- from the kubelet's text as remembered:
// k8s.io/kubernetes is not in this module's cache to check). That message is
// tolerated only when the pod reads no Secret but its own: no Secret volume,
// no projected Secret, no envFrom Secret, and every secretKeyRef of main
// names the step's Secret.
func stepEnvSecretSyncPending(pod *corev1.Pod, now time.Time) (time.Duration, bool) {
	name := pod.Annotations[stepEnvSecretAnnotation]
	if name == "" || pod.CreationTimestamp.IsZero() {
		return 0, false
	}
	left := stepEnvSecretSyncGrace - now.Sub(pod.CreationTimestamp.Time)
	if left <= 0 {
		return 0, false
	}
	pending := false
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil || !terminalWaitingReasons[cs.State.Waiting.Reason] {
			continue
		}
		if cs.Name != mainContainerName || cs.State.Waiting.Reason != "CreateContainerConfigError" {
			return 0, false
		}
		message := cs.State.Waiting.Message
		names := strings.Contains(message, `"`+name+`"`)
		unsynced := strings.Contains(message, "failed to sync secret cache") && readsOnlySecret(pod, name)
		if !names && !unsynced {
			return 0, false
		}
		pending = true
	}
	return left, pending
}

// readsOnlySecret reports whether the only Secret pod reads is name.
func readsOnlySecret(pod *corev1.Pod, name string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Secret != nil {
			return false
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil {
					return false
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, container := range containers {
		for _, from := range container.EnvFrom {
			if from.SecretRef != nil {
				return false
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name != name {
				return false
			}
		}
	}
	return true
}

// deleteStepEnvSecret deletes a handle's params Secret, if there is one.
func deleteStepEnvSecret(ctx context.Context, clientset kubernetes.Interface, namespace, handle string) error {
	err := clientset.CoreV1().Secrets(namespace).Delete(ctx, stepEnvSecretName(handle), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
