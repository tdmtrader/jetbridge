package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// An upstream image, not installed echo scripts. Resolve updates deliberately;
// the registry receipt for this pin is recorded in the migration journal.
const gitResourceImage = "concourse/git-resource@sha256:6ae5106a362ec97b719276d4b0560526c385afac87444b24736ec5b3660d0175"
const gitSourcePath = "/tmp/brine-git-source"
const gitRemotePath = "/tmp/brine-git-remote.git"
const gitPutInput = "/tmp/build/put/my-repo"

type GitResourceOutcome struct {
	creationErr                                 error
	Cluster                                     WorkerReady
	Kind, Handle, ExpectedRef                   string
	Stdout, Stderr, Files, MountInfo, ProcessID string
	Status                                      int
	Err, observationErr                         error
	Pod                                         *corev1.Pod
	startUID                                    types.UID
}

func GitResourceDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Transform[WorkerReady, GitResourceOutcome]("a Git {string} resource runs with a {string} request",
			func(in WorkerReady, a Args) (GitResourceOutcome, error) {
				return runGitResource(in, a.String(0), a.String(1))
			}),
		Assert[GitResourceOutcome]("the Git resource reports {string}",
			func(in GitResourceOutcome, a Args) error { return checkGitResource(in, a.String(0)) }),
		CheckThat[GitResourceOutcome]("the Git resource keeps its container and pod contract", checkGitResourceContract),
	}
}

