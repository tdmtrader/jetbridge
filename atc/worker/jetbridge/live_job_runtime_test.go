//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The job step runtime against the deployed cluster: a step is a suspended
// Job whose pod runs the command from its own spec under the launcher
// copied from the deployed jetbridge image, and nothing is exec'd.

// jobRuntimeWorker is a live worker on the job step runtime. The launcher
// image is the deployed web's own; the envelope URL is this process's, when
// one is served.
func jobRuntimeWorker(t *testing.T, envelopes *jetbridge.StepEnvelopes, envelopeURL string) (*jetbridge.Worker, kubernetes.Interface, *jetbridge.Config) {
	t.Helper()
	clientset, cfg := kubeClient(t)
	worker, _, _, _ := newLiveWorker(t, nil, nil, func(cfg *jetbridge.Config, deps *jetbridge.WorkerDeps) {
		cfg.StepRuntime = jetbridge.StepRuntimeJob
		cfg.LauncherImage = deployed.webContainer.Image
		cfg.StepEnvelopeURL = envelopeURL
		deps.StepEnvelopes = envelopes
	})
	cfg.StepRuntime = jetbridge.StepRuntimeJob
	cfg.LauncherImage = deployed.webContainer.Image
	cfg.StepEnvelopeURL = envelopeURL
	return worker, clientset, cfg
}

func cleanupStepJob(t *testing.T, clientset kubernetes.Interface, namespace, name string) {
	t.Helper()
	propagation := metav1.DeletePropagationForeground
	_ = clientset.BatchV1().Jobs(namespace).Delete(context.Background(), name, metav1.DeleteOptions{PropagationPolicy: &propagation})
}

func TestLiveJobRuntimeTask(t *testing.T) {
	handle := "live-job-task-" + time.Now().Format("150405")
	worker, clientset, cfg := jobRuntimeWorker(t, nil, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "job-task"},
		runtime.ContainerSpec{TeamID: 1, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	jobName := jetbridge.GeneratePodName(db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "job-task"}, handle)
	t.Cleanup(func() { cleanupStepJob(t, clientset, cfg.Namespace, jobName) })

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", "echo hello-from-the-launcher; echo to-stderr >&2; exit 7"},
	}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitStatus != 7 {
		t.Errorf("exit = %d, want 7 from the main container's terminated status", result.ExitStatus)
	}
	if !strings.Contains(stdout.String(), "hello-from-the-launcher") || !strings.Contains(stdout.String(), "to-stderr") {
		t.Errorf("stdout = %q: the pod's log did not reach the step", stdout.String())
	}

	job, err := clientset.BatchV1().Jobs(cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step's Job %q is not there: %v", jobName, err)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("the Job may retry")
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		t.Error("the Job is still suspended")
	}
	pods, err := clientset.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + jobName})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("the Job owns %d pods (%v), want 1", len(pods.Items), err)
	}
	pod := pods.Items[0]
	if pod.Labels["concourse.ci/runtime"] != jetbridge.StepRuntimeJob {
		t.Errorf("pod labels %v lack the job runtime label", pod.Labels)
	}
	if pod.Annotations["concourse.ci/exit-status"] != "7" {
		t.Errorf("pod annotations %v lack the exit status", pod.Annotations)
	}
	// The phase may still read Pending or Running an instant after main
	// terminated; the exit is main's terminated status, not the phase.
	if exit, done := mainExitStatus(pod); !done || exit != 7 {
		t.Errorf("main container terminated status = %d (terminated %v), want exit 7", exit, done)
	}
	main := pod.Spec.Containers[0]
	if len(main.Command) != 1 || main.Command[0] != "/jb/jb-launcher" {
		t.Errorf("main command = %v", main.Command)
	}
	if pod.Spec.InitContainers[0].Image != deployed.webContainer.Image {
		t.Errorf("launcher init image = %q, want the deployed web's %q", pod.Spec.InitContainers[0].Image, deployed.webContainer.Image)
	}
}

