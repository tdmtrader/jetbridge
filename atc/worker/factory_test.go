package worker_test

import (
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("DefaultFactory", func() {
	var (
		logger       = lagertest.NewTestLogger("factory-test")
		fakeDBWorker *dbfakes.FakeWorker
	)

	BeforeEach(func() {
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("test-worker")
	})

	Context("when K8s config is set", func() {
		It("creates a K8s worker", func() {
			fakeClientset := fake.NewSimpleClientset()
			cfg := jetbridge.NewConfig("test-namespace", "")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
			}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())
			Expect(w.Name()).To(Equal("test-worker"))

			// Should be a jetbridge.Worker
			_, ok := w.(*jetbridge.Worker)
			Expect(ok).To(BeTrue())
		})
	})

	Context("when NativeConfig is set", func() {
		It("creates a native worker for matching platform", func() {
			fakeClientset := fake.NewSimpleClientset()
			cfg := jetbridge.NewConfig("test-namespace", "")
			nativeCfg := native.Config{
				WorkDir:    "/tmp/test",
				CacheDir:   "/tmp/test/caches",
				Platform:   "darwin",
				WorkerName: "native-darwin",
			}

			fakeDBWorker.PlatformReturns("darwin")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
				NativeConfig: &nativeCfg,
				Compression:  compression.NewGzipCompression(),
			}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())

			_, ok := w.(*native.Worker)
			Expect(ok).To(BeTrue(), "expected native.Worker for darwin platform")
		})

		It("creates a K8s worker for non-matching platform", func() {
			fakeClientset := fake.NewSimpleClientset()
			cfg := jetbridge.NewConfig("test-namespace", "")
			nativeCfg := native.Config{
				WorkDir:    "/tmp/test",
				CacheDir:   "/tmp/test/caches",
				Platform:   "darwin",
				WorkerName: "native-darwin",
			}

			fakeDBWorker.PlatformReturns("linux")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
				NativeConfig: &nativeCfg,
				Compression:  compression.NewGzipCompression(),
			}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())

			_, ok := w.(*jetbridge.Worker)
			Expect(ok).To(BeTrue(), "expected jetbridge.Worker for linux platform")
		})
	})

	Context("when NativeConfig is nil", func() {
		It("creates K8s worker even for darwin platform", func() {
			fakeClientset := fake.NewSimpleClientset()
			cfg := jetbridge.NewConfig("test-namespace", "")

			fakeDBWorker.PlatformReturns("darwin")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
			}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())

			_, ok := w.(*jetbridge.Worker)
			Expect(ok).To(BeTrue(), "expected jetbridge.Worker when NativeConfig is nil")
		})
	})
})
