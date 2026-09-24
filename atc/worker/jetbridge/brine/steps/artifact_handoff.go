package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ArtifactHandoff struct {
	Expected, Received map[string]string
	NodeName           string
	Err                error
}

func ArtifactHandoffDefinitions() []brine.StepDefinition {
	const action = "a task produces {string} containing {string} and hands it to a following task using {string} with fault {string}"
	const report = "the handoff reports {string}"
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, ArtifactHandoff](action, func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (ArtifactHandoff, error) {
			return applyAction(action, in, p, func(in LiveTaskPlan, a Args) (ArtifactHandoff, error) {
				out, err := handoff(in, rec, a.String(0), a.String(1), a.String(2), a.String(3))
				out.Err = err
				return out, nil
			})
		}),
		check[ArtifactHandoff](report, func(in ArtifactHandoff, p brine.Params) error {
			want, err := paramAt(report, p, 0)
			if err != nil {
				return err
			}
			if want == "artifact unavailable on its producer node or any peer" {
				if in.NodeName == "" {
					return fmt.Errorf("no actual producer node observed")
				}
				want = fmt.Sprintf("artifact not found on node %s or any peer", in.NodeName)
			}
			message := "exact artifact delivered"
			if in.Err != nil {
				message = in.Err.Error()
			} else if !reflect.DeepEqual(in.Expected, in.Received) {
				return fmt.Errorf("expected exact artifact %v, received %v", in.Expected, in.Received)
			}
			if !strings.Contains(message, want) {
				return fmt.Errorf("artifact handoff: expected %q, got %q", want, message)
			}
			return nil
		}),
	}
}

