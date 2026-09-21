package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// These scenarios are explicitly selected in Linux CI. They never substitute
// envtest for kubelet or silently skip when the disposable cluster is absent.
func ReviewKubeletDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineMapUsing[brine.Empty, brine.Empty]("a real review kubelet handles {string}", []string{"jetbridge-db", "review-binaries", "review-workspace"}, func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (brine.Empty, error) {
		mode, _ := p.GetString(0)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		in, executor, err := liveReviewRuntime(ctx, rec, res)
		if err != nil {
			return brine.Empty{}, err
		}
		if mode == "read-only named input" {
			err = liveReviewReadOnlyInput(ctx, in, executor, rec)
		} else if strings.Contains(mode, "cancellation") {
			err = liveReviewCancellation(ctx, in, executor, mode)
		} else {
			err = liveReviewCredentialLoss(ctx, in, executor, mode, res)
		}
		return brine.Empty{}, err
	})}
}

func liveReviewRuntime(ctx context.Context, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, jetbridge.PodExecutor, error) {
	var in RunOutputRuntime
	path := os.Getenv("BRINE_KUBELET_CONFIG")
	if path == "" {
		return in, nil, fmt.Errorf("live review scenarios require BRINE_KUBELET_CONFIG from the disposable CI cluster")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return in, nil, err
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Scheme != "https" || u.Hostname() != "127.0.0.1" {
		return in, nil, fmt.Errorf("live review accepts only the disposable loopback K3s API")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return in, nil, err
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, nil, err
	}
	if len(nodes.Items) != 1 || nodes.Items[0].Labels["brine.dev/review-kubelet"] != "owned-ci" {
		return in, nil, fmt.Errorf("cluster is not marked as the single disposable review CI node")
	}
	node := nodes.Items[0].DeepCopy()
	for _, key := range []string{"concourse.dev/artifact-cache", "concourse.dev/hangar-v1", executioncontrol.ReadyLabel, output.ReadyLabel} {
		node.Labels[key] = "ready"
	}
	node, err = client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return in, nil, err
	}
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "brine-review-"}}, metav1.CreateOptions{})
	if err != nil {
		return in, nil, err
	}
	TrackDisposer(rec, "the review namespace "+ns.Name, func() error {
		return releasedIfGone(client.CoreV1().Namespaces().Delete(context.Background(), ns.Name, metav1.DeleteOptions{}))
	})
	in.Client, in.Node = client, node
	in.Start, err = runOutputFixtureConfig(rec, res, "current", string(node.UID), false, node.Name)
	if err != nil {
		return in, nil, err
	}
	if in.Start.Err != nil {
		return in, nil, in.Start.Err
	}
	d := in.Start.Daemon.Output
	if err = d.crash(); err != nil {
		return in, nil, err
	}
	listenArgs := 0
	for i, arg := range d.cmd.Args {
		if arg == "--listen" && i+1 < len(d.cmd.Args) {
			listenArgs++
			d.cmd.Args[i+1] = strings.Replace(d.cmd.Args[i+1], "127.0.0.1:", "0.0.0.0:", 1)
		}
	}
	if listenArgs != 1 {
		return in, nil, fmt.Errorf("live daemon has no single listen address")
	}
	if err = d.restart(ctx, in.Start.Daemon.HTTP); err != nil {
		return in, nil, err
	}
	in.Config = jetbridge.NewConfig(ns.Name, "")
	in.Config.OutputPlaneEnabled = true
	in.Config.ArtifactHelperImage = "busybox:1.37"
	in.Config.OutputActivationEpoch = int64(hangarEpoch)
	in.Config.OutputDaemonPort, err = hangarDaemonPort(d.URL)
	if err != nil {
		return in, nil, err
	}
	in.Config.OutputDaemonTLSCert = filepath.Join(in.Start.Daemon.CertDir, "client.crt")
	in.Config.OutputDaemonTLSKey = filepath.Join(in.Start.Daemon.CertDir, "client.key")
	in.Config.OutputDaemonTLSCACert = filepath.Join(in.Start.Daemon.CertDir, "ca.crt")
	in.Config.OutputDaemonTLSServerName = "artifact-daemon"
	executor := jetbridge.NewSPDYExecutor(client, cfg)
	in.OutcomeReader = executor
	return in, executor, nil
}

