package jetbridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/artifactwire"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// cancelledOutputBackend observes the real process's storage boundary. The
// embedded interface is deliberately unused: Wait only records locations.
type cancelledOutputBackend struct {
	StorageBackend
	record func(context.Context, string, string, []*Volume, runtime.ContainerSpec)
}

func (b cancelledOutputBackend) RecordOutputs(ctx context.Context, handle, node string, volumes []*Volume, spec runtime.ContainerSpec) {
	b.record(ctx, handle, node, volumes, spec)
}

type cancelledOutputExecutor struct{}

func (cancelledOutputExecutor) ExecInPod(ctx context.Context, _, _, _ string, _ []string, _ io.Reader, _, _ io.Writer, _ bool, _ ExecAttrs) error {
	<-ctx.Done()
	return errors.New("exec stream closed on cancellation")
}

var _ = Describe("Outputs of an abandoned task", func() {
	for _, kind := range []string{"exec", "job"} {
		for _, termination := range []string{"timeout", "abort"} {
			for _, placement := range []string{"running on a node", "unscheduled", "assigned but main never started"} {
				It(kind+" records partial outputs on "+termination+" only after "+placement, func() {
					pod := runningJobPod("task-pod", "node-1")
					pod.Namespace = "test-ns"
					pod.Labels = map[string]string{jobUIDLabelKey: "job-uid"}
					if placement != "running on a node" {
						pod.Status = corev1.PodStatus{Phase: corev1.PodPending}
						if placement == "unscheduled" {
							pod.Spec.NodeName = ""
						}
					}
					job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "task-job", Namespace: "test-ns", UID: "job-uid"}}
					clientset := fake.NewSimpleClientset(pod, job)
					var events []string
					clientset.PrependReactor("delete", "*", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
						events = append(events, "stop-"+action.GetResource().Resource)
						return false, nil, nil
					})
					volumes := []*Volume{NewStubVolume("task-output-report", "worker", "/work/report")}
					spec := runtime.ContainerSpec{Type: db.ContainerTypeTask, Outputs: runtime.OutputPaths{"report": "/work/report"}}
					recorded := 0
					backend := cancelledOutputBackend{record: func(ctx context.Context, handle, node string, gotVolumes []*Volume, gotSpec runtime.ContainerSpec) {
						recorded++
						events = append(events, "record")
						Expect(ctx.Err()).NotTo(HaveOccurred(), "output recording must outlive the task context")
						deadline, bounded := ctx.Deadline()
						Expect(bounded).To(BeTrue(), "output recording must have a bounded budget")
						Expect(time.Until(deadline)).To(BeNumerically(">", 0))
						Expect(time.Until(deadline)).To(BeNumerically("<=", 30*time.Second))
						Expect(ctx.Value("output-test")).To(Equal("retained"))
						Expect(handle).To(Equal("task-handle"))
						Expect(node).To(Equal("node-1"))
						Expect(gotVolumes).To(Equal(volumes))
						Expect(gotSpec).To(Equal(spec))
					}}
					container := &Container{handle: "task-handle", podName: pod.Name, jobUID: job.UID,
						clientset: clientset, config: Config{Namespace: "test-ns"}, metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
						containerSpec: spec, volumes: volumes, storageBackend: backend}
					var process runtime.Process
					if kind == "exec" {
						process = newExecProcess("task", pod.Name, clientset, container.config, container,
							cancelledOutputExecutor{}, runtime.ProcessSpec{Path: "write-report"}, runtime.ProcessIO{}, backend)
					} else {
						container.podName = job.Name
						process = newJobProcess("task", container, runtime.ProcessIO{})
					}
					base := context.WithValue(context.Background(), "output-test", "retained")
					var ctx context.Context
					var cancel context.CancelFunc
					want := context.Canceled
					if termination == "timeout" {
						ctx, cancel = context.WithTimeout(base, 100*time.Millisecond)
						want = context.DeadlineExceeded
					} else {
						ctx, cancel = context.WithCancel(base)
						timer := time.AfterFunc(100*time.Millisecond, cancel)
						DeferCleanup(func() { timer.Stop() })
					}
					defer cancel()
					_, err := process.Wait(ctx)
					Expect(errors.Is(err, want)).To(BeTrue(), "original cancellation lost: %v", err)
					if placement == "running on a node" {
						Expect(recorded).To(Equal(1), "a task that ran must file its output locations")
						resource := "pods"
						if kind == "job" {
							resource = "jobs"
						}
						Expect(events).To(Equal([]string{"stop-" + resource, "record"}), "stop the writer before recording its outputs")
					} else {
						Expect(recorded).To(BeZero(), "a command that never started has no outputs to file")
					}
				})
			}
		}
	}
})

