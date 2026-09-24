package steps

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// A transparent TLS route is closed only after real task stdout arrives.
// Independent exec and the storage observer prove writes continue after Wait.
// Neither the exec error nor pod status is supplied by the fixture.
func severLiveArtifact(in LiveTaskPlan, rec *brine.Recorder, output string) (SeveredExecOutcome, error) {
	if !filepath.IsLocal(output) || filepath.Base(output) != output || output == "." {
		return SeveredExecOutcome{}, fmt.Errorf("unsafe output name %q", output)
	}
	ctx, cancel := context.WithTimeout(execLogger("live-severed-artifact"), 3*time.Minute)
	rec.RegisterDisposer(cancel)
	daemon, err := newLiveArtifactDaemon(ctx, rec)
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	s := daemon.store
	dw, err := in.Database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	team, err := in.Database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	config := jetbridge.NewConfig(s.cluster.Namespace, "")
	config.ArtifactDaemonService = liveArtifactDaemonService
	config.ArtifactDaemonHostPath, config.ArtifactDaemonPort = s.root, int(daemon.port)
	config.PodStartupTimeout, config.PodSchedulingTimeout = 30*time.Second, 30*time.Second
	locator := jetbridge.NewArtifactLocator()
	deps := jetbridge.WorkerDeps{
		ArtifactLocator: locator,
		VolumeRepo:      in.Database.VolumeRepository,
		Executor:        s.executor,
		DaemonClient:    jetbridge.NewDaemonClient(lagertest.NewTestLogger("severed-artifact-daemon"), s.cluster.Clientset, s.cluster.Namespace, config.ArtifactDaemonService, int(daemon.port), nil),
	}
	worker := jetbridge.NewWorker(dw, s.cluster.Clientset, config, deps)
	path := "/tmp/build/workdir/" + output
	spec := runtime.ContainerSpec{TeamID: team.ID(), TeamName: team.Name(), Type: db.ContainerTypeTask,
		Dir: "/tmp/build/workdir", ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37.0"}, Outputs: runtime.OutputPaths{output: path}}

	// Establish a working publication path before testing its absence.
	process, volume, positive, err := liveHandoffContainer(ctx, rec, s, worker, "completed-artifact", spec, path,
		runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", "printf complete > \"$1/complete.txt\"", "complete", path}}, runtime.ProcessIO{})
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 {
		return SeveredExecOutcome{}, fmt.Errorf("publication prerequisite exit=%d: %v", result.ExitStatus, err)
	}
	if _, found := locator.Locate(jetbridge.ArtifactKey(volume.Handle())); !found {
		return SeveredExecOutcome{}, fmt.Errorf("successful task did not publish its actual output handle")
	}
	artifact := worker.ArtifactFromVolume(volume)
	if err := deleteLiveStoragePod(ctx, s, positive); err != nil {
		return SeveredExecOutcome{}, err
	}
	stream, err := artifact.StreamOut(ctx, ".", compression.NewGzipCompression())
	if err != nil {
		return SeveredExecOutcome{}, fmt.Errorf("completed artifact cannot be read after pod deletion: %w", err)
	}
	files, readErr := filesInGzippedTar(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || len(files) != 1 || files["complete.txt"] != "complete" {
		return SeveredExecOutcome{}, fmt.Errorf("completed artifact files=%v read=%v close=%v", files, readErr, closeErr)
	}
	fmt.Printf("F23 prerequisite: completed task UID %s deleted; actual published handle %s delivered complete bytes through node daemon\n", positive.UID, volume.Handle())

	executor, route, err := liveExecutionRoute(ctx, rec, s.cluster.Config)
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	// The same worker wiring on the severable route: same locator, daemon
	// client and volumes, so only the transport differs.
	deps.Executor = executor
	worker = jetbridge.NewWorker(dw, s.cluster.Clientset, config, deps)
	out := &liveOutputBarrier{marker: "writing-" + s.cluster.Marker, started: make(chan struct{}), release: make(chan struct{}), ctx: ctx}
	var once sync.Once
	release := func() { once.Do(func() { close(out.release) }) }
	stderr := new(liveLogBuffer)
	command := "echo $$ > /tmp/brine-f23.pid; printf 'started\\n' > \"$1/progress.txt\"; printf '%s\\n' \"$2\"; while [ ! -f /tmp/brine-f23-stop ]; do printf 'writing\\n' >> \"$1/progress.txt\"; sleep 0.1; done"
	process, volume, pod, err := liveHandoffContainer(ctx, rec, s, worker, "severed-handle", spec, path,
		runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", command, "write-artifact", path, out.marker}}, runtime.ProcessIO{Stdout: out, Stderr: stderr})
	if err != nil {
		return SeveredExecOutcome{}, err
	}
	key := volume.Handle()
	if key == "" {
		return SeveredExecOutcome{}, fmt.Errorf("torn task has no output volume handle")
	}
	done := make(chan error, 1)
	go func() { _, err := process.Wait(ctx); done <- err }()
	joined := false
	defer func() {
		release()
		if !joined {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("F23 Wait did not stop during cleanup")
			}
		}
	}()
	select {
	case <-out.started:
	case err := <-done:
		joined = true
		return SeveredExecOutcome{}, fmt.Errorf("task finished before real write checkpoint: %v; stderr=%s", err, stderr.String())
	case <-ctx.Done():
		return SeveredExecOutcome{}, ctx.Err()
	}
	if err := route.Close(); err != nil {
		return SeveredExecOutcome{}, err
	}
	// Observe through a different pod, not the cut exec connection or a cached buffer.
	progress := func() (int, error) {
		text, err := s.exec(ctx, s.observer.Name, []string{"sh", "-ec", "wc -l < \"$1\"", "read-progress", "/store/steps/severed-handle/" + output + "/progress.txt"}, nil)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(text))
	}
	initial, err := progress()
	if err != nil || initial < 1 {
		return SeveredExecOutcome{}, fmt.Errorf("no actual partial artifact: lines=%d error=%v", initial, err)
	}
	release()
	var waitErr error
	select {
	case waitErr = <-done:
		joined = true
	case <-ctx.Done():
		return SeveredExecOutcome{}, fmt.Errorf("runtime did not finish after real socket cut: %w", ctx.Err())
	}
	// Both growth observations occur after Wait returns: writes that merely
	// happened during error handling cannot satisfy this premise.
	before, err := progress()
	if err != nil || before < initial {
		return SeveredExecOutcome{}, fmt.Errorf("partial artifact lost bytes after Wait: initial=%d current=%d error=%v", initial, before, err)
	}
	for {
		after, err := progress()
		if err != nil {
			return SeveredExecOutcome{}, err
		}
		if after > before {
			latest, err := s.cluster.Clientset.CoreV1().Pods(s.cluster.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return SeveredExecOutcome{}, err
			}
			running := false
			for _, c := range latest.Status.ContainerStatuses {
				running = running || c.Name == "main" && c.ContainerID != "" && c.State.Running != nil
			}
			if latest.UID != pod.UID || latest.Status.Phase != corev1.PodRunning || !running {
				return SeveredExecOutcome{}, fmt.Errorf("writer no longer occupies the original running pod")
			}
			alive, err := s.exec(ctx, pod.Name, []string{"sh", "-ec", "kill -0 \"$(cat /tmp/brine-f23.pid)\"; printf alive"}, nil)
			if err != nil || alive != "alive" {
				return SeveredExecOutcome{}, fmt.Errorf("writer not alive after failed Wait: %q %v", alive, err)
			}
			fmt.Printf("F23 real disconnect: pod %s/%s UID %s node %s, output handle %s grew from %d to %d lines after Wait returned %v; child alive, main Running\n", s.cluster.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, key, before, after, waitErr)
			break
		}
		select {
		case <-ctx.Done():
			return SeveredExecOutcome{}, fmt.Errorf("writer stopped after socket cut: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Stop further writes only after proving the mid-write premise. The failed
	// runtime has already returned; this does not publish or change its outcome.
	if _, err := s.exec(ctx, pod.Name, []string{"touch", "/tmp/brine-f23-stop"}, nil); err != nil {
		return SeveredExecOutcome{}, err
	}
	return SeveredExecOutcome{Err: waitErr, Message: errorMessage(waitErr), Locator: locator, OutputKey: key,
		artifact: worker.ArtifactFromVolume(volume), ctx: ctx, daemon: daemon}, nil
}

func requireUnpublishedLiveArtifact(in SeveredExecOutcome) error {
	if in.artifact == nil || in.ctx == nil || in.daemon == nil {
		return fmt.Errorf("missing real downstream artifact")
	}
	// The daemon is reachable, so a refusal cannot pass because setup is offline.
	var health bytes.Buffer
	if err := in.daemon.store.executor.ExecInPod(in.ctx, in.daemon.store.cluster.Namespace, in.daemon.store.observer.Name, "main",
		[]string{"wget", "-T", "5", "-qO", "-", in.daemon.url() + "/healthz"}, nil, &health, nil, false, jetbridge.ExecAttrs{}); err != nil {
		return fmt.Errorf("artifact daemon unavailable: %w", err)
	}
	stream, err := in.artifact.StreamOut(in.ctx, ".", compression.NewGzipCompression())
	if stream != nil {
		stream.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "artifact not found") {
		return fmt.Errorf("downstream must refuse the unregistered partial artifact, got %v", err)
	}
	return nil
}
