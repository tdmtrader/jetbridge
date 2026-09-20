package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
)

type integrationPublication struct{ commit, remote string }

func liveArtifactIntegrationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, StepRan]("a persisted artifact becomes a task input", func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (StepRan, error) {
			return runLiveArtifactIntegration(in, rec, false)
		}),
		brine.DefineMap[LiveTaskPlan, StepRan]("a task's output is published by a Git put", func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (StepRan, error) {
			return runLiveArtifactIntegration(in, rec, true)
		}),
		CheckThat[StepRan]("the resource published the task's commit", func(in StepRan) error {
			if in.Err != nil {
				return in.Err
			}
			if in.publication == nil || in.publication.commit == "" {
				return fmt.Errorf("no actual producer commit")
			}
			var reply struct{ Version struct{ Ref string } }
			if err := json.Unmarshal([]byte(in.Stdout), &reply); err != nil {
				return fmt.Errorf("resource returned invalid JSON: %w", err)
			}
			if reply.Version.Ref != in.publication.commit || in.publication.remote != in.publication.commit+"\nbuilt:source\n" {
				return fmt.Errorf("put did not publish the task's actual commit and bytes: reply=%q commit=%s remote=%q", in.Stdout, in.publication.commit, in.publication.remote)
			}
			return nil
		}),
	}
}

