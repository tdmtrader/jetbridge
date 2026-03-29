package worker_test

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeFactory implements worker.Factory for pool tests. It records which
// db.Worker was passed to NewWorker so we can assert routing decisions.
type fakeFactory struct {
	lastDBWorker db.Worker
}

func (f *fakeFactory) NewWorker(logger lager.Logger, dbWorker db.Worker) runtime.Worker {
	f.lastDBWorker = dbWorker
	// Return a minimal fake runtime.Worker.
	return &fakeRuntimeWorker{name: dbWorker.Name()}
}

type fakeRuntimeWorker struct {
	name string
}

func (w *fakeRuntimeWorker) Name() string { return w.name }
func (w *fakeRuntimeWorker) FindOrCreateContainer(_ context.Context, _ db.ContainerOwner, _ db.ContainerMetadata, _ runtime.ContainerSpec, _ runtime.BuildStepDelegate) (runtime.Container, []runtime.VolumeMount, error) {
	return nil, nil, nil
}
func (w *fakeRuntimeWorker) CreateVolumeForArtifact(_ context.Context, _ int) (runtime.Volume, db.WorkerArtifact, error) {
	return nil, nil, nil
}
func (w *fakeRuntimeWorker) LookupContainer(_ context.Context, _ string) (runtime.Container, bool, error) {
	return nil, false, nil
}
func (w *fakeRuntimeWorker) LookupVolume(_ context.Context, _ string) (runtime.Volume, bool, error) {
	return nil, false, nil
}

// Ensure fakeRuntimeWorker satisfies runtime.Worker.
var _ runtime.Worker = (*fakeRuntimeWorker)(nil)

// Ensure fakeFactory satisfies worker.Factory.
var _ worker.Factory = (*fakeFactory)(nil)


var _ = Describe("Pool platform filtering", func() {
	var (
		factory          *fakeFactory
		fakeWorkerFactory *dbfakes.FakeWorkerFactory
		fakeTeamFactory  *dbfakes.FakeTeamFactory
		pool             worker.Pool
	)

	BeforeEach(func() {
		factory = &fakeFactory{}
		fakeWorkerFactory = new(dbfakes.FakeWorkerFactory)
		fakeTeamFactory = new(dbfakes.FakeTeamFactory)

		db := worker.DB{
			WorkerFactory: fakeWorkerFactory,
			TeamFactory:   fakeTeamFactory,
		}

		pool = worker.NewPool(factory, db)
	})

	Context("with linux and darwin workers", func() {
		var linuxWorker, darwinWorker *dbfakes.FakeWorker

		BeforeEach(func() {
			linuxWorker = new(dbfakes.FakeWorker)
			linuxWorker.NameReturns("k8s-worker")
			linuxWorker.PlatformReturns("linux")
			linuxWorker.StateReturns(db.WorkerStateRunning)

			darwinWorker = new(dbfakes.FakeWorker)
			darwinWorker.NameReturns("native-darwin")
			darwinWorker.PlatformReturns("darwin")
			darwinWorker.StateReturns(db.WorkerStateRunning)

			fakeWorkerFactory.WorkersReturns([]db.Worker{linuxWorker, darwinWorker}, nil)
			fakeWorkerFactory.FindWorkersForContainerByOwnerReturns(nil, nil)
		})

		It("routes darwin platform to darwin worker", func() {
			w, err := pool.FindOrSelectWorker(
				ctx,
				db.NewFixedHandleContainerOwner("test"),
				runtime.ContainerSpec{TeamID: 0},
				worker.Spec{Platform: "darwin"},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(w.Name()).To(Equal("native-darwin"))
		})

		It("routes linux platform to linux worker", func() {
			w, err := pool.FindOrSelectWorker(
				ctx,
				db.NewFixedHandleContainerOwner("test"),
				runtime.ContainerSpec{TeamID: 0},
				worker.Spec{Platform: "linux"},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(w.Name()).To(Equal("k8s-worker"))
		})

		It("routes empty platform to any worker", func() {
			w, err := pool.FindOrSelectWorker(
				ctx,
				db.NewFixedHandleContainerOwner("test"),
				runtime.ContainerSpec{TeamID: 0},
				worker.Spec{Platform: ""},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(w).ToNot(BeNil())
		})

		It("returns error when platform has no match", func() {
			_, err := pool.FindOrSelectWorker(
				ctx,
				db.NewFixedHandleContainerOwner("test"),
				runtime.ContainerSpec{TeamID: 0},
				worker.Spec{Platform: "windows"},
			)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("with team-scoped workers", func() {
		BeforeEach(func() {
			teamWorker := new(dbfakes.FakeWorker)
			teamWorker.NameReturns("team-darwin")
			teamWorker.PlatformReturns("darwin")
			teamWorker.StateReturns(db.WorkerStateRunning)
			teamWorker.TeamIDReturns(42)

			globalWorker := new(dbfakes.FakeWorker)
			globalWorker.NameReturns("global-darwin")
			globalWorker.PlatformReturns("darwin")
			globalWorker.StateReturns(db.WorkerStateRunning)
			globalWorker.TeamIDReturns(0)

			fakeWorkerFactory.WorkersReturns([]db.Worker{teamWorker, globalWorker}, nil)
			fakeWorkerFactory.FindWorkersForContainerByOwnerReturns(nil, nil)
		})

		It("prefers team workers over global workers", func() {
			w, err := pool.FindOrSelectWorker(
				ctx,
				db.NewFixedHandleContainerOwner("test"),
				runtime.ContainerSpec{TeamID: 0},
				worker.Spec{TeamID: 42, Platform: "darwin"},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(w.Name()).To(Equal("team-darwin"))
		})
	})
})
