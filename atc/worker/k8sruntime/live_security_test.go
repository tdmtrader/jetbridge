//go:build live
// +build live

package k8sruntime_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setupLiveWorkerWithConfig creates a Worker backed by a real K8s clientset
// with a custom Config. Used for testing imagePullSecrets, service accounts,
// and resource limits.
func setupLiveWorkerWithConfig(t *testing.T, handle string, cfgMutator func(*k8sruntime.Config)) (*k8sruntime.Worker, runtime.BuildStepDelegate) {
	t.Helper()

	clientset, cfg := kubeClient(t)
	if cfgMutator != nil {
		cfgMutator(cfg)
	}

	restConfig, err := k8sruntime.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("live-k8s-worker")

	fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
	fakeCreatingContainer.HandleReturns(handle)
	fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
	fakeCreatedContainer.HandleReturns(handle)
	fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)

	fakeDBWorker.FindContainerReturns(nil, nil, nil)
	fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

	worker := k8sruntime.NewWorker(fakeDBWorker, clientset, *cfg)
	executor := k8sruntime.NewSPDYExecutor(clientset, restConfig)
	worker.SetExecutor(executor)

	return worker, &liveNoopDelegate{}
}

// TestLiveResourceLimitsQoS verifies that pods created with CPU/Memory limits
// get the Guaranteed QoS class from K8s (because requests == limits).
func TestLiveResourceLimitsQoS(t *testing.T) {
	handle := "live-qos-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	cpu := uint64(100)     // 100m = 0.1 CPU
	memory := uint64(64 * 1024 * 1024) // 64Mi

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Limits: runtime.ContainerLimits{
				CPU:    &cpu,
				Memory: &memory,
			},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run a quick command to trigger pod creation.
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo qos-test"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Give K8s a moment to assign QoS class.
	time.Sleep(2 * time.Second)

	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	t.Logf("pod QoS class: %s", pod.Status.QOSClass)
	if pod.Status.QOSClass != corev1.PodQOSGuaranteed {
		t.Fatalf("expected Guaranteed QoS, got %s", pod.Status.QOSClass)
	}

	// Verify resource requests == limits on the main container.
	mainContainer := pod.Spec.Containers[0]
	cpuReq := mainContainer.Resources.Requests.Cpu()
	cpuLim := mainContainer.Resources.Limits.Cpu()
	memReq := mainContainer.Resources.Requests.Memory()
	memLim := mainContainer.Resources.Limits.Memory()

	if cpuReq.Cmp(*cpuLim) != 0 {
		t.Fatalf("CPU request (%s) != limit (%s)", cpuReq, cpuLim)
	}
	if memReq.Cmp(*memLim) != 0 {
		t.Fatalf("memory request (%s) != limit (%s)", memReq, memLim)
	}
	t.Logf("CPU: req=%s lim=%s, Memory: req=%s lim=%s — Guaranteed QoS confirmed",
		cpuReq, cpuLim, memReq, memLim)

	// Wait for process to complete.
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	t.Logf("exit status: %d", result.ExitStatus)
}

// TestLiveSecureDefaults verifies that pods created through the Worker
// interface have the expected security context applied.
func TestLiveSecureDefaults(t *testing.T) {
	handle := "live-secure-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID: 1,
			ImageSpec: runtime.ImageSpec{
				ImageURL:   "docker:///busybox",
				Privileged: false,
			},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run a command to trigger pod creation. Note: with RunAsNonRoot=true
	// and busybox (root user), the pod may fail to start — but we still
	// verify the security context was applied to the pod spec.
	_, err = container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "whoami"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Give K8s a moment to create the pod.
	time.Sleep(time.Second)

	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	// Verify pod-level security context.
	if pod.Spec.SecurityContext == nil {
		t.Fatal("pod SecurityContext is nil")
	}
	if pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("expected RunAsNonRoot=true on non-privileged pod")
	}
	t.Logf("pod RunAsNonRoot=%t", *pod.Spec.SecurityContext.RunAsNonRoot)

	// Verify container-level security context.
	mainContainer := pod.Spec.Containers[0]
	if mainContainer.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if mainContainer.SecurityContext.AllowPrivilegeEscalation == nil || *mainContainer.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("expected AllowPrivilegeEscalation=false on non-privileged container")
	}
	t.Logf("container AllowPrivilegeEscalation=%t", *mainContainer.SecurityContext.AllowPrivilegeEscalation)

	// Clean up — pod may or may not have started depending on the image user.
	_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), handle, metav1.DeleteOptions{})
	t.Logf("secure defaults verified on pod spec")
}

// TestLiveServiceAccount verifies that the serviceAccountName configured in
// Config is applied to created pods.
func TestLiveServiceAccount(t *testing.T) {
	handle := "live-sa-" + time.Now().Format("150405")
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	// Use the "default" service account since it always exists.
	worker, delegate := setupLiveWorkerWithConfig(t, handle, func(c *k8sruntime.Config) {
		c.ServiceAccount = "default"
	})

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	_, err = container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo sa-test"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	time.Sleep(time.Second)

	pod, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	if pod.Spec.ServiceAccountName != "default" {
		t.Fatalf("expected serviceAccountName 'default', got %q", pod.Spec.ServiceAccountName)
	}
	t.Logf("serviceAccountName=%s confirmed on pod spec", pod.Spec.ServiceAccountName)

	// Clean up.
	_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), handle, metav1.DeleteOptions{})
}
