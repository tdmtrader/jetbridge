package jetbridge

// An exact task on the job step runtime, against a real artifact daemon
// output plane (exact_execution_harness_test.go).
//
// The launcher is played by the spec: it asks the web's real envelope
// handler for its envelope with the token the TokenReview double binds to
// the Job's pod, and the kubelet's part -- main terminating with the
// launcher's record -- is a status update on the fake API. Everything the
// ledger says is the real daemon's.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// eventLog is the order things happened in, across the node, the Run and the
// launcher.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *eventLog) add(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *eventLog) all() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

// orderedOutputControl is the real client with its two writes logged as
// they return.
type orderedOutputControl struct {
	OutputControl
	log *eventLog
}

func (ordered *orderedOutputControl) RecordStart(ctx context.Context, id executioncontrol.Identity,
	pod executioncontrol.PodUID, process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	ack, err := ordered.OutputControl.RecordStart(ctx, id, pod, process)
	if err == nil {
		ordered.log.add("node-start")
	}
	return ack, err
}

func (ordered *orderedOutputControl) RecordOutcome(ctx context.Context, id executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind, outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	ack, err := ordered.OutputControl.RecordOutcome(ctx, id, kind, outcome)
	if err == nil {
		ordered.log.add("node-outcome")
	}
	return ack, err
}

// servedRecorder logs the moment the envelope's bytes leave the web.
type servedRecorder struct {
	*httptest.ResponseRecorder
	log    *eventLog
	logged bool
}

func (served *servedRecorder) Write(body []byte) (int, error) {
	if !served.logged && (served.Code == 0 || served.Code == http.StatusOK) {
		served.logged = true
		served.log.add("envelope-served")
	}
	return served.ResponseRecorder.Write(body)
}

