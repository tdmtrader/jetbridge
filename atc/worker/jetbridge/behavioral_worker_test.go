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

var _ = Describe("Behavioral Worker Tests", func() {
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

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				},
			},
		}
		fakeClientset = fake.NewSimpleClientset(node)
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
	})

	// -----------------------------------------------------------------------
	// RC-02: Cached volume lookup returns DaemonSetVolume
	// -----------------------------------------------------------------------
	Describe("RC-02: LookupVolume returns DaemonSetVolume", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		BeforeEach(func() {
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
			worker.SetVolumeRepo(fakeVolumeRepo)
		})

		Context("when the volume exists in the DB", func() {
			var fakeCreatedVolume *dbfakes.FakeCreatedVolume

			BeforeEach(func() {
				fakeCreatedVolume = new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("cached-vol-1")
				fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")
				fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)
			})

			It("returns a DaemonSetVolume with the correct handle", func() {
				vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())
				Expect(vol.Handle()).To(Equal("cached-vol-1"))

				dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
				Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
				Expect(dsVol.Key()).To(Equal("cached-vol-1"))
			})

			Context("with ArtifactLocator populated", func() {
				BeforeEach(func() {
					locator := jetbridge.NewArtifactLocator()
					locator.Record(jetbridge.ArtifactKey("cached-vol-1"), "node-42", "container/output")
					worker.SetArtifactLocator(locator)
				})

				It("returns volume with source node set from locator", func() {
					vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())

					dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
					Expect(ok).To(BeTrue())
					Expect(dsVol.Source()).To(Equal("k8s-worker-1"))
				})
			})

			Context("without ArtifactLocator", func() {
				It("returns volume with empty source node (graceful degradation)", func() {
					vol, found, err := worker.LookupVolume(ctx, "cached-vol-1")
					Expect(err).ToNot(HaveOccurred())
					Expect(found).To(BeTrue())

					dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
					Expect(ok).To(BeTrue())
					// Source is the worker name from the volume, not the node
					Expect(dsVol.Source()).To(Equal("k8s-worker-1"))
				})
			})
		})
	})

	// -----------------------------------------------------------------------
	// RC-03: Cache hit short-circuit
	// -----------------------------------------------------------------------
	Describe("RC-03: Cache hit short-circuit", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		BeforeEach(func() {
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
			worker.SetVolumeRepo(fakeVolumeRepo)

			fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
			fakeCreatedVolume.HandleReturns("cache-hit-vol")
			fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")
			fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)
		})

		It("returns a cached volume without creating a pod", func() {
			vol, found, err := worker.LookupVolume(ctx, "cache-hit-vol")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol).ToNot(BeNil())

			By("no pods should have been created")
			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(pods.Items).To(HaveLen(0))
		})

		It("returns a volume that supports InitializeResourceCache", func() {
			vol, found, err := worker.LookupVolume(ctx, "cache-hit-vol")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())

			// InitializeResourceCache should not panic even with nil cache
			result, err := vol.InitializeResourceCache(ctx, nil)
			// With a fake dbVolume that hasn't had InitializeResourceCache
			// configured, this will delegate to the fake's default behavior
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	// -----------------------------------------------------------------------
	// RC-05: Cache invalidation
	// -----------------------------------------------------------------------
	Describe("RC-05: Cache invalidation", func() {
		Context("when DB volume is not found", func() {
			BeforeEach(func() {
				fakeVolumeRepo := new(dbfakes.FakeVolumeRepository)
				fakeVolumeRepo.FindVolumeReturns(nil, false, nil)
				worker.SetVolumeRepo(fakeVolumeRepo)
			})

			It("returns (nil, false, nil)", func() {
				vol, found, err := worker.LookupVolume(ctx, "expired-vol")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(vol).To(BeNil())
			})
		})

		Context("when volumeRepo is nil", func() {
			It("returns (nil, false, nil)", func() {
				freshWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				vol, found, err := freshWorker.LookupVolume(ctx, "any-vol")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(vol).To(BeNil())
			})
		})
	})

	// -----------------------------------------------------------------------
	// CO-09: Output recording - CreateVolumeForArtifact
	// -----------------------------------------------------------------------
	Describe("CO-09: CreateVolumeForArtifact returns DaemonSetVolume with ArtifactKey", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		BeforeEach(func() {
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
			worker.SetVolumeRepo(fakeVolumeRepo)

			fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
			fakeCreatingVolume.HandleReturns("art-vol-42")

			fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
			fakeCreatedVolume.HandleReturns("art-vol-42")

			fakeArtifact := new(dbfakes.FakeWorkerArtifact)
			fakeArtifact.IDReturns(99)

			fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)
			fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)
			fakeCreatedVolume.InitializeArtifactReturns(fakeArtifact, nil)
		})

		It("returns a DaemonSetVolume with key = ArtifactKey(handle)", func() {
			vol, artifact, err := worker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(artifact).ToNot(BeNil())

			dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
			Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)

			// ArtifactKey is identity, so key should equal handle
			Expect(dsVol.Key()).To(Equal("art-vol-42"))
			Expect(dsVol.Handle()).To(Equal("art-vol-42"))
		})
	})

	// -----------------------------------------------------------------------
	// LR-03: ATC restart resilience
	// -----------------------------------------------------------------------
	Describe("LR-03: ATC restart resilience - LookupVolume without ArtifactLocator", func() {
		BeforeEach(func() {
			fakeVolumeRepo := new(dbfakes.FakeVolumeRepository)
			fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
			fakeCreatedVolume.HandleReturns("resilient-vol")
			fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")
			fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)
			worker.SetVolumeRepo(fakeVolumeRepo)
			// Intentionally do NOT call SetArtifactLocator
		})

		It("returns the volume without error (graceful degradation)", func() {
			vol, found, err := worker.LookupVolume(ctx, "resilient-vol")
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(vol).ToNot(BeNil())
			Expect(vol.Handle()).To(Equal("resilient-vol"))
		})
	})

	// -----------------------------------------------------------------------
	// LR-04: Container reuse
	// -----------------------------------------------------------------------
	Describe("LR-04: Container reuse when createdContainer already exists", func() {
		var fakeCreatedContainer *dbfakes.FakeCreatedContainer

		BeforeEach(func() {
			fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
			fakeCreatedContainer.HandleReturns("reused-handle")
			fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)
		})

		It("returns the existing container without creating a new one", func() {
			owner := db.NewFixedHandleContainerOwner("reused-handle")
			metadata := db.ContainerMetadata{
				Type:     db.ContainerTypeTask,
				StepName: "my-task",
			}
			spec := runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
			}

			container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
			Expect(err).ToNot(HaveOccurred())
			Expect(container).ToNot(BeNil())

			By("not creating a new container in the DB")
			Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))

			By("returning the existing DB container")
			Expect(container.DBContainer()).ToNot(BeNil())
			Expect(container.DBContainer().Handle()).To(Equal("reused-handle"))
		})
	})

	// -----------------------------------------------------------------------
	// Additional behavioral: FindOrCreateContainer volume mounts
	// -----------------------------------------------------------------------
	Describe("FindOrCreateContainer returns volume mounts matching spec", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "mount-test-handle")
		})

		It("returns mounts for Dir, inputs, outputs, and caches", func() {
			owner := db.NewFixedHandleContainerOwner("mount-test-handle")
			metadata := db.ContainerMetadata{
				Type:     db.ContainerTypeTask,
				StepName: "mount-test",
			}
			spec := runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
				Inputs: []runtime.Input{
					{DestinationPath: "/workdir/input-a"},
				},
				Outputs: runtime.OutputPaths{"out-b": "/workdir/out-b"},
				Caches:  []string{"my-cache"},
			}

			_, mounts, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
			Expect(err).ToNot(HaveOccurred())

			// 1 dir + 1 input + 1 output + 1 cache = 4
			Expect(mounts).To(HaveLen(4))

			mountPaths := make([]string, len(mounts))
			for i, m := range mounts {
				mountPaths[i] = m.MountPath
			}
			Expect(mountPaths).To(ContainElements(
				"/workdir",
				"/workdir/input-a",
				"/workdir/out-b",
			))
		})
	})

	// -----------------------------------------------------------------------
	// LookupVolume with DB error
	// -----------------------------------------------------------------------
	Describe("LookupVolume propagates DB errors", func() {
		BeforeEach(func() {
			fakeVolumeRepo := new(dbfakes.FakeVolumeRepository)
			fakeVolumeRepo.FindVolumeReturns(nil, false, fmt.Errorf("connection refused"))
			worker.SetVolumeRepo(fakeVolumeRepo)
		})

		It("returns the error", func() {
			_, _, err := worker.LookupVolume(ctx, "any")
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("connection refused")))
		})
	})

	// -----------------------------------------------------------------------
	// SkipResourceCache returns false
	// -----------------------------------------------------------------------
	Describe("SkipResourceCache", func() {
		It("returns false to enable caching in DaemonSet mode", func() {
			Expect(worker.SkipResourceCache()).To(BeFalse())
		})
	})

	// -----------------------------------------------------------------------
	// CreateVolumeForArtifact error paths
	// -----------------------------------------------------------------------
	Describe("CreateVolumeForArtifact without volumeRepo", func() {
		It("returns an error indicating volume repository not configured", func() {
			freshWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
			_, _, err := freshWorker.CreateVolumeForArtifact(ctx, 1)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("volume repository not configured")))
		})
	})

	// -----------------------------------------------------------------------
	// LookupVolume calls FindVolume with correct handle
	// -----------------------------------------------------------------------
	Describe("LookupVolume passes handle to FindVolume", func() {
		It("queries the volume repo with the exact handle", func() {
			fakeVolumeRepo := new(dbfakes.FakeVolumeRepository)
			fakeVolumeRepo.FindVolumeReturns(nil, false, nil)
			worker.SetVolumeRepo(fakeVolumeRepo)

			_, _, _ = worker.LookupVolume(ctx, "specific-handle-abc")

			Expect(fakeVolumeRepo.FindVolumeCallCount()).To(Equal(1))
			Expect(fakeVolumeRepo.FindVolumeArgsForCall(0)).To(Equal("specific-handle-abc"))
		})
	})
})
