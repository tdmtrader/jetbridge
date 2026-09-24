package jetbridge

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestManagedReadClientsCoverTheConfiguredOperation(t *testing.T) {
	config := NewConfig("test-ns", "")
	config.OutputOperationTimeout = 15 * time.Minute
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "read-node", UID: "node-uid", Labels: map[string]string{
			executioncontrol.ReadyLabel: "ready", output.ReadyLabel: "ready",
		}},
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	source := NewOutputSource(fake.NewSimpleClientset(node), config, nil, 7)
	reader, err := source.ForResultRead(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if reader.ManagedReadTimeout() != 15*time.Minute {
		t.Fatalf("result admission lost the configured operation budget: %s", reader.ManagedReadTimeout())
	}
	if !output.MayStartWork(output.LeaseTermFor(reader.ManagedReadTimeout()), config.OutputOperationTimeout) {
		t.Fatal("result lease cannot admit the configured node operation")
	}
	// Both leases are minted before the pod exists. After startup and the first
	// full transfer, the second input must still be allowed to open its tree.
	remaining := output.LeaseTermFor(source.ManagedInputTimeout(2)) -
		DefaultPodSchedulingTimeout - output.ReadTransferTimeout(config.OutputOperationTimeout)
	if !output.MayStartWork(remaining, config.OutputOperationTimeout) {
		t.Fatal("the second input loses its lease while waiting for the first")
	}
	if reader.http.Timeout <= config.OutputOperationTimeout {
		t.Fatal("result transport expires before the node operation can finish")
	}
}

func TestManagedInputDownloaderCoversTheConfiguredOperation(t *testing.T) {
	config := NewConfig("test-ns", "")
	config.OutputOperationTimeout = 15 * time.Minute
	config.OutputPlaneEnabled, config.HangarEnabled = true, true
	config.ArtifactDaemonHostPath = "/artifacts"
	ref := hangar.TreeRef{Scope: "scope", Digest: hangar.Digest("sha256:" + strings.Repeat("a", 64)), Generation: 1}
	input := runtime.Input{DestinationPath: "/workspace/input", HangarTree: &ref,
		HangarRead: &output.ManagedReadRequest{Ref: ref, Warrant: "signed-warrant",
			Destination: output.ReadDestination{Handle: "consumer", Volume: "input-0"}}}
	volumes := []corev1.Volume{{Name: "input-0", VolumeSource: corev1.VolumeSource{
		HostPath: &corev1.HostPathVolumeSource{Path: "/artifacts/steps/consumer/input-0"},
	}}}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: input.DestinationPath}}
	init, err := NewDaemonSetBackend(config, nil, nil, nil).managedInputInit("consumer", input, volumes, mounts, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range init.VolumeMounts {
		found := false
		for _, volume := range volumes {
			found = found || volume.Name == mount.Name
		}
		if !found || !mount.ReadOnly {
			t.Fatalf("managed input mount is not backed by a read-only volume: %+v", mount)
		}
	}
	// Execute the generated command, recording the downloader's arguments.
	// A permanent refusal ends the loop immediately; no request leaves the host.
	dir := t.TempDir()
	capture := filepath.Join(dir, "wget-args")
	if err := os.WriteFile(filepath.Join(dir, "wget"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$CAPTURE\"\nprintf 'HTTP/1.1 400 Bad Request\\n' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", init.Command[2])
	command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "CAPTURE="+capture, "HOST_IP=127.0.0.1", "TMPDIR="+dir)
	if err := command.Run(); err == nil {
		t.Fatal("the deliberate download refusal was ignored")
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-T\n960\n") {
		t.Fatalf("input download does not cover the 15-minute operation: %s", args)
	}
}

func TestManagedInputsCanFinishBeyondTheOrdinaryStartupBudget(t *testing.T) {
	config := NewConfig("test-ns", "")
	config.PodStartupTimeout, config.PodSchedulingTimeout = 20*time.Millisecond, 20*time.Millisecond
	config.OutputOperationTimeout = time.Second
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "initializing", Namespace: config.Namespace},
		Status: corev1.PodStatus{Phase: corev1.PodPending}}
	client := fake.NewSimpleClientset(pod)
	process := &execProcess{config: config, clientset: client, podName: pod.Name,
		processIO: runtime.ProcessIO{Stderr: io.Discard},
		container: &Container{containerSpec: runtime.ContainerSpec{Inputs: []runtime.Input{
			{HangarRead: &output.ManagedReadRequest{}},
		}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			ready <- ctx.Err()
		case <-time.After(100 * time.Millisecond):
			updated := pod.DeepCopy()
			updated.Status.Phase = corev1.PodRunning
			_, err := client.CoreV1().Pods(config.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
			ready <- err
		}
	}()
	if err := process.waitForRunning(ctx); err != nil {
		t.Fatalf("startup abandoned an input still inside its operation budget: %v", err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
}
