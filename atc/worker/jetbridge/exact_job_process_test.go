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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
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

	mu sync.Mutex
	// loseStartAnswers is how many starts the node commits whose answer
	// never reaches the web, as a connection cut after the write does.
	loseStartAnswers int
	// starts is every start the node answered, replays included.
	starts []executioncontrol.Acknowledgement
}

// distinctStarts is how many different starts the node answered with: a
// replay answers with the start it holds, a second start would not.
func (ordered *orderedOutputControl) distinctStarts() int {
	ordered.mu.Lock()
	defer ordered.mu.Unlock()
	seen := map[executioncontrol.LedgerSequence]bool{}
	for _, start := range ordered.starts {
		seen[start.LedgerSequence] = true
	}
	return len(seen)
}

func (ordered *orderedOutputControl) RecordStart(ctx context.Context, id executioncontrol.Identity,
	pod executioncontrol.PodUID, process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	ack, err := ordered.OutputControl.RecordStart(ctx, id, pod, process)
	if err == nil {
		ordered.log.add("node-start")
		ordered.mu.Lock()
		defer ordered.mu.Unlock()
		ordered.starts = append(ordered.starts, ack)
		if ordered.loseStartAnswers > 0 {
			ordered.loseStartAnswers--
			return executioncontrol.Acknowledgement{}, errors.New("connection reset by peer")
		}
	}
	return ack, err
}

func (log *eventLog) count(event string) int {
	n := 0
	for _, e := range log.all() {
		if e == event {
			n++
		}
	}
	return n
}