func handoff(in LiveTaskPlan, rec *brine.Recorder, name, content, encoding, fault string) (ArtifactHandoff, error) {
	out := ArtifactHandoff{Expected: map[string]string{name: content, "manifest.txt": "version=1", "output.txt": "hello-from-the-step"}}
	if !filepath.IsLocal(name) || filepath.Clean(name) == "." {
		return out, fmt.Errorf("unsafe artifact filename %q", name)
	}
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
	if encoding != "raw" && (enc == nil || string(enc.Encoding()) != encoding) {
		return out, fmt.Errorf("requested encoding %q was not provided by codec %T", encoding, enc)
	}
	ctx, cancel := context.WithTimeout(execLogger("live-artifact-handoff"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	daemon, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		return out, err
	}
	store := daemon.store
	dbWorker, err := in.Database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return out, err
	}
	config := jetbridge.NewConfig(store.cluster.Namespace, "")
	config.ArtifactDaemonService = liveArtifactDaemonService
	config.ArtifactDaemonHostPath, config.ArtifactDaemonPort = store.root, int(daemon.port)
	config.PodStartupTimeout, config.PodSchedulingTimeout = 30*time.Second, 30*time.Second
	worker := jetbridge.NewWorker(dbWorker, store.cluster.Clientset, config, jetbridge.WorkerDeps{
		Executor: store.executor,
		// Discovery is restricted to this owned namespace, never production peers.
		DaemonClient: jetbridge.NewDaemonClient(lagertest.NewTestLogger("handoff-daemon"), store.cluster.Clientset, store.cluster.Namespace, config.ArtifactDaemonService, int(daemon.port), nil),
	})
	team, err := in.Database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return out, fmt.Errorf("create owned main team: %w", err)
	}
	spec := runtime.ContainerSpec{TeamID: team.ID(), Type: db.ContainerTypeTask,
		ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37.0"}, Dir: "/work", Outputs: map[string]string{"result": "/work/result"}}
	var producerOut, producerErr bytes.Buffer
	command := runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", "mkdir -p \"$(dirname \"$1/$3\")\"; printf '%s' \"$2\" > \"$1/$3\"; printf 'version=1' > \"$1/manifest.txt\"; printf 'hello-from-the-step' > \"$1/output.txt\"; printf decoy > \"$1/../decoy.txt\"", "brine", "/work/result", content, name}}
	process, output, producer, err := liveHandoffContainer(ctx, rec, store, worker, "producer", spec, "/work/result", command, runtime.ProcessIO{Stdout: &producerOut, Stderr: &producerErr})
	if err != nil {
		return out, fmt.Errorf("producing task: %w", err)
	}
	out.NodeName = producer.Spec.NodeName
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		return out, fmt.Errorf("producing task exit %d: %v; stderr=%s", result.ExitStatus, err, producerErr.String())
	}
	direct, err := output.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return out, fmt.Errorf("read returned output volume: %w", err)
	}
	files, readErr := filesInGzippedTar(direct)
	closeErr := direct.Close()
	if readErr != nil || closeErr != nil || !reflect.DeepEqual(files, out.Expected) {
		return out, fmt.Errorf("returned output volume: expected exact files %v, got %v; read=%v close=%v", out.Expected, files, readErr, closeErr)
	}
	artifact := worker.ArtifactFromVolume(output)
	if err := deleteLiveStoragePod(ctx, store, producer); err != nil {
		return out, err
	}
	fmt.Printf("handoff producer UID %s on actual node %s deleted before artifact read; exact returned output verified\n", producer.UID, out.NodeName)
	if fault == "producer-offline" {
		if err := daemon.close(ctx); err != nil {
			return out, fmt.Errorf("stop producing node's daemon: %w", err)
		}
	}
	stream, err := artifact.StreamOut(ctx, ".", enc)
	if err != nil {
		return out, fmt.Errorf("read collected producer's artifact: %w", err)
	}
	defer stream.Close()
	spec.Outputs = nil
	spec.Inputs = []runtime.Input{{Artifact: artifact, DestinationPath: "/work/input"}}
	var consumerOut, consumerErr bytes.Buffer
	process, input, consumer, err := liveHandoffContainer(ctx, rec, store, worker, "consumer", spec, "/work/input",
		runtime.ProcessSpec{Path: "tar", Args: []string{"cf", "-", "-C", "/work/input", "."}}, runtime.ProcessIO{Stdout: &consumerOut, Stderr: &consumerErr})
	if err != nil {
		return out, fmt.Errorf("consuming task: %w", err)
	}
	// Independently prove the production init delivered exact data before the
	// public StreamIn API gets an opportunity to repair missing/wrong contents.
	fetched, err := input.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return out, err
	}
	files, readErr = filesInGzippedTar(fetched)
	closeErr = fetched.Close()
	if readErr != nil || closeErr != nil || !reflect.DeepEqual(files, out.Expected) {
		return out, fmt.Errorf("input-init delivery: expected %v, got %v; read=%v close=%v", out.Expected, files, readErr, closeErr)
	}
	initFound := false
	for _, init := range consumer.Status.InitContainerStatuses {
		if init.Name == "fetch-inputs" {
			initFound = init.ContainerID != "" && init.State.Terminated != nil && init.State.Terminated.ExitCode == 0
		}
	}
	if !initFound || consumer.Status.HostIP != daemon.nodeIP {
		return out, fmt.Errorf("no successful real fetch-inputs on daemon's node IP")
	}
	fmt.Printf("real fetch-inputs completed for consumer UID %s on %s; exact files verified before StreamIn\n", consumer.UID, consumer.Status.HostIP)
	// Clear only the named contents under this returned owned input mount.
	// The second observation must prove StreamIn, not re-read init's copy.
	_, err = store.exec(ctx, consumer.Name, []string{"sh", "-ec", "rm -- \"$1\" /work/input/manifest.txt /work/input/output.txt", "clear-input", "/work/input/" + name}, nil)
	if err != nil {
		return out, err
	}
	const collisionContents = "pre-existing destination contents"
	if fault == "write-refused" {
		_, err = store.exec(ctx, consumer.Name, []string{"sh", "-ec", "mkdir -- \"$1\"; printf '%s' \"$2\" > \"$1/output.txt\"", "collision", "/work/input/" + name, collisionContents}, nil)
		if err != nil {
			return out, err
		}
	}
	transferErr := input.StreamIn(ctx, ".", enc, 0, stream)
	if fault == "write-refused" {
		kept, err := store.exec(ctx, consumer.Name, []string{"cat", "/work/input/" + name + "/output.txt"}, nil)
		if err != nil || kept != collisionContents {
			return out, fmt.Errorf("destination collision not preserved: %q err=%v", kept, err)
		}
		var exit *jetbridge.ExecExitError
		// StreamIn does not request stderr from SPDY. Require the real exit
		// plus exact partial extraction and preserved collision bytes, not
		// a diagnostic manufactured by the old host executor.
		if !errors.As(transferErr, &exit) || exit.ExitCode != 1 {
			return out, fmt.Errorf("expected real BusyBox tar to refuse nonempty destination: %v", transferErr)
		}
		partial, err := input.StreamOut(ctx, ".", compression.NewGzipCompression())
		if err != nil {
			return out, fmt.Errorf("inspect refused write: %w", err)
		}
		written, readErr := filesInGzippedTar(partial)
		closeErr := partial.Close()
		wantPartial := map[string]string{name + "/output.txt": collisionContents, "manifest.txt": "version=1", "output.txt": "hello-from-the-step"}
		if readErr != nil || closeErr != nil || !reflect.DeepEqual(written, wantPartial) {
			return out, fmt.Errorf("tar refusal did not retain exact partial extraction: got=%v read=%v close=%v", written, readErr, closeErr)
		}
		fmt.Printf("real BusyBox tar refused collision with exit %d; existing destination bytes preserved\n", exit.ExitCode)
	}
	if transferErr != nil {
		return out, fmt.Errorf("stream into returned input volume: %w", transferErr)
	}
	result, err = process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		return out, fmt.Errorf("consuming task exit %d: %v; stderr=%s", result.ExitStatus, err, consumerErr.String())
	}
	out.Received, err = filesInTar(bytes.NewReader(consumerOut.Bytes()))
	if err == nil {
		fmt.Printf("real handoff %s completed from deleted producer UID %s to consumer UID %s\n", encoding, producer.UID, consumer.UID)
	}
	return out, err
}

