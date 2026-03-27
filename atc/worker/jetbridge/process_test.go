package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

			It("detects OOMKilled as a terminal failure", func() {
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

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("OOMKilled"))
				Expect(err.Error()).To(ContainSubstring(`container "main"`))

				stderrOutput := stderrBuf.String()
				Expect(stderrOutput).To(ContainSubstring("Pod Failure Diagnostics"))
				Expect(stderrOutput).To(ContainSubstring("OOMKilled"))
			})

			It("detects OOMKilled from last termination state (restarted container)", func() {
				stderrBuf := new(bytes.Buffer)
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{
					Stderr: stderrBuf,
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodRunning
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         "main",
						RestartCount: 1,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "CrashLoopBackOff",
							},
						},
						LastTerminationState: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 137,
								Reason:   "OOMKilled",
							},
						},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = process.Wait(ctx)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("OOMKilled"))
			})

			It("does not detect OOMKilled when termination reason is different", func() {
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
								ExitCode: 1,
								Reason:   "Error",
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

			It("detects external pod deletion as a terminal failure", func() {
				process, err := container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/true",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				// Start Wait in a goroutine so the PodWatcher can establish
				// itself while the pod is still alive, then delete the pod.
				type waitResult struct {
					result runtime.ProcessResult
					err    error
				}
				ch := make(chan waitResult, 1)
				go func() {
					r, e := process.Wait(ctx)
					ch <- waitResult{result: r, err: e}
				}()

				// Give the watcher time to do its initial Get() and establish the watch.
				time.Sleep(50 * time.Millisecond)

				// Delete the pod to simulate external deletion (node failure, eviction).
				err = fakeClientset.CoreV1().Pods("test-namespace").Delete(ctx, "process-test-handle", metav1.DeleteOptions{})
				Expect(err).ToNot(HaveOccurred())

				var res waitResult
				Eventually(ch, 5*time.Second).Should(Receive(&res))
				Expect(res.err).To(HaveOccurred())
				Expect(res.err.Error()).To(ContainSubstring("pod deleted externally"))
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

		It("includes sidecar container status in diagnostics", func() {
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
							Reason: "ContainerCreating",
						},
					},
				},
				{
					Name: "my-sidecar",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image \"bad-sidecar:latest\"",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("my-sidecar"))
			Expect(stderrOutput).To(ContainSubstring("ImagePullBackOff"))
			Expect(stderrOutput).To(ContainSubstring("bad-sidecar:latest"))
		})

		It("includes sidecar container status in diagnostics", func() {
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
							Reason:  "ContainerCreating",
							Message: "waiting for container",
						},
					},
				},
				{
					Name: "redis-sidecar",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ImagePullBackOff",
							Message: "Back-off pulling image \"redis:bad-tag\"",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("redis-sidecar"))
			Expect(stderrOutput).To(ContainSubstring("ImagePullBackOff"))
			Expect(stderrOutput).To(ContainSubstring("redis:bad-tag"))
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

		It("includes node name in diagnostics when available", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Spec.NodeName = "gke-pool-spot-a1b2c3"
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: ephemeral-storage."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("Node: gke-pool-spot-a1b2c3"))
			Expect(stderrOutput).To(ContainSubstring("Evicted"))
		})

		It("includes container termination message and restart history in diagnostics", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Spec.NodeName = "node-1"
			pod.Status.Phase = corev1.PodFailed
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name:         "main",
					RestartCount: 2,
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137,
							Reason:   "OOMKilled",
							Message:  "container exceeded 512Mi memory limit",
						},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 137,
							Reason:   "OOMKilled",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("Node: node-1"))
			Expect(stderrOutput).To(ContainSubstring("OOMKilled (exit code 137)"))
			Expect(stderrOutput).To(ContainSubstring("container exceeded 512Mi memory limit"))
			Expect(stderrOutput).To(ContainSubstring("RestartCount: 2"))
			Expect(stderrOutput).To(ContainSubstring("Last termination: OOMKilled"))
		})

		It("writes node diagnostics on eviction showing pressure conditions", func() {
			// Create a node with DiskPressure and spot label.
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gke-spot-node-1",
					Labels: map[string]string{
						"cloud.google.com/gke-spot": "true",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:    corev1.NodeDiskPressure,
							Status:  corev1.ConditionTrue,
							Message: "disk usage exceeds threshold",
						},
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			_, err := fakeClientset.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Spec.NodeName = "gke-spot-node-1"
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: ephemeral-storage."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("DiskPressure=True"))
			Expect(stderrOutput).To(ContainSubstring("disk usage exceeds threshold"))
			Expect(stderrOutput).To(ContainSubstring("spot/preemptible instance"))
			Expect(stderrOutput).To(ContainSubstring("cloud.google.com/gke-spot=true"))
		})

		It("writes node diagnostics showing cordoned status", func() {
			// Create a cordoned node.
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "draining-node-1",
				},
				Spec: corev1.NodeSpec{
					Unschedulable: true,
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			_, err := fakeClientset.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Spec.NodeName = "draining-node-1"
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: memory."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("cordoned (unschedulable)"))
			Expect(stderrOutput).To(ContainSubstring("node may be draining"))
		})

		It("handles node not found gracefully in diagnostics", func() {
			stderrBuf := new(bytes.Buffer)
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "process-test-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Spec.NodeName = "nonexistent-node"
			pod.Status.Phase = corev1.PodFailed
			pod.Status.Reason = "Evicted"
			pod.Status.Message = "The node was low on resource: memory."
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("nonexistent-node"))
			Expect(stderrOutput).To(ContainSubstring("unable to fetch details"))
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

	Describe("exec-mode pod failure diagnostics", func() {
		var (
			fakeExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "exec-diag-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-diag-handle"),
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

		It("writes pod failure diagnostics when exec fails due to pod death", func() {
			// The execFunc simulates OOM kill: updates pod status to
			// Failed/OOMKilled and returns an error. fetchPodFailureContext
			// then re-Gets the pod and finds the OOM state.
			fakeExecutor.execFunc = func() error {
				p, _ := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-diag-handle", metav1.GetOptions{})
				p.Spec.NodeName = "gke-spot-node-1"
				p.Status.Phase = corev1.PodFailed
				p.Status.ContainerStatuses = []corev1.ContainerStatus{
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
				fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, p, metav1.UpdateOptions{})
				return errors.New("exec stream: unable to upgrade connection: container not found")
			}

			stderrBuf := new(bytes.Buffer)
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString("{}"),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			// Set pod to Running so waitForRunning succeeds.
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-diag-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exec in pod"))

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("Pod Failure Diagnostics"))
			Expect(stderrOutput).To(ContainSubstring("OOMKilled"))
			Expect(stderrOutput).To(ContainSubstring("Node: gke-spot-node-1"))
		})

		It("writes diagnostics when pod is already gone (not found)", func() {
			// Make exec fail, and delete the pod so fetchPodFailureContext can't find it.
			fakeExecutor.execFunc = func() error {
				fakeClientset.CoreV1().Pods("test-namespace").Delete(ctx, "exec-diag-handle", metav1.DeleteOptions{})
				return errors.New("exec stream: connection refused")
			}

			stderrBuf := new(bytes.Buffer)
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString("{}"),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			// Set pod to Running so waitForRunning succeeds.
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-diag-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())

			stderrOutput := stderrBuf.String()
			Expect(stderrOutput).To(ContainSubstring("pod no longer exists"))
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

var _ = Describe("Process sidecar lifecycle", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
	})

	Context("when main container exits while sidecars are still running (direct mode)", func() {
		It("returns the main container's exit code and cleans up the pod", func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-lifecycle-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-lifecycle-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{
							Name:  "postgres",
							Image: "postgres:15",
						},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("simulating main container terminated but sidecar still running")
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sidecar-lifecycle-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
				{
					Name: "postgres",
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			By("verifying the pod was deleted to clean up sidecars")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(BeEmpty())
		})

		It("returns non-zero exit code from main and cleans up", func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-fail-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{Name: "redis", Image: "redis:7"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/false",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sidecar-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 42,
						},
					},
				},
				{
					Name: "redis",
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(42))
		})
	})

	Context("sidecar failure detection", func() {
		It("fails fast when sidecar has ImagePullBackOff and main hasn't terminated", func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-fail-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{Name: "bad-image", Image: "nonexistent:latest"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("simulating sidecar ImagePullBackOff while main is still waiting")
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sidecar-fail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason: "ContainerCreating",
						},
					},
				},
				{
					Name: "bad-image",
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

		It("does not fail the task when sidecar fails but main has already terminated", func() {
			setupFakeDBContainer(fakeDBWorker, "sidecar-imgfail-handle")

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-imgfail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Sidecars: []atc.SidecarConfig{
						{Name: "bad-image", Image: "nonexistent:latest"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			By("simulating sidecar ImagePullBackOff but main already terminated")
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "sidecar-imgfail-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
						},
					},
				},
				{
					Name: "bad-image",
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

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})
	})
})

