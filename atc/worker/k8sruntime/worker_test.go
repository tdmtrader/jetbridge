package k8sruntime_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
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

var _ = Describe("Worker", func() {
	var (
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *k8sruntime.Worker
		ctx           context.Context
		cfg           k8sruntime.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = k8sruntime.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = k8sruntime.NewWorker(fakeDBWorker, fakeClientset, cfg)
	})

	Describe("Name", func() {
		It("returns the db worker name", func() {
			Expect(worker.Name()).To(Equal("k8s-worker-1"))
		})
	})

	Describe("DBWorker", func() {
		It("returns the underlying db worker", func() {
			Expect(worker.DBWorker()).To(BeIdenticalTo(fakeDBWorker))
		})
	})

	Describe("FindOrCreateContainer", func() {
		var (
			owner    db.ContainerOwner
			metadata db.ContainerMetadata
			spec     runtime.ContainerSpec
		)

		BeforeEach(func() {
			owner = db.NewFixedHandleContainerOwner("test-handle")
			metadata = db.ContainerMetadata{
				Type:     db.ContainerTypeTask,
				StepName: "my-task",
			}
			spec = runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
			}
		})

		Context("when no container exists in the DB", func() {
			var (
				fakeCreatingContainer *dbfakes.FakeCreatingContainer
				fakeCreatedContainer  *dbfakes.FakeCreatedContainer
			)

			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, nil)

				fakeCreatingContainer = new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("test-handle")
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("test-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			})

			It("creates a container in the DB and defers Pod creation to Run", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("creating the container in the DB")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(1))

				By("marking the container as created")
				Expect(fakeCreatingContainer.CreatedCallCount()).To(Equal(1))

				By("not creating a Pod yet (deferred to Run)")
				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(0))

				By("creating the Pod when Run is called")
				_, err = container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err = fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))
				Expect(pods.Items[0].Name).To(Equal("test-handle"))
			})
		})

		Context("when a created container already exists in the DB", func() {
			var fakeCreatedContainer *dbfakes.FakeCreatedContainer

			BeforeEach(func() {
				fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("existing-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)
			})

			It("returns the existing container without creating a new one in the DB", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("not creating a new container in the DB")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))
			})
		})
	})

	Describe("LookupContainer", func() {
		Context("when the Pod exists", func() {
			BeforeEach(func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lookup-handle",
						Namespace: "test-namespace",
					},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns the container", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(container).ToNot(BeNil())
			})
		})

		Context("when the Pod does not exist", func() {
			It("returns not found", func() {
				_, found, err := worker.LookupContainer(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("LookupVolume", func() {
		Context("when the volume does not exist", func() {
			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
})

type noopDelegate struct{}

func (d *noopDelegate) StreamingVolume(_ lager.Logger, _, _, _ string) {}
func (d *noopDelegate) WaitingForStreamedVolume(_ lager.Logger, _, _ string) {}
func (d *noopDelegate) BuildStartTime() time.Time                           { return time.Time{} }
