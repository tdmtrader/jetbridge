//go:build live
// +build live

package k8sruntime_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveVolumePassingGetToTask tests the full volume passing flow from a
// get step to a task step. The get step writes a file to its output path,
// and the task step receives that output as an input artifact and reads it.
func TestLiveVolumePassingGetToTask(t *testing.T) {
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	ts := time.Now().Format("150405")

	// --- Step 1: Simulate a "get" step that produces output ---
	getHandle := "live-vp-get-" + ts
	getWorker, getDelegate := setupLiveWorker(t, getHandle)

	getContainer, getMounts, err := getWorker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(getHandle),
		db.ContainerMetadata{Type: db.ContainerTypeGet},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"my-resource": "/tmp/build/workdir/my-resource"},
		},
		getDelegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (get): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), getHandle, metav1.DeleteOptions{})
	})

	// Run the get step: write a file to the output volume.
	getProcess, err := getContainer.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo 'hello from get step' > /tmp/build/workdir/my-resource/data.txt"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run (get): %v", err)
	}

	getResult, err := getProcess.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (get): %v", err)
	}
	if getResult.ExitStatus != 0 {
		t.Fatalf("get step expected exit 0, got %d", getResult.ExitStatus)
	}
	t.Logf("get step completed with %d volume mounts", len(getMounts))

	// Find the output volume for "my-resource".
	var getOutputVolume runtime.Volume
	for _, m := range getMounts {
		if m.MountPath == "/tmp/build/workdir/my-resource" {
			getOutputVolume = m.Volume
			break
		}
	}
	if getOutputVolume == nil {
		t.Fatal("no volume mount found for output path /tmp/build/workdir/my-resource")
	}
	t.Logf("get output volume: handle=%s source=%s", getOutputVolume.Handle(), getOutputVolume.Source())

	// --- Step 2: Simulate a "task" step that receives the get output as input ---
	taskHandle := "live-vp-task-" + ts
	taskWorker, taskDelegate := setupLiveWorker(t, taskHandle)

	var taskStdout bytes.Buffer
	taskContainer, _, err := taskWorker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(taskHandle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        getOutputVolume,
					DestinationPath: "/tmp/build/workdir/my-resource",
				},
			},
		},
		taskDelegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (task): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), taskHandle, metav1.DeleteOptions{})
	})

	// Run the task step: read the file from the input volume.
	taskProcess, err := taskContainer.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "cat /tmp/build/workdir/my-resource/data.txt"},
	}, runtime.ProcessIO{
		Stdout: &taskStdout,
	})
	if err != nil {
		t.Fatalf("Run (task): %v", err)
	}

	taskResult, err := taskProcess.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (task): %v", err)
	}
	if taskResult.ExitStatus != 0 {
		t.Fatalf("task step expected exit 0, got %d", taskResult.ExitStatus)
	}

	output := strings.TrimSpace(taskStdout.String())
	if output != "hello from get step" {
		t.Fatalf("expected 'hello from get step', got %q", output)
	}
	t.Logf("volume passing get -> task successful: %q", output)
}

// TestLiveVolumePassingTaskChain tests that a task step's output is available
// as the next task step's input. This simulates a two-task pipeline where
// task-1 produces output consumed by task-2.
func TestLiveVolumePassingTaskChain(t *testing.T) {
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	ts := time.Now().Format("150405")

	// --- Task 1: Produce output ---
	t1Handle := "live-vp-t1-" + ts
	t1Worker, t1Delegate := setupLiveWorker(t, t1Handle)

	t1Container, t1Mounts, err := t1Worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(t1Handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"build-output": "/tmp/build/workdir/build-output"},
		},
		t1Delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (task-1): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), t1Handle, metav1.DeleteOptions{})
	})

	// Task 1 writes a result file.
	t1Process, err := t1Container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo 'compiled-binary-v1.0' > /tmp/build/workdir/build-output/artifact.txt && echo 'build-metadata' > /tmp/build/workdir/build-output/meta.txt"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run (task-1): %v", err)
	}

	t1Result, err := t1Process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (task-1): %v", err)
	}
	if t1Result.ExitStatus != 0 {
		t.Fatalf("task-1 expected exit 0, got %d", t1Result.ExitStatus)
	}
	t.Logf("task-1 completed, %d volume mounts", len(t1Mounts))

	// Find the output volume for "build-output".
	var t1OutputVolume runtime.Volume
	for _, m := range t1Mounts {
		if m.MountPath == "/tmp/build/workdir/build-output" {
			t1OutputVolume = m.Volume
			break
		}
	}
	if t1OutputVolume == nil {
		t.Fatal("no volume mount found for task-1 output path /tmp/build/workdir/build-output")
	}
	t.Logf("task-1 output volume: handle=%s", t1OutputVolume.Handle())

	// --- Task 2: Consume task-1 output as input ---
	t2Handle := "live-vp-t2-" + ts
	t2Worker, t2Delegate := setupLiveWorker(t, t2Handle)

	var t2Stdout bytes.Buffer
	t2Container, _, err := t2Worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(t2Handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        t1OutputVolume,
					DestinationPath: "/tmp/build/workdir/build-output",
				},
			},
		},
		t2Delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (task-2): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), t2Handle, metav1.DeleteOptions{})
	})

	// Task 2 reads both files from the input volume.
	t2Process, err := t2Container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "cat /tmp/build/workdir/build-output/artifact.txt && cat /tmp/build/workdir/build-output/meta.txt"},
	}, runtime.ProcessIO{
		Stdout: &t2Stdout,
	})
	if err != nil {
		t.Fatalf("Run (task-2): %v", err)
	}

	t2Result, err := t2Process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (task-2): %v", err)
	}
	if t2Result.ExitStatus != 0 {
		t.Fatalf("task-2 expected exit 0, got %d", t2Result.ExitStatus)
	}

	lines := strings.Split(strings.TrimSpace(t2Stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines of output, got %d: %q", len(lines), t2Stdout.String())
	}
	if lines[0] != "compiled-binary-v1.0" {
		t.Fatalf("expected 'compiled-binary-v1.0', got %q", lines[0])
	}
	if lines[1] != "build-metadata" {
		t.Fatalf("expected 'build-metadata', got %q", lines[1])
	}
	t.Logf("volume passing task-1 -> task-2 successful: %v", lines)
}