// TestLiveJobRuntimeResourceCommand runs a check whose request reaches the
// pod as a step envelope served by this process, and whose answer comes
// back through the artifact daemon.
func TestLiveJobRuntimeResourceCommand(t *testing.T) {
	ip := ownPodIP(t)
	envelopes := jetbridge.NewStepEnvelopes()
	clientset, _ := kubeClient(t)
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatalf("listening for envelope requests: %v", err)
	}
	server := &http.Server{Handler: &jetbridge.StepEnvelopeHandler{
		Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: liveTestNamespace(), Envelopes: envelopes,
	}}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	envelopeURL := "http://" + listener.Addr().String() + jetbridge.StepEnvelopePath

	handle := "live-job-check-" + time.Now().Format("150405")
	worker, clientset, cfg := jobRuntimeWorker(t, envelopes, envelopeURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "job-check"}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
		runtime.ContainerSpec{TeamID: 1, Type: db.ContainerTypeCheck, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	jobName := jetbridge.GeneratePodName(metadata, handle)
	t.Cleanup(func() { cleanupStepJob(t, clientset, cfg.Namespace, jobName) })

	// The command answers with what it read: the envelope's stdin, which
	// never appears in the Job's pod spec.
	request := `{"source":{"uri":"https://example.invalid/secret-in-request"}}`
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", `echo checking >&2; printf '[{"request":%s}]' "$(cat)"`},
	}, runtime.ProcessIO{Stdin: strings.NewReader(request), Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitStatus != 0 {
		t.Fatalf("exit = %d (stderr: %s)", result.ExitStatus, stderr.String())
	}
	if want := `[{"request":` + request + `}]`; stdout.String() != want {
		t.Errorf("answer = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "checking") {
		t.Errorf("stderr = %q: the command's log did not reach the step's stderr", stderr.String())
	}

	job, err := clientset.BatchV1().Jobs(cfg.Namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step's Job is not there: %v", err)
	}
	if spec := jobSpecText(job); strings.Contains(spec, "secret-in-request") {
		t.Error("the request reached the Job's pod spec")
	}
	// The envelope was taken once, by the Job's pod, and forgotten after.
	if _, err := envelopes.Take(jobName, "another-pod"); err == nil {
		t.Error("the envelope outlived the step")
	}

	// A repeated check reuses its container: the finished Job is replaced
	// and the new pod, not the old one the garbage collector may still be
	// taking, answers with the new request.
	second := `{"source":{"uri":"https://example.invalid/second"}}`
	stdout.Reset()
	process, err = container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", `printf '[{"request":%s}]' "$(cat)"`},
	}, runtime.ProcessIO{Stdin: strings.NewReader(second), Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if result, err := process.Wait(ctx); err != nil || result.ExitStatus != 0 {
		t.Fatalf("second Wait: %v, exit %d (stderr: %s)", err, result.ExitStatus, stderr.String())
	}
	if want := `[{"request":` + second + `}]`; stdout.String() != want {
		t.Errorf("second answer = %q, want %q: the old Job's pod was followed", stdout.String(), want)
	}
}

// exactTaskPreparer admits every container as one exact execution, the way a
// Run's ExecutionStarter does, and keeps the Run witnesses it is handed.
type exactTaskPreparer struct {
	control   *runtime.ExecutionControl
	mu        sync.Mutex
	witnesses []executioncontrol.Acknowledgement
}

func (p *exactTaskPreparer) PrepareInputs(_ context.Context, _ db.ContainerOwner, _ string, _ []string, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	return spec, nil
}

func (p *exactTaskPreparer) PrepareContainer(_ context.Context, _ db.ContainerOwner, _ db.ContainerMetadata, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	spec.ExecutionControl = p.control
	return spec, p.control.Validate(spec)
}

func (p *exactTaskPreparer) CheckStart(context.Context, db.ContainerOwner, runtime.ContainerSpec) error {
	return nil
}

func (p *exactTaskPreparer) CheckIntercept(context.Context, string) error { return nil }

func (p *exactTaskPreparer) RecordWitness(_ context.Context, _ db.ContainerOwner, witness executioncontrol.Acknowledgement) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.witnesses = append(p.witnesses, witness)
	return nil
}

func (p *exactTaskPreparer) RelocateContainer(context.Context, db.ContainerOwner, runtime.ContainerSpec, []string) (runtime.ContainerSpec, error) {
	return runtime.ContainerSpec{}, errors.New("not relocated in this test")
}

func (p *exactTaskPreparer) recorded() []executioncontrol.Acknowledgement {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]executioncontrol.Acknowledgement(nil), p.witnesses...)
}

