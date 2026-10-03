//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"github.com/concourse/concourse/atc"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// setupLiveWorkerWithConfig creates a Worker backed by a real K8s clientset
// with a custom Config. Used for testing imagePullSecrets, service accounts,
// and resource limits.
func setupLiveWorkerWithConfig(t *testing.T, cfgMutator func(*jetbridge.Config)) (*jetbridge.Worker, runtime.BuildStepDelegate, jetbridgeDB) {
	t.Helper()

	clientset, cfg := kubeClient(t)
	if cfgMutator != nil {
		cfgMutator(cfg)
	}

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	database := withLiveBuilds(t, useLiveJetbridgeDB(t))
	dbWorker, err := persistNamedWorker(database, "live-k8s-worker")
	if err != nil {
		t.Fatalf("persisting worker: %v", err)
	}

	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	worker := jetbridge.NewWorker(dbWorker, clientset, *cfg, jetbridge.WorkerDeps{Executor: executor, StepPodBuilds: database.BuildFactory})

	return worker, nil, database
}

// TestLiveResourceLimitsQoS verifies that pods created with CPU/Memory limits
// get the Guaranteed QoS class from K8s (because requests == limits).
func TestLiveResourceLimitsQoS(t *testing.T) {
	handle := "live-qos-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx := context.Background()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	cpu := uint64(100)                 // 100m = 0.1 CPU
	memory := uint64(64 * 1024 * 1024) // 64Mi

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
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

	cleanupPod(t, clientset, cfg.Namespace, handle)

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

	// Run a command to trigger pod creation.
	process, err := container.Run(ctx, runtime.ProcessSpec{
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

	// Verify pod-level security context exists but does NOT set RunAsNonRoot
	// (resource images like concourse/time-resource run as root).
	if pod.Spec.SecurityContext == nil {
		t.Fatal("pod SecurityContext is nil")
	}
	if pod.Spec.SecurityContext.RunAsNonRoot != nil {
		t.Fatalf("expected RunAsNonRoot to be nil (not enforced), got %t", *pod.Spec.SecurityContext.RunAsNonRoot)
	}
	t.Log("pod RunAsNonRoot is nil (not enforced) — correct")

	// Verify container-level security context.
	mainContainer := pod.Spec.Containers[0]
	if mainContainer.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if mainContainer.SecurityContext.AllowPrivilegeEscalation == nil || *mainContainer.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("expected AllowPrivilegeEscalation=false on non-privileged container")
	}
	t.Logf("container AllowPrivilegeEscalation=%t", *mainContainer.SecurityContext.AllowPrivilegeEscalation)

	// Wait for the command to finish (pod should start fine without RunAsNonRoot).
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	t.Logf("secure defaults verified on pod spec")
}

// TestLiveServiceAccount checks the default step pod identity on a real
// kubelet. A task with a sidecar, in a job no grant maps, runs on a worker
// whose configuration grants a different job. Its pod names the default
// ServiceAccount with no API token: neither the main container nor the
// sidecar finds a token file, and an API request from the task is refused as
// unauthenticated.
func TestLiveServiceAccount(t *testing.T) {
	handle := "live-sa-" + time.Now().Format("150405")
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	worker, delegate, database := setupLiveWorkerWithConfig(t, func(c *jetbridge.Config) {
		c.ServiceAccount = "default"
		grant, err := jetbridge.ParseStepPodGrant("name=other,owner=main/live-sa/granted,service-account=live-sa-granted")
		if err != nil {
			t.Fatal(err)
		}
		c.StepPodGrants = []jetbridge.StepPodGrant{grant}
	})

	team, found, err := database.TeamFactory.FindTeam("main")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		if team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	task := atc.JobConfig{Name: "unmapped", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t",
		Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}}
	granted := task
	granted.Name = "granted"
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "live-sa"}, atc.Config{Jobs: atc.JobConfigs{task, granted}}, db.ConfigVersion(0), false)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := pipeline.Job("unmapped")
	if err != nil || !found {
		t.Fatalf("job unmapped: found=%v err=%v", found, err)
	}
	build, err := job.CreateBuild("live-sa")
	if err != nil {
		t.Fatal(err)
	}

	// The sidecar runs the token check as its own command and stays up only
	// if it passes; RestartPolicy Never leaves a failure Terminated.
	const tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	container, _, err := worker.FindOrCreateContainer(ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask, BuildID: build.ID(), BuildName: build.Name(),
			PipelineID: pipeline.ID(), PipelineName: pipeline.Name(), JobID: job.ID(), JobName: job.Name()},
		runtime.ContainerSpec{
			TeamID:    team.ID(),
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"},
			Sidecars: []atc.SidecarConfig{{Name: "probe", Image: "alpine",
				Command: []string{"sh", "-c", "test ! -e " + tokenPath + " && exec sleep 600"}}},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	cleanupPod(t, clientset, cfg.Namespace, jetbridge.GeneratePodName(db.ContainerMetadata{Type: db.ContainerTypeTask,
		BuildID: build.ID(), BuildName: build.Name(), PipelineID: pipeline.ID(), PipelineName: pipeline.Name(),
		JobID: job.ID(), JobName: job.Name()}, handle))

	// Alpine's wget speaks TLS through ssl_client. A request with no
	// credentials is answered 401 or 403; reaching the API at all with a
	// 2xx would mean some identity was presented.
	script := `set -u
test ! -e ` + tokenPath + ` || { echo "a token is mounted"; exit 91; }
out=$(wget -q -O- --no-check-certificate https://kubernetes.default.svc/api 2>&1) && { echo "the API answered: $out"; exit 92; }
echo "$out" | grep -qE '40[13]' || { echo "unexpected API failure: $out"; exit 93; }
`
	var stdout bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", script}},
		runtime.ProcessIO{Stdout: &stdout, Stderr: &stdout})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("the main container's check exited %d: %s", result.ExitStatus, stdout.String())
	}

	// The sidecar may still be pulling its image when the main check returns;
	// wait for it to run or to have stopped, whichever comes first.
	var pod corev1.Pod
	for deadline := time.Now().Add(time.Minute); ; {
		pods, err := clientset.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + handle})
		if err != nil || len(pods.Items) != 1 {
			t.Fatalf("the step's pod: %d found, err %v", len(pods.Items), err)
		}
		pod = pods.Items[0]
		settled := false
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "probe" && (status.State.Running != nil || status.State.Terminated != nil) {
				settled = true
			}
		}
		if settled || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if pod.Spec.ServiceAccountName != "default" {
		t.Fatalf("serviceAccountName = %q, want the default identity's %q", pod.Spec.ServiceAccountName, "default")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken = %v, want false", pod.Spec.AutomountServiceAccountToken)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "probe" {
			continue
		}
		if status.State.Running == nil {
			t.Fatalf("the sidecar is not running (its token check failed?): %+v", status.State)
		}
		t.Logf("default identity confirmed: no token in main or sidecar, the API refused the task")
		return
	}
	t.Fatalf("the pod has no probe sidecar status: %+v", pod.Status.ContainerStatuses)
}
