package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ArtifactHandoff struct {
	Expected map[string]string
	Received map[string]string
	Err      error
}

func ArtifactHandoffDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		TransformUsing[ArtifactCluster, ArtifactHandoff](
			"a task produces {string} containing {string} and hands it to a following task using {string} with fault {string}",
			[]string{"task-workspace"},
			func(in ArtifactCluster, a Args, res brine.Resources) (ArtifactHandoff, error) {
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return ArtifactHandoff{}, fmt.Errorf("task-workspace resource is %T", res.Get("task-workspace"))
				}
				out, err := handoff(in, workspace, a.String(0), a.String(1), a.String(2), a.String(3))
				out.Err = err
				return out, nil
			}),
		CheckContains[ArtifactHandoff]("the handoff reports {string}", "artifact handoff",
			func(in ArtifactHandoff) (string, error) {
				if in.Err != nil {
					return in.Err.Error(), nil
				}
				if !reflect.DeepEqual(in.Expected, in.Received) {
					return "", fmt.Errorf("expected exact artifact %v, received %v", in.Expected, in.Received)
				}
				return "exact artifact delivered", nil
			}),
	}
}

func handoff(in ArtifactCluster, workspace TaskWorkspace, name, content, encoding, fault string) (ArtifactHandoff, error) {
	ctx, cancel := context.WithTimeout(in.Ctx, 15*time.Second)
	defer cancel()
	in.Ctx = ctx
	out := ArtifactHandoff{Expected: map[string]string{name: content, "manifest.txt": "version=1", "output.txt": "hello-from-the-step"}}
	if fault != "none" && fault != "write-refused" && fault != "producer-offline" {
		return out, fmt.Errorf("unknown handoff fault %q", fault)
	}
	var enc compression.Compression
	switch encoding {
	case "raw":
	case "gzip":
		enc = compression.NewGzipCompression()
	case "s2":
		enc = compression.NewS2Compression()
	default:
		return out, fmt.Errorf("unknown handoff encoding %q", encoding)
	}
	// The requested encoding is an independent expectation: using the same
	// mislabeled codec at both ends must not make the round trip pass.
	if encoding != "raw" && (enc == nil || string(enc.Encoding()) != encoding) {
		return out, fmt.Errorf("requested encoding %q was not provided by codec %T", encoding, enc)
	}
	producerState := TaskWorkspace{Dir: filepath.Join(workspace.Dir, "producer")}
	consumerState := TaskWorkspace{Dir: filepath.Join(workspace.Dir, "consumer")}
	in.Worker.SetExecutor(localExecutor{client: in.Clientset, supervisorRoot: producerState.Dir})
	spec := runtime.ContainerSpec{TeamID: in.Team.ID(), Type: db.ContainerTypeTask,
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}, Dir: "/work",
		Outputs: map[string]string{"result": "/work/result"}}
	producer, output, producerDir, err := handoffContainer(in, "producer", spec, "/work/result")
	if err != nil {
		return out, err
	}
	// The local process writes into the directory selected by the actual pod.
	// Host paths are passed as shell arguments, never interpolated into code.
	_, err = runHandoffTask(in, producerState, producer, runtime.ProcessSpec{Path: "sh", Args: []string{
		"-c", `mkdir -p "$(dirname "$1/$3")"; printf '%s' "$2" > "$1/$3"; printf 'version=1' > "$1/manifest.txt"; printf 'hello-from-the-step' > "$1/output.txt"; printf decoy > "$1/../decoy.txt"`,
		"brine", producerDir, content, name,
	}})
	if err != nil {
		return out, fmt.Errorf("producing task: %w", err)
	}
	// Keep the public volume compatibility contract before collecting the pod.
	// Production artifact reads below still go through the daemon after deletion.
	direct, err := output.StreamOut(in.Ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return out, fmt.Errorf("read returned output volume: %w", err)
	}
	files, readErr := filesInGzippedTar(direct)
	closeErr := direct.Close()
	if readErr != nil {
		return out, fmt.Errorf("read returned output volume: %w", readErr)
	}
	if closeErr != nil {
		return out, fmt.Errorf("close returned output volume: %w", closeErr)
	}
	if !reflect.DeepEqual(out.Expected, files) {
		return out, fmt.Errorf("returned output volume: expected exact files %v, received %v", out.Expected, files)
	}
	artifact := in.Worker.ArtifactFromVolume(output)
	if err := in.Clientset.CoreV1().Pods(in.Namespace).Delete(in.Ctx, "producer", metav1.DeleteOptions{}); err != nil {
		return out, err
	}
	if fault == "producer-offline" {
		if err := in.Node.stop(); err != nil {
			return out, fmt.Errorf("stop producing node's daemon: %w", err)
		}
	}
	// Reading after removal proves that this is the daemon-backed production
	// route. A raw volume read would try to exec into the deleted pod.
	stream, err := artifact.StreamOut(in.Ctx, ".", enc)
	if err != nil {
		return out, fmt.Errorf("read collected producer's artifact: %w", err)
	}
	defer stream.Close()
	spec.Outputs = nil
	spec.Inputs = []runtime.Input{{Artifact: artifact, DestinationPath: "/work/input"}}
	consumerExecutor := localExecutor{client: in.Clientset, supervisorRoot: consumerState.Dir}
	if fault == "write-refused" {
		consumerExecutor.failure = "transfer refused"
	}
	in.Worker.SetExecutor(consumerExecutor)
	consumer, input, consumerDir, err := handoffContainer(in, "consumer", spec, "/work/input")
	if err != nil {
		return out, err
	}
	// Exercise the returned input volume's public streaming API. Init-container
	// execution remains covered separately by the real-daemon fetch scenarios.
	if err := input.StreamIn(in.Ctx, ".", enc, 0, stream); err != nil {
		return out, fmt.Errorf("stream into returned input volume: %w", err)
	}
	read, err := runHandoffTask(in, consumerState, consumer, runtime.ProcessSpec{
		Path: "tar", Args: []string{"cf", "-", "-C", consumerDir, "."},
	})
	if err != nil {
		return out, fmt.Errorf("consuming task: %w", err)
	}
	out.Received, err = filesInTar(bytes.NewReader(read))
	return out, err
}