// TestLiveJobRuntimeExactTask runs a Run-owned exact task as a step Job on a
// real kubelet, routed there by the test gate: the start is recorded when the
// launcher fetches its envelope, before the command runs, and main's
// terminated status closes the execution at the node and in the Run. The
// ledgers are this process's own daemons, one per node (the deployed
// daemons' control plane takes only the web's certificate).
func TestLiveJobRuntimeExactTask(t *testing.T) {
	ip := ownPodIP(t)
	envelopes := jetbridge.NewStepEnvelopes()
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	controls, err := jetbridge.StartLiveNodeOutputControls(ctx, clientset)
	if err != nil {
		t.Fatalf("starting the node ledgers: %v", err)
	}
	t.Cleanup(controls.Stop)

	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatalf("listening for envelope requests: %v", err)
	}
	server := &http.Server{Handler: &jetbridge.StepEnvelopeHandler{
		Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: liveTestNamespace(), Envelopes: envelopes,
	}}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	envelopeURL := "http://" + listener.Addr().String() + jetbridge.StepEnvelopePath

	identity := executioncontrol.Identity{
		ExecutionID: executioncontrol.ExecutionID(fmt.Sprintf("%08x-cccc-4ccc-8ccc-%012x", os.Getpid(), time.Now().UnixNano()&0xffffffffffff)),
		Fence:       1,
	}
	preparer := &exactTaskPreparer{control: &runtime.ExecutionControl{
		Version:    runtime.ExecutionControlVersion,
		Phase:      runtime.ControlPhaseAdmitted,
		Identity:   identity,
		Endpoint:   controls.Endpoint(),
		Capability: "base-capability",
	}}
	worker, _, _, _ := newLiveWorker(t, nil, nil, func(cfg *jetbridge.Config, deps *jetbridge.WorkerDeps) {
		cfg.StepRuntime = jetbridge.StepRuntimeJob
		cfg.LauncherImage = deployed.webContainer.Image
		cfg.StepEnvelopeURL = envelopeURL
		deps.StepEnvelopes = envelopes
		deps.OutputControls = controls
		deps.ExecutionPreparer = preparer
	})
	worker.RouteExactTasksToJobRuntimeForTesting()
	namespace := liveTestNamespace()

	handle := "live-job-exact-" + time.Now().Format("150405")
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "job-exact"}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
		runtime.ContainerSpec{TeamID: 1, Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	jobName := jetbridge.GeneratePodName(metadata, handle)
	t.Cleanup(func() { cleanupStepJob(t, clientset, namespace, jobName) })

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", "echo exact-from-the-launcher; exit 7"},
	}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitStatus != 7 {
		t.Errorf("exit = %d, want 7 from the main container's terminated status", result.ExitStatus)
	}
	if !strings.Contains(stdout.String(), "exact-from-the-launcher") {
		t.Errorf("stdout = %q: the pod's log did not reach the step", stdout.String())
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + jobName})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("the Job owns %d pods (%v), want 1", len(pods.Items), err)
	}
	pod := pods.Items[0]
	job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the step's Job is not there: %v", err)
	}

	witnesses := preparer.recorded()
	if len(witnesses) != 2 || witnesses[0].Kind != executioncontrol.AcknowledgementStart || witnesses[1].Kind != executioncontrol.AcknowledgementFinish {
		t.Fatalf("Run witnesses = %+v, want a start and a finish", witnesses)
	}
	start := witnesses[0]
	if start.PodUID != executioncontrol.PodUID(pod.UID) {
		t.Errorf("the start names pod %s, not the Job's pod %s", start.PodUID, pod.UID)
	}
	if want := executioncontrol.ProcessIdentity("job-v1:" + string(job.UID)); start.ProcessIdentity != want {
		t.Errorf("the start's process identity = %q, want %q", start.ProcessIdentity, want)
	}
	if witnesses[1].Outcome == nil || witnesses[1].Outcome.ExitCode != 7 {
		t.Errorf("the finish witness = %+v, want exit 7", witnesses[1])
	}
	ledger, err := controls.ForNode(ctx, pod.Spec.NodeName)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := ledger.Classify(ctx, identity)
	if err != nil {
		t.Fatalf("classifying at the node: %v", err)
	}
	if !closed.Classification.Authoritative() || closed.Acknowledgement == nil || closed.Acknowledgement.Outcome == nil || closed.Acknowledgement.Outcome.ExitCode != 7 {
		t.Errorf("the node classifies the execution %s (%+v), want it closed with exit 7", closed.Classification, closed.Acknowledgement)
	}
	if pod.Annotations["concourse.ci/exit-status"] != "7" {
		t.Errorf("pod annotations %v lack the exit status", pod.Annotations)
	}
}