func runGitResource(w WorkerReady, kind, request string) (GitResourceOutcome, error) {
	out := GitResourceOutcome{Kind: kind, Handle: "real-git-" + kind}
	if kind != "get" && kind != "put" && kind != "check" || request != "valid" && request != "invalid" {
		return out, fmt.Errorf("unknown Git resource case %q %q", kind, request)
	}
	w.Config.ResourceTypeImages = jetbridge.MergeResourceTypeImages([]string{"git=" + gitResourceImage})
	w.Config.PodStartupTimeout, w.Config.PodSchedulingTimeout = time.Minute, time.Minute
	w = w.rebuild()
	out.Cluster = w
	cpu, memory := uint64(250), uint64(128*1024*1024)
	dir := "/tmp/build/" + kind
	spec := runtime.ContainerSpec{Type: db.ContainerType(kind), Dir: dir, CertsBindMount: true,
		ImageSpec: runtime.ImageSpec{ResourceType: "git"},
		Limits:    runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}
	// This is a real exec-backed artifact with real Git files inside this pod.
	// The mount test supplies it through the public volume API. It does not
	// stand in for DaemonSetBackend publication or automatic init fetching.
	source := jetbridge.NewDeferredVolume("owned-git-source", w.DBWorker.Name(), w.Executor, w.Namespace, "main", gitSourcePath)
	source.SetPodName(out.Handle)
	if kind == "put" {
		spec.Inputs = []runtime.Input{{Artifact: source, DestinationPath: gitPutInput}}
	}
	container, mounts, err := w.Worker.FindOrCreateContainer(w.Ctx,
		db.NewFixedHandleContainerOwner(out.Handle), db.ContainerMetadata{Type: db.ContainerType(kind)}, spec, nil)
	if err != nil {
		return out, err
	}
	// Preserve the original creation-time assertion before Run can change
	// anything. The final contract check reports this observed outcome.
	out.creationErr = checkContainerRow(IntegrationCluster{WorkerReady: w}, out.Handle, kind, w.DBWorker.Name())
	fmt.Printf("observed Git container %s before Run: %v\n", out.Handle, out.creationErr)
	processSpec := runtime.ProcessSpec{ID: "resource", Path: "/opt/resource/in", Args: []string{dir}}
	switch kind {
	case "put":
		processSpec.Path = "/opt/resource/out"
	case "check":
		processSpec = runtime.ProcessSpec{Path: "/opt/resource/check"}
	}
	// Run creates the pause pod, but Wait alone starts its resource command.
	// Fill stdin after seeding the real repository, before that single Wait.
	var input, stdout, stderr bytes.Buffer
	process, err := container.Run(w.Ctx, processSpec, runtime.ProcessIO{Stdin: &input, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		out.Err = err
		return out, nil
	}
	out.ProcessID = process.ID()
	pod, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, out.Handle)
	if err != nil {
		return out, err
	}
	out.startUID = pod.UID
	seed := "git init -q -b main \"$1\"; git -C \"$1\" config user.name Brine; git -C \"$1\" config user.email brine@example.invalid; printf 'seed\\n' > \"$1/payload.txt\"; git -C \"$1\" add payload.txt; git -C \"$1\" -c commit.gpgsign=false commit -qm seed; git clone -q --bare \"$1\" \"$2\"; git -C \"$1\" remote add origin \"$2\"; git -C \"$1\" rev-parse HEAD"
	ref, err := gitFixtureExec(w, out.Handle, []string{"sh", "-ec", seed, "seed-git", gitSourcePath, gitRemotePath})
	if err != nil {
		return out, fmt.Errorf("seed actual Git repository: %w", err)
	}
	out.ExpectedRef = strings.TrimSpace(ref)
	if len(out.ExpectedRef) != 40 && len(out.ExpectedRef) != 64 {
		return out, fmt.Errorf("invalid actual Git commit %q", out.ExpectedRef)
	}
	if kind == "put" {
		var input runtime.Volume
		for _, mount := range mounts {
			if mount.MountPath == gitPutInput {
				input = mount.Volume
			}
		}
		if input == nil {
			return out, fmt.Errorf("worker returned no Git input volume")
		}
		if err := streamGitInput(w, source, input); err != nil {
			return out, err
		}
		ref, err := gitFixtureExec(w, out.Handle, []string{"sh", "-ec",
			"printf 'published\\n' > \"$1/payload.txt\"; git -C \"$1\" add payload.txt; git -C \"$1\" -c commit.gpgsign=false commit -qm published; git -C \"$1\" rev-parse HEAD",
			"advance-git", gitPutInput})
		if err != nil {
			return out, err
		}
		out.ExpectedRef = strings.TrimSpace(ref)
	}
	src := map[string]any{"uri": "file://" + gitRemotePath, "branch": "main"}
	if request == "invalid" {
		src = map[string]any{}
	}
	payload := map[string]any{"source": src}
	switch kind {
	case "get":
		payload["version"] = map[string]string{"ref": out.ExpectedRef}
	case "put":
		payload["params"] = map[string]string{"repository": "my-repo"}
	}
	requestBytes, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	input.Write(requestBytes)
	result, waitErr := process.Wait(w.Ctx)
	out.Status, out.Err, out.Stdout, out.Stderr = result.ExitStatus, waitErr, stdout.String(), stderr.String()
	out.Pod, out.observationErr = w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, out.Handle, metav1.GetOptions{})
	if waitErr == nil && result.ExitStatus == 0 && request == "valid" {
		switch kind {
		case "get":
			out.Files, err = gitFixtureExec(w, out.Handle, []string{"sh", "-ec", "git -C \"$1\" rev-parse HEAD; cat \"$1/payload.txt\"", "read-git", dir})
		case "put":
			out.Files, err = gitFixtureExec(w, out.Handle, []string{"sh", "-ec", "git --git-dir=\"$1\" rev-parse refs/heads/main; git --git-dir=\"$1\" show refs/heads/main:payload.txt", "read-pushed-git", gitRemotePath})
			if err == nil {
				out.MountInfo, err = gitFixtureExec(w, out.Handle, []string{"cat", "/proc/self/mountinfo"})
			}
		}
		out.observationErr = errors.Join(out.observationErr, err)
	}
	fmt.Printf("actual Git resource %s pod %s/%s UID %s commit %s exit %d\n", kind, w.Namespace, out.Handle, pod.UID, out.ExpectedRef, out.Status)
	return out, nil
}