var _ = Describe("An exact task on the job step runtime", func() {
	var (
		harness   *outputDaemonHarness
		clientset *fake.Clientset
		container *Container
		handler   *StepEnvelopeHandler
		events    *eventLog
		witnesses []executioncontrol.Acknowledgement
		witnessMu sync.Mutex
		ctx       context.Context
		jobName   string
		mainPod   string
	)

	executionID := executioncontrol.ExecutionID(fmt.Sprintf("%08x-bbbb-4bbb-8bbb-bbbbbbbbbbbb", os.Getpid()))
	identity := executioncontrol.Identity{ExecutionID: executionID, Fence: 1}

	recorded := func() []executioncontrol.Acknowledgement {
		witnessMu.Lock()
		defer witnessMu.Unlock()
		return append([]executioncontrol.Acknowledgement(nil), witnesses...)
	}

	// fetch is the launcher asking for its envelope with token.
	fetch := func(token string) int {
		request := httptest.NewRequest(http.MethodPost, StepEnvelopePath, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := &servedRecorder{ResponseRecorder: httptest.NewRecorder(), log: events}
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	// terminate is the kubelet reporting main's exit, with the launcher's
	// record as its termination message when message is set.
	terminate := func(exit int32, message string) {
		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		pod.Status.Phase = corev1.PodSucceeded
		if exit != 0 {
			pod.Status.Phase = corev1.PodFailed
		}
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  mainContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Message: message}},
		}}
		_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	launcherRecord := func(exit int) string {
		body, err := json.Marshal(map[string]any{"exit": exit, "started_at": time.Now(), "finished_at": time.Now(), "stdout_bytes": 0})
		Expect(err).ToNot(HaveOccurred())
		return string(body)
	}

	classify := func() executioncontrol.ClassifyResult {
		result, err := harness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		return result
	}

	type waited struct {
		result runtime.ProcessResult
		err    error
	}

	// start runs the task and has Wait follow it, returning once the node
	// has admitted the execution against the Job's pod.
	start := func() <-chan waited {
		process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "exit 3"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
		Expect(err).ToNot(HaveOccurred())
		done := make(chan waited, 1)
		go func() {
			defer GinkgoRecover()
			result, err := process.Wait(ctx)
			done <- waited{result, err}
		}()
		Eventually(func() error {
			_, err := harness.Client.Classify(ctx, identity)
			return err
		}, 10*time.Second, 50*time.Millisecond).Should(Succeed(), "the execution was never admitted")
		return done
	}

	BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
		DeferCleanup(cancel)

		started, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		harness = started
		DeferCleanup(harness.Stop)

		events = &eventLog{}
		witnesses = nil

		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
		})

		config := jobRuntimeConfig()
		config.ExactTasksOnJobRuntimeForTesting = true
		metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
		container = newContainer("exact-job-handle", metadata, runtime.ContainerSpec{
			Type:      db.ContainerTypeTask,
			Dir:       "/tmp/build/task",
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			ExecutionControl: &runtime.ExecutionControl{
				Version:    runtime.ExecutionControlVersion,
				Phase:      runtime.ControlPhaseAdmitted,
				Identity:   identity,
				Endpoint:   harness.Endpoint,
				Capability: "base-capability",
			},
		}, nil, clientset, config, "worker", nil, nil, nil, stepPodIdentities{}, false, false)
		container.envelopes = NewStepEnvelopes()
		container.outputControls = staticOutputControls{control: &orderedOutputControl{OutputControl: harness.Client, log: events}}
		container.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
			witnessMu.Lock()
			witnesses = append(witnesses, witness)
			witnessMu.Unlock()
			events.add("run-witness-" + string(witness.Kind))
			return nil
		}

		jobName = container.podName
		mainPod = jobName + "-abcde"
		// The Job controller gives the Job one running pod on node-1. A
		// second pod that also names the Job as its owner stands for an
		// earlier Job's pod the garbage collector has not taken.
		jobControllerDouble(clientset, func(job *batchv1.Job) *corev1.Pod {
			return &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: mainPod, UID: types.UID("uid-" + mainPod)},
				Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
					Name: mainContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}}},
			}
		})
		other := stepJobPod(jobName+"-other", jobName)
		other.Spec.NodeName = "node-1"
		_, err = clientset.CoreV1().Pods("test-ns").Create(ctx, other, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
		tokenReviewDouble(clientset, map[string]string{"tok-main": mainPod, "tok-other": jobName + "-other"})
		handler = &StepEnvelopeHandler{Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: "test-ns", Envelopes: container.envelopes}
	})

	It("records the start at the node and in the Run before the envelope is served", func() {
		done := start()
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted),
			"the start was recorded before the launcher asked for its envelope")

		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		Expect(events.all()).To(Equal([]string{"node-start", "run-witness-start", "envelope-served"}))

		starts := recorded()
		Expect(starts).To(HaveLen(1))
		Expect(starts[0].PodUID).To(Equal(executioncontrol.PodUID("uid-" + mainPod)), "the start names the token's pod")
		Expect(starts[0].ProcessIdentity).To(Equal(executioncontrol.ProcessIdentity("job-v1:uid-" + jobName)))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))

		terminate(3, launcherRecord(3))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
		Expect(finished.result.ExitStatus).To(Equal(3))
	})

	It("serves a launcher's retry by the same pod again without a second start", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		first := classify()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		second := classify()

		Expect(second.Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(second.Acknowledgement).To(Equal(first.Acknowledgement), "the node's start changed on the retry")
		starts := recorded()
		Expect(starts).To(HaveLen(2))
		Expect(starts[1]).To(Equal(starts[0]), "the retry's start is not the ledger's replay of the first")

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	It("refuses a second pod of the execution and records nothing for it", func() {
		done := start()
		Expect(fetch("tok-other")).To(Equal(http.StatusConflict))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
		Expect(events.all()).To(BeEmpty())
		Expect(recorded()).To(BeEmpty())

		// The refusal did not take the envelope from the admitted pod.
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		Expect(fetch("tok-other")).To(Equal(http.StatusConflict))
		Expect(events.all()).To(Equal([]string{"node-start", "run-witness-start", "envelope-served"}))

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	DescribeTable("never serves an execution the ledger does not classify never_started",
		func(prepare func(podUID executioncontrol.PodUID)) {
			done := start()
			prepare(executioncontrol.PodUID("uid-" + mainPod))
			before := classify()

			Expect(fetch("tok-main")).To(Equal(http.StatusConflict))
			Expect(events.all()).To(BeEmpty())
			Expect(recorded()).To(BeEmpty())
			Expect(classify()).To(Equal(before), "the refused start changed the ledger")

			// The launcher, refused, exits without running the command.
			terminate(125, "")
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(MatchError(ContainSubstring("before its exact start was recorded")))
			Expect(classify()).To(Equal(before))
		},
		Entry("executing under another process", func(pod executioncontrol.PodUID) {
			_, err := harness.Client.RecordStart(ctx, identity, pod, "supervisor-v1:/tmp/jb-exact-elsewhere")
			Expect(err).ToNot(HaveOccurred())
		}),
		Entry("finished", func(pod executioncontrol.PodUID) {
			_, err := harness.Client.RecordStart(ctx, identity, pod, "supervisor-v1:/tmp/jb-exact-elsewhere")
			Expect(err).ToNot(HaveOccurred())
			_, err = harness.Client.RecordOutcome(ctx, identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
			Expect(err).ToNot(HaveOccurred())
		}),
	)

	It("leaves a started task unresolved when its pod ends without the launcher's record", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		terminate(137, "")
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(events.all()).ToNot(ContainElement("node-outcome"))
		_, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred(), "the Job of a started execution with no outcome was deleted")
	})

	It("closes the execution from main's terminated status, with ledger and Run rows shaped as the exec runtime's", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		terminate(3, launcherRecord(3))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
		Expect(finished.result.ExitStatus).To(Equal(3))
		Expect(events.all()).To(Equal([]string{"node-start", "run-witness-start", "envelope-served", "node-outcome", "run-witness-finish"}))

		closed := classify()
		Expect(closed.Classification.Authoritative()).To(BeTrue())
		Expect(closed.Acknowledgement.Outcome.ExitCode).To(Equal(3))
		properties, _ := container.Properties()
		Expect(properties[exitStatusPropertyName]).To(Equal("3"))

		// The same task on the exec runtime, against a ledger of its own.
		execHarness, err := startOutputDaemon()
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(execHarness.Stop)
		execClientset := fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pause-pod", Namespace: "test-ns", UID: types.UID("uid-" + mainPod)},
			Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: mainContainerName}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: mainContainerName, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}}},
		})
		var execWitnesses []executioncontrol.Acknowledgement
		execConfig := NewConfig("test-ns", "")
		execContainer := &Container{
			handle:        "pause-handle",
			podName:       "pause-pod",
			metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{Type: db.ContainerTypeTask, ExecutionControl: &runtime.ExecutionControl{
				Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted, Identity: identity,
				Endpoint: execHarness.Endpoint, Capability: "base-capability",
			}},
			clientset:      execClientset,
			config:         execConfig,
			properties:     map[string]string{},
			outputControls: execHarness,
			recordWitness: func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				execWitnesses = append(execWitnesses, witness)
				return nil
			},
		}
		execProcess := newExecProcess("exact-exec-shape", "pause-pod", execClientset, execConfig, execContainer,
			&controlExecutor{run: func(int) error { return &ExecExitError{ExitCode: 3} }},
			runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "exit 3"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
		result, err := execProcess.Wait(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.ExitStatus).To(Equal(3))

		// Row for row the same, but for the process identity and the time.
		jobWitnesses := recorded()
		Expect(jobWitnesses).To(HaveLen(len(execWitnesses)))
		normalize := func(ack executioncontrol.Acknowledgement) executioncontrol.Acknowledgement {
			ack.ProcessIdentity, ack.ObservedAt = "", executioncontrol.Timestamp{}
			return ack
		}
		for i := range execWitnesses {
			Expect(jsonKeys(jobWitnesses[i])).To(Equal(jsonKeys(execWitnesses[i])))
			Expect(normalize(jobWitnesses[i])).To(Equal(normalize(execWitnesses[i])))
			Expect(jobWitnesses[i].ProcessIdentity).ToNot(Equal(execWitnesses[i].ProcessIdentity))
		}
		execClosed, err := execHarness.Client.Classify(ctx, identity)
		Expect(err).ToNot(HaveOccurred())
		Expect(execClosed.Classification).To(Equal(closed.Classification))
		Expect(normalize(*closed.Acknowledgement)).To(Equal(normalize(*execClosed.Acknowledgement)))
	})
})

func jsonKeys(value any) []string {
	body, err := json.Marshal(value)
	Expect(err).ToNot(HaveOccurred())
	var fields map[string]json.RawMessage
	Expect(json.Unmarshal(body, &fields)).To(Succeed())
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
