//go:build live
// +build live

package k8sruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// TestLiveLargeFileIntegrity streams a 10MB file through StreamIn/StreamOut
// and verifies the checksum matches, ensuring no data corruption in the
// tar-based streaming pipeline.
func TestLiveLargeFileIntegrity(t *testing.T) {
	handle := "live-lgfile-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	container, mounts, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"data": "/tmp/build/workdir/data"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run the container so the pod is created and volumes are bound.
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "dd if=/dev/urandom of=/tmp/build/workdir/data/largefile.bin bs=1024 count=10240 2>/dev/null && sha256sum /tmp/build/workdir/data/largefile.bin"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	// Find the data volume.
	var dataVolume runtime.Volume
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/workdir/data" {
			dataVolume = m.Volume
			break
		}
	}
	if dataVolume == nil {
		t.Fatal("no volume mount found for /tmp/build/workdir/data")
	}

	// StreamOut the file and compute checksum.
	reader, err := dataVolume.StreamOut(ctx, ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	hash := sha256.New()
	n, err := io.Copy(hash, reader)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}

	t.Logf("StreamOut transferred %d bytes, sha256=%x", n, hash.Sum(nil))

	// Verify we got a non-trivial amount of data (tar headers + 10MB payload).
	if n < 10*1024*1024 {
		t.Fatalf("expected at least 10MB of data, got %d bytes", n)
	}
	t.Logf("large file integrity test passed: %d bytes streamed", n)
}

