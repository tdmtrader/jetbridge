package jetbridge_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("Process", func() {
	var (
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
		container     runtime.Container
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)

		var err error
		container, _, err = worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("process-test-handle"),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID:    1,
				Dir:       "/workdir",
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
				var interruption runtime.InterruptionError
				Expect(errors.As(err, &interruption)).To(BeTrue())
				Expect(interruption.InterruptionReason()).To(Equal(runtime.InterruptionEvicted))
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

			// Use very short timeouts for testing.
			timeoutCfg = jetbridge.NewConfig("test-namespace", "")
			timeoutCfg.PodStartupTimeout = 200 * time.Millisecond
			timeoutCfg.PodSchedulingTimeout = 200 * time.Millisecond

			timeoutWorker = jetbridge.NewWorker(dbWorker, fakeClientset, timeoutCfg)
			timeoutWorker.SetExecutor(fakeExecutor)

			var err error
			timeoutContainer, _, err = timeoutWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("timeout-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
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
			fakeExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
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

		It("waits for Unschedulable pod and times out", func() {
			// Use short timeouts so the test completes quickly.
			// PodSchedulingTimeout must be <= PodStartupTimeout since
			// waitForRunning extends the startup context to the scheduling
			// deadline when Unschedulable is detected.
			shortCfg := cfg
			shortCfg.PodSchedulingTimeout = 3 * time.Second
			shortCfg.PodStartupTimeout = 2 * time.Second
			shortWorker := jetbridge.NewWorker(dbWorker, fakeClientset, shortCfg)
			shortWorker.SetExecutor(&fakeExecExecutor{})

			shortContainer, _, err := shortWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-sched-timeout-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			stderrBuf := new(bytes.Buffer)
			process, err := shortContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-timeout-handle", metav1.GetOptions{})
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
			Expect(err.Error()).To(ContainSubstring("pod scheduling timeout"))
			Expect(err.Error()).To(ContainSubstring("Unschedulable"))
			Expect(stderrBuf.String()).To(ContainSubstring("waiting up to"))
			Expect(stderrBuf.String()).To(ContainSubstring("cluster resources"))
		})

		It("waits for Unschedulable pod and succeeds when scheduled", func() {
			shortCfg := cfg
			shortCfg.PodSchedulingTimeout = 30 * time.Second
			shortWorker := jetbridge.NewWorker(dbWorker, fakeClientset, shortCfg)
			shortWorker.SetExecutor(&fakeExecExecutor{})

			shortContainer, _, err := shortWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-sched-recover-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			stderrBuf := new(bytes.Buffer)
			process, err := shortContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/opt/resource/in",
				Args: []string{"/tmp/build/get"},
			}, runtime.ProcessIO{
				Stdin:  bytes.NewBufferString(`{}`),
				Stdout: new(bytes.Buffer),
				Stderr: stderrBuf,
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-recover-handle", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())

			// First: set the pod as Unschedulable.
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

			// After a short delay, simulate the pod getting scheduled and running.
			go func() {
				defer GinkgoRecover()
				time.Sleep(500 * time.Millisecond)

				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-recover-handle", metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				pod.Status.Phase = corev1.PodRunning
				pod.Status.Conditions = []corev1.PodCondition{
					{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					},
				}
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:  "main",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					},
				}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				// Then terminate the main container so Wait returns.
				time.Sleep(200 * time.Millisecond)
				// By now process.Wait may have already returned (the pod reached
				// Running above) and the spec may have ended, tearing down and
				// re-creating the shared fakeClientset in the next spec's
				// BeforeEach. If so, this Get returns NotFound — bail out rather
				// than UpdateStatus-ing an empty (name="") pod, which would fail
				// an assertion in this leaked goroutine and be misattributed to
				// whichever spec is running at the time.
				pod, err = fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-sched-recover-handle", metav1.GetOptions{})
				if err != nil {
					return
				}
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
			}()

			result, err := process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
			Expect(stderrBuf.String()).To(ContainSubstring("waiting up to"))
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
			var interruption runtime.InterruptionError
			Expect(errors.As(err, &interruption)).To(BeTrue())
			Expect(interruption.InterruptionReason()).To(Equal(runtime.InterruptionEvicted))
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

		// This is a get step: its command runs on the exec stream and dies
		// with it, so a cancelled context leaves nothing running and the pod
		// is worth keeping for fly hijack. Only supervised task steps, whose
		// command outlives the stream, are torn down (see the sibling spec
		// below and integration_test.go).
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

		// The same cancellation on a supervised task step, whose command was
		// started with SIGHUP ignored and so would otherwise keep running.
		It("deletes a supervised task's pause pod when context is cancelled", func() {
			taskContainer, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-task-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ImageURL: "busybox:latest"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			var deleteOptions []metav1.DeleteOptions
			fakeClientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
				deleteOptions = append(deleteOptions, action.(k8stesting.DeleteActionImpl).DeleteOptions)
				return false, nil, nil
			})

			// No Stdin: this is what makes the step supervised.
			process, err := taskContainer.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "trap '' TERM; sleep 600"},
			}, runtime.ProcessIO{Stdout: new(bytes.Buffer)})
			Expect(err).ToNot(HaveOccurred())

			cancelCtx, cancel := context.WithCancel(ctx)
			cancel()

			_, err = process.Wait(cancelCtx)
			Expect(err).To(HaveOccurred())

			By("verifying the abandoned pause pod was deleted, with no grace period")
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, "exec-task-handle", metav1.GetOptions{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the task's pause pod to be gone")

			Expect(deleteOptions).To(HaveLen(1))
			Expect(deleteOptions[0].GracePeriodSeconds).ToNot(BeNil())
			Expect(*deleteOptions[0].GracePeriodSeconds).To(BeEquivalentTo(0))
		})
	})

	DescribeTable("supervised gates the in-pod supervisor on container type and stdin (F18)",
		func(cType db.ContainerType, withStdin bool, wantSupervisor bool) {
			handle := fmt.Sprintf("supervised-%s-stdin-%v", cType, withStdin)
			fakeExecutor := &fakeExecExecutor{}
			execWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			c, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{Type: cType},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:      cType,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			var stdin io.Reader
			if withStdin {
				stdin = bytes.NewBufferString("{}")
			}

			process, err := c.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hi"},
			}, runtime.ProcessIO{
				Stdin:  stdin,
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())

			_, err = process.Wait(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
			command := fakeExecutor.execCalls[0].command
			if wantSupervisor {
				expectSupervisedExec(command, `'/bin/sh' '-c' 'echo hi'`)
			} else {
				Expect(command).To(Equal([]string{"/bin/sh", "-c", "echo hi"}))
			}
		},
		Entry("task, no stdin → supervised", db.ContainerTypeTask, false, true),
		Entry("get, no stdin → raw command", db.ContainerTypeGet, false, false),
		Entry("task with stdin → raw command", db.ContainerTypeTask, true, false),
	)

	Describe("exec-mode pod failure diagnostics", func() {
		var (
			fakeExecutor  *fakeExecExecutor
			execWorker    *jetbridge.Worker
			execContainer runtime.Container
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-diag-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
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

	Describe("severed-exec output-location recording (F23)", func() {
		var (
			fakeExecutor *fakeExecExecutor
			locator      *jetbridge.ArtifactLocator
			execWorker   *jetbridge.Worker
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)
			// The real DaemonSetBackend, writing into a real locator. What
			// F23 is about is the locator entry itself — a step whose entry
			// is missing or carries the wrong node makes flight ingestion
			// see sourceNode=="" and StreamOut fail instantly — so that is
			// what these tests read back.
			locator = jetbridge.NewArtifactLocator()
			execWorker.SetArtifactLocator(locator)
		})

		// makeContainer creates a container of the given type with a single
		// output volume, wired to the real storage backend so the
		// severed-exec output-publication path can be observed.
		makeContainer := func(handle string, cType db.ContainerType) runtime.Container {

			c, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{Type: cType},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:      cType,
					Outputs:   map[string]string{"out": "/workdir/out"},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			return c
		}

		// markPodRunning schedules the pause pod (created by Run) onto a node
		// and flips it to Running so Wait's waitForRunning proceeds to the
		// exec. The node matters: a pod that is Running has necessarily been
		// scheduled, and the node it landed on is what the locator entry has
		// to carry — an entry recorded with an empty node is the
		// sourceNode=="" failure this whole path exists to prevent.
		markPodRunning := func(handle string) {
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Spec.NodeName = "node-1"
			pod, err = fakeClientset.CoreV1().Pods("test-namespace").Update(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		It("does NOT record output locations for a task step on a severed exec, preserving fail-fast (review 2026-07-12)", func() {
			// On the severed path the supervised in-pod process is still
			// running and writing its outputs. Publishing a DaemonSet locator
			// for a generic task/get step would let an on_failure/on_error
			// hook StreamOut a half-written artifact with NO error; the
			// missing locator must keep failing fast instead. Only agent
			// steps (which need the flight-recorder locator) publish here.
			c := makeContainer("severed-task-handle", db.ContainerTypeTask)
			fakeExecutor.execErr = errors.New("error dialing backend: EOF")

			process, err := c.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hi"},
			}, runtime.ProcessIO{
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			markPodRunning("severed-task-handle")

			_, err = process.Wait(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exec in pod"))

			// No locator is published — a torn task artifact must fail fast.
			_, found := locator.Locate("severed-task-handle-output-out")
			Expect(found).To(BeFalse(), "a torn task artifact must not be locatable")
		})
	})

	Describe("terminal-end agent kill (timed-out/aborted agent step)", func() {
		var (
			fakeExecutor *fakeExecExecutor
			execWorker   *jetbridge.Worker
		)

		BeforeEach(func() {
			fakeExecutor = &fakeExecExecutor{}
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)
		})

		// makeContainer creates a supervised-eligible container of the given
		// type.
		makeContainer := func(handle string, cType db.ContainerType) runtime.Container {

			c, _, err := execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner(handle),
				db.ContainerMetadata{Type: cType},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:      cType,
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			return c
		}

		// markPodRunning flips the pause pod (created by Run) to Running so
		// Wait's waitForRunning proceeds to the exec.
		markPodRunning := func(handle string) {
			pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, handle, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod.Status.Phase = corev1.PodRunning
			_, err = fakeClientset.CoreV1().Pods("test-namespace").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
			Expect(err).ToNot(HaveOccurred())
		}

		// runSevered starts the process (no stdin → supervised for
		// task/agent) and severs the exec: the first exec call cancels the
		// step context and returns a transport error, simulating the SPDY
		// session dying because the step timed out or the build was aborted.
		// Later exec calls (the kill) succeed.
		runSevered := func(handle string, c runtime.Container) error {
			waitCtx, waitCancel := context.WithCancel(ctx)
			defer waitCancel()

			var execCount int32
			fakeExecutor.execFunc = func() error {
				if atomic.AddInt32(&execCount, 1) == 1 {
					waitCancel()
					return errors.New("context canceled")
				}
				return nil
			}

			process, err := c.Run(ctx, runtime.ProcessSpec{
				Path: "agent-runner",
			}, runtime.ProcessIO{
				Stdout: new(bytes.Buffer),
				Stderr: new(bytes.Buffer),
			})
			Expect(err).ToNot(HaveOccurred())

			markPodRunning(handle)

			_, err = process.Wait(waitCtx)
			return err
		}

		It("leaves timed-out/aborted task steps alone (existing semantics)", func() {
			err := runSevered("terminal-kill-task", makeContainer("terminal-kill-task", db.ContainerTypeTask))
			Expect(err).To(HaveOccurred())

			Expect(fakeExecutor.execCalls).To(HaveLen(1))
		})
	})

	Describe("transient API error handling", func() {
		It("tolerates a single API error during pollUntilDone", func() {
			errorClientset := fake.NewSimpleClientset()
			errorCfg := jetbridge.NewConfig("test-namespace", "")
			errorWorker := jetbridge.NewWorker(dbWorker, errorClientset, errorCfg)

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-ok-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
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
			errorWorker := jetbridge.NewWorker(dbWorker, errorClientset, errorCfg)

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
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
			errorWorker := jetbridge.NewWorker(dbWorker, errorClientset, errorCfg)

			transientContainer, _, err := errorWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("transient-reset-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
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
				execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				execWorker.SetExecutor(execExecutor)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("image-pull-fail-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/workdir",
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
				execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				execWorker.SetExecutor(execExecutor)

				var err error
				execContainer, _, err = execWorker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("startup-duration-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/workdir",
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
		dbWorker      db.Worker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}
		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
	})

	Context("when main container exits while sidecars are still running (direct mode)", func() {
		It("returns the main container's exit code and cleans up the pod", func() {

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-lifecycle-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
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

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
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

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-fail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
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

			container, _, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("sidecar-imgfail-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
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
		dbWorker      db.Worker
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
		database := useJetbridgeDB()
		persistedWorker, persistErr := persistNamedWorker(database, "k8s-worker-1")
		Expect(persistErr).NotTo(HaveOccurred())
		dbWorker = persistedWorker
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
			worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("phase-span-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
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
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("exec-phase-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeGet},
				runtime.ContainerSpec{
					TeamID:    1,
					ImageSpec: runtime.ImageSpec{ResourceType: "git"},
					Type:      db.ContainerTypeGet,
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
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("init-sidecar-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:      db.ContainerTypeTask,
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
			execWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
			execWorker.SetExecutor(fakeExecutor)

			var err error
			execContainer, _, err = execWorker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("pvc-image-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Type:      db.ContainerTypeTask,
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

})