func liveHandoffContainer(ctx context.Context, rec *brine.Recorder, s *liveArtifactStore, w *jetbridge.Worker, handle string, spec runtime.ContainerSpec, path string, command runtime.ProcessSpec, streams runtime.ProcessIO) (runtime.Process, runtime.Volume, *corev1.Pod, error) {
	container, mounts, err := w.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: spec.Type}, spec, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	process, runErr := container.Run(ctx, command, streams)
	created, err := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pod creation: %v; Run=%v", err, runErr)
	}
	TrackDisposer(rec, "the handoff task pod "+created.Name, func() error {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return deleteLiveStoragePod(clean, s, created)
	})
	if runErr != nil {
		return nil, nil, nil, runErr
	}
	pod, err := awaitLiveStoragePod(ctx, s, created)
	if err != nil {
		return nil, nil, nil, err
	}
	if pod.Spec.NodeName != s.anchor.Spec.NodeName {
		return nil, nil, nil, fmt.Errorf("task escaped owned storage node")
	}
	if err := validatePodMounts(pod); err != nil {
		return nil, nil, nil, err
	}
	// Validate every production host path, not just the returned mount.
	for _, v := range pod.Spec.Volumes {
		if v.HostPath != nil && v.HostPath.Path != s.root && !strings.HasPrefix(v.HostPath.Path, s.root+"/") {
			return nil, nil, nil, fmt.Errorf("task host path outside owned storage: %s", v.HostPath.Path)
		}
	}
	var volume runtime.Volume
	for _, mount := range mounts {
		if mount.MountPath == path {
			if volume != nil {
				return nil, nil, nil, fmt.Errorf("duplicate returned mount at %q", path)
			}
			volume = mount.Volume
		}
	}
	if volume == nil {
		return nil, nil, nil, fmt.Errorf("no returned volume at %q", path)
	}
	return process, volume, pod, nil
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
