package jetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// Cancel only after the first alias reaches the daemon. Unlike the fake
// clientset, this transport honours cancellation on subsequent requests.
type cancellingAliasTransport struct {
	cancel  context.CancelFunc
	aliases []string
}

func (t *cancellingAliasTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	deadline, bounded := req.Context().Deadline()
	Expect(bounded).To(BeTrue(), "every daemon request needs a bounded recording context")
	Expect(time.Until(deadline)).To(BeNumerically(">", 0))
	Expect(time.Until(deadline)).To(BeNumerically("<=", 5*time.Second), "each alias keeps its own five-second cap")
	status := http.StatusAccepted
	if req.URL.Path == "/register" {
		var registration artifactwire.RegisterRequest
		Expect(json.NewDecoder(req.Body).Decode(&registration)).To(Succeed())
		t.aliases = append(t.aliases, registration.Key)
		if len(t.aliases) == 1 {
			t.cancel()
		}
		status = http.StatusCreated
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

func cancellingAliasBackend(clientset kubernetes.Interface, cancel context.CancelFunc) (*DaemonSetBackend, *cancellingAliasTransport) {
	GinkgoHelper()
	transport := &cancellingAliasTransport{cancel: cancel}
	wire, err := artifactwire.NewClient(7780, artifactwire.TLS{})
	Expect(err).NotTo(HaveOccurred())
	wire.WithTransport(transport)
	backend := NewDaemonSetBackend(testDaemonConfig(), NewArtifactLocator(), NewNodeIPResolver(clientset),
		&DaemonClient{wire: wire, logger: lagertest.NewTestLogger("output-recording")})
	backend.wire = wire
	return backend, transport
}

func twoOutputVolumes() ([]*Volume, runtime.ContainerSpec) {
	return []*Volume{NewStubVolume("report-volume", "worker", "/report"), NewStubVolume("result-volume", "worker", "/result")},
		runtime.ContainerSpec{Type: db.ContainerTypeTask, Outputs: runtime.OutputPaths{"report": "/report", "result": "/result"}}
}

type completedOutputExecutor struct{ exit int }

func (e completedOutputExecutor) ExecInPod(context.Context, string, string, string, []string, io.Reader, io.Writer, io.Writer, bool, ExecAttrs) error {
	if e.exit != 0 {
		return &ExecExitError{ExitCode: e.exit}
	}
	return nil
}

var _ = Describe("Output recording completion budgets", func() {
	for _, kind := range []string{"exec", "job"} {
		for _, exit := range []int{0, 23} {
			It(fmt.Sprintf("%s preserves both aliases when cancellation arrives during recording after exit %d", kind, exit), func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pod := runningJobPod("task-pod", "node-1")
				pod.Namespace = "test-ns"
				pod.Labels = map[string]string{jobUIDLabelKey: "job-uid"}
				if kind == "job" {
					pod.Status.Phase = corev1.PodSucceeded
					if exit != 0 {
						pod.Status.Phase = corev1.PodFailed
					}
					pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: int32(exit)}}
				}
				clientset := fake.NewSimpleClientset(pod, &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
					Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}},
				})
				backend, transport := cancellingAliasBackend(clientset, cancel)
				volumes, spec := twoOutputVolumes()
				container := &Container{handle: "task-handle", podName: pod.Name, jobUID: "job-uid", clientset: clientset,
					config: Config{Namespace: "test-ns"}, metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
					containerSpec: spec, volumes: volumes, properties: map[string]string{}, storageBackend: backend}
				var process runtime.Process
				if kind == "exec" {
					process = newExecProcess("task", pod.Name, clientset, container.config, container,
						completedOutputExecutor{exit: exit}, runtime.ProcessSpec{Path: "write-outputs"}, runtime.ProcessIO{}, backend)
				} else {
					container.podName = "task-job"
					process = newJobProcess("task", container, runtime.ProcessIO{})
				}
				result, err := process.Wait(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(exit))
				Expect(ctx.Err()).To(MatchError(context.Canceled), "the first alias never cancelled the task")
				Expect(transport.aliases).To(Equal([]string{"report-volume", "result-volume"}))
			})
		}
	}

	It("deletes the abandoned Job by UID and records with a live context after the final pod read times out", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pod := runningJobPod("task-pod", "node-1")
		pod.Namespace = "test-ns"
		pod.Labels = map[string]string{jobUIDLabelKey: "job-uid"}
		transport := &stalledFinalPodTransport{pod: pod, cancel: cancel}
		clientset, err := kubernetes.NewForConfigAndClient(&rest.Config{Host: "http://step-api.invalid", ContentConfig: rest.ContentConfig{ContentType: "application/json"}}, &http.Client{Transport: transport})
		Expect(err).NotTo(HaveOccurred())
		recorded := false
		var recordErr error
		var recordRemaining time.Duration
		backend := cancelledOutputBackend{record: func(recordCtx context.Context, _, node string, _ []*Volume, _ runtime.ContainerSpec) {
			recorded = true
			recordErr = recordCtx.Err()
			Expect(node).To(Equal("node-1"))
			deadline, bounded := recordCtx.Deadline()
			Expect(bounded).To(BeTrue())
			recordRemaining = time.Until(deadline)
		}}
		container := &Container{handle: "task-handle", podName: "task-job", jobUID: "job-uid", clientset: clientset,
			config: Config{Namespace: "test-ns"}, metadata: db.ContainerMetadata{Type: db.ContainerTypeTask}, storageBackend: backend}
		_, err = newJobProcess("task", container, runtime.ProcessIO{}).Wait(ctx)
		Expect(err).To(MatchError(context.Canceled))
		Expect(transport.finalReadExpired).To(BeTrue(), "the final read must exhaust its actual context")
		Expect(transport.deletedUIDs).To(Equal([]types.UID{"job-uid"}), "the DELETE must reach the API with this Job's UID")
		Expect(recorded).To(BeTrue())
		Expect(recordErr).NotTo(HaveOccurred())
		Expect(recordRemaining).To(BeNumerically(">", 9*time.Second), "recording has its own fresh budget")
	})
})

