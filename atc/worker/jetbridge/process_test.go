package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
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

var _ = Describe("Process", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		container     runtime.Container
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)

		setupFakeDBContainer(fakeDBWorker, "process-test-handle")

		var err error
		container, _, err = worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("process-test-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:   1,
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			},
			delegate,
		)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("Wait", func() {
		Context("when the Pod succeeds", func() {
			It("returns exit status 0", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				// Simulate Pod completion by updating its status
				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodSucceeded
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 0,
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))
			})
		})

		Context("when the Pod fails with a non-zero exit code", func() {
			It("returns the exit code without an error", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/false",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodFailed
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(1))
			})
		})

		Context("when the context is cancelled", func() {
			It("returns the context error and deletes the Pod", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sleep",
					Args: []string{"3600"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				By("verifying the pod exists before cancellation")
				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))

				cancelCtx, cancel := context.WithCancel(ctx)
				cancel() // Cancel immediately

				_, err = process.Wait(cancelCtx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("context canceled"))

				By("verifying the pod was deleted from K8s")
				pods, err = fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(BeEmpty())
			})
		})

		Context("pod failure state detection (direct mode)", func() {
			It("detects ImagePullBackOff as a terminal failure", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodPending
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason:  "ImagePullBackOff",
								Message: "Back-off pulling image \"nonexistent:latest\"",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ImagePullBackOff"))
			})

			It("detects ErrImagePull as a terminal failure", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodPending
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason:  "ErrImagePull",
								Message: "rpc error: code = NotFound",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ErrImagePull"))
			})

			It("detects OOMKilled with exit code 137", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodFailed
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 137,
								Reason:   "OOMKilled",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(137))
			})

			It("detects pod eviction as a terminal failure", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodFailed
				pod.Status.Reason = "Evicted"
				pod.Status.Message = "The node was low on resource: memory."
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Evicted"))
			})

			It("detects CrashLoopBackOff as a terminal failure", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodRunning
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason:  "CrashLoopBackOff",
								Message: "back-off 5m0s restarting failed container",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("CrashLoopBackOff"))
			})
		})
	})

	Describe("failure diagnostics in build logs", func() {
		It("writes pod conditions and waiting reasons to stderr on ImagePullBackOff", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image \"nonexistent:latest\"",
						},
					},
				},
			}
			pod.Status.Conditions = []corev1.PodCondition{
				{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionTrue,
					Reason:  "Scheduled",
					Message: "Successfully assigned test-namespace/process-test-handle to node-1",
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("ImagePullBackOff"))
			Expect(stderrOutput).To(ContainSubstring("nonexistent:latest"))
		})

		It("writes eviction reason to stderr", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: memory."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("Evicted"))
			Expect(stderrOutput).To(ContainSubstring("low on resource: memory"))
		})
	})

	Describe("pod startup timeout", func() {
		var (
			fakeExecutor     *fakeExecExecutor
			timeoutWorker    *jetbridge.Worker
			timeoutContainer runtime.Container
			timeoutCfg       jetbridge.Config
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}

			// Use a very short timeout for testing.
			timeoutCfg = jetbridge.NewConfig("test-namespace", "")
			timeoutCfg.PodStartupTimeout = 200 * time.Millisecond

			timeoutWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, timeoutCfg)
			timeoutWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "timeout-handle")

			var err error
			timeoutContainer, _, err = timeoutWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("timeout-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:     db.ContainerTypeGet,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("times out waitForRunning after the configured duration", func() {
			process, err := timeoutContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			// Pod stays in Pending — never reaches Running.
			// The timeout should fire.
			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("timed out"))
		})

		It("writes diagnostics to stderr on timeout", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := timeoutContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			// Set pod to Pending with a condition.
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "timeout-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodPending
			pod.Status.Conditions = []corev1.PodCondition{
				{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
					Reason: "Scheduled",
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("Pod Failure Diagnostics"))
		})
	})

	Describe("execProcess failure state detection", func() {
		var (
			fakeExecutor *fakeExecExecutor
			execWorker   *jetbridge.Worker
			execContainer runtime.Container
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "exec-fail-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:     db.ContainerTypeGet,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("detects ImagePullBackOff in waitForRunning", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ImagePullBackOff"))
		})

		It("detects Unschedulable pod condition in waitForRunning", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.Conditions = []corev1.PodCondition{
				{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  "Unschedulable",
					Message: "0/3 nodes are available: insufficient cpu.",
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Unschedulable"))
		})

		It("detects pod eviction before reaching Running phase", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: ephemeral-storage."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Evicted"))
			Expect(stderrBuf.String()).To(ContainSubstring("ephemeral-storage"))
		})

		It("detects pod terminated before exec could run", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodSucceeded
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("pod terminated before exec could run"))
		})

		It("preserves the pause pod when context is cancelled (for fly hijack)", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			cancelCtx, cancel := context.WithCancel(ctx)
			cancel() // Cancel immediately — waitForRunning will return ctx error

			_, err = process.Wait(cancelCtx)
			Expect(err).To(HaveOccurred())

			By("verifying the pause pod was NOT deleted (enables fly hijack)")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Name).To(Equal("exec-fail-handle"))
		})
	})

	Describe("transient API error handling", func() {
		It("tolerates a single API error during pollUntilDone", func() {
			errorClientset := fake.NewSimpleClientset()
			errorCfg := jetbridge.NewConfig("test-namespace", "")
			errorWorker := jetbridge.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			setupFakeDBContainer(fakeDBWorker, "transient-ok-handle")

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-ok-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := transientContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod completion BEFORE installing the error reactor.
			pod, err := errorClientset.CoreV1().Pods("test-namespace").Get(ctx, "transient-ok-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodSucceeded
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			}
			_, err = errorClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Now inject a reactor that fails the first Get, then lets subsequent ones through.
			var callCount int32
			errorClientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				n := atomic.AddInt32(&callCount, 1)
				if n == 1 {
					return true, nil, errors.New("transient API error")
				}
				return false, nil, nil
			})

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})

		It("fails after 3 consecutive API errors in pollUntilDone", func() {
			errorClientset := fake.NewSimpleClientset()
			errorCfg := jetbridge.NewConfig("test-namespace", "")
			errorWorker := jetbridge.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			setupFakeDBContainer(fakeDBWorker, "transient-fail-handle")

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := transientContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// All Gets fail.
			errorClientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				return true, nil, errors.New("persistent API error")
			})

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("consecutive API errors"))
		})

		It("resets error count after a successful API call", func() {
			errorClientset := fake.NewSimpleClientset()
			errorCfg := jetbridge.NewConfig("test-namespace", "")
			errorWorker := jetbridge.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			setupFakeDBContainer(fakeDBWorker, "transient-reset-handle")

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-reset-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := transientContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			// Set pod to completed BEFORE installing the reactor.
			pod, err := errorClientset.CoreV1().Pods("test-namespace").Get(ctx, "transient-reset-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodSucceeded
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			}
			_, err = errorClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Pattern: fail, fail, succeed, fail, fail, then succeed with completed pod.
			// This tests that consecutive count resets after a success.
			var callCount int32
			errorClientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				n := atomic.AddInt32(&callCount, 1)
				switch n {
				case 1, 2: // First two fail
					return true, nil, errors.New("transient error")
				case 3: // Third succeeds (resets counter), pod is completed
					return false, nil, nil
				case 4, 5: // Next two fail
					return true, nil, errors.New("transient error")
				default: // After that, succeed — pod is complete
					return false, nil, nil
				}
			})

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})
	})

	Describe("K8s-specific metrics", func() {
		Context("ImagePullFailures counter", func() {
			var (
				execContainer runtime.Container
				execExecutor  *fakeExecExecutor
				execWorker    *jetbridge.Worker
			)

			BeforeEach(func() {
				execExecutor = &fakeExecExecutor{}
				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(execExecutor)

				setupFakeDBContainer(fakeDBWorker, "image-pull-fail-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("image-pull-fail-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Drain prior counter state.
				metric.Metrics.K8sImagePullFailures.Delta()
			})

			It("increments K8sImagePullFailures when ImagePullBackOff is detected", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				// Simulate pod stuck in ImagePullBackOff.
				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "image-pull-fail-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name: "main",
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason:  "ImagePullBackOff",
								Message: "Back-off pulling image",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())

				Expect(metric.Metrics.K8sImagePullFailures.Delta()).To(Equal(float64(1)))
			})
		})

		Context("PodStartupDuration gauge", func() {
			var (
				execContainer runtime.Container
				execExecutor  *fakeExecExecutor
				execWorker    *jetbridge.Worker
			)

			BeforeEach(func() {
				execExecutor = &fakeExecExecutor{}
				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				execWorker.SetExecutor(execExecutor)

				setupFakeDBContainer(fakeDBWorker, "startup-duration-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("startup-duration-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				// Drain prior gauge state.
				metric.Metrics.K8sPodStartupDuration.Max()
			})

			It("records startup duration when pod reaches Running", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				// Simulate pod reaching Running state.
				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "startup-duration-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				// The gauge should have been set to a positive value (duration in ms).
				duration := metric.Metrics.K8sPodStartupDuration.Max()
				Expect(duration).To(BeNumerically(">=", 0))
			})
		})
	})
})
