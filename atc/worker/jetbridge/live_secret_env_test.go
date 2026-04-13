//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveSecretEnvRef verifies that when SecretEnv is set on a ContainerSpec,
// the created pod uses ValueFrom.SecretKeyRef and the secret value is available
// as an env var inside the container (fetched by the kubelet, not embedded in
// the pod spec).
func TestLiveSecretEnvRef(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ctx := context.Background()
	ns := cfg.Namespace

	// 1. Create a K8s Secret to reference
	secretName := "live-test-secret-" + time.Now().Format("150405")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
		},
		Data: map[string][]byte{
			"value": []byte("live-secret-value"),
		},
	}
	_, err := clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test secret: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Secrets(ns).Delete(context.Background(), secretName, metav1.DeleteOptions{})
	})

	// 2. Create a worker and container with SecretEnv
	handle := "live-secret-env-" + time.Now().Format("150405")
	cleanupPod(t, clientset, ns, handle)

	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("live-k8s-worker")
	setupFakeDBContainer(fakeDBWorker, handle)

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:   1,
			TeamName: "main",
			Dir:      "/tmp",
			ImageSpec: runtime.ImageSpec{
				ImageURL: "docker:///busybox",
			},
			// The literal value is set here but will be replaced by SecretKeyRef
			Env: []string{"SECRET_VAR=placeholder", "PLAIN_VAR=hello"},
			SecretEnv: map[string]vars.SecretRef{
				"SECRET_VAR": {
					Namespace: ns,
					Name:      secretName,
					Key:       "value",
				},
			},
		},
		&noopDelegate{},
	)
	if err != nil {
		t.Fatalf("creating container: %v", err)
	}

	// 3. Run a command — this creates the pod and lets us verify the spec
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo $SECRET_VAR"},
		Dir:  "/tmp",
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("running process: %v", err)
	}

	// 4. Verify the pod spec uses SecretKeyRef (pod exists now after Run)
	pod, err := clientset.CoreV1().Pods(ns).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	var secretEnvVar, plainEnvVar *corev1.EnvVar
	for i := range pod.Spec.Containers[0].Env {
		ev := &pod.Spec.Containers[0].Env[i]
		switch ev.Name {
		case "SECRET_VAR":
			secretEnvVar = ev
		case "PLAIN_VAR":
			plainEnvVar = ev
		}
	}

	if secretEnvVar == nil {
		t.Fatal("SECRET_VAR not found in pod env")
	}
	if secretEnvVar.Value != "" {
		t.Errorf("SECRET_VAR should not have literal Value, got: %q", secretEnvVar.Value)
	}
	if secretEnvVar.ValueFrom == nil || secretEnvVar.ValueFrom.SecretKeyRef == nil {
		t.Fatal("SECRET_VAR should have ValueFrom.SecretKeyRef")
	}
	if secretEnvVar.ValueFrom.SecretKeyRef.Name != secretName {
		t.Errorf("expected SecretKeyRef.Name=%q, got=%q", secretName, secretEnvVar.ValueFrom.SecretKeyRef.Name)
	}
	if secretEnvVar.ValueFrom.SecretKeyRef.Key != "value" {
		t.Errorf("expected SecretKeyRef.Key=%q, got=%q", "value", secretEnvVar.ValueFrom.SecretKeyRef.Key)
	}

	if plainEnvVar == nil {
		t.Fatal("PLAIN_VAR not found in pod env")
	}
	if plainEnvVar.Value != "hello" {
		t.Errorf("expected PLAIN_VAR=%q, got=%q", "hello", plainEnvVar.Value)
	}
	if plainEnvVar.ValueFrom != nil {
		t.Error("PLAIN_VAR should not have ValueFrom")
	}

	// 5. Wait for the process and verify the kubelet injected the secret value
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("waiting for process: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitStatus)
	}

	t.Log("live secret env ref test passed — pod spec uses SecretKeyRef, kubelet injected value")
}