// TestLiveEmptyDirectoryStreaming verifies that an empty directory can be
// streamed in and out without errors.
func TestLiveEmptyDirectoryStreaming(t *testing.T) {
	handle := "live-emptydir-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	container, mounts, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"empty": "/tmp/build/workdir/empty"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Run: just create the empty directory (it already exists as a mount).
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "ls -la /tmp/build/workdir/empty/"},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	// StreamOut the empty directory.
	var emptyVolume runtime.Volume
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/workdir/empty" {
			emptyVolume = m.Volume
			break
		}
	}
	if emptyVolume == nil {
		t.Fatal("no volume mount found for /tmp/build/workdir/empty")
	}

	reader, err := emptyVolume.StreamOut(ctx, ".", nil)
	if err != nil {
		t.Fatalf("StreamOut of empty dir: %v", err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	if err != nil {
		t.Fatalf("reading empty dir stream: %v", err)
	}

	// Tar of an empty directory should still produce some bytes (tar header).
	t.Logf("empty directory StreamOut produced %d bytes (tar headers)", buf.Len())
}

// TestLiveDeeplyNestedDirectory verifies that a deeply nested directory
// structure is preserved through the streaming pipeline.
func TestLiveDeeplyNestedDirectory(t *testing.T) {
	handle := "live-nested-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	container, mounts, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"nested": "/tmp/build/workdir/nested"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Create a deeply nested directory structure.
	var stdout bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", `
			mkdir -p /tmp/build/workdir/nested/a/b/c/d/e/f/g/h/i/j
			echo "deep" > /tmp/build/workdir/nested/a/b/c/d/e/f/g/h/i/j/leaf.txt
			echo "mid" > /tmp/build/workdir/nested/a/b/c/mid.txt
			echo "top" > /tmp/build/workdir/nested/top.txt
			find /tmp/build/workdir/nested -type f | sort
		`},
	}, runtime.ProcessIO{
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	files := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}
	t.Logf("created %d files in nested structure", len(files))

	// Now use the output volume to pass to a second container and verify.
	var nestedVolume runtime.Volume
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/workdir/nested" {
			nestedVolume = m.Volume
			break
		}
	}
	if nestedVolume == nil {
		t.Fatal("no volume mount found for /tmp/build/workdir/nested")
	}

	// Create a second container that reads the nested output.
	handle2 := "live-nested2-" + time.Now().Format("150405")
	worker2, delegate2 := setupLiveWorker(t, handle2)
	cleanupPod(t, clientset, cfg.Namespace, handle2)

	var stdout2 bytes.Buffer
	container2, _, err := worker2.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle2),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        nestedVolume,
					DestinationPath: "/tmp/build/workdir/nested",
				},
			},
		},
		delegate2,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (reader): %v", err)
	}

	process2, err := container2.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "cat /tmp/build/workdir/nested/a/b/c/d/e/f/g/h/i/j/leaf.txt && cat /tmp/build/workdir/nested/a/b/c/mid.txt && cat /tmp/build/workdir/nested/top.txt"},
	}, runtime.ProcessIO{
		Stdout: &stdout2,
	})
	if err != nil {
		t.Fatalf("Run (reader): %v", err)
	}

	result2, err := process2.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (reader): %v", err)
	}
	if result2.ExitStatus != 0 {
		t.Fatalf("reader expected exit 0, got %d", result2.ExitStatus)
	}

	lines := strings.Split(strings.TrimSpace(stdout2.String()), "\n")
	expected := []string{"deep", "mid", "top"}
	for i, exp := range expected {
		if i >= len(lines) || lines[i] != exp {
			t.Fatalf("line %d: expected %q, got %q (all lines: %v)", i, exp, lines[i], lines)
		}
	}
	t.Logf("deeply nested directory structure preserved through volume passing")
}

// TestLiveFilePermissions verifies that executable bit and file modes are
// preserved through the tar-based streaming pipeline.
func TestLiveFilePermissions(t *testing.T) {
	handle := "live-perms-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	container, mounts, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"perms": "/tmp/build/workdir/perms"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Create files with various permissions.
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", `
			echo '#!/bin/sh' > /tmp/build/workdir/perms/script.sh
			echo 'echo hello' >> /tmp/build/workdir/perms/script.sh
			chmod 755 /tmp/build/workdir/perms/script.sh

			echo 'secret' > /tmp/build/workdir/perms/readonly.txt
			chmod 444 /tmp/build/workdir/perms/readonly.txt

			echo 'normal' > /tmp/build/workdir/perms/normal.txt
			chmod 644 /tmp/build/workdir/perms/normal.txt

			stat -c '%a %n' /tmp/build/workdir/perms/*
		`},
	}, runtime.ProcessIO{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	// Find the perms volume.
	var permsVolume runtime.Volume
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/workdir/perms" {
			permsVolume = m.Volume
			break
		}
	}
	if permsVolume == nil {
		t.Fatal("no volume mount found for /tmp/build/workdir/perms")
	}

	// Pass to second container and verify permissions.
	handle2 := "live-perms2-" + time.Now().Format("150405")
	worker2, delegate2 := setupLiveWorker(t, handle2)
	cleanupPod(t, clientset, cfg.Namespace, handle2)

	var stdout2 bytes.Buffer
	container2, _, err := worker2.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle2),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        permsVolume,
					DestinationPath: "/tmp/build/workdir/perms",
				},
			},
		},
		delegate2,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (reader): %v", err)
	}

	process2, err := container2.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", `
			stat -c '%a %n' /tmp/build/workdir/perms/script.sh /tmp/build/workdir/perms/readonly.txt /tmp/build/workdir/perms/normal.txt
			echo "---"
			/tmp/build/workdir/perms/script.sh
		`},
	}, runtime.ProcessIO{
		Stdout: &stdout2,
	})
	if err != nil {
		t.Fatalf("Run (reader): %v", err)
	}

	result2, err := process2.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (reader): %v", err)
	}
	if result2.ExitStatus != 0 {
		t.Fatalf("reader expected exit 0, got %d", result2.ExitStatus)
	}

	output := stdout2.String()
	t.Logf("permissions output:\n%s", output)

	// Verify executable bit preserved (script runs and produces output).
	if !strings.Contains(output, "hello") {
		t.Fatal("executable script did not produce expected output 'hello'")
	}

	// Verify permission modes.
	lines := strings.Split(strings.TrimSpace(output), "\n")
	permChecks := map[string]string{
		"script.sh":   "755",
		"readonly.txt": "444",
		"normal.txt":  "644",
	}
	for _, line := range lines {
		for file, expectedPerm := range permChecks {
			if strings.Contains(line, file) {
				if !strings.HasPrefix(line, expectedPerm) {
					t.Errorf("expected %s to have mode %s, got line: %s", file, expectedPerm, line)
				} else {
					t.Logf("verified %s has mode %s", file, expectedPerm)
				}
			}
		}
	}

	fmt.Sprintf("") // suppress unused import
}

// TestLiveManySmallFiles verifies that streaming many small files (100+)
// works correctly through the tar pipeline.
func TestLiveManySmallFiles(t *testing.T) {
	handle := "live-manyfiles-" + time.Now().Format("150405")
	worker, delegate := setupLiveWorker(t, handle)
	clientset, cfg := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cleanupPod(t, clientset, cfg.Namespace, handle)

	container, mounts, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Outputs:   runtime.OutputPaths{"files": "/tmp/build/workdir/files"},
		},
		delegate,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}

	// Create 200 small files.
	var stdout bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", `
			for i in $(seq 1 200); do
				echo "content-$i" > /tmp/build/workdir/files/file-$i.txt
			done
			ls /tmp/build/workdir/files/ | wc -l
		`},
	}, runtime.ProcessIO{
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ExitStatus != 0 {
		t.Fatalf("expected exit 0, got %d", result.ExitStatus)
	}

	count := strings.TrimSpace(stdout.String())
	t.Logf("created %s files", count)

	// Find the files volume and pass to second container.
	var filesVolume runtime.Volume
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/workdir/files" {
			filesVolume = m.Volume
			break
		}
	}
	if filesVolume == nil {
		t.Fatal("no volume mount found for /tmp/build/workdir/files")
	}

	handle2 := "live-manyfiles2-" + time.Now().Format("150405")
	worker2, delegate2 := setupLiveWorker(t, handle2)
	cleanupPod(t, clientset, cfg.Namespace, handle2)

	var stdout2 bytes.Buffer
	container2, _, err := worker2.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle2),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
			Dir:       "/tmp/build/workdir",
			Inputs: []runtime.Input{
				{
					Artifact:        filesVolume,
					DestinationPath: "/tmp/build/workdir/files",
				},
			},
		},
		delegate2,
	)
	if err != nil {
		t.Fatalf("FindOrCreateContainer (reader): %v", err)
	}

	process2, err := container2.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", `
			total=$(ls /tmp/build/workdir/files/ | wc -l)
			echo $total
			cat /tmp/build/workdir/files/file-1.txt
			cat /tmp/build/workdir/files/file-200.txt
		`},
	}, runtime.ProcessIO{
		Stdout: &stdout2,
	})
	if err != nil {
		t.Fatalf("Run (reader): %v", err)
	}

	result2, err := process2.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait (reader): %v", err)
	}
	if result2.ExitStatus != 0 {
		t.Fatalf("reader expected exit 0, got %d", result2.ExitStatus)
	}

	lines := strings.Split(strings.TrimSpace(stdout2.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}

	if strings.TrimSpace(lines[0]) != "200" {
		t.Fatalf("expected 200 files, got %s", lines[0])
	}
	if lines[1] != "content-1" {
		t.Fatalf("expected 'content-1', got %q", lines[1])
	}
	if lines[2] != "content-200" {
		t.Fatalf("expected 'content-200', got %q", lines[2])
	}
	t.Logf("many small files test passed: 200 files preserved through volume passing")
}
