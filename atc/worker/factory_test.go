package worker_test

import (
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
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
})

func TestFactory(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Worker Factory Suite")
}
