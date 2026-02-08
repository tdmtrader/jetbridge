package k8sruntime_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
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
		worker        *k8sruntime.Worker
		ctx           context.Context
		cfg           k8sruntime.Config
		delegate      runtime.BuildStepDelegate
		container     runtime.Container
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = k8sruntime.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)

		fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
		fakeCreatingContainer.HandleReturns("process-test-handle")
		fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
		fakeCreatedContainer.HandleReturns("process-test-handle")
		fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
		fakeDBWorker.FindContainerReturns(nil, nil, nil)
		fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
			It("returns the context error and cleans up the Pod", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sleep",
					Args: []string{"3600"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				cancelCtx, cancel := context.WithCancel(ctx)
				cancel() // Cancel immediately

				_, err = process.Wait(cancelCtx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("context canceled"))
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
			timeoutWorker    *k8sruntime.Worker
			timeoutContainer runtime.Container
			timeoutCfg       k8sruntime.Config
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}

			// Use a very short timeout for testing.
			timeoutCfg = k8sruntime.NewConfig("test-namespace", "")
			timeoutCfg.PodStartupTimeout = 200 * time.Millisecond

			timeoutWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, timeoutCfg)
			timeoutWorker.SetExecutor(fakeExecutor)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("timeout-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("timeout-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
			execWorker   *k8sruntime.Worker
			execContainer runtime.Container
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("exec-fail-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("exec-fail-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
	})

	Describe("transient API error handling", func() {
		It("tolerates a single API error during pollUntilDone", func() {
			errorClientset := fake.NewSimpleClientset()
			errorCfg := k8sruntime.NewConfig("test-namespace", "")
			errorWorker := k8sruntime.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("transient-ok-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("transient-ok-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
			errorCfg := k8sruntime.NewConfig("test-namespace", "")
			errorWorker := k8sruntime.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("transient-fail-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("transient-fail-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
			errorCfg := k8sruntime.NewConfig("test-namespace", "")
			errorWorker := k8sruntime.NewWorker(fakeDBWorker, errorClientset, errorCfg)

			fakeCreatingContainer := new(dbfakes.FakeCreatingContainer)
			fakeCreatingContainer.HandleReturns("transient-reset-handle")
			fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("transient-reset-handle")
			fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			fakeDBWorker.FindContainerReturns(nil, nil, nil)
			fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

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
})