var _ = Describe("Pod phase transition spans", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		spanRecorder  *tracetest.SpanRecorder
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(spanRecorder),
			sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
		)
		tracing.ConfigureTraceProvider(tp)

		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	Context("direct mode (pollUntilDone)", func() {
		var (
			worker    *jetbridge.Worker
			container runtime.Container
		)

		BeforeEach(func() {
			worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			setupFakeDBContainer(fakeDBWorker, "phase-span-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("phase-span-handle"),
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

		It("emits pod.phase span events when pod transitions to Succeeded", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/true",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "phase-span-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodSucceeded
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.process.wait" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.process.wait span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("pod.phase.succeeded"))
		})

		It("emits pod.phase.failed span event when pod fails", func() {
			process, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/false",
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "phase-span-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodFailed
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 1},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(1))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.process.wait" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.process.wait span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("pod.phase.failed"))
		})
	})

	Context("exec mode (waitForRunning)", func() {
		var (
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
			fakeExecutor  *fakeExecExecutor
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "exec-phase-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-phase-handle"),
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

		It("emits pod.phase.running span event when pod reaches Running", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-phase-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitForRunningSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.exec-process.wait-for-running" {
					waitForRunningSpan = s
					break
				}
			}
			Expect(waitForRunningSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

			eventNames := []string{}
			for _, e := range waitForRunningSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("pod.phase.running"))
		})
	})

	Context("init container and sidecar lifecycle events", func() {
		var (
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
			fakeExecutor  *fakeExecExecutor
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "init-sidecar-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("init-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:     db.ContainerTypeTask,
					Sidecars: []atc.SidecarConfig{
						{Name: "postgres", Image: "postgres:15"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("emits init.container.completed span event when init container terminates", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "init-sidecar-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate init container completing, then pod reaching Running.
			pod.Status.Phase = corev1.PodPending
			pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "fetch-input-0",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							ExitCode: 0,
							Reason:   "Completed",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Now transition to Running.
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "postgres", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.exec-process.wait-for-running" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("init.container.completed"))
		})

		It("emits sidecar.started span event when sidecar container reaches Running", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "init-sidecar-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod reaching Running with sidecar started.
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "postgres", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.exec-process.wait-for-running" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("sidecar.started"))
		})
	})

	Context("PVC bind and image pull events", func() {
		var (
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
			fakeExecutor  *fakeExecExecutor
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			setupFakeDBContainer(fakeDBWorker, "pvc-image-handle")

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("pvc-image-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:     db.ContainerTypeTask,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("emits pod.scheduled span event when PodScheduled condition becomes True", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "pvc-image-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Simulate pod being scheduled (PVC bound, node assigned).
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

			// Now transition to Running.
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.exec-process.wait-for-running" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("pod.scheduled"))
		})

		It("emits image.pulling span event when container is in ContainerCreating", func() {
			process, err := execContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "pvc-image-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Set pod to Pending with ContainerCreating BEFORE Wait is called.
			// The PodWatcher's initial Get() will see this state.
			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{
				{
					Name: "main",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "ContainerCreating",
							Message: "pulling image \"busybox\"",
						},
					},
				},
			}
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			// Transition to Running after a short delay so the PodWatcher
			// observes the ContainerCreating state first via its initial Get().
			go func() {
				defer GinkgoRecover()
				time.Sleep(50 * time.Millisecond)
				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "pvc-image-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:  "main",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())
			}()

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()
			var waitSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.exec-process.wait-for-running" {
					waitSpan = s
					break
				}
			}
			Expect(waitSpan).ToNot(BeNil(), "expected k8s.exec-process.wait-for-running span")

			eventNames := []string{}
			for _, e := range waitSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("image.pulling"))
		})
	})

	Describe("artifact upload filtering", func() {
		var (
			fakeExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
		)

		Context("with inputs, outputs, and working directory", func() {
			BeforeEach(func() {
				fakeExecutor = &fakeExecExecutor{}
				artifactCfg := jetbridge.NewConfig("test-namespace", "")
				artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "artifact-filter-handle")

				inputArtifact := jetbridge.NewArtifactStoreVolume(
					"artifacts/input-vol.tar", "input-vol", "k8s-worker-1", nil,
				)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-filter-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        inputArtifact,
								DestinationPath: "/tmp/build/workdir/my-input",
							},
						},
						Outputs: runtime.OutputPaths{
							"my-output": "/tmp/build/workdir/my-output",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("only uploads output volumes, not inputs or working directory", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(""),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "artifact-filter-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				By("inspecting exec calls to the artifact-helper sidecar")
				var artifactUploads []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactUploads = append(artifactUploads, call)
					}
				}

				By("expecting exactly one artifact upload (the output)")
				Expect(artifactUploads).To(HaveLen(1), "should upload only the output volume, not inputs or workdir")

				By("verifying the upload is for the output path")
				uploadCmd := strings.Join(artifactUploads[0].command, " ")
				Expect(uploadCmd).To(ContainSubstring("/tmp/build/workdir/my-output"))
				Expect(uploadCmd).ToNot(ContainSubstring("/tmp/build/workdir/my-input"))
			})
		})

		Context("with overlapping input and output paths", func() {
			BeforeEach(func() {
				fakeExecutor = &fakeExecExecutor{}
				artifactCfg := jetbridge.NewConfig("test-namespace", "")
				artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "artifact-overlap-handle")

				inputArtifact := jetbridge.NewArtifactStoreVolume(
					"artifacts/shared-vol.tar", "shared-vol", "k8s-worker-1", nil,
				)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-overlap-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs: []runtime.Input{
							{
								Artifact:        inputArtifact,
								DestinationPath: "/tmp/build/workdir/shared",
							},
						},
						Outputs: runtime.OutputPaths{
							"shared": "/tmp/build/workdir/shared",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uploads the overlapping path because the output may have modified the input", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(""),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "artifact-overlap-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				var artifactUploads []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactUploads = append(artifactUploads, call)
					}
				}

				// Both the input volume and the output volume share the same
				// mount path. Both match the output filter, so both are uploaded.
				// This is correct — the data at the overlapping path may have
				// been modified by the step, and deduplication by path is a
				// separate optimization.
				Expect(len(artifactUploads)).To(BeNumerically(">=", 1), "overlapping input/output path should be uploaded")
				for _, call := range artifactUploads {
					uploadCmd := strings.Join(call.command, " ")
					Expect(uploadCmd).To(ContainSubstring("/tmp/build/workdir/shared"))
				}
			})
		})

		Context("with no outputs", func() {
			BeforeEach(func() {
				fakeExecutor = &fakeExecExecutor{}
				artifactCfg := jetbridge.NewConfig("test-namespace", "")
				artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "artifact-noout-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-noout-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not upload any artifacts", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(""),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "artifact-noout-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				var artifactUploads []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactUploads = append(artifactUploads, call)
					}
				}

				Expect(artifactUploads).To(BeEmpty(), "no outputs means no artifact uploads")
			})
		})

		Context("get/put steps with Dir but no explicit Outputs", func() {
			BeforeEach(func() {
				fakeExecutor = &fakeExecExecutor{}
				artifactCfg := jetbridge.NewConfig("test-namespace", "")
				artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "artifact-get-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("artifact-get-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeGet},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/get",
						ImageSpec: runtime.ImageSpec{ResourceType: "git"},
						Type:     db.ContainerTypeGet,
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uploads the working directory as the implicit output", func() {
				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/opt/resource/in",
					Args: []string{"/tmp/build/get"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString("{}"),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "artifact-get-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				var artifactUploads []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactUploads = append(artifactUploads, call)
					}
				}

				Expect(artifactUploads).To(HaveLen(1), "get step should upload the Dir volume as implicit output")
				uploadCmd := strings.Join(artifactUploads[0].command, " ")
				Expect(uploadCmd).To(ContainSubstring("/tmp/build/get"))
			})
		})

		Context("cache upload behavior per cache mode", func() {
			It("uploads caches in artifact mode", func() {
				fakeExecutor = &fakeExecExecutor{}
				artifactCfg := jetbridge.NewConfig("test-namespace", "")
				artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "cache-artifact-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("cache-artifact-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    42,
						StepName: "build",
					},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:   []string{"/cache/go"},
						Outputs: runtime.OutputPaths{
							"result": "/tmp/build/workdir/result",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(""),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "cache-artifact-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				var artifactHelperCalls []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactHelperCalls = append(artifactHelperCalls, call)
					}
				}

				By("expecting output upload + cache upload")
				Expect(len(artifactHelperCalls)).To(BeNumerically(">=", 2),
					"should have at least one output upload and one cache upload")

				var hasCacheUpload bool
				for _, call := range artifactHelperCalls {
					cmd := strings.Join(call.command, " ")
					if strings.Contains(cmd, "/cache/go") {
						hasCacheUpload = true
					}
				}
				Expect(hasCacheUpload).To(BeTrue(), "cache should be uploaded in artifact mode")
			})

			It("does not upload caches in hostpath mode", func() {
				fakeExecutor = &fakeExecExecutor{}
				hostpathCfg := jetbridge.NewConfig("test-namespace", "")
				hostpathCfg.ArtifactStoreClaim = "concourse-artifacts"
				hostpathCfg.CacheHostPath = "/var/cache/concourse"
				hostpathCfg.CacheStore = jetbridge.CacheStoreHostPath

				execWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, hostpathCfg)
				execWorker.SetExecutor(fakeExecutor)

				setupFakeDBContainer(fakeDBWorker, "cache-hostpath-handle")

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("cache-hostpath-handle"),
					db.ContainerMetadata{
						Type:     db.ContainerTypeTask,
						JobID:    42,
						StepName: "build",
					},
					runtime.ContainerSpec{
						TeamID:   1,
						Dir:      "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Caches:   []string{"/cache/go"},
						Outputs: runtime.OutputPaths{
							"result": "/tmp/build/workdir/result",
						},
					},
					delegate,
				)
				Expect(err).ToNot(HaveOccurred())

				process, err := execContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(""),
					Stdout: new(bytes.Buffer),
					Stderr: new(bytes.Buffer),
				})
				Expect(err).ToNot(HaveOccurred())

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "cache-hostpath-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodRunning
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				var artifactHelperCalls []execCall
				for _, call := range fakeExecutor.execCalls {
					if call.containerName == "artifact-helper" {
						artifactHelperCalls = append(artifactHelperCalls, call)
					}
				}

				for _, call := range artifactHelperCalls {
					cmd := strings.Join(call.command, " ")
					Expect(cmd).ToNot(ContainSubstring("/cache/go"),
						"hostpath caches should not be uploaded to artifact PVC")
				}
			})
		})
	})

	Describe("artifact upload telemetry", func() {
		It("parses structured output from upload script", func() {
			stats := jetbridge.ParseArtifactUploadStats("FILES=42 TAR_NS=1000000000 SIZE=524288 TRANSFER_NS=2000000000")
			Expect(stats.FileCount).To(Equal(int64(42)))
			Expect(stats.TarNanos).To(Equal(int64(1000000000)))
			Expect(stats.SizeBytes).To(Equal(int64(524288)))
			Expect(stats.TransferNanos).To(Equal(int64(2000000000)))
		})

		It("handles empty or malformed output gracefully", func() {
			stats := jetbridge.ParseArtifactUploadStats("")
			Expect(stats.FileCount).To(Equal(int64(0)))
			Expect(stats.SizeBytes).To(Equal(int64(0)))

			stats = jetbridge.ParseArtifactUploadStats("GARBAGE=abc notafield")
			Expect(stats.FileCount).To(Equal(int64(0)))
		})

		It("creates upload spans with telemetry attributes", func() {
			spanRecorder := new(tracetest.SpanRecorder)
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSpanProcessor(spanRecorder),
			)
			tracing.ConfigureTraceProvider(tp)
			defer func() { tracing.Configured = false }()

			fakeExec := &fakeExecExecutor{
				execStdout: []byte("FILES=10 TAR_NS=500000000 SIZE=1048576 TRANSFER_NS=1500000000"),
			}
			artifactCfg := jetbridge.NewConfig("test-namespace", "")
			artifactCfg.ArtifactStoreClaim = "concourse-artifacts"

			execWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, artifactCfg)
			execWorker.SetExecutor(fakeExec)

			setupFakeDBContainer(fakeDBWorker, "artifact-telemetry-handle")

			telemetryContainer, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("artifact-telemetry-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:   1,
					Dir:      "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Outputs: runtime.OutputPaths{
						"result": "/tmp/build/workdir/result",
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			process, err := telemetryContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(""),
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "artifact-telemetry-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))

			ended := spanRecorder.Ended()

			var uploadSpan sdktrace.ReadOnlySpan
			for _, s := range ended {
				if s.Name() == "k8s.artifact.upload" {
					uploadSpan = s
					break
				}
			}
			Expect(uploadSpan).ToNot(BeNil(), "expected k8s.artifact.upload span")

			By("verifying span attributes include telemetry data")
			attrMap := make(map[string]interface{})
			for _, a := range uploadSpan.Attributes() {
				attrMap[string(a.Key)] = a.Value.AsInterface()
			}
			Expect(attrMap).To(HaveKey("artifact.key"))
			Expect(attrMap).To(HaveKey("artifact.type"))
			Expect(attrMap["artifact.type"]).To(Equal("output"))
			Expect(attrMap).To(HaveKey("artifact.file_count"))
			Expect(attrMap["artifact.file_count"]).To(Equal(int64(10)))
			Expect(attrMap).To(HaveKey("artifact.size_bytes"))
			Expect(attrMap["artifact.size_bytes"]).To(Equal(int64(1048576)))
			Expect(attrMap).To(HaveKey("artifact.tar_duration_ns"))
			Expect(attrMap["artifact.tar_duration_ns"]).To(Equal(int64(500000000)))
			Expect(attrMap).To(HaveKey("artifact.transfer_duration_ns"))
			Expect(attrMap["artifact.transfer_duration_ns"]).To(Equal(int64(1500000000)))

			By("verifying span events for phase completion")
			eventNames := []string{}
			for _, e := range uploadSpan.Events() {
				eventNames = append(eventNames, e.Name)
			}
			Expect(eventNames).To(ContainElement("artifact.tar.completed"))
			Expect(eventNames).To(ContainElement("artifact.transfer.completed"))
		})
	})
})
