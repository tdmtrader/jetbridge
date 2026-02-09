package jetbridge_test

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
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

	Describe("Name", func() {
		It("returns the db worker name", func() {
			Expect(worker.Name()).To(Equal("k8s-worker-1"))
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

		Context("when transitioning to created state fails", func() {
			var fakeCreatingContainer *dbfakes.FakeCreatingContainer

			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, nil)

				fakeCreatingContainer = new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("test-handle")
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				fakeCreatingContainer.CreatedReturns(nil, fmt.Errorf("db connection lost"))
			})

			It("marks the container as failed so the GC can clean it up", func() {
				_, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).To(HaveOccurred())

				Expect(fakeCreatingContainer.FailedCallCount()).To(Equal(1))
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

				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("lookup-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)
			})

			It("returns the container", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(container).ToNot(BeNil())
			})

			It("returns a container with a valid DBContainer for hijack support", func() {
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("lookup-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)

				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				By("having a non-nil DBContainer that the hijack handler can call UpdateLastHijack on")
				Expect(container.DBContainer()).ToNot(BeNil())
				Expect(container.DBContainer().Handle()).To(Equal("lookup-handle"))
			})
		})

		Context("when the Pod exists but the DB container does not", func() {
			BeforeEach(func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphan-pod",
						Namespace: "test-namespace",
					},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				fakeDBWorker.FindContainerReturns(nil, nil, nil)
			})

			It("returns not found since the container is not tracked in the DB", func() {
				_, found, err := worker.LookupContainer(ctx, "orphan-pod")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
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

	Describe("CreateVolumeForArtifact", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		Context("when the volume repo is configured", func() {
			var (
				fakeCreatingVolume *dbfakes.FakeCreatingVolume
				fakeCreatedVolume  *dbfakes.FakeCreatedVolume
				fakeArtifact       *dbfakes.FakeWorkerArtifact
			)

			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume = new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeCreatingVolume.IDReturns(42)

				fakeCreatedVolume = new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("artifact-volume-handle")

				fakeArtifact = new(dbfakes.FakeWorkerArtifact)
				fakeArtifact.IDReturns(7)

				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)
				fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)
				fakeCreatedVolume.InitializeArtifactReturns(fakeArtifact, nil)
			})

			It("creates an artifact volume and returns it with the artifact", func() {
				vol, artifact, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(vol).ToNot(BeNil())
				Expect(artifact).ToNot(BeNil())

				By("creating a volume with the correct team ID, worker name, and type")
				Expect(fakeVolumeRepo.CreateVolumeCallCount()).To(Equal(1))
				teamID, workerName, volType := fakeVolumeRepo.CreateVolumeArgsForCall(0)
				Expect(teamID).To(Equal(1))
				Expect(workerName).To(Equal("k8s-worker-1"))
				Expect(volType).To(Equal(db.VolumeTypeArtifact))

				By("transitioning the volume to created state")
				Expect(fakeCreatingVolume.CreatedCallCount()).To(Equal(1))

				By("initializing the artifact on the created volume")
				Expect(fakeCreatedVolume.InitializeArtifactCallCount()).To(Equal(1))
				name, buildID := fakeCreatedVolume.InitializeArtifactArgsForCall(0)
				Expect(name).To(Equal(""))
				Expect(buildID).To(Equal(0))

				By("returning the artifact from the DB")
				Expect(artifact.ID()).To(Equal(7))
			})

			It("returns a volume with the correct handle", func() {
				vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(vol.Handle()).To(Equal("artifact-volume-handle"))
			})

			Context("when the artifact store is configured", func() {
				BeforeEach(func() {
					cfg.ArtifactStoreClaim = "my-artifact-pvc"
					worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
					worker.SetVolumeRepo(fakeVolumeRepo)
				})

				It("returns an ArtifactStoreVolume", func() {
					vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
					Expect(err).ToNot(HaveOccurred())
					Expect(vol).ToNot(BeNil())

					asVol, ok := vol.(*jetbridge.ArtifactStoreVolume)
					Expect(ok).To(BeTrue(), "expected ArtifactStoreVolume, got %T", vol)
					Expect(asVol.Key()).To(Equal("artifacts/artifact-volume-handle.tar"))
					Expect(asVol.Handle()).To(Equal("artifact-volume-handle"))
				})
			})

			Context("when the artifact store is NOT configured", func() {
				It("returns a DeferredVolume (regular Volume)", func() {
					vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
					Expect(err).ToNot(HaveOccurred())
					Expect(vol).ToNot(BeNil())

					_, isArtifactStore := vol.(*jetbridge.ArtifactStoreVolume)
					Expect(isArtifactStore).To(BeFalse(), "expected regular Volume, not ArtifactStoreVolume")
				})
			})
		})

		Context("when the volume repo is NOT configured", func() {
			It("returns an error", func() {
				// Create a fresh worker without SetVolumeRepo
				freshWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				_, _, err := freshWorker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("volume repository not configured")))
			})
		})

		Context("when CreateVolume fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)
				fakeVolumeRepo.CreateVolumeReturns(nil, fmt.Errorf("db connection lost"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("db connection lost")))
			})
		})

		Context("when transitioning to created state fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)

				fakeCreatingVolume.CreatedReturns(nil, fmt.Errorf("transition error"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("transition error")))
			})
		})

		Context("when InitializeArtifact fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)

				fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("artifact-volume-handle")
				fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)

				fakeCreatedVolume.InitializeArtifactReturns(nil, fmt.Errorf("artifact init error"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("artifact init error")))
			})
		})
	})

	Describe("LookupVolume", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		BeforeEach(func() {
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
			worker.SetVolumeRepo(fakeVolumeRepo)
		})

		Context("when the volume exists in the DB", func() {
			var fakeCreatedVolume *dbfakes.FakeCreatedVolume

			BeforeEach(func() {
				fakeCreatedVolume = new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("vol-handle-1")
				fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")
				fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)
			})

			It("returns a cache-backed volume", func() {
				vol, found, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())
				Expect(vol.Handle()).To(Equal("vol-handle-1"))
			})

			It("calls FindVolume with the correct handle", func() {
				_, _, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeVolumeRepo.FindVolumeCallCount()).To(Equal(1))
				Expect(fakeVolumeRepo.FindVolumeArgsForCall(0)).To(Equal("vol-handle-1"))
			})
		})

		Context("when the volume does not exist in the DB", func() {
			BeforeEach(func() {
				fakeVolumeRepo.FindVolumeReturns(nil, false, nil)
			})

			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the DB returns an error", func() {
			BeforeEach(func() {
				fakeVolumeRepo.FindVolumeReturns(nil, false, fmt.Errorf("db connection lost"))
			})

			It("returns the error", func() {
				_, _, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("db connection lost")))
			})
		})

		Context("when no volume repo is configured", func() {
			BeforeEach(func() {
				worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				// intentionally do NOT call SetVolumeRepo
			})

			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
})