// TestLiveVolumeDataIntegrity verifies that data written in step 1 is read
// back identically in step 2 — no corruption, truncation, or encoding issues
// in the tar-based volume streaming pipeline.
func TestLiveVolumeDataIntegrity(t *testing.T) {
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	ts := time.Now().Format("150405")

	// --- Step 1: Write known data ---
	s1Handle := "live-vp-int1-" + ts
	s1Worker, s1Delegate := setupLiveWorker(t, s1Handle)

	// Build a test payload with special characters, newlines, and enough
	// size to exercise buffering.
	testContent := "line-1: hello world\nline-2: special chars !@#$%^&*()\nline-3: unicode \n"

	s1Container, s1Mounts, err := s1Worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(s1Handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"data": "/tmp/build/workdir/data"},
		},
		s1Delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (step-1): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), s1Handle, metav1.DeleteOptions{})
	})

	// Write the test content to a file in the output volume.
	// Using printf to preserve exact content without shell interpretation.
	s1Process, err := s1Container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "cat > /tmp/build/workdir/data/payload.txt"},
	}, runtime.ProcessIO{
		Stdin: strings.NewReader(testContent),
	})
	if err != nil {
		t.Fatalf("Run (step-1): %v", err)
	}

	s1Result, err := s1Process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (step-1): %v", err)
	}
	if s1Result.ExitStatus != 0 {
		t.Fatalf("step-1 expected exit 0, got %d", s1Result.ExitStatus)
	}
	t.Logf("step-1 wrote %d bytes to payload.txt", len(testContent))

	// Find the output volume.
	var s1OutputVolume runtime.Volume
	for _, m := range s1Mounts {
		if m.MountPath == "/tmp/build/workdir/data" {
			s1OutputVolume = m.Volume
			break
		}
	}
	if s1OutputVolume == nil {
		t.Fatal("no volume mount found for step-1 output path")
	}

	// --- Step 2: Read data back and verify ---
	s2Handle := "live-vp-int2-" + ts
	s2Worker, s2Delegate := setupLiveWorker(t, s2Handle)

	var s2Stdout bytes.Buffer
	s2Container, _, err := s2Worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(s2Handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        s1OutputVolume,
					DestinationPath: "/tmp/build/workdir/data",
				},
			},
		},
		s2Delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (step-2): %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(cfg.Namespace).Delete(context.Background(), s2Handle, metav1.DeleteOptions{})
	})

	// Read the payload back.
	s2Process, err := s2Container.Run(ctx, runtime.ProcessSpec{
		Path: "cat",
		Args: []string{"/tmp/build/workdir/data/payload.txt"},
	}, runtime.ProcessIO{
		Stdout: &s2Stdout,
	})
	if err != nil {
		t.Fatalf("Run (step-2): %v", err)
	}

	s2Result, err := s2Process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (step-2): %v", err)
	}
	if s2Result.ExitStatus != 0 {
		t.Fatalf("step-2 expected exit 0, got %d", s2Result.ExitStatus)
	}

	readBack := s2Stdout.String()
	if readBack != testContent {
		t.Fatalf("data integrity check failed.\nexpected (%d bytes): %q\ngot      (%d bytes): %q",
			len(testContent), testContent, len(readBack), readBack)
	}
	t.Logf("data integrity verified: %d bytes match exactly", len(readBack))
}
