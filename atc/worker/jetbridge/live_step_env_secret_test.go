//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// A task's params reach its step pod from the step's own Secret,
// step-env-<handle>, never as literal values in the Pod (or the step Job). On
// the deployed cluster: the command sees each value exactly as written --
// including the kubelet's own expansion syntax, which it never applies to a
// value read from a Secret -- and an empty one as set-and-empty; the stored
// Pod and Job hold no value; the Secret is immutable and owned by the pod
// (exec) or the Job (job); and the garbage collector takes it with its owner.
//
// The live ServiceAccount needs secrets get in its namespace to read the
// Secret back; web itself is granted create, patch and delete only.

// liveParamValue carries the kubelet's $(VAR) syntax and a $$, which a value
// in env[].value would have expanded and collapsed.
const liveParamValue = "s3cr$(HOME)$$et"

func liveTaskParams() []runtime.Param {
	return runtime.ParamsFrom(atc.TaskEnv{"TOKEN": liveParamValue, "EMPTY": ""})
}

const liveParamCommand = `printf '%s|%s|%s' "$TOKEN" "${EMPTY-unset}" "${EMPTY+set}"`

func assertLiveParamOutput(t *testing.T, stdout string) {
	t.Helper()
	if want := liveParamValue + "||set"; !strings.Contains(stdout, want) {
		t.Errorf("the command saw %q, want %q: a param did not arrive as written", stdout, want)
	}
}

func assertNoLiveParamValue(t *testing.T, what string, object any) {
	t.Helper()
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "s3cr") {
		t.Errorf("%s holds the param value: %s", what, encoded)
	}
}

// assertLiveParamRefs requires each param to be in the main container's env,
// read from the step's Secret: a param missing from the spec altogether is a
// failure, not a pass.
func assertLiveParamRefs(t *testing.T, env []corev1.EnvVar, handle string) {
	t.Helper()
	for _, name := range []string{"TOKEN", "EMPTY"} {
		found := false
		for _, entry := range env {
			if entry.Name != name {
				continue
			}
			found = true
			if entry.Value != "" || entry.ValueFrom == nil || entry.ValueFrom.SecretKeyRef == nil || entry.ValueFrom.SecretKeyRef.Name != "step-env-"+handle {
				t.Errorf("%s = %+v, want a secretKeyRef to step-env-%s", name, entry, handle)
			}
		}
		if !found {
			t.Errorf("the main container has no %s entry: %+v", name, env)
		}
	}
}

func assertLiveStepSecret(t *testing.T, clientset kubernetes.Interface, namespace, handle, ownerKind string, ownerUID types.UID) {
	t.Helper()
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), "step-env-"+handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step's params Secret: %v", err)
	}
	if secret.Immutable == nil || !*secret.Immutable {
		t.Error("the params Secret is not immutable")
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != ownerKind || secret.OwnerReferences[0].UID != ownerUID {
		t.Errorf("Secret owners = %+v, want the %s %s", secret.OwnerReferences, ownerKind, ownerUID)
	}
}

func awaitLiveStepSecretGone(t *testing.T, clientset kubernetes.Interface, namespace, handle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), "step-env-"+handle, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the params Secret outlived its owner by two minutes (last read: %v)", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func TestLiveATaskParamIsReadFromTheStepsOwnSecret(t *testing.T) {
	handle := "live-step-env-" + time.Now().Format("150405")
	worker, _, _, _ := newLiveWorker(t, nil, nil)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cleanupPod(t, clientset, cfg.Namespace, handle)
	t.Cleanup(func() {
		_ = clientset.CoreV1().Secrets(cfg.Namespace).Delete(context.Background(), "step-env-"+handle, metav1.DeleteOptions{})
	})

	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{TeamID: 1, Dir: "/tmp", ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}, Params: liveTaskParams()}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", liveParamCommand}, Dir: "/tmp"},
		runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		t.Fatalf("Wait = %+v, %v (stderr: %s)", result, err, stderr.String())
	}
	assertLiveParamOutput(t, stdout.String())

	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step pod: %v", err)
	}
	assertNoLiveParamValue(t, "the stored Pod", pod)
	assertLiveParamRefs(t, pod.Spec.Containers[0].Env, handle)
	assertLiveStepSecret(t, clientset, cfg.Namespace, handle, "Pod", pod.UID)

	if err := clientset.CoreV1().Pods(cfg.Namespace).Delete(ctx, handle, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting the step pod: %v", err)
	}
	awaitLiveStepSecretGone(t, clientset, cfg.Namespace, handle)
}

func TestLiveJobRuntimeTaskParamIsReadFromTheStepsOwnSecret(t *testing.T) {
	handle := "live-job-step-env-" + time.Now().Format("150405")
	worker, clientset, cfg := jobRuntimeWorker(t, nil, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "job-step-env"}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Secrets(cfg.Namespace).Delete(context.Background(), "step-env-"+handle, metav1.DeleteOptions{})
	})

	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
		runtime.ContainerSpec{TeamID: 1, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}, Params: liveTaskParams()}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	jobName := jetbridge.GeneratePodName(metadata, handle)
	t.Cleanup(func() { cleanupStepJob(t, clientset, cfg.Namespace, jobName) })

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", liveParamCommand}},
		runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		t.Fatalf("Wait = %+v, %v (stderr: %s)", result, err, stderr.String())
	}
	assertLiveParamOutput(t, stdout.String())

	job, err := clientset.BatchV1().Jobs(cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step Job: %v", err)
	}
	assertNoLiveParamValue(t, "the stored Job", job)
	assertLiveParamRefs(t, job.Spec.Template.Spec.Containers[0].Env, handle)
	pods, err := clientset.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + jobName})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("the Job owns %d pods (%v), want 1", len(pods.Items), err)
	}
	assertNoLiveParamValue(t, "the stored Pod", pods.Items[0])
	assertLiveStepSecret(t, clientset, cfg.Namespace, handle, "Job", job.UID)

	cleanupStepJob(t, clientset, cfg.Namespace, jobName)
	awaitLiveStepSecretGone(t, clientset, cfg.Namespace, handle)
}
