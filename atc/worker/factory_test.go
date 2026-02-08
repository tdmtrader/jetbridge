package worker_test

import (
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("DefaultFactory", func() {
	var (
		logger     = lagertest.NewTestLogger("factory-test")
		fakeDBWorker *dbfakes.FakeWorker
	)

	BeforeEach(func() {
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("test-worker")
	})

	Context("when K8s config is set and the worker has no GardenAddr", func() {
		It("creates a K8s worker", func() {
			fakeDBWorker.GardenAddrReturns(nil)

			fakeClientset := fake.NewSimpleClientset()
			cfg := k8sruntime.NewConfig("test-namespace", "")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
			}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())
			Expect(w.Name()).To(Equal("test-worker"))

			// Should be a k8sruntime.Worker
			_, ok := w.(*k8sruntime.Worker)
			Expect(ok).To(BeTrue())
		})
	})

	Context("when the worker has a GardenAddr", func() {
		It("creates a Garden worker", func() {
			gardenAddr := "10.0.0.1:7777"
			fakeDBWorker.GardenAddrReturns(&gardenAddr)
			baggageclaimURL := "http://10.0.0.1:7788"
			fakeDBWorker.BaggageclaimURLReturns(&baggageclaimURL)

			factory := worker.DefaultFactory{}

			w := factory.NewWorker(logger, fakeDBWorker)
			Expect(w).ToNot(BeNil())
		})
	})
})

func TestFactory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Factory Suite")
}
