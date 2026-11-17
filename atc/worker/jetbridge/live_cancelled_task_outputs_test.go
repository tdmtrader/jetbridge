//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// A task that is aborted after its command started records the outputs it
// had written, on either step runtime, so a hook or a step after a `try`
// can read them. A task aborted before its command started records nothing.

const cancelledTaskReport = "written-before-the-abort"

// stepRuntimes are the two ways a step's command reaches its pod. Each
// configures the producer's worker; the consumer always runs on exec.
var stepRuntimes = []struct {
	name      string
	configure func(*jetbridge.Config, *jetbridge.WorkerDeps)
}{
	{name: jetbridge.StepRuntimeExec, configure: func(*jetbridge.Config, *jetbridge.WorkerDeps) {}},
	{name: jetbridge.StepRuntimeJob, configure: func(cfg *jetbridge.Config, _ *jetbridge.WorkerDeps) {
		cfg.StepRuntime = jetbridge.StepRuntimeJob
		cfg.LauncherImage = deployed.webContainer.Image
	}},
}

// lineSignal is a step's stdout that closes seen once a line containing want
// has been written: the signal that the command has done what the test waits
// for, rather than a guess at how long that takes.
type lineSignal struct {
	want string
	seen chan struct{}

	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
}

func newLineSignal(want string) *lineSignal {
	return &lineSignal{want: want, seen: make(chan struct{})}
}

func (s *lineSignal) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.buf.Write(p)
	if strings.Contains(s.buf.String(), s.want) {
		s.once.Do(func() { close(s.seen) })
	}
	return n, err
}

func (s *lineSignal) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// cleanupProducer removes the producer's step pod, or its step Job, which
// owns the pod.
func cleanupProducer(t *testing.T, clientset kubernetes.Interface, namespace, runtimeName, podName string) {
	t.Helper()
	if runtimeName == jetbridge.StepRuntimeJob {
		t.Cleanup(func() { cleanupStepJob(t, clientset, namespace, podName) })
		return
	}
	cleanupPod(t, clientset, namespace, podName)
}

// mountedVolume is the volume a step's container mounts at path.
func mountedVolume(t *testing.T, mounts []runtime.VolumeMount, path string) runtime.Volume {
	t.Helper()
	for _, m := range mounts {
		if m.MountPath == path {
			return m.Volume
		}
	}
	t.Fatalf("no volume is mounted at %s", path)
	return nil
}

func TestLiveCancelledTaskRecordsItsOutputs(t *testing.T) {
	for _, stepRuntime := range stepRuntimes {
		t.Run(stepRuntime.name, func(t *testing.T) {
			clientset, cfg := kubeClient(t)
			ts := time.Now().Format("150405")
			ctx, cancelAll := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancelAll()

			// One locator for the build, as one web serves every step of it.
			locator := jetbridge.NewArtifactLocator()
			producer, _, _, _ := newLiveWorker(t, locator, nil, stepRuntime.configure)

			handle := "live-cancel-out-" + stepRuntime.name + "-" + ts
			metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "cancelled-producer"}
			container, mounts, err := producer.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Dir:       "/tmp/build/workdir",
					Outputs:   runtime.OutputPaths{"report": "/tmp/build/workdir/report"},
				}, nil)
			if err != nil {
				t.Fatalf("FindOrCreateContainer (producer): %v", err)
			}
			cleanupProducer(t, clientset, cfg.Namespace, stepRuntime.name, jetbridge.GeneratePodName(metadata, handle))
			output := mountedVolume(t, mounts, "/tmp/build/workdir/report")

			stdout := newLineSignal("report-written")
			stderr := &bytes.Buffer{}
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo " + cancelledTaskReport + " > /tmp/build/workdir/report/report.txt && sync && echo report-written && sleep 600"},
			}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
			if err != nil {
				t.Fatalf("Run (producer): %v", err)
			}

			stepCtx, abort := context.WithCancel(ctx)
			defer abort()
			waited := make(chan error, 1)
			go func() {
				_, err := process.Wait(stepCtx)
				waited <- err
			}()
			select {
			case <-stdout.seen:
			case err := <-waited:
				t.Fatalf("the producer ended before it wrote its report: %v (stdout %q, stderr %q)", err, stdout.String(), stderr.String())
			case <-ctx.Done():
				t.Fatalf("the producer never wrote its report (stdout %q, stderr %q)", stdout.String(), stderr.String())
			}
			abort()

			select {
			case err = <-waited:
			case <-ctx.Done():
				t.Fatal("Wait did not return after the abort")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Wait = %v, want the abort's context.Canceled", err)
			}
			if node, ok := locator.LocateNode(jetbridge.ArtifactKey(output.Handle())); !ok {
				t.Fatalf("the aborted task's output %s has no locator entry", output.Handle())
			} else {
				t.Logf("the aborted task's output is recorded on node %s", node)
			}

			// A consumer, as an on_failure hook or a step after a try would be,
			// reads what the aborted task wrote.
			consumerHandle := "live-cancel-in-" + stepRuntime.name + "-" + ts
			consumerMetadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "cancelled-consumer"}
			consumerWorker, _, _, _ := newLiveWorker(t, locator, nil)
			consumer, _, err := consumerWorker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(consumerHandle), consumerMetadata,
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Dir:       "/tmp/build/workdir",
					Inputs:    []runtime.Input{{Artifact: output, DestinationPath: "/tmp/build/workdir/report"}},
				}, nil)
			if err != nil {
				t.Fatalf("FindOrCreateContainer (consumer): %v", err)
			}
			cleanupPod(t, clientset, cfg.Namespace, jetbridge.GeneratePodName(consumerMetadata, consumerHandle))

			var read, readErr bytes.Buffer
			consumerProcess, err := consumer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "cat /tmp/build/workdir/report/report.txt"},
			}, runtime.ProcessIO{Stdout: &read, Stderr: &readErr})
			if err != nil {
				t.Fatalf("Run (consumer): %v", err)
			}
			result, err := consumerProcess.Wait(ctx)
			if err != nil {
				t.Fatalf("Wait (consumer): %v (stderr %q)", err, readErr.String())
			}
			if result.ExitStatus != 0 {
				t.Fatalf("the consumer could not read the aborted task's output: exit %d (stdout %q, stderr %q)", result.ExitStatus, read.String(), readErr.String())
			}
			if got := strings.TrimSpace(read.String()); got != cancelledTaskReport {
				t.Fatalf("the consumer read %q, want %q", got, cancelledTaskReport)
			}
		})
	}
}

