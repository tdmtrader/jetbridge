package k8sruntime_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
	})
})