// TestLiveJobRuntimeExactTaskStop aborts a started exact task on a real
// kubelet: the stop cuts the pod's activeDeadlineSeconds, which only a real
// kubelet acts on -- it must end the containers with their grace, keep the
// pod and keep the launcher's termination record -- and the outcome is read
// from that record. What no unit tier can say: the web's Role may patch the
// pod, the kubelet sends SIGTERM rather than SIGKILL first, and the pod is
// still there to read when the command has ended.
func TestLiveJobRuntimeExactTaskStop(t *testing.T) {
	ip := ownPodIP(t)
	envelopes := jetbridge.NewStepEnvelopes()
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	controls, err := jetbridge.StartLiveNodeOutputControls(ctx, clientset)
	if err != nil {
		t.Fatalf("starting the node ledgers: %v", err)
	}
	t.Cleanup(controls.Stop)
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatalf("listening for envelope requests: %v", err)
	}
	server := &http.Server{Handler: &jetbridge.StepEnvelopeHandler{
		Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: liveTestNamespace(), Envelopes: envelopes,
	}}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	envelopeURL := "http://" + listener.Addr().String() + jetbridge.StepEnvelopePath

	identity := executioncontrol.Identity{
		ExecutionID: executioncontrol.ExecutionID(fmt.Sprintf("%08x-dddd-4ddd-8ddd-%012x", os.Getpid(), time.Now().UnixNano()&0xffffffffffff)),
		Fence:       1,
	}
	preparer := &exactTaskPreparer{control: &runtime.ExecutionControl{
		Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted,
		Identity: identity, Endpoint: controls.Endpoint(), Capability: "base-capability",
	}}
	worker, _, _, _ := newLiveWorker(t, nil, nil, func(cfg *jetbridge.Config, deps *jetbridge.WorkerDeps) {
		cfg.StepRuntime = jetbridge.StepRuntimeJob
		cfg.LauncherImage = deployed.webContainer.Image
		cfg.StepEnvelopeURL = envelopeURL
		deps.StepEnvelopes = envelopes
		deps.OutputControls = controls
		deps.ExecutionPreparer = preparer
	})
	worker.RouteExactTasksToJobRuntimeForTesting()
	namespace := liveTestNamespace()

	handle := "live-job-exact-stop-" + time.Now().Format("150405")
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "job-exact-stop"}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata,
		runtime.ContainerSpec{TeamID: 1, Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
	if err != nil {
		t.Fatalf("FindOrCreateContainer: %v", err)
	}
	jobName := jetbridge.GeneratePodName(metadata, handle)
	t.Cleanup(func() { cleanupStepJob(t, clientset, namespace, jobName) })

	stepCtx, abort := context.WithCancel(ctx)
	defer abort()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process, err := container.Run(stepCtx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", "echo running; exec sleep 600"},
	}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	type waited struct {
		result runtime.ProcessResult
		err    error
	}
	done := make(chan waited, 1)
	go func() {
		result, err := process.Wait(stepCtx)
		done <- waited{result, err}
	}()

	// Abort once the start is recorded: the command is running.
	deadline := time.Now().Add(3 * time.Minute)
	for len(preparer.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the start was never recorded (stderr: %s)", stderr.String())
		}
		time.Sleep(500 * time.Millisecond)
	}
	abort()
	var finished waited
	select {
	case finished = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("Wait did not return after the abort")
	}
	if finished.err != nil {
		t.Fatalf("Wait: %v (stderr: %s)", finished.err, stderr.String())
	}
	if finished.result.ExitStatus != 143 {
		t.Errorf("exit = %d, want 143 from the command SIGTERM ended", finished.result.ExitStatus)
	}
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + jobName})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("the Job owns %d pods (%v), want the stopped one kept", len(pods.Items), err)
	}
	if pods.Items[0].DeletionTimestamp != nil {
		t.Error("the stop deleted the pod that witnesses the outcome")
	}
	ledger, err := controls.ForNode(ctx, pods.Items[0].Spec.NodeName)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := ledger.Classify(ctx, identity)
	if err != nil {
		t.Fatalf("classifying at the node: %v", err)
	}
	if !closed.Classification.Authoritative() || closed.Acknowledgement == nil || closed.Acknowledgement.Outcome == nil || closed.Acknowledgement.Outcome.ExitCode != 143 {
		t.Errorf("the node classifies the execution %s (%+v), want it closed with exit 143", closed.Classification, closed.Acknowledgement)
	}
}

func mainExitStatus(pod corev1.Pod) (int32, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "main" && status.State.Terminated != nil {
			return status.State.Terminated.ExitCode, true
		}
	}
	return 0, false
}

func jobSpecText(job *batchv1.Job) string {
	var text strings.Builder
	for _, c := range append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...) {
		text.WriteString(strings.Join(c.Command, " "))
		text.WriteString(strings.Join(c.Args, " "))
		for _, env := range c.Env {
			text.WriteString(env.Value)
		}
	}
	return text.String()
}

// ownPodIP is the address a step pod can reach this process on: the live
// tier runs as a pod in the cluster. Off-cluster there is no such address
// and the envelope cannot be served, so the test is skipped.
func ownPodIP(t *testing.T) string {
	t.Helper()
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	if host == "" {
		t.Skip("not running in the cluster: a step pod cannot reach this process to fetch its envelope")
	}
	conn, err := net.Dial("udp", net.JoinHostPort(host, "443"))
	if err != nil {
		t.Skipf("no route to the API server: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