// A context-aware API transport is necessary here: fake clientset reactors
// do not receive the caller's context and accept a DELETE even when expired.
type stalledFinalPodTransport struct {
	pod              *corev1.Pod
	cancel           context.CancelFunc
	podReads         int
	finalReadExpired bool
	deletedUIDs      []types.UID
}

func (t *stalledFinalPodTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	var answer any
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/pods"):
		answer = &corev1.PodList{TypeMeta: metav1.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: []corev1.Pod{*t.pod}}
	case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/pods/"+t.pod.Name):
		t.podReads++
		if t.podReads == 1 {
			t.cancel()
			answer = t.pod
		} else {
			<-req.Context().Done()
			t.finalReadExpired = req.Context().Err() == context.DeadlineExceeded
			return nil, req.Context().Err()
		}
	case req.Method == http.MethodDelete && strings.HasSuffix(req.URL.Path, "/jobs/task-job"):
		deadline, bounded := req.Context().Deadline()
		Expect(bounded).To(BeTrue())
		Expect(time.Until(deadline)).To(BeNumerically(">", 9*time.Second), "deletion has its own fresh ten-second budget")
		var opts metav1.DeleteOptions
		Expect(json.NewDecoder(req.Body).Decode(&opts)).To(Succeed())
		Expect(opts.Preconditions).NotTo(BeNil())
		Expect(opts.Preconditions.UID).NotTo(BeNil())
		t.deletedUIDs = append(t.deletedUIDs, *opts.Preconditions.UID)
		answer = &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess}
	default:
		return nil, fmt.Errorf("unexpected Kubernetes request: %s %s", req.Method, req.URL)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
}
