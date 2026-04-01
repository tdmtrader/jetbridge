package native_test

import (
	"context"
	"os"
	"path/filepath"

	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

var _ = Describe("Registrar", func() {
	var (
		ctx              context.Context
		logger           = lagertest.NewTestLogger("registrar-test")
		fakeWorkerFactory *dbfakes.FakeWorkerFactory
		registrar        *native.Registrar
		config           native.Config
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeWorkerFactory = new(dbfakes.FakeWorkerFactory)

		config = native.Config{
			WorkDir:    filepath.Join(GinkgoT().TempDir(), "work"),
			CacheDir:   filepath.Join(GinkgoT().TempDir(), "cache"),
			Platform:   "darwin",
			WorkerName: "native-darwin",
		}

		registrar = native.NewRegistrar(logger, config, fakeWorkerFactory)
	})

	Describe("Register", func() {
		It("saves worker with correct attributes", func() {
			fakeWorkerFactory.SaveWorkerReturns(nil, nil)

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeWorkerFactory.SaveWorkerCallCount()).To(Equal(1))
			savedWorker, ttl := fakeWorkerFactory.SaveWorkerArgsForCall(0)

			By("setting the correct name")
			Expect(savedWorker.Name).To(Equal("native-darwin"))

			By("setting the correct platform")
			Expect(savedWorker.Platform).To(Equal("darwin"))

			By("setting state to running")
			Expect(savedWorker.State).To(Equal("running"))

			By("setting the version")
			Expect(savedWorker.Version).To(Equal(concourse.WorkerVersion))

			By("using 30s TTL")
			Expect(ttl.Seconds()).To(Equal(30.0))
		})

		It("reports active container count from directory listing", func() {
			// Create some fake container directories.
			containersDir := filepath.Join(config.WorkDir, "containers")
			Expect(os.MkdirAll(filepath.Join(containersDir, "container-1"), 0755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(containersDir, "container-2"), 0755)).To(Succeed())
			// Create a file (should not be counted).
			Expect(os.WriteFile(filepath.Join(containersDir, "not-a-dir"), []byte("x"), 0644)).To(Succeed())

			fakeWorkerFactory.SaveWorkerReturns(nil, nil)

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(2))
		})

		It("reports 0 when containers dir doesn't exist", func() {
			fakeWorkerFactory.SaveWorkerReturns(nil, nil)

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(0))
		})
	})
})
