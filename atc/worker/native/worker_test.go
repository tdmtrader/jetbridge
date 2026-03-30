package native_test

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worker", func() {
	var (
		ctx          context.Context
		fakeDBWorker *dbfakes.FakeWorker
		fakeVolRepo  *dbfakes.FakeVolumeRepository
		worker       *native.Worker
		config       native.Config
		delegate     runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("native-darwin")
		fakeVolRepo = new(dbfakes.FakeVolumeRepository)
		delegate = &noopDelegate{}

		config = native.Config{
			WorkDir:    filepath.Join(GinkgoT().TempDir(), "work"),
			CacheDir:   filepath.Join(GinkgoT().TempDir(), "cache"),
			Platform:   "darwin",
			WorkerName: "native-darwin",
		}

		worker = native.NewWorker(fakeDBWorker, config, fakeVolRepo, compression.NewGzipCompression())
	})

	Describe("Name", func() {
		It("returns the db worker name", func() {
			Expect(worker.Name()).To(Equal("native-darwin"))
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
			metadata = db.ContainerMetadata{Type: db.ContainerTypeTask}
			spec = runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				Inputs: []runtime.Input{
					{DestinationPath: "/workdir/input-a"},
					{DestinationPath: "/workdir/input-b"},
				},
				Outputs: runtime.OutputPaths{"result": "/workdir/result"},
				Caches:  []string{".cache"},
				ScratchPaths: []string{".scratch"},
				JobID:    42,
				StepName: "build",
			}
		})

		Context("when no container exists in the DB", func() {
			var (
				fakeCreating *dbfakes.FakeCreatingContainer
				fakeCreated  *dbfakes.FakeCreatedContainer
			)

			BeforeEach(func() {
				fakeCreating, fakeCreated = setupFakeDBContainer(fakeDBWorker, "test-handle")
			})

			It("creates a container in the DB and returns volume mounts", func() {
				container, mounts, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("creating the container in the DB")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(1))

				By("transitioning to created state")
				Expect(fakeCreating.CreatedCallCount()).To(Equal(1))

				By("returning volume mounts for Dir, 2 Inputs, 1 Output, 1 Cache, 1 Scratch")
				// Dir + 2 inputs + 1 output + 1 cache + 1 scratch = 6
				Expect(mounts).To(HaveLen(6))

				By("creating scratch directory on disk")
				containerDir := filepath.Join(config.WorkDir, "containers", "test-handle")
				Expect(containerDir).To(BeADirectory())

				_ = fakeCreated // suppress unused warning
			})

			It("creates cache volumes in CacheDir, not containerDir", func() {
				_, mounts, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())

				// Find the cache mount.
				var cachePath string
				for _, m := range mounts {
					vol, ok := m.Volume.(*native.Volume)
					if ok && filepath.HasPrefix(vol.Path(), config.CacheDir) {
						cachePath = vol.Path()
					}
				}
				Expect(cachePath).ToNot(BeEmpty(), "expected a cache volume in CacheDir")
				Expect(cachePath).To(HavePrefix(config.CacheDir))
			})
		})

		Context("when created container already exists", func() {
			BeforeEach(func() {
				fakeCreated := new(dbfakes.FakeCreatedContainer)
				fakeCreated.HandleReturns("test-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreated, nil)
			})

			It("returns the existing container without re-creating", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("not calling CreateContainer")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))
			})
		})

		Context("when DB find returns an error", func() {
			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, fmt.Errorf("db down"))
			})

			It("returns the error", func() {
				_, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("db down"))
			})
		})

		Context("when transitioning to created fails", func() {
			BeforeEach(func() {
				fakeCreating := new(dbfakes.FakeCreatingContainer)
				fakeCreating.HandleReturns("test-handle")
				fakeCreating.CreatedReturns(nil, fmt.Errorf("transition failed"))
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
				fakeDBWorker.CreateContainerReturns(fakeCreating, nil)
			})

			It("marks the container as failed", func() {
				_, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("buildVolumeMountsForSpec (via FindOrCreateContainer)", func() {
		It("returns inputVolumes indexed 1:1 with Inputs (regression for Bug 1)", func() {
			setupFakeDBContainer(fakeDBWorker, "bug1-handle")

			spec := runtime.ContainerSpec{
				TeamID: 1,
				Dir:    "/workdir",
				Inputs: []runtime.Input{
					{DestinationPath: "/workdir/input-0"},
					{DestinationPath: "/workdir/input-1"},
				},
				Outputs: runtime.OutputPaths{"out": "/workdir/out"},
			}

			_, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("bug1-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				spec,
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())

			By("having Dir mount first, then Inputs, then Outputs")
			// Dir=1, Inputs=2, Outputs=1 = 4 total
			Expect(mounts).To(HaveLen(4))
			Expect(mounts[0].MountPath).To(Equal("/workdir"))
			Expect(mounts[1].MountPath).To(Equal("/workdir/input-0"))
			Expect(mounts[2].MountPath).To(Equal("/workdir/input-1"))
			Expect(mounts[3].MountPath).To(Equal("/workdir/out"))
		})

		It("handles empty spec", func() {
			setupFakeDBContainer(fakeDBWorker, "empty-handle")

			_, mounts, err := worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("empty-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{TeamID: 1},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(mounts).To(BeEmpty())
		})
	})

	Describe("LookupContainer", func() {
		Context("when found in DB", func() {
			BeforeEach(func() {
				fakeCreated := new(dbfakes.FakeCreatedContainer)
				fakeCreated.HandleReturns("lookup-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreated, nil)
			})

			It("returns the container", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(container).ToNot(BeNil())
			})
		})

		Context("when not found", func() {
			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, nil)
			})

			It("returns false", func() {
				_, found, err := worker.LookupContainer(ctx, "missing")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("LookupVolume", func() {
		Context("when found in DB", func() {
			BeforeEach(func() {
				fakeCreatedVol := new(dbfakes.FakeCreatedVolume)
				fakeCreatedVol.HandleReturns("vol-handle")
				fakeVolRepo.FindVolumeReturns(fakeCreatedVol, true, nil)
			})

			It("returns the volume", func() {
				vol, found, err := worker.LookupVolume(ctx, "vol-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol.Handle()).To(Equal("vol-handle"))
			})
		})

		Context("when not found", func() {
			BeforeEach(func() {
				fakeVolRepo.FindVolumeReturns(nil, false, nil)
			})

			It("returns false", func() {
				_, found, err := worker.LookupVolume(ctx, "missing")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
})