func (ordered *orderedOutputControl) RequestStop(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	result, err := ordered.OutputControl.RequestStop(ctx, id)
	if err == nil {
		ordered.log.add("node-stop")
	}
	return result, err
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
		ordered   *orderedOutputControl
		witnesses []executioncontrol.Acknowledgement
		witnessMu sync.Mutex
		// failStartWitnesses is how many start witnesses the Run refuses.
		failStartWitnesses int
		ctx                context.Context
		jobName            string
		mainPod            string
		// podFor is the pod the Job controller gives the nth Job of the
		// step's name.
		podFor func(n int, job *batchv1.Job) *corev1.Pod
		// newWeb is a web: a container for the step with its own envelope
		// store, and the handler serving it. A spec calls it again for a
		// web that restarted.
		newWeb func() *Container
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
	terminatePod := func(name string, exit int32, message string) {
		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, name, metav1.GetOptions{})
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
	terminate := func(exit int32, message string) { terminatePod(mainPod, exit, message) }

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
	run := func() *jobProcess {
		process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "exit 3"}},
			runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
		Expect(err).ToNot(HaveOccurred())
		return process.(*jobProcess)
	}
	startWith := func(waitCtx context.Context) (*jobProcess, <-chan waited) {
		process := run()
		done := make(chan waited, 1)
		go func() {
			defer GinkgoRecover()
			result, err := process.Wait(waitCtx)
			done <- waited{result, err}
		}()
		Eventually(func() error {
			_, err := harness.Client.Classify(ctx, identity)
			return err
		}, 10*time.Second, 50*time.Millisecond).Should(Succeed(), "the execution was never admitted")
		return process, done
	}
	start := func() <-chan waited {
		_, done := startWith(ctx)
		return done
	}
	jobExists := func() bool {
		_, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "reading the Job: %v", err)
			return false
		}
		return true
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
		failStartWitnesses = 0

		// A stop the kubelet never acts on is given up quickly.
		previousStopBudget := exactJobStopBudget
		exactJobStopBudget = time.Second
		DeferCleanup(func() { exactJobStopBudget = previousStopBudget })

		clientset = fake.NewSimpleClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
			Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}},
		})

		ordered = &orderedOutputControl{OutputControl: harness.Client, log: events}
		newWeb = func() *Container {
			config := jobRuntimeConfig()
			metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, PipelineName: "p", JobName: "j", BuildName: "1", StepName: "s"}
			web := newContainer("exact-job-handle", metadata, runtime.ContainerSpec{
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
			web.envelopes = NewStepEnvelopes()
			web.routeExactTasksToJobRuntime()
			web.outputControls = staticOutputControls{control: ordered}
			web.recordWitness = func(_ context.Context, witness executioncontrol.Acknowledgement) error {
				witnessMu.Lock()
				if witness.Kind == executioncontrol.AcknowledgementStart && failStartWitnesses > 0 {
					failStartWitnesses--
					witnessMu.Unlock()
					return errors.New("the Run's database is unreachable")
				}
				witnesses = append(witnesses, witness)
				witnessMu.Unlock()
				events.add("run-witness-" + string(witness.Kind))
				return nil
			}
			handler = &StepEnvelopeHandler{Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: "test-ns", Envelopes: web.envelopes}
			return web
		}
		container = newWeb()

		jobName = container.podName
		mainPod = jobName + "-abcde"
		// The Job controller gives the Job one running pod on node-1. Two
		// more pods name the Job as their owner: one owned by this very Job
		// (a second pod of it, which backoffLimit 0 should never make), and
		// one owned by an earlier Job of the same name that the garbage
		// collector has not taken.
		podFor = func(n int, job *batchv1.Job) *corev1.Pod { return runningJobPod(nthJobPod(jobName, n), "node-1") }
		countingJobController(clientset, func(n int, job *batchv1.Job) *corev1.Pod { return podFor(n, job) })
		for name, owner := range map[string]types.UID{jobName + "-other": types.UID("uid-" + jobName), jobName + "-stale": "uid-an-earlier-job"} {
			pod := stepJobPod(name, jobName)
			pod.Spec.NodeName = "node-1"
			pod.OwnerReferences[0].UID = owner
			_, err = clientset.CoreV1().Pods("test-ns").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}
		tokenReviewDouble(clientset, map[string]string{
			"tok-main": mainPod, "tok-other": jobName + "-other", "tok-stale": jobName + "-stale",
			"tok-2": nthJobPod(jobName, 2), "tok-3": nthJobPod(jobName, 3),
		})
	})

	It("records the start at the node and in the Run before the envelope is served", func() {
		done := start()
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted),
			"the start was recorded before the launcher asked for its envelope")

		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		Expect(events.all()).To(Equal([]string{"node-start", "run-witness-start", "envelope-served"}))

		starts := recorded()
		Expect(starts).To(HaveLen(1))
		Expect(starts[0].PodUID).To(Equal(executioncontrol.PodUID("uid-"+mainPod)), "the start names the token's pod")
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
		Expect(ordered.distinctStarts()).To(Equal(1), "the retry recorded a second start")
		Expect(recorded()).To(HaveLen(1))
		Expect(events.count("envelope-served")).To(Equal(2))

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	It("asks the ledger, not its memory, before serving a retry", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		// The node closes the execution behind this web's back (a Run's
		// cancellation recovered it, say): the start this web remembers is
		// no longer one the node answers for.
		_, err := harness.Client.RecordOutcome(ctx, identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
		Expect(err).ToNot(HaveOccurred())
		Expect(fetch("tok-main")).To(Equal(http.StatusConflict), "a retry was served on memory of a start the node has closed")

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
	})

	It("serves two concurrent requests by the same pod on one start", func() {
		done := start()
		codes := make(chan int, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for range 2 {
			go func() {
				defer GinkgoRecover()
				ready.Done()
				ready.Wait()
				codes <- fetch("tok-main")
			}()
		}
		Expect(<-codes).To(Equal(http.StatusOK))
		Expect(<-codes).To(Equal(http.StatusOK))
		Expect(ordered.distinctStarts()).To(Equal(1), "two requests recorded two starts")
		Expect(recorded()).To(HaveLen(1))

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	It("serves a start whose answer the web lost as the ledger's replay of it", func() {
		done := start()
		ordered.mu.Lock()
		ordered.loseStartAnswers = 1
		ordered.mu.Unlock()
		Expect(fetch("tok-main")).To(Equal(http.StatusServiceUnavailable))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(events.all()).To(Equal([]string{"node-start"}), "the envelope was served on a start the web never saw")
		first := classify().Acknowledgement

		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		Expect(classify().Acknowledgement).To(Equal(first), "the retry was not the ledger's replay")
		Expect(recorded()).To(HaveLen(1))

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	DescribeTable("refuses another pod and records nothing for it",
		func(token string) {
			done := start()
			Expect(fetch(token)).To(Equal(http.StatusConflict))
			Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
			Expect(events.all()).To(BeEmpty())
			Expect(recorded()).To(BeEmpty())

			// The refusal did not take the envelope from the admitted pod.
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			Expect(fetch(token)).To(Equal(http.StatusConflict))
			Expect(events.all()).To(Equal([]string{"node-start", "run-witness-start", "envelope-served"}))

			terminate(0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
		},
		// Owned by this Job, so only the admitted pod's UID tells it apart.
		Entry("a second pod of this Job", "tok-other"),
		Entry("a pod of an earlier Job of the same name", "tok-stale"),
	)

	It("serves nothing while the Run cannot retain the start, and serves it once the Run does", func() {
		done := start()
		witnessMu.Lock()
		failStartWitnesses = 1
		witnessMu.Unlock()
		Expect(fetch("tok-main")).To(Equal(http.StatusServiceUnavailable))
		Expect(events.all()).To(Equal([]string{"node-start"}), "the envelope was served with the Run not holding the start")
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))

		// The retry is the ledger's replay of the one start, then its witness.
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		Expect(events.all()).To(Equal([]string{"node-start", "node-start", "run-witness-start", "envelope-served"}))
		Expect(ordered.distinctStarts()).To(Equal(1))

		terminate(0, launcherRecord(0))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
	})

	It("tells a launcher that asks before admission to retry", func() {
		previous := exactJobAdmissionWait
		exactJobAdmissionWait = 200 * time.Millisecond
		DeferCleanup(func() { exactJobAdmissionWait = previous })
		// Run, and no Wait: nothing admits the execution.
		run()
		Expect(fetch("tok-main")).To(Equal(http.StatusServiceUnavailable))
		Expect(events.all()).To(BeEmpty())
		Expect(recorded()).To(BeEmpty())
	})

	It("closes a task whose command could not be started on the launcher's record", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		terminate(127, launcherRecord(127))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).ToNot(HaveOccurred())
		Expect(finished.result.ExitStatus).To(Equal(127))
		closed := classify()
		Expect(closed.Classification.Authoritative()).To(BeTrue())
		Expect(closed.Acknowledgement.Outcome.ExitCode).To(Equal(127))
	})

	It("leaves a started task unresolved when main's exit and the launcher's record disagree", func() {
		done := start()
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		terminate(3, launcherRecord(4))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(events.all()).ToNot(ContainElement("node-outcome"))
		Expect(jobExists()).To(BeTrue(), "the Job of a started execution with no outcome was deleted")
	})

	It("reports an abort after the start as unresolved and keeps the Job when the command is not shown to stop", func() {
		// No kubelet acts on the stop here: the pod's command never ends.
		waitCtx, abort := context.WithCancel(ctx)
		defer abort()
		_, done := startWith(waitCtx)
		Expect(fetch("tok-main")).To(Equal(http.StatusOK))
		abort()
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(finished.err).To(MatchError(context.Canceled), "the step's own cancellation is not reported")
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(jobExists()).To(BeTrue())
	})

	It("never records a start after an aborted step's Job was deleted", func() {
		waitCtx, abort := context.WithCancel(ctx)
		defer abort()
		process, done := startWith(waitCtx)
		pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())

		abort()
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(context.Canceled))
		Expect(jobExists()).To(BeFalse(), "an aborted step that never started kept its Job")

		// The envelope went with the step, so only the gate itself can be
		// asked: it is shut, and nothing reaches the ledger.
		Expect(process.exact.beforeServe(ctx, pod)).To(MatchError(errExactJobClosed))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
		Expect(events.all()).To(BeEmpty())
	})

	// The node committed the start and its answer never reached this web,
	// so nothing in memory says the command may be running: the ledger does.
	loseTheStartAnswer := func() {
		ordered.mu.Lock()
		ordered.loseStartAnswers = 1
		ordered.mu.Unlock()
		Expect(fetch("tok-main")).To(Equal(http.StatusServiceUnavailable))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
	}

	It("keeps the Job of an aborted step whose start the node holds and the web never heard", func() {
		waitCtx, abort := context.WithCancel(ctx)
		defer abort()
		_, done := startWith(waitCtx)
		loseTheStartAnswer()

		abort()
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(finished.err).To(MatchError(context.Canceled))
		Expect(jobExists()).To(BeTrue(), "the Job of an execution the node holds as executing was deleted")
	})

	It("keeps the Job of a step whose pod failed after a start the web never heard", func() {
		done := start()
		loseTheStartAnswer()
		terminate(1, "")
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
		Expect(jobExists()).To(BeTrue(), "the Job of an execution the node holds as executing was deleted")
	})

	It("does not serve a pod whose command a stop has already ended", func() {
		done := start()
		loseTheStartAnswer()
		// A Run's cancellation found the node's start and stopped the pod.
		Expect(stopStepJobPod(ctx, clientset, "test-ns", mainPod, types.UID("uid-"+mainPod))).To(Succeed())
		Expect(fetch("tok-main")).To(Equal(http.StatusConflict), "the launcher's retry was served after its pod was stopped")
		terminate(143, "")
		Eventually(done, 15*time.Second).Should(Receive())
	})

	It("keeps the Job of a start recorded while the step was being aborted", func() {
		waitCtx, abort := context.WithCancel(ctx)
		defer abort()
		process, done := startWith(waitCtx)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		process.exact.beforeStart = func() {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
		served := make(chan int, 1)
		go func() {
			defer GinkgoRecover()
			served <- fetch("tok-main")
		}()
		Eventually(entered, 10*time.Second).Should(BeClosed())

		// The abort's cleanup runs while the gate is inside: it waits.
		abort()
		Consistently(done, 300*time.Millisecond).ShouldNot(Receive(), "Wait decided on the Job with a start in flight")
		close(release)

		Expect(<-served).To(BeElementOf(http.StatusOK, http.StatusServiceUnavailable))
		var finished waited
		Eventually(done, 15*time.Second).Should(Receive(&finished))
		Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
		Expect(jobExists()).To(BeTrue(), "a recorded start's Job was deleted")
		Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
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
			handle:   "pause-handle",
			podName:  "pause-pod",
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
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

	// The kubelet's part in a stop: once the pod's deadline is cut, it ends
	// the containers and the launcher records the command's exit.
	kubeletEndsOnDeadline := func(name string, exit int32) {
		Eventually(func() *int64 {
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, name, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			return pod.Spec.ActiveDeadlineSeconds
		}, 10*time.Second, 20*time.Millisecond).ShouldNot(BeNil(), "the stop never reached the pod")
		terminatePod(name, exit, launcherRecord(int(exit)))
	}

	// outputSource is a Run's view of the node, as a web with no step in
	// hand has it: mTLS to the node's daemon.
	outputSource := func() *OutputSource {
		endpoint, err := url.Parse(harness.Endpoint)
		Expect(err).ToNot(HaveOccurred())
		port, err := strconv.Atoi(endpoint.Port())
		Expect(err).ToNot(HaveOccurred())
		config := NewConfig("test-ns", "")
		config.OutputPlaneEnabled = true
		config.ArtifactDaemonPort = port
		config.ArtifactDaemonTLSEnabled = true
		config.ArtifactDaemonTLSCert = harness.PKI.clientCert
		config.ArtifactDaemonTLSKey = harness.PKI.clientKey
		config.ArtifactDaemonTLSCACert = harness.PKI.caCert
		return NewOutputSource(clientset, config, harness.Signer)
	}

	Describe("stopping", func() {
		It("stops an aborted task through the node and the pod's deadline, and records how it ended", func() {
			clientset.PrependReactor("patch", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
				events.add("pod-deadline")
				return false, nil, nil
			})
			waitCtx, abort := context.WithCancel(ctx)
			defer abort()
			_, done := startWith(waitCtx)
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))

			abort()
			kubeletEndsOnDeadline(mainPod, 143)
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(finished.result.ExitStatus).To(Equal(143))

			// The node is asked first, then the pod: a stop the node never
			// accepted must not end a command the node still holds as running.
			Expect(events.all()).To(ContainElements("node-stop", "pod-deadline"))
			Expect(indexOf(events.all(), "node-stop")).To(BeNumerically("<", indexOf(events.all(), "pod-deadline")))
			stopped, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(*stopped.Spec.ActiveDeadlineSeconds).To(BeNumerically("<=", 2), "the deadline is not what the pod had run")

			closed := classify()
			Expect(closed.Classification.Authoritative()).To(BeTrue())
			Expect(closed.Acknowledgement.Outcome.ExitCode).To(Equal(143))
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred(), "the stop deleted the pod that witnesses the outcome")
			Expect(pod.DeletionTimestamp).To(BeNil())
			Expect(jobExists()).To(BeTrue())
		})

		It("interrupts and recovers a started task for a Run's cancellation, from another web", func() {
			done := start()
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			start := recorded()[0]
			source := outputSource()

			_, err := source.RecoverExecutionOutcome(ctx, "node-1", start)
			Expect(err).To(MatchError(output.ErrUnresolved), "a running command was recovered")
			Expect(source.InterruptExecution(ctx, "node-1", start)).To(Succeed())
			kubeletEndsOnDeadline(mainPod, 143)

			recovered, err := source.RecoverExecutionOutcome(ctx, "node-1", start)
			Expect(err).ToNot(HaveOccurred())
			Expect(recovered.Classification.Authoritative()).To(BeTrue())
			Expect(recovered.Acknowledgement.Outcome.ExitCode).To(Equal(143))

			// The step's own Wait reads the same exit and agrees with the node.
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(finished.result.ExitStatus).To(Equal(143))
		})

		It("leaves a stopped task unresolved when its launcher leaves no record", func() {
			waitCtx, abort := context.WithCancel(ctx)
			defer abort()
			_, done := startWith(waitCtx)
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			abort()
			Eventually(func() *int64 {
				pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				return pod.Spec.ActiveDeadlineSeconds
			}, 10*time.Second, 20*time.Millisecond).ShouldNot(BeNil())
			// Killed before it could write anything.
			terminate(137, "")
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
			Expect(finished.err).To(MatchError(context.Canceled))
			Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
			Expect(jobExists()).To(BeTrue())
		})

		It("reports the outcome the node already holds when an aborted task had ended", func() {
			waitCtx, abort := context.WithCancel(ctx)
			defer abort()
			_, done := startWith(waitCtx)
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			_, err := harness.Client.RecordOutcome(ctx, identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 6})
			Expect(err).ToNot(HaveOccurred())
			abort()
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(finished.result.ExitStatus).To(Equal(6))
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pod.Spec.ActiveDeadlineSeconds).To(BeNil(), "an ended command's pod was stopped")
			Expect(events.all()).ToNot(ContainElement("node-stop"))
		})

		DescribeTable("leaves a Run's recovery unresolved or refused when the start's pod is not there to read",
			func(lose func(), want error) {
				done := start()
				Expect(fetch("tok-main")).To(Equal(http.StatusOK))
				start := recorded()[0]
				lose()
				_, err := outputSource().RecoverExecutionOutcome(ctx, "node-1", start)
				Expect(err).To(MatchError(want))
				Expect(classify().Classification).To(Equal(executioncontrol.ClassificationExecuting))
				if _, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{}); err == nil {
					terminate(0, launcherRecord(0))
				}
				Eventually(done, 15*time.Second).Should(Receive())
			},
			Entry("the pod is gone", func() {
				Expect(clientset.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "test-ns", mainPod)).To(Succeed())
			}, output.ErrUnresolved),
			Entry("the pod is another Job's", func() {
				pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.OwnerReferences[0].UID = "uid-another-job"
				_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())
			}, output.ErrInvalidIdentity),
		)
	})

	Describe("attaching", func() {
		It("answers an exact task from the exit the node acknowledged, never from the pod alone", func() {
			first := run()
			first.exact.admit(ctx, mainPod)
			Expect(first.exact.awaitAdmission(ctx)).To(Succeed())
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			terminate(7, launcherRecord(7))

			// A new web: the pod has an exit and nothing acknowledged it.
			container = newWeb()
			_, err := container.Attach(ctx, "exact-job-handle", runtime.ProcessIO{})
			Expect(err).To(HaveOccurred(), "an unacknowledged exit was reported")

			// The annotation is written only once the node holds the outcome.
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, mainPod, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Annotations = map[string]string{exitStatusAnnotationKey: "7"}
			_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			container = newWeb()
			process, err := container.Attach(ctx, "exact-job-handle", runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())
			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(7))
		})
	})

	Describe("after a web restart", func() {
		// The web that made the Job is gone; this one is new, with nothing in
		// memory of it.
		restartWeb := func() <-chan waited {
			container = newWeb()
			process := run()
			Expect(process.exact.reattached).To(BeTrue(), "the new web made a Job rather than resuming the one there")
			done := make(chan waited, 1)
			go func() {
				defer GinkgoRecover()
				result, err := process.Wait(ctx)
				done <- waited{result, err}
			}()
			return done
		}
		// admitOnly is the first web admitting its pod and dying before it
		// follows anything.
		admitOnly := func() {
			first := run()
			first.exact.admit(ctx, mainPod)
			Expect(first.exact.awaitAdmission(ctx)).To(Succeed())
		}
		jobUID := func() types.UID {
			job, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			return job.UID
		}

		It("serves the start of a Job a web before it made and never started", func() {
			run()
			before := jobUID()
			done := restartWeb()
			Eventually(func() int { return fetch("tok-main") }, 10*time.Second, 50*time.Millisecond).Should(Equal(http.StatusOK))
			terminate(0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(jobUID()).To(Equal(before), "the Job was replaced rather than resumed")
		})

		It("unsuspends a Job a web before it made and died before unsuspending", func() {
			job, err := container.buildStepJob(runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "exit 3"}}, true)
			Expect(err).ToNot(HaveOccurred())
			_, err = clientset.BatchV1().Jobs("test-ns").Create(ctx, job, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
			done := restartWeb()
			Eventually(func() bool {
				resumed, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				return resumed.Spec.Suspend != nil && *resumed.Spec.Suspend
			}, 10*time.Second, 50*time.Millisecond).Should(BeFalse(), "the resumed Job is still suspended")
			Eventually(func() int { return fetch("tok-main") }, 10*time.Second, 50*time.Millisecond).Should(Equal(http.StatusOK))
			terminate(0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
		})

		It("follows a command a web before it started, on the ledger's replay of its start", func() {
			admitOnly()
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			done := restartWeb()
			Consistently(done, 300*time.Millisecond).ShouldNot(Receive(), "a running command was answered without its exit")
			terminate(5, launcherRecord(5))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(finished.result.ExitStatus).To(Equal(5))
			Expect(classify().Acknowledgement.Outcome.ExitCode).To(Equal(5))
			Expect(ordered.distinctStarts()).To(Equal(1))
		})

		It("reports the outcome the node holds without following anything", func() {
			admitOnly()
			Expect(fetch("tok-main")).To(Equal(http.StatusOK))
			_, err := harness.Client.RecordOutcome(ctx, identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 4})
			Expect(err).ToNot(HaveOccurred())
			done := restartWeb()
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(finished.result.ExitStatus).To(Equal(4))
			Expect(recorded()[len(recorded())-1].Kind).To(Equal(executioncontrol.AcknowledgementFinish), "the Run did not retain the node's outcome")
		})

		It("leaves unresolved a Job whose start the ledger holds for another process", func() {
			_, err := harness.Client.Admit(ctx, executioncontrol.Envelope{
				ProtocolVersion: executioncontrol.ProtocolVersion, Identity: identity,
				NodeUID: executioncontrol.NodeUID(harnessNodeUID), Capability: "base-capability",
			})
			Expect(err).ToNot(HaveOccurred())
			run()
			_, err = harness.Client.RecordStart(ctx, identity, executioncontrol.PodUID("uid-"+mainPod), "supervisor-v1:/tmp/jb-exact-elsewhere")
			Expect(err).ToNot(HaveOccurred())
			done := restartWeb()
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
			Expect(fetch("tok-main")).ToNot(Equal(http.StatusOK))
			Expect(jobExists()).To(BeTrue())
		})
	})

	Describe("a pod lost before its start", func() {
		evict := func(name string) {
			pod, err := clientset.CoreV1().Pods("test-ns").Get(ctx, name, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "Evicted"
			pod.Status.ContainerStatuses = nil
			_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		It("is replaced once, and the replacement's pod starts the command", func() {
			done := start()
			evict(mainPod)
			Eventually(func() int { return fetch("tok-2") }, 10*time.Second, 50*time.Millisecond).Should(Equal(http.StatusOK))
			Expect(recorded()[0].ProcessIdentity).To(Equal(executioncontrol.ProcessIdentity("job-v1:uid-" + jobName + "-2")))
			terminatePod(nthJobPod(jobName, 2), 0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
		})

		It("is not replaced a second time", func() {
			done := start()
			evict(mainPod)
			Eventually(func() error {
				_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 2), metav1.GetOptions{})
				return err
			}, 10*time.Second).Should(Succeed())
			evict(nthJobPod(jobName, 2))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(HaveOccurred())
			_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 3), metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a third Job was made")
			Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted))
		})

		It("is never replaced once the node holds a start, even one the web never heard", func() {
			done := start()
			// Nothing in this web's memory says the command started; only the
			// node's classification can stop the replacement.
			loseTheStartAnswer()
			evict(mainPod)
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(MatchError(ErrExactOutcomeUnresolved))
			_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 2), metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a started command's pod was replaced")
			Expect(jobExists()).To(BeTrue())
		})

		It("is not replaced while a capture holds its source", func() {
			container.config.OutputPlaneEnabled = true
			container.captureClass = &heldClassifier{class: captureClassHeld}
			done := start()
			evict(mainPod)
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).To(HaveOccurred())
			_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 2), metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a capture-held step got a new pod")
		})
	})

	Describe("a pod its node cannot take", func() {
		var (
			other     *outputDaemonHarness
			relocated []([]string)
			refuse    error
		)
		const otherNodeUID = "harness-node-2"

		BeforeEach(func() {
			previousGrace := relocationGrace
			relocationGrace = 100 * time.Millisecond
			DeferCleanup(func() { relocationGrace = previousGrace })

			started, err := startOutputDaemonForNode(true, otherNodeUID)
			Expect(err).ToNot(HaveOccurred())
			other = started
			DeferCleanup(other.Stop)
			_, err = clientset.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2", UID: otherNodeUID}}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Pinned to node-1, where the scheduler cannot place it; a move
			// lands it on node-2, which takes it.
			container.containerSpec.ExecutionControl.Node = &runtime.ExecutionNode{Name: "node-1", UID: harnessNodeUID}
			container.outputControls = nodeControls{"node-1": ordered, "node-2": other.Client}
			podFor = func(n int, job *batchv1.Job) *corev1.Pod {
				if n == 1 {
					return unschedulableJobPod(nthJobPod(jobName, 1))
				}
				return runningJobPod(nthJobPod(jobName, n), "node-2")
			}
			relocated, refuse = nil, nil
			container.relocate = func(_ context.Context, exclude []string) (runtime.ContainerSpec, error) {
				relocated = append(relocated, exclude)
				if refuse != nil {
					return runtime.ContainerSpec{}, refuse
				}
				spec := container.containerSpec
				control := *spec.ExecutionControl
				control.Node = &runtime.ExecutionNode{Name: "node-2", UID: otherNodeUID}
				control.Endpoint = other.Endpoint
				spec.ExecutionControl = &control
				return spec, nil
			}
		})

		It("moves the step to another node before anything starts, and starts it there", func() {
			process := run()
			done := make(chan waited, 1)
			go func() {
				defer GinkgoRecover()
				result, err := process.Wait(ctx)
				done <- waited{result, err}
			}()
			Eventually(func() int { return fetch("tok-2") }, 10*time.Second, 50*time.Millisecond).Should(Equal(http.StatusOK))
			Expect(relocated).To(HaveLen(1))
			Expect(classify().Classification).To(Equal(executioncontrol.ClassificationNeverStarted), "the old node holds a start")
			moved, err := other.Client.Classify(ctx, identity)
			Expect(err).ToNot(HaveOccurred())
			Expect(moved.Classification).To(Equal(executioncontrol.ClassificationExecuting))

			terminatePod(nthJobPod(jobName, 2), 0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
		})

		It("puts a refused move back where it was and spends the relocations", func() {
			refuse = errors.New("no other node fits")
			podFor = func(n int, job *batchv1.Job) *corev1.Pod { return unschedulableJobPod(nthJobPod(jobName, n)) }
			process := run()
			done := make(chan waited, 1)
			go func() {
				defer GinkgoRecover()
				result, err := process.Wait(ctx)
				done <- waited{result, err}
			}()
			Eventually(func() error {
				_, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 2), metav1.GetOptions{})
				return err
			}, 10*time.Second).Should(Succeed(), "the refused move did not put the Job back")
			Consistently(func() int { return len(relocated) }, 500*time.Millisecond).Should(Equal(1), "a spent relocation was tried again")

			// The node frees up: the Job put back is scheduled where it was
			// admitted, and starts there.
			back, err := clientset.CoreV1().Pods("test-ns").Get(ctx, nthJobPod(jobName, 2), metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			scheduled := runningJobPod(back.Name, "node-1")
			back.Spec.NodeName, back.Status = scheduled.Spec.NodeName, scheduled.Status
			_, err = clientset.CoreV1().Pods("test-ns").Update(ctx, back, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() int { return fetch("tok-2") }, 10*time.Second, 50*time.Millisecond).Should(Equal(http.StatusOK))
			terminatePod(back.Name, 0, launcherRecord(0))
			var finished waited
			Eventually(done, 15*time.Second).Should(Receive(&finished))
			Expect(finished.err).ToNot(HaveOccurred())
			Expect(classify().Classification.Authoritative()).To(BeTrue())
		})

		It("does not move a pod that was bound and admitted", func() {
			podFor = func(n int, job *batchv1.Job) *corev1.Pod { return runningJobPod(nthJobPod(jobName, n), "node-1") }
			process := run()
			process.exact.admit(ctx, mainPod)
			Expect(process.exact.awaitAdmission(ctx)).To(Succeed())
			before, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			Expect(process.relocateExactJob(ctx)).To(Succeed())
			Expect(relocated).To(BeEmpty(), "an admitted pod's execution was moved")
			Expect(process.relocationSpent).To(BeTrue())
			after, err := clientset.BatchV1().Jobs("test-ns").Get(ctx, jobName, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(after.UID).To(Equal(before.UID))
			Expect(fetch("tok-main")).To(Equal(http.StatusOK), "the admitted pod's gate was shut")
		})
	})
})

func indexOf(values []string, value string) int {
	for i, v := range values {
		if v == value {
			return i
		}
	}
	return -1
}

// nodeControls is one output control per node name.
type nodeControls map[string]OutputControl

func (controls nodeControls) ForNode(_ context.Context, node string) (OutputControl, error) {
	control, ok := controls[node]
	if !ok {
		return nil, fmt.Errorf("no output plane for node %s", node)
	}
	return control, nil
}

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