// Run creates the pause pod without starting the command. We then supply the
// kubelet's Running status; the later Wait executes the real supervised task.
func handoffContainer(in ArtifactCluster, handle string, spec runtime.ContainerSpec, path string) (runtime.Container, runtime.Volume, string, error) {
	container, mounts, err := in.Worker.FindOrCreateContainer(in.Ctx,
		db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: spec.Type}, spec, &noopDelegate{})
	if err != nil {
		return nil, nil, "", err
	}
	if _, err := container.Run(in.Ctx, runtime.ProcessSpec{Path: "true"}, runtime.ProcessIO{}); err != nil {
		return nil, nil, "", err
	}
	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(in.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return nil, nil, "", err
	}
	if err := validatePodMounts(pod); err != nil {
		return nil, nil, "", err
	}
	pod.Spec.NodeName = in.NodeName
	pod.Status.Phase = corev1.PodRunning
	if _, err := pods.Update(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return nil, nil, "", err
	}
	dir, err := podHostDir(pod, handle, path)
	if err != nil {
		return nil, nil, "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, "", err
	}
	var volume runtime.Volume
	for _, mount := range mounts {
		if mount.MountPath == path {
			if volume != nil {
				return nil, nil, "", fmt.Errorf("duplicate returned mount at %q", path)
			}
			volume = mount.Volume
		}
	}
	if volume == nil {
		return nil, nil, "", fmt.Errorf("no returned volume at %q", path)
	}
	return container, volume, dir, nil
}

func runHandoffTask(in ArtifactCluster, state TaskWorkspace, container runtime.Container, spec runtime.ProcessSpec) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	process, err := container.Run(in.Ctx, spec, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return nil, err
	}
	result, err := process.Wait(in.Ctx)
	if err != nil || result.ExitStatus != 0 {
		return nil, fmt.Errorf("task exit %d: %v; stderr: %s", result.ExitStatus, err, stderr.String())
	}
	if err := state.requireSupervisorState(); err != nil {
		return nil, err
	}
	return io.ReadAll(&stdout)
}

func validatePodMounts(pod *corev1.Pod) error {
	declared := map[string]int{}
	for _, v := range pod.Spec.Volumes {
		declared[v.Name]++
	}
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, container := range containers {
		paths := map[string]bool{}
		for _, mount := range container.VolumeMounts {
			if declared[mount.Name] != 1 {
				return fmt.Errorf("container %q mounts %q at %q; expected one declared volume, got %d",
					container.Name, mount.Name, mount.MountPath, declared[mount.Name])
			}
			if paths[mount.MountPath] {
				return fmt.Errorf("container %q has duplicate mounts at %q", container.Name, mount.MountPath)
			}
			paths[mount.MountPath] = true
		}
	}
	return nil
}