func gitFixtureExec(w WorkerReady, pod string, command []string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := w.Executor.ExecInPod(w.Ctx, w.Namespace, pod, "main", command, nil, &stdout, &stderr, false,
		jetbridge.ExecAttrs{Purpose: "git-fixture"})
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func streamGitInput(w WorkerReady, source, destination runtime.Volume) error {
	ctx, cancel := context.WithTimeout(w.Ctx, 20*time.Second)
	defer cancel()
	stream, err := source.StreamOut(ctx, ".", nil)
	if err != nil {
		return err
	}
	err = destination.StreamIn(ctx, ".", nil, 0, stream)
	return errors.Join(err, stream.Close())
}

func checkGitResource(in GitResourceOutcome, want string) error {
	if in.Err != nil {
		return fmt.Errorf("Git resource transport failed: %w", in.Err)
	}
	if want == "rejected" {
		if in.Status != 1 || strings.TrimSpace(in.Stdout) != "" || !strings.Contains(in.Stderr, "source.uri is required") {
			return fmt.Errorf("expected real Git source rejection: status=%d stdout=%q stderr=%q", in.Status, in.Stdout, in.Stderr)
		}
		return nil
	}
	if in.Status != 0 {
		return fmt.Errorf("Git %s exited %d: %s", in.Kind, in.Status, in.Stderr)
	}
	if in.observationErr != nil {
		return in.observationErr
	}
	var ref string
	switch want {
	case "checked":
		var versions []struct{ Ref string }
		if err := json.Unmarshal([]byte(in.Stdout), &versions); err != nil {
			return err
		}
		if len(versions) != 1 {
			return fmt.Errorf("expected the owned repository's one version, got %s", in.Stdout)
		}
		ref = versions[0].Ref
	case "fetched", "published":
		var response struct{ Version struct{ Ref string } }
		if err := json.Unmarshal([]byte(in.Stdout), &response); err != nil {
			return err
		}
		ref = response.Version.Ref
		body := "seed\n"
		if want == "published" {
			body = "published\n"
		}
		if in.Files != in.ExpectedRef+"\n"+body {
			return fmt.Errorf("Git %s did not produce the actual commit/file: %q", want, in.Files)
		}
	default:
		return fmt.Errorf("unknown Git outcome %q", want)
	}
	if ref != in.ExpectedRef {
		return fmt.Errorf("Git resource returned %q, actual commit is %q", ref, in.ExpectedRef)
	}
	return nil
}

func checkGitResourceContract(in GitResourceOutcome) error {
	if in.creationErr != nil {
		return fmt.Errorf("at container creation: %w", in.creationErr)
	}
	if in.observationErr != nil {
		return in.observationErr
	}
	if in.Pod == nil || in.Pod.UID != in.startUID {
		return fmt.Errorf("Git resource lost or replaced its pod")
	}
	if err := checkContainerRow(IntegrationCluster{WorkerReady: in.Cluster}, in.Handle, in.Kind, in.Cluster.DBWorker.Name()); err != nil {
		return err
	}
	wantID := "resource"
	if in.Kind == "check" {
		wantID = in.Handle
	}
	if in.ProcessID != wantID {
		return fmt.Errorf("resource process ID %q, want %q", in.ProcessID, wantID)
	}
	if len(in.Pod.Spec.Containers) != 1 {
		return fmt.Errorf("resource pod has %d containers, want just main", len(in.Pod.Spec.Containers))
	}
	main := in.Pod.Spec.Containers[0]
	if main.Name != "main" || main.Image != gitResourceImage || len(main.Command) == 0 || strings.HasPrefix(main.Command[0], "/opt/resource/") {
		return fmt.Errorf("unexpected resource pause container: name=%s image=%s command=%v", main.Name, main.Image, main.Command)
	}
	volumes := map[string]bool{}
	for _, v := range in.Pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	if len(volumes) == 0 || len(main.VolumeMounts) == 0 {
		return fmt.Errorf("Git resource has no volume mounts to verify")
	}
	input := false
	for _, containers := range [][]corev1.Container{in.Pod.Spec.Containers, in.Pod.Spec.InitContainers} {
		for _, container := range containers {
			for _, mount := range container.VolumeMounts {
				if !volumes[mount.Name] {
					return fmt.Errorf("pod mount %s has no volume", mount.Name)
				}
				input = input || container.Name == "main" && mount.MountPath == gitPutInput
			}
		}
	}
	if in.Kind == "put" {
		actualMount := false
		for _, line := range strings.Split(in.MountInfo, "\n") {
			fields := strings.Fields(line)
			actualMount = actualMount || len(fields) > 4 && fields[4] == gitPutInput
		}
		if !input || !actualMount {
			return fmt.Errorf("Git put input is not an actual pod mount")
		}
	}
	return nil
}