// The first artifact is uploaded through the real daemon, persisted in the DB
// and looked up anew. Task/put input delivery is exclusively production init:
// no StreamIn call can repair a missing task input after pod creation.
func runLiveArtifactIntegration(in LiveTaskPlan, rec *brine.Recorder, publish bool) (StepRan, error) {
	ctx, cancel := context.WithTimeout(execLogger("live-artifact-integration"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	daemon, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		return StepRan{}, err
	}
	s := daemon.store
	dw, err := in.Database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return StepRan{}, err
	}
	team, err := in.Database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return StepRan{}, err
	}
	config := jetbridge.NewConfig(s.cluster.Namespace, "")
	config.ArtifactDaemonHostPath, config.ArtifactDaemonPort = s.root, int(daemon.port)
	config.ArtifactDaemonService = liveArtifactDaemonService
	config.ResourceTypeImages = jetbridge.MergeResourceTypeImages([]string{"git=" + gitResourceImage})
	config.PodStartupTimeout, config.PodSchedulingTimeout = 30*time.Second, 30*time.Second
	w := WorkerReady{DB: in.Database, Namespace: s.cluster.Namespace, Clientset: s.cluster.Clientset, Config: config, DBWorker: dw, TeamID: team.ID(), Ctx: ctx,
		Executor: s.executor, ProducerExecutor: s.executor, VolumeRepo: in.Database.VolumeRepository,
		Locator:      jetbridge.NewArtifactLocator(),
		DaemonClient: jetbridge.NewDaemonClient(lagertest.NewTestLogger("integration-daemon"), s.cluster.Clientset, s.cluster.Namespace, config.ArtifactDaemonService, int(daemon.port), nil)}
	w = w.rebuild()
	cluster := IntegrationCluster{WorkerReady: w, Team: team}
	volume, _, err := w.Worker.CreateVolumeForArtifact(ctx, team.ID())
	if err != nil {
		return StepRan{}, err
	}
	data := "artifact data received\n"
	if publish {
		data = "source\n"
	}
	tar, err := plainTarOfOneFile("payload.txt", data)
	if err != nil {
		return StepRan{}, err
	}
	if err := volume.StreamIn(ctx, ".", nil, 0, bytes.NewReader(tar)); err != nil {
		return StepRan{}, fmt.Errorf("upload persisted input through discovered real daemon: %w", err)
	}
	persisted, found, err := w.Worker.LookupVolume(ctx, volume.Handle())
	if err != nil || !found || persisted == nil {
		return StepRan{}, fmt.Errorf("persisted input lookup: found=%t error=%v", found, err)
	}
	row, ok := persisted.(interface{ DBVolume() db.CreatedVolume })
	if !ok || row.DBVolume() == nil || row.DBVolume().Handle() != volume.Handle() || row.DBVolume().WorkerName() != dw.Name() {
		return StepRan{}, fmt.Errorf("looked-up input lost its persisted worker/volume row")
	}
	stream, err := persisted.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return StepRan{}, err
	}
	files, readErr := filesInGzippedTar(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || len(files) != 1 || files["payload.txt"] != data {
		return StepRan{}, fmt.Errorf("persisted artifact bytes=%v read=%v close=%v", files, readErr, closeErr)
	}
	fmt.Printf("live integration persisted volume %s looked up on worker %s and read exact uploaded bytes through real discovery\n", persisted.Handle(), persisted.Source())

	handle, inputPath := "task-consume-artifact", "/tmp/build/workdir/my-input"
	spec := runtime.ContainerSpec{TeamID: team.ID(), TeamName: team.Name(), Type: db.ContainerTypeTask, Dir: "/tmp/build/workdir", ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37.0"}}
	command := runtime.ProcessSpec{Path: "cat", Args: []string{inputPath + "/payload.txt"}}
	selectedPath := inputPath
	if publish {
		handle, inputPath = "task-build-step", "/tmp/build/workdir/repo"
		spec.ImageSpec = runtime.ImageSpec{ImageURL: gitResourceImage}
		selectedPath = "/tmp/build/workdir/binary"
		spec.Outputs = runtime.OutputPaths{"binary": selectedPath}
		command = runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", "git init -q -b main \"$2\"; git -C \"$2\" config user.name Brine; git -C \"$2\" config user.email brine@example.invalid; printf built: > \"$2/payload.txt\"; cat \"$1/payload.txt\" >> \"$2/payload.txt\"; git -C \"$2\" add payload.txt; git -C \"$2\" -c commit.gpgsign=false commit -qm built; printf 'built\\n'", "build-artifact", inputPath, selectedPath}}
	}
	spec.Inputs = []runtime.Input{{Artifact: persisted, DestinationPath: inputPath}}
	var stdout, stderr bytes.Buffer
	process, output, pod, err := liveHandoffContainer(ctx, rec, s, w.Worker, handle, spec, selectedPath, command, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return StepRan{}, err
	}
	if err := requireLiveIntegrationInput(s, ctx, pod, inputPath+"/payload.txt", data); err != nil {
		return StepRan{}, err
	}
	result, waitErr := process.Wait(ctx)
	out := StepRan{Created: StepCreated{Cluster: cluster, Handle: handle, Metadata: db.ContainerMetadata{Type: spec.Type}, Spec: spec}, Pod: pod,
		Stdout: stdout.String(), ExitStatus: result.ExitStatus, Err: waitErr, Message: errorMessage(waitErr)}
	if !publish {
		return out, nil
	}
	if waitErr != nil || result.ExitStatus != 0 || stdout.String() != "built\n" {
		return StepRan{}, fmt.Errorf("producing task exit=%d error=%v stdout=%q stderr=%q", result.ExitStatus, waitErr, stdout.String(), stderr.String())
	}
	if err := checkContainerRow(cluster, handle, "task", dw.Name()); err != nil {
		return StepRan{}, err
	}
	commit, err := s.exec(ctx, pod.Name, []string{"git", "-C", selectedPath, "rev-parse", "HEAD"}, nil)
	if err != nil {
		return StepRan{}, err
	}
	commit = strings.TrimSpace(commit)
	if len(commit) != 40 {
		return StepRan{}, fmt.Errorf("producer has no actual Git commit: %q", commit)
	}
	produced := w.Worker.ArtifactFromVolume(output)
	if err := deleteLiveStoragePod(ctx, s, pod); err != nil {
		return StepRan{}, err
	}
	// The producer is absent. Only the daemon can deliver its artifact to put.
	putPath := "/tmp/build/put/binary"
	putSpec := runtime.ContainerSpec{TeamID: team.ID(), TeamName: team.Name(), Type: db.ContainerTypePut, Dir: "/tmp/build/put", CertsBindMount: true,
		ImageSpec: runtime.ImageSpec{ResourceType: "git"}, Inputs: []runtime.Input{{Artifact: produced, DestinationPath: putPath}}}
	var request, reply, putErr bytes.Buffer
	process, _, putPod, err := liveHandoffContainer(ctx, rec, s, w.Worker, "put-upload-step", putSpec, putPath,
		runtime.ProcessSpec{ID: "resource", Path: "/opt/resource/out", Args: []string{putSpec.Dir}}, runtime.ProcessIO{Stdin: &request, Stdout: &reply, Stderr: &putErr})
	if err != nil {
		return StepRan{}, err
	}
	if err := requireLiveIntegrationInput(s, ctx, putPod, putPath+"/payload.txt", "built:source\n"); err != nil {
		return StepRan{}, err
	}
	actual, err := s.exec(ctx, putPod.Name, []string{"git", "-C", putPath, "rev-parse", "HEAD"}, nil)
	if err != nil || strings.TrimSpace(actual) != commit {
		return StepRan{}, fmt.Errorf("put input lost producer commit: %q %v", actual, err)
	}
	remote := "/tmp/brine-integration-remote.git"
	if _, err := s.exec(ctx, putPod.Name, []string{"git", "init", "-q", "--bare", "-b", "main", remote}, nil); err != nil {
		return StepRan{}, err
	}
	payload, err := json.Marshal(map[string]any{"source": map[string]string{"uri": "file://" + remote, "branch": "main"}, "params": map[string]string{"repository": "binary"}})
	if err != nil {
		return StepRan{}, err
	}
	request.Write(payload)
	result, waitErr = process.Wait(ctx)
	out = StepRan{Created: StepCreated{Cluster: cluster, Handle: "put-upload-step", Metadata: db.ContainerMetadata{Type: db.ContainerTypePut}, Spec: putSpec}, Pod: putPod,
		Stdout: reply.String(), ExitStatus: result.ExitStatus, Err: waitErr, Message: errorMessage(waitErr), publication: &integrationPublication{commit: commit}}
	if waitErr == nil && result.ExitStatus == 0 {
		out.publication.remote, err = s.exec(ctx, putPod.Name, []string{"sh", "-ec", "git --git-dir=\"$1\" rev-parse refs/heads/main; git --git-dir=\"$1\" show refs/heads/main:payload.txt", "observe-published", remote}, nil)
		if err != nil {
			return StepRan{}, fmt.Errorf("observe real Git publication: %w", err)
		}
	}
	fmt.Printf("live integration producer UID %s deleted before put UID %s fetched commit %s; actual Git out exit=%d error=%v stderr=%q\n", pod.UID, putPod.UID, commit, result.ExitStatus, waitErr, putErr.String())
	return out, nil
}

func requireLiveIntegrationInput(s *liveArtifactStore, ctx context.Context, pod *corev1.Pod, path, want string) error {
	fetched := false
	for _, c := range pod.Status.InitContainerStatuses {
		if c.Name == "fetch-inputs" {
			fetched = c.ContainerID != "" && c.State.Terminated != nil && c.State.Terminated.ExitCode == 0
		}
	}
	if !fetched {
		return fmt.Errorf("pod %s has no successful real fetch-inputs", pod.Name)
	}
	actual, err := s.exec(ctx, pod.Name, []string{"cat", path}, nil)
	if err != nil || actual != want {
		return fmt.Errorf("production input delivery at %s: got=%q want=%q error=%v", path, actual, want, err)
	}
	fmt.Printf("live integration pod UID %s fetched exact input bytes at %s before command execution\n", pod.UID, path)
	return nil
}