// A transport double checks the context on the actual daemon requests. Merely
// passing a deadline to RecordOutputs does not bound it if the backend replaces
// that context before registering the alias or triggering its mirror.
type outputRecordingTransport struct {
	paths []string
}

func (t *outputRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.paths = append(t.paths, req.URL.Path)
	Expect(req.Context().Value("output-test")).To(Equal("retained"))
	deadline, bounded := req.Context().Deadline()
	Expect(bounded).To(BeTrue())
	Expect(time.Until(deadline)).To(BeNumerically("<=", time.Second))
	status := http.StatusAccepted
	if req.URL.Path == "/register" {
		status = http.StatusCreated
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

var _ = Describe("Recording abandoned output locations", func() {
	It("keeps the caller's budget through alias registration and mirroring", func() {
		clientset := fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}},
		})
		transport := &outputRecordingTransport{}
		wire, err := artifactwire.NewClient(7780, artifactwire.TLS{})
		Expect(err).NotTo(HaveOccurred())
		wire.WithTransport(transport)
		backend := NewDaemonSetBackend(testDaemonConfig(), NewArtifactLocator(), NewNodeIPResolver(clientset), &DaemonClient{wire: wire, logger: lagertest.NewTestLogger("output-recording")})
		backend.wire = wire
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), "output-test", "retained"), time.Second)
		defer cancel()
		backend.RecordOutputs(ctx, "task", "node-1", []*Volume{NewStubVolume("report", "worker", "/report")},
			runtime.ContainerSpec{Type: db.ContainerTypeTask, Outputs: runtime.OutputPaths{"report": "/report"}})
		Expect(transport.paths).To(Equal([]string{"/register", "/mirror"}))
	})
})

// The transport can report the command's own exit as cancellation arrives.
// Wait already preserves that exit; its output recording must survive too.
type exitedOutputExecutor struct{ exit int }

func (e exitedOutputExecutor) ExecInPod(ctx context.Context, _, _, _ string, _ []string, _ io.Reader, _, _ io.Writer, _ bool, _ ExecAttrs) error {
	<-ctx.Done()
	if e.exit != 0 {
		return &ExecExitError{ExitCode: e.exit}
	}
	return nil
}

var _ = Describe("An exit racing task cancellation", func() {
	for _, exit := range []int{0, 23} {
		It("records outputs when the transport reports the command's own exit", func() {
			pod := runningJobPod("task-pod", "node-1")
			pod.Namespace = "test-ns"
			clientset := fake.NewSimpleClientset(pod)
			recorded := 0
			backend := cancelledOutputBackend{record: func(ctx context.Context, _, node string, _ []*Volume, _ runtime.ContainerSpec) {
				recorded++
				Expect(ctx.Err()).NotTo(HaveOccurred(), "the command exited; its output registration must survive cancellation")
				Expect(node).To(Equal("node-1"))
			}}
			container := &Container{handle: "task-handle", podName: pod.Name, clientset: clientset,
				config: Config{Namespace: "test-ns"}, metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
				properties: map[string]string{}, storageBackend: backend}
			process := newExecProcess("task", pod.Name, clientset, container.config, container,
				exitedOutputExecutor{exit: exit}, runtime.ProcessSpec{Path: "write-report"}, runtime.ProcessIO{}, backend)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			result, err := process.Wait(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(exit))
			Expect(recorded).To(Equal(1))
		})
	}
})
