package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			err = kubeletActiveCancellation(ctx, in, executor, mode == "TERM-resistant cancellation")
		} else {
			err = liveReviewCredentialLoss(ctx, in, executor, mode, res)
		}
		return brine.Empty{}, err
	})}
}

// liveReviewRuntime is the disposable kubelet runtime under the review
// tier's own node marker.
func liveReviewRuntime(ctx context.Context, rec *brine.Recorder, res brine.Resources) (RunOutputRuntime, jetbridge.PodExecutor, error) {
	return disposableKubeletRuntime(ctx, rec, res, "brine.dev/review-kubelet", "brine-review-", false)
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
	if err = waitKubeletProbe(ctx, executor, pod.Namespace, pod.Name, "mkdir -p /tmp/review/input /dev/shm/jb-review; chmod 700 /dev/shm/jb-review"); err != nil {
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
	if err = kubeletProbe(ctx, executor, pod.Namespace, pod.Name, `/tmp/review/jb-review-worker --input /tmp/review/input --output /tmp/review/report --runtime-dir /dev/shm/jb-review --codex /tmp/review/provider --model handoff-wait --timeout 3m --auth-socket /dev/shm/jb-review/auth.sock --handoff-timeout 1m --run-id 417 </dev/null >/tmp/review/stdout 2>/tmp/review/stderr & echo $! > /tmp/review/worker.pid`); err != nil {
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
		if err = kubeletProbe(ctx, executor, pod.Namespace, pod.Name, `kill -KILL "$(cat /tmp/review/worker.pid)"`); err != nil {
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