func liveReviewCancellation(ctx context.Context, in RunOutputRuntime, executor jetbridge.PodExecutor, mode string) error {
	row, err := in.Start.DB.PersistNamedWorker("review-kubelet")
	if err != nil {
		return err
	}
	w := jetbridge.NewWorker(row, in.Client, in.Config)
	w.SetExecutor(executor)
	w.SetOutputControls(jetbridge.NewOutputControls(in.Config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)))
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(in.Start.Daemon.ControlPublic)}}}
	w.SetExecutionPreparer(&runs.ExecutionStarter{Conn: in.Start.DB.Conn, Factory: factory, Source: in.source(), Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys})
	build := in.Start.Creation.EntryBuilds[0]
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}
	spec := runtime.ContainerSpec{TeamID: build.TeamID(), Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37"}}
	c, _, err := w.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(build.ID(), "live-review", build.TeamID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	a, found, err := factory.RunExecution(ctx, tx, build.ID(), "live-review")
	db.Rollback(tx)
	if err != nil || !found {
		return fmt.Errorf("missing live execution: %v", err)
	}
	script := `printf x >> /tmp/review-started; sleep 180 & C=$!; printf '%s\n' "$C" > /tmp/review-child; wait "$C"`
	if mode == "TERM-resistant cancellation" {
		script = "trap '' TERM; " + script
	}
	process, err := c.Run(ctx, runtime.ProcessSpec{ID: "review-live-command", Path: "sh", Args: []string{"-c", script}}, runtime.ProcessIO{})
	if err != nil {
		return err
	}
	waitCtx, stopWait := context.WithCancel(ctx)
	done := make(chan error, 1)
	joined := false
	go func() { _, err := process.Wait(waitCtx); done <- err }()
	defer func() {
		stopWait()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	}()
	name := jetbridge.GeneratePodName(metadata, c.DBContainer().Handle())
	if err = waitReviewProbe(ctx, executor, in.Config.Namespace, name, "test -s /tmp/review-child"); err != nil {
		return err
	}
	pod, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = exerciseBaseExecutionCancellation(in, a, executioncontrol.ClassificationAuthoritativeFinish); err != nil {
		return err
	}
	select {
	case err = <-done:
		joined = true
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	current, err := in.Client.CoreV1().Pods(in.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || current.UID != pod.UID || current.DeletionTimestamp != nil {
		return fmt.Errorf("cancellation destroyed the original Pod/source: %v", err)
	}
	return reviewProbe(ctx, executor, in.Config.Namespace, name, `test "$(cat /tmp/review-started)" = x; P=$(cat /tmp/review-child); test ! -f /proc/$P/stat || awk '$3 != "Z" { exit 1 }' /proc/$P/stat`)
}

func reviewProbe(ctx context.Context, executor jetbridge.PodExecutor, namespace, name, script string) error {
	return executor.ExecInPod(ctx, namespace, name, "main", []string{"sh", "-ec", script}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "brine-live-review-probe"})
}

func waitReviewProbe(ctx context.Context, executor jetbridge.PodExecutor, namespace, name, script string) error {
	for {
		if err := reviewProbe(ctx, executor, namespace, name, script); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func liveReviewCredentialLoss(ctx context.Context, in RunOutputRuntime, executor jetbridge.PodExecutor, mode string, res brine.Resources) error {
	if mode != "a session deadline" && mode != "a killed worker" && mode != "a lost node runtime" {
		return fmt.Errorf("unknown kubelet case %q", mode)
	}
	cluster := os.Getenv("BRINE_K3S_CONTAINER")
	if cluster != "brine-review-k3s" {
		return fmt.Errorf("credential destruction proof requires the owned K3s container")
	}
	pod, err := in.Client.CoreV1().Pods(in.Config.Namespace).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "session-"}, Spec: corev1.PodSpec{NodeName: in.Node.Name, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37", Command: []string{"sh", "-ec", "sleep 300"}}}}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	if err = waitReviewProbe(ctx, executor, pod.Namespace, pod.Name, "mkdir -p /tmp/review/input /dev/shm/jb-review; chmod 700 /dev/shm/jb-review"); err != nil {
		return err
	}
	// Exec can work before kubelet has published Running/ContainerID. The
	// production task path waits for that status before recording its start;
	// this manually launched worker must wait at the same boundary.
	for {
		current, err := in.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID || current.DeletionTimestamp != nil || current.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("original session Pod became unavailable before handoff")
		}
		if current.Status.Phase == corev1.PodRunning && current.Status.StartTime != nil && len(current.Status.ContainerStatuses) == 1 {
			status := current.Status.ContainerStatuses[0]
			if status.State.Running != nil && status.ContainerID != "" && status.RestartCount == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	change, err := newReviewChange(res)
	if err != nil {
		return err
	}
	change.Stdout, change.Stderr, change.CommandErr = reviewCommand(change.Binaries.CLI, change.captureArgs(change.Input), "")
	if change.CommandErr != nil {
		return fmt.Errorf("capture live input: %w: %s", change.CommandErr, change.Stderr)
	}
	var captured struct {
		Digest string `json:"input_digest"`
	}
	if err = json.Unmarshal(change.Stdout, &captured); err != nil {
		return err
	}
	change.Digest = captured.Digest
	if err = copyReviewPodFiles(ctx, executor, pod, change); err != nil {
		return err
	}
	if err = reviewProbe(ctx, executor, pod.Namespace, pod.Name, `/tmp/review/jb-review-worker --input /tmp/review/input --output /tmp/review/report --runtime-dir /dev/shm/jb-review --codex /tmp/review/provider --model handoff-wait --timeout 3m --auth-socket /dev/shm/jb-review/auth.sock --handoff-timeout 1m --run-id 417 </dev/null >/tmp/review/stdout 2>/tmp/review/stderr & echo $! > /tmp/review/worker.pid`); err != nil {
		return err
	}
	source := in.source()
	id := executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(freshUUID()), Fence: 1}
	if _, err = source.BaseRuntimeControl(ctx, in.Node.Name, string(in.Node.UID), executioncontrol.ActivationEpoch(hangarEpoch), id); err != nil {
		return err
	}
	control := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	start, err := control.RecordStart(ctx, id, executioncontrol.PodUID(pod.UID), "live-review-credential-session")
	if err != nil {
		return err
	}
	var ack bytes.Buffer
	if err = source.ExecBoundSession(ctx, in.Node.Name, start, time.Minute, []string{"/tmp/review/jb-review-worker", "auth-handoff", "--socket", "/dev/shm/jb-review/auth.sock", "--run-id", "417", "--timeout", "30s"}, strings.NewReader(reviewSyntheticAuth), &ack); err != nil {
		return err
	}
	if !bytes.Contains(ack.Bytes(), []byte(`"status":"ready"`)) {
		return fmt.Errorf("live handoff did not acknowledge readiness")
	}
	var path bytes.Buffer
	if err = executor.ExecInPod(ctx, pod.Namespace, pod.Name, "main", []string{"find", "/dev/shm/jb-review", "-name", "auth.json"}, nil, &path, nil, false, jetbridge.ExecAttrs{Purpose: "brine-live-review-path"}); err != nil {
		return err
	}
	authPath := strings.TrimSpace(path.String())
	if !strings.HasPrefix(authPath, "/dev/shm/jb-review/") || strings.Contains(authPath, "\n") {
		return fmt.Errorf("live session has no single private auth file")
	}
	pod, err = in.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if len(pod.Status.ContainerStatuses) != 1 {
		return fmt.Errorf("unexpected live session containers")
	}
	containerID := strings.TrimPrefix(pod.Status.ContainerStatuses[0].ContainerID, "containerd://")
	data, err := exec.CommandContext(ctx, "docker", "exec", cluster, "crictl", "inspect", containerID).Output()
	if err != nil {
		return fmt.Errorf("inspect live memory mount: %w", err)
	}
	var inspect struct {
		Info struct {
			RuntimeSpec struct {
				Mounts []struct{ Destination, Source string }
			}
		}
	}
	if err = json.Unmarshal(data, &inspect); err != nil {
		return err
	}
	shm := ""
	for _, mount := range inspect.Info.RuntimeSpec.Mounts {
		if mount.Destination == "/dev/shm" {
			shm = mount.Source
		}
	}
	if !strings.HasPrefix(shm, "/run/k3s/containerd/") || !strings.HasSuffix(shm, "/shm") {
		return fmt.Errorf("cannot identify the original container's private memory mount")
	}
	nodeAuth := filepath.Join(shm, strings.TrimPrefix(authPath, "/dev/shm/"))
	if err = exec.CommandContext(ctx, "docker", "exec", cluster, "test", "-f", nodeAuth).Run(); err != nil {
		return fmt.Errorf("node inspection did not observe staged session credentials")
	}
	if mode == "a lost node runtime" {
		return loseReviewNodeRuntime(ctx, cluster, shm, nodeAuth)
	}
	if mode == "a killed worker" {
		if err = reviewProbe(ctx, executor, pod.Namespace, pod.Name, `kill -KILL "$(cat /tmp/review/worker.pid)"`); err != nil {
			return err
		}
		if err = exec.CommandContext(ctx, "docker", "exec", cluster, "test", "-f", nodeAuth).Run(); err != nil {
			return fmt.Errorf("killed-worker case did not leave credentials for independent cleanup")
		}
	}
	// No client, worker or web cleanup participates. The deadline installed
	// before stdin must terminate the container and destroy its original tmpfs.
	for {
		current, err := in.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return fmt.Errorf("deadline replaced the session Pod")
		}
		if current.Status.Phase == corev1.PodFailed {
			if current.Status.Reason != "DeadlineExceeded" {
				return fmt.Errorf("session failed for %s, not its deadline", current.Status.Reason)
			}
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kubelet did not enforce the session deadline: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	for {
		if err = exec.CommandContext(ctx, "docker", "exec", cluster, "test", "!", "-e", nodeAuth).Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("original session credentials survived container deadline: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// A separate final CI invocation owns this destructive scenario. Kill the
// disposable node's process namespace, then restart the same container with
// its disk intact. The original session's memory must not survive that loss.
func loseReviewNodeRuntime(ctx context.Context, cluster, shm, authPath string) error {
	kind, err := exec.CommandContext(ctx, "docker", "exec", cluster, "stat", "-f", "-c", "%T", shm).Output()
	if err != nil || strings.TrimSpace(string(kind)) != "tmpfs" {
		return fmt.Errorf("original credential mount is not verified tmpfs: %v", err)
	}
	if err = exec.CommandContext(ctx, "docker", "kill", "--signal=KILL", cluster).Run(); err != nil {
		return fmt.Errorf("kill owned node runtime: %w", err)
	}
	state, err := exec.CommandContext(ctx, "docker", "inspect", "--format={{.State.Running}}", cluster).Output()
	if err != nil || strings.TrimSpace(string(state)) != "false" {
		return fmt.Errorf("owned node runtime did not stop: %v", err)
	}
	if err = exec.CommandContext(ctx, "docker", "start", cluster).Run(); err != nil {
		return fmt.Errorf("restart owned node runtime: %w", err)
	}
	if err = exec.CommandContext(ctx, "docker", "exec", cluster, "test", "!", "-e", authPath).Run(); err != nil {
		return fmt.Errorf("original credentials survived node runtime loss: %w", err)
	}
	// Return only once the same node's API is usable for fixture cleanup.
	for {
		if err = exec.CommandContext(ctx, "docker", "exec", cluster, "kubectl", "get", "--raw=/readyz").Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned node API did not recover: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func copyReviewPodFiles(ctx context.Context, executor jetbridge.PodExecutor, pod *corev1.Pod, change ReviewChange) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		archive := tar.NewWriter(writer)
		var err error
		for name, path := range map[string]string{"jb-review-worker": change.Binaries.Worker, "provider": change.Binaries.Provider} {
			if err != nil {
				break
			}
			var file *os.File
			file, err = os.Open(path)
			if err != nil {
				break
			}
			var info os.FileInfo
			info, err = file.Stat()
			if err == nil {
				err = archive.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: info.Size()})
			}
			if err == nil {
				_, err = io.Copy(archive, file)
			}
			file.Close()
		}
		if closeErr := archive.Close(); err == nil {
			err = closeErr
		}
		writer.CloseWithError(err)
		done <- err
	}()
	err := executor.ExecInPod(ctx, pod.Namespace, pod.Name, "main", []string{"tar", "-x", "-C", "/tmp/review"}, reader, nil, nil, false, jetbridge.ExecAttrs{Purpose: "brine-live-review-binaries"})
	reader.CloseWithError(err)
	if sendErr := <-done; err == nil {
		err = sendErr
	}
	if err != nil {
		return err
	}
	bundle, err := change.bundle()
	if err != nil {
		return err
	}
	reader, writer = io.Pipe()
	go func() { err := bundle.WriteRunInputArchive(ctx, writer); writer.CloseWithError(err); done <- err }()
	err = executor.ExecInPod(ctx, pod.Namespace, pod.Name, "main", []string{"tar", "-x", "--strip-components=1", "-C", "/tmp/review/input"}, reader, nil, nil, false, jetbridge.ExecAttrs{Purpose: "brine-live-review-input"})
	reader.CloseWithError(err)
	if sendErr := <-done; err == nil {
		err = sendErr
	}
	return err
}