// TestLiveCancelledTaskThatNeverStartedRecordsNothing aborts a task whose
// pod cannot be scheduled: its command never started, so it has no outputs
// to record.
func TestLiveCancelledTaskThatNeverStartedRecordsNothing(t *testing.T) {
	for _, stepRuntime := range stepRuntimes {
		t.Run(stepRuntime.name, func(t *testing.T) {
			clientset, cfg := kubeClient(t)
			// Kubernetes stamps creation to the second; a pod made before
			// this second is not this step's.
			began := time.Now().Truncate(time.Second)
			ts := began.Format("150405")
			ctx, cancelAll := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancelAll()

			locator := jetbridge.NewArtifactLocator()
			worker, _, _, _ := newLiveWorker(t, locator, nil, stepRuntime.configure)

			// No node offers 64 TiB of memory, so the pod stays Pending,
			// Unschedulable, until the abort.
			unschedulable := uint64(64) << 40
			handle := "live-cancel-never-" + stepRuntime.name + "-" + ts
			metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "never-started"}
			container, mounts, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Dir:       "/tmp/build/workdir",
					Outputs:   runtime.OutputPaths{"report": "/tmp/build/workdir/report"},
					Limits:    runtime.ContainerLimits{MemoryRequest: &unschedulable},
				}, nil)
			if err != nil {
				t.Fatalf("FindOrCreateContainer: %v", err)
			}
			podName := jetbridge.GeneratePodName(metadata, handle)
			cleanupProducer(t, clientset, cfg.Namespace, stepRuntime.name, podName)
			output := mountedVolume(t, mounts, "/tmp/build/workdir/report")

			stderr := &bytes.Buffer{}
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "echo started > /tmp/build/workdir/report/report.txt"},
			}, runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: stderr})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			stepCtx, abort := context.WithCancel(ctx)
			defer abort()
			waited := make(chan error, 1)
			go func() {
				_, err := process.Wait(stepCtx)
				waited <- err
			}()

			// The exec runtime's pause pod is named for the step; the job
			// runtime's pod is the step Job's, found by its label.
			list := metav1.ListOptions{FieldSelector: "metadata.name=" + podName}
			if stepRuntime.name == jetbridge.StepRuntimeJob {
				list = metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + podName}
			}
			for !podUnschedulable(ctx, t, clientset, cfg.Namespace, list, began) {
				select {
				case err := <-waited:
					t.Fatalf("Wait returned before the pod was seen Unschedulable: %v (stderr %q)", err, stderr.String())
				case <-ctx.Done():
					t.Fatal("the pod was never seen Unschedulable")
				case <-time.After(time.Second):
				}
			}
			abort()

			select {
			case err = <-waited:
			case <-ctx.Done():
				t.Fatal("Wait did not return after the abort")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Wait = %v, want the abort's context.Canceled", err)
			}
			if node, ok := locator.LocateNode(jetbridge.ArtifactKey(output.Handle())); ok {
				t.Fatalf("a task whose command never started recorded its output on node %s", node)
			}
		})
	}
}

// podUnschedulable reports whether a pod this step made (created no earlier
// than began, bound to no node) carries the scheduler's Unschedulable
// condition, and logs the observation the abort is made on.
func podUnschedulable(ctx context.Context, t *testing.T, clientset kubernetes.Interface, namespace string, list metav1.ListOptions, began time.Time) bool {
	t.Helper()
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, list)
	if err != nil {
		t.Logf("listing the step's pod: %v", err)
		return false
	}
	for _, pod := range pods.Items {
		if pod.CreationTimestamp.Time.Before(began) || pod.Spec.NodeName != "" {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
				t.Logf("pod %s (uid %s, created %s) is Unschedulable: %s; aborting", pod.Name, pod.UID, pod.CreationTimestamp.Format(time.RFC3339), condition.Message)
				return true
			}
		}
	}
	return false
}
