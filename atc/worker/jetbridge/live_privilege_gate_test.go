//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLivePrivilegeGate holds the privilege gate to a real API server and
// kubelet, on the exec step runtime, under --kubernetes-step-privilege=granted:
//
//   - a privileged task of a job a privileged step pod grant names is admitted,
//     and its command holds privilege (it mounts a tmpfs, which needs
//     CAP_SYS_ADMIN);
//   - a privileged task of a job no grant names is refused at creation, and
//     the cluster has no pod for it;
//   - a privileged check of a resource whose type a resource-type grant names
//     is admitted through check eligibility, annotated with that grant, and
//     holds privilege;
//   - a pod made privileged under any is not reused once the web enforces
//     granted: the step fails naming the grant it lacks, and the pod goes.
//
// The namespace (K8S_TEST_NAMESPACE) must admit privileged pods: a Pod
// Security level below privileged refuses the admitted cases, and the test
// fails saying so rather than skipping.
func TestLivePrivilegeGate(t *testing.T) {
	clientset, baseCfg := kubeClient(t)
	restConfig, err := jetbridge.RestConfig(*baseCfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	database := withLiveBuilds(t, useLiveJetbridgeDB(t))
	dbWorker, err := persistNamedWorker(database, "live-k8s-worker")
	if err != nil {
		t.Fatalf("persisting worker: %v", err)
	}
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	namespace := baseCfg.Namespace
	stamp := time.Now().Format("150405")

	const pipelineName = "live-privgate"
	team, found, err := database.TeamFactory.FindTeam("main")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		if team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"}); err != nil {
			t.Fatal(err)
		}
	}
	task := func(name string) atc.JobConfig {
		return atc.JobConfig{Name: name, PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t",
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}}
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: pipelineName}, atc.Config{
		ResourceTypes: atc.ResourceTypes{{Name: "dind", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "busybox"}}},
		Resources:     atc.ResourceConfigs{{Name: "repo", Type: "dind", Source: atc.Source{"uri": "https://example.com/repo"}}},
		Jobs:          atc.JobConfigs{task("dind"), task("unit")},
	}, db.ConfigVersion(0), false)
	if err != nil {
		t.Fatal(err)
	}

	const (
		dindGrant   = "name=live-privgate-dind,owner=main/" + pipelineName + "/dind,privileged=true"
		checksGrant = "name=live-privgate-checks,owner=main/" + pipelineName + "/resource-type/dind,privileged=true"
	)
	worker := func(mode string, grants ...string) *jetbridge.Worker {
		t.Helper()
		cfg := *baseCfg
		cfg.StepRuntime = jetbridge.StepRuntimeExec
		cfg.StepPrivilege = mode
		cfg.StepPodGrants = nil
		for _, value := range grants {
			grant, err := jetbridge.ParseStepPodGrant(value)
			if err != nil {
				t.Fatal(err)
			}
			cfg.StepPodGrants = append(cfg.StepPodGrants, grant)
		}
		return jetbridge.NewWorker(dbWorker, clientset, cfg, jetbridge.WorkerDeps{
			Executor:         executor,
			StepPodBuilds:    database.BuildFactory,
			StepPodPipelines: jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory),
		})
	}
	jobStep := func(t *testing.T, name string) db.ContainerMetadata {
		t.Helper()
		job, found, err := pipeline.Job(name)
		if err != nil || !found {
			t.Fatalf("job %s: found=%v err=%v", name, found, err)
		}
		build, err := job.CreateBuild("live-privgate")
		if err != nil {
			t.Fatal(err)
		}
		return db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "t", BuildID: build.ID(), BuildName: build.Name(),
			PipelineID: pipeline.ID(), PipelineName: pipeline.Name(), JobID: job.ID(), JobName: job.Name()}
	}
	repoCheck := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "repo", BuildName: db.CheckBuildName,
		PipelineID: pipeline.ID(), PipelineName: pipeline.Name()}

	// A tmpfs mount needs CAP_SYS_ADMIN: it succeeds only in a privileged
	// container.
	const holdsPrivilege = "mkdir -p /tmp/gate && mount -t tmpfs none /tmp/gate && echo privileged"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// start makes the step's container and runs script in it, privileged.
	start := func(t *testing.T, w *jetbridge.Worker, handle string, metadata db.ContainerMetadata, script string) (runtime.Process, *bytes.Buffer, error) {
		t.Helper()
		spec := runtime.ContainerSpec{TeamID: team.ID(), Type: metadata.Type,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true}}
		if metadata.Type == db.ContainerTypeCheck {
			spec.StepResourceType = "dind"
		}
		container, _, err := w.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata, spec, &noopDelegate{})
		if err != nil {
			t.Fatalf("FindOrCreateContainer: %v", err)
		}
		cleanupPod(t, clientset, namespace, jetbridge.GeneratePodName(metadata, handle))
		var out bytes.Buffer
		process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", script}},
			runtime.ProcessIO{Stdout: &out, Stderr: &out})
		return process, &out, err
	}
	runs := func(t *testing.T, w *jetbridge.Worker, handle string, metadata db.ContainerMetadata) {
		t.Helper()
		process, out, err := start(t, w, handle, metadata, holdsPrivilege)
		if err != nil {
			if apierrors.IsForbidden(err) || strings.Contains(err.Error(), "violates PodSecurity") {
				t.Fatalf("namespace %s does not admit privileged pods, which this test needs: %v", namespace, err)
			}
			t.Fatalf("Run: %v", err)
		}
		result, err := process.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if result.ExitStatus != 0 || !strings.Contains(out.String(), "privileged") {
			t.Fatalf("the admitted step did not hold privilege: exit %d: %s", result.ExitStatus, out.String())
		}
	}
	pods := func(t *testing.T, handle string) int {
		t.Helper()
		list, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + handle})
		if err != nil {
			t.Fatal(err)
		}
		live := 0
		for _, pod := range list.Items {
			if pod.DeletionTimestamp == nil {
				live++
			}
		}
		return live
	}

	t.Run("a granted privileged task is admitted and holds privilege", func(t *testing.T) {
		runs(t, worker(jetbridge.StepPrivilegeGranted, dindGrant), "live-privgate-granted-"+stamp, jobStep(t, "dind"))
	})

	t.Run("an ungranted privileged task is refused at creation", func(t *testing.T) {
		handle := "live-privgate-refused-" + stamp
		_, _, err := start(t, worker(jetbridge.StepPrivilegeGranted, dindGrant), handle, jobStep(t, "unit"), holdsPrivilege)
		if err == nil || !strings.Contains(err.Error(), "Owner main/"+pipelineName+"/unit has no step pod grant") {
			t.Fatalf("Run = %v, want a refusal naming the owner's missing grant", err)
		}
		if n := pods(t, handle); n != 0 {
			t.Fatalf("a refused step has %d pods in the cluster", n)
		}
	})

	t.Run("a check of a resource-type grant's type is admitted, annotated with the grant", func(t *testing.T) {
		handle := "live-privgate-check-" + stamp
		runs(t, worker(jetbridge.StepPrivilegeGranted, checksGrant), handle, repoCheck)
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, jetbridge.GeneratePodName(repoCheck, handle), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := pod.Annotations["concourse.dev/step-pod-grant"]; got != "live-privgate-checks" {
			t.Fatalf("the check pod's grant annotation is %q, want live-privgate-checks", got)
		}
	})

	t.Run("a pod made privileged under any is not reused under granted, and is deleted", func(t *testing.T) {
		handle := "live-privgate-reuse-" + stamp
		metadata := jobStep(t, "unit")
		runs(t, worker(jetbridge.StepPrivilegeAny), handle, metadata)
		podName := jetbridge.GeneratePodName(metadata, handle)
		if n := pods(t, handle); n != 1 {
			t.Fatalf("the pause pod is not live after its first step: %d pods", n)
		}

		_, _, err := start(t, worker(jetbridge.StepPrivilegeGranted), handle, metadata, holdsPrivilege)
		if err == nil {
			t.Fatal("the over-grant pod was handed back to the step")
		}
		for _, want := range []string{"Owner main/" + pipelineName + "/unit has no step pod grant", "deleted"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q does not say %q", err, want)
			}
		}
		for deadline := time.Now().Add(2 * time.Minute); ; {
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) || (err == nil && pod.DeletionTimestamp != nil) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("pod %s is still live two minutes after its re-admission was refused", podName)
			}
			time.Sleep(2 * time.Second)
		}
	})
}
