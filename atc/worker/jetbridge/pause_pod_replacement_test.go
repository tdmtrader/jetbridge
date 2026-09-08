package jetbridge_test

// What a step does when the pause pod it was going to exec into is dead.
//
// There are two ways to find that out and they used to disagree: the pod going
// terminal before it ever reached Running (waitForRunning) recreated only for
// Failed, and the SPDY dial failing after it had been Running (the exec retry)
// recreated only for Succeeded. Each refused exactly the case the other took.
//
// The phase was never the signal. The reaper and a node drain stop a pause pod
// cleanly (Succeeded); an eviction or a preemption fails it with a Reason; an
// OOM of the pause process fails it too. On both paths the step's command has
// not started, so the pod is replaced once whatever the phase says — and never
// once the exec transport has carried a byte, because a second exec would run
// the command's side effects twice.
//
// Every spec below is written so that it fails under EITHER of the old
// one-phase rules.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("Replacing a dead pause pod", func() {
	const namespace = "test-namespace"

	var (
		ctx           context.Context
		logger        *lagertest.TestLogger
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		cfg           jetbridge.Config
		executor      *fakeExecExecutor
		worker        *jetbridge.Worker

		// podsCreated counts pause pods the runtime has created, which is how
		// every spec here tells a replacement from a refusal.
		podsCreated atomic.Int32
		// bornDead makes the NEXT pod the runtime creates arrive already
		// terminal, for the specs about a pod that dies twice.
		bornDead atomic.Bool
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("pause-pod")
		ctx = lagerctx.NewContext(context.Background(), logger)

		database := useJetbridgeDB()
		persisted, err := persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		dbWorker = persisted

		fakeClientset = fake.NewSimpleClientset()
		podsCreated.Store(0)
		bornDead.Store(false)

		// A pod the cluster brings up immediately, so a spec only has to say
		// how it dies. Mutating the object and falling through leaves the
		// tracker holding the pod as the kubelet would have left it.
		fakeClientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
			pod, ok := action.(k8stesting.CreateActionImpl).GetObject().(*corev1.Pod)
			if !ok {
				return false, nil, nil
			}
			podsCreated.Add(1)
			if bornDead.Swap(false) {
				pod.Status.Phase = corev1.PodSucceeded
			} else {
				pod.Status.Phase = corev1.PodRunning
			}
			return false, nil, nil
		})

		cfg = jetbridge.NewConfig(namespace, "")
		// Short enough that a spec which fails to replace the pod reports it
		// rather than sitting out the five-minute production default.
		cfg.PodStartupTimeout = 5 * time.Second
		cfg.PodSchedulingTimeout = 5 * time.Second

		executor = &fakeExecExecutor{}
		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		worker.SetExecutor(executor)
	})

	// A task step: no stdin, so nothing is read from the step's own streams
	// until the transport is up and the command is talking.
	runTaskStep := func(handle string) (runtime.Process, *bytes.Buffer) {
		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			},
			&noopDelegate{},
		)
		Expect(err).ToNot(HaveOccurred())

		stderr := new(bytes.Buffer)
		process, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh",
			Args: []string{"-c", "echo hello"},
		}, runtime.ProcessIO{Stdout: new(bytes.Buffer), Stderr: stderr})
		Expect(err).ToNot(HaveOccurred())
		Expect(podsCreated.Load()).To(BeEquivalentTo(1))
		return process, stderr
	}

	killPod := func(handle string, kill func(*corev1.Pod)) {
		GinkgoHelper()
		pods := fakeClientset.CoreV1().Pods(namespace)
		pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		kill(pod)
		_, err = pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}

	// The four ways a pause pod dies with the step's command still unstarted.
	// Succeeded and Failed both appear, which is what makes each row fail
	// under one of the two old rules.
	deaths := []struct {
		name string
		kill func(*corev1.Pod)
	}{
		{
			// The GC reaper collecting a previous check's pod, or a node
			// drain: the pause process traps TERM and exits 0.
			name: "reaped or drained (Succeeded)",
			kill: func(pod *corev1.Pod) { pod.Status.Phase = corev1.PodSucceeded },
		},
		{
			name: "evicted (Failed, Reason Evicted)",
			kill: func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodFailed
				pod.Status.Reason = "Evicted"
				pod.Status.Message = "The node was low on resource: memory"
			},
		},
		{
			name: "preempted (Failed, DisruptionTarget PreemptionByScheduler)",
			kill: func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodFailed
				pod.Status.Reason = "Preempted"
				pod.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue,
					Reason: "PreemptionByScheduler",
				}}
			},
		},
		{
			name: "the pause process OOM-killed (Failed, OOMKilled)",
			kill: func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodFailed
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
					Name: "main",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
					}},
				}}
			},
		},
	}

	Describe("when the pod dies before it ever reaches Running", func() {
		for _, death := range deaths {
			death := death

			It("replaces it and runs the step — "+death.name, func() {
				const handle = "pre-running-handle"
				process, _ := runTaskStep(handle)
				killPod(handle, death.kill)

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred(),
					"the step should have got a fresh pause pod, not the death of the old one")
				Expect(result.ExitStatus).To(Equal(0))
				Expect(podsCreated.Load()).To(BeEquivalentTo(2))
				Expect(executor.callCount()).To(Equal(1))
			})
		}

		It("logs the phase and reason it replaced the pod on", func() {
			const handle = "pre-running-logged-handle"
			process, _ := runTaskStep(handle)
			killPod(handle, deaths[1].kill) // evicted

			_, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())

			var replaced string
			for _, log := range logger.Logs() {
				if log.Message == "pause-pod.recreate-pause-pod.replacing-dead-pause-pod" {
					replaced = string(log.ToJSON())
				}
			}
			Expect(replaced).ToNot(BeEmpty(), "expected an info log for the replacement")
			Expect(replaced).To(ContainSubstring(`"phase":"Failed"`))
			Expect(replaced).To(ContainSubstring(`"reason":"Evicted"`))
		})

		It("replaces the pod only once, then reports the second death", func() {
			const handle = "twice-dead-handle"
			process, _ := runTaskStep(handle)
			bornDead.Store(true) // the replacement is collected on arrival too
			killPod(handle, deaths[1].kill)

			_, err := process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("pod terminated before exec could run"))
			Expect(podsCreated.Load()).To(BeEquivalentTo(2),
				"a pod that dies twice is not losing a race — it must not be replaced again")
			Expect(executor.callCount()).To(Equal(0))
		})

		It("does not replace a pod whose own init container failed", func() {
			const handle = "init-failed-handle"
			process, _ := runTaskStep(handle)
			// Succeeded, so this also pins that the new rule did not simply
			// become "always replace": the step's inputs could not be staged,
			// and a replacement would hide why while failing the same way.
			killPod(handle, func(pod *corev1.Pod) {
				pod.Status.Phase = corev1.PodSucceeded
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
					Name: "stage-inputs",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1, Reason: "Error",
					}},
				}}
			})

			_, err := process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("stage-inputs"))
			Expect(podsCreated.Load()).To(BeEquivalentTo(1))
		})
	})

	Describe("when the exec dial finds the pod gone", func() {
		// The pod was Running when the runtime checked, and the dial is what
		// discovers it is not any more — the race the retry loop exists for.
		// The dial never connected, so the step's command has not run.
		dieOnFirstDial := func(handle string, kill func(*corev1.Pod)) {
			var dials atomic.Int32
			executor.setExecFuncIO(func(_ io.Reader, _, _ io.Writer) error {
				if dials.Add(1) > 1 {
					return nil // the replacement pod takes the exec
				}
				killPod(handle, kill)
				return errors.New("exec stream: unable to upgrade connection: container not found")
			})
		}

		for _, death := range deaths {
			death := death

			It("replaces the pod and re-execs — "+death.name, func() {
				const handle = "dial-failed-handle"
				process, _ := runTaskStep(handle)
				dieOnFirstDial(handle, death.kill)

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred(),
					"a dial that never connected leaves the step unstarted — it must be retried")
				Expect(result.ExitStatus).To(Equal(0))
				Expect(podsCreated.Load()).To(BeEquivalentTo(2))
				Expect(executor.callCount()).To(Equal(2))
			})
		}

		It("writes the dead pod's diagnostics before replacing it", func() {
			const handle = "dial-diagnostics-handle"
			process, stderr := runTaskStep(handle)
			dieOnFirstDial(handle, deaths[3].kill) // OOM-killed

			_, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			// The replacement destroys the pod the user would have been shown,
			// so the post-mortem has to be written first or the reason for a
			// Failed pause pod is lost for good.
			Expect(stderr.String()).To(ContainSubstring("Pod Failure Diagnostics"))
			Expect(stderr.String()).To(ContainSubstring("OOMKilled"))
		})

		It("replaces the pod only once across both paths", func() {
			const handle = "dial-twice-handle"
			process, _ := runTaskStep(handle)

			var dials atomic.Int32
			executor.setExecFuncIO(func(_ io.Reader, _, _ io.Writer) error {
				dials.Add(1)
				killPod(handle, deaths[0].kill) // Succeeded, every time
				return errors.New("exec stream: unable to upgrade connection: container not found")
			})

			_, err := process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exec in pod"))
			Expect(podsCreated.Load()).To(BeEquivalentTo(2))
			Expect(dials.Load()).To(BeEquivalentTo(2),
				"the second dial failure has no replacement left to spend")
		})

		It("never replaces the pod once the command has started talking", func() {
			const handle = "already-streaming-handle"
			process, _ := runTaskStep(handle)

			executor.setExecFuncIO(func(_ io.Reader, stdout, _ io.Writer) error {
				// The command ran far enough to produce output. Whatever
				// killed the pod after that, re-execing would run its side
				// effects a second time.
				_, _ = stdout.Write([]byte("half of the deploy\n"))
				killPod(handle, deaths[0].kill)
				return errors.New("exec stream: unable to upgrade connection: container not found")
			})

			_, err := process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exec in pod"))
			Expect(podsCreated.Load()).To(BeEquivalentTo(1),
				"a step that has started must never be run a second time")
			Expect(executor.callCount()).To(Equal(1))
		})
	})
})
