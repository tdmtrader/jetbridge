package steps

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
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

// The disposable kubelet runtime: one K3s node that a CI task created,
// marked owned by the tier running on it, labelled into the output plane's
// ready cohort, with the Run's real artifact daemon -- which mounts the output
// plane -- bound to every address and certified for the node's, so the node's
// Pods and the ATC reach it there. It never substitutes envtest for a kubelet
// and never runs against a cluster it did not create (hack/test-run-kubelet).
func disposableKubeletRuntime(ctx context.Context, rec *brine.Recorder, res brine.Resources, owner, namespacePrefix string, checks bool) (RunOutputRuntime, jetbridge.PodExecutor, error) {
	var in RunOutputRuntime
	path := os.Getenv("BRINE_KUBELET_CONFIG")
	if path == "" {
		return in, nil, fmt.Errorf("disposable kubelet scenarios require BRINE_KUBELET_CONFIG from the disposable CI cluster")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return in, nil, err
	}
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Scheme != "https" || u.Hostname() != "127.0.0.1" {
		return in, nil, fmt.Errorf("disposable kubelet scenarios accept only the loopback K3s API")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return in, nil, err
	}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, nil, err
	}
	if len(nodes.Items) != 1 || nodes.Items[0].Labels[owner] != "owned-ci" {
		return in, nil, fmt.Errorf("cluster is not marked %s=owned-ci on its single disposable node", owner)
	}
	node := nodes.Items[0].DeepCopy()
	for _, key := range []string{"concourse.dev/artifact-cache", executioncontrol.ReadyLabel, output.ReadyLabel} {
		node.Labels[key] = "ready"
	}
	node, err = client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return in, nil, err
	}
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: namespacePrefix}}, metav1.CreateOptions{})
	if err != nil {
		return in, nil, err
	}
	TrackDisposer(rec, "the disposable kubelet namespace "+ns.Name, func() error {
		return releasedIfGone(client.CoreV1().Namespaces().Delete(context.Background(), ns.Name, metav1.DeleteOptions{}))
	})
	in.Client, in.Node = client, node
	// The ATC reaches the node's artifact daemon at the node's InternalIP and
	// verifies its certificate against that address, so the fixture mints the
	// certificate naming it.
	resolved, err := jetbridge.NewNodeIPResolver(client).Resolve(ctx, node.Name)
	if err != nil {
		return in, nil, err
	}
	nodeIP := net.ParseIP(resolved)
	if nodeIP == nil {
		return in, nil, fmt.Errorf("the disposable kubelet node %s resolves to %q, which is not an IP address", node.Name, resolved)
	}
	in.Start, err = runOutputFixtureConfig(rec, res, string(node.UID), checks, nodeIP)
	if err != nil {
		return in, nil, err
	}
	if in.Start.Err != nil {
		return in, nil, in.Start.Err
	}
	d := in.Start.Daemon.Output
	if address := daemonListenAddress(d.cmd.Args); address != "" {
		return in, nil, fmt.Errorf("the artifact daemon holds only %s; the node's Pods dial it at %s", address, nodeIP)
	}
	in.Config = jetbridge.NewConfig(ns.Name, "")
	in.Config.OutputPlaneEnabled = true
	in.Config.ArtifactHelperImage = "busybox:1.37"
	if err = outputPlaneConfig(&in.Config, d.URL, in.Start.Daemon.CertDir); err != nil {
		return in, nil, err
	}
	executor := jetbridge.NewSPDYExecutor(client, cfg)
	in.OutcomeReader = executor
	return in, executor, nil
}

// daemonListenAddress is the one address an artifact daemon's argv binds it
// to, or "" when it binds every address. The launcher passes --listen-address
// only when a caller named a host; the disposable kubelet's Pods and the ATC
// dial the daemon at the node's address, so it must bind every one.
func daemonListenAddress(args []string) string {
	address := ""
	for i, arg := range args {
		switch {
		case arg == "--listen-address" && i+1 < len(args):
			address = args[i+1]
		case strings.HasPrefix(arg, "--listen-address="):
			address = strings.TrimPrefix(arg, "--listen-address=")
		}
	}
	return address
}

// kubeletWorker is the production worker a Run step uses on the node: the real
// executor, the node's output controls and the Run's execution starter.
func kubeletWorker(in RunOutputRuntime, executor jetbridge.PodExecutor, name string) (*jetbridge.Worker, db.PipelineRunFactory, error) {
	row, err := in.Start.DB.PersistNamedWorker(name)
	if err != nil {
		return nil, nil, err
	}
	factory := db.NewPipelineRunFactory(in.Start.DB.Conn, in.Start.DB.LockFactory)
	return jetbridge.NewWorker(row, in.Client, in.Config, jetbridge.WorkerDeps{
		Executor:          executor,
		OutputControls:    jetbridge.NewOutputControls(in.Config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter),
		ExecutionPreparer: &runs.ExecutionStarter{Conn: in.Start.DB.Conn, Factory: factory, Source: in.source()},
	}), factory, nil
}

func kubeletActiveCancellation(ctx context.Context, in RunOutputRuntime, executor jetbridge.PodExecutor, ignoresTERM bool) error {
	w, factory, err := kubeletWorker(in, executor, "kubelet-active")
	if err != nil {
		return err
	}
	build := in.Start.Creation.EntryBuilds[0]
	metadata := db.ContainerMetadata{BuildID: build.ID(), PipelineID: build.PipelineID(), Type: db.ContainerTypeTask}
	spec := runtime.ContainerSpec{TeamID: build.TeamID(), Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox:1.37"}}
	c, _, err := w.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(build.ID(), "active-step", build.TeamID()), metadata, spec, nil)
	if err != nil {
		return err
	}
	tx, err := in.Start.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	a, found, err := factory.RunExecution(ctx, tx, build.ID(), "active-step")
	db.Rollback(tx)
	if err != nil || !found {
		return fmt.Errorf("missing live execution: %v", err)
	}
	script := `printf x >> /tmp/run-started; sleep 180 & C=$!; printf '%s\n' "$C" > /tmp/run-child; wait "$C"`
	if ignoresTERM {
		script = "trap '' TERM; " + script
	}
	process, err := c.Run(ctx, runtime.ProcessSpec{ID: "active-command", Path: "sh", Args: []string{"-c", script}}, runtime.ProcessIO{})
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
	if err = waitKubeletProbe(ctx, executor, in.Config.Namespace, name, "test -s /tmp/run-child"); err != nil {
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
	return kubeletProbe(ctx, executor, in.Config.Namespace, name, `test "$(cat /tmp/run-started)" = x; P=$(cat /tmp/run-child); test ! -f /proc/$P/stat || awk '$3 != "Z" { exit 1 }' /proc/$P/stat`)
}

func kubeletProbe(ctx context.Context, executor jetbridge.PodExecutor, namespace, name, script string) error {
	return executor.ExecInPod(ctx, namespace, name, "main", []string{"sh", "-ec", script}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "brine-kubelet-probe"})
}

func waitKubeletProbe(ctx context.Context, executor jetbridge.PodExecutor, namespace, name, script string) error {
	for {
		if err := kubeletProbe(ctx, executor, namespace, name, script); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
