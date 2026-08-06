package worker_test

import (
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("DefaultFactory", func() {
	var (
		logger   = lagertest.NewTestLogger("factory-test")
		dbWorker db.Worker
	)

	BeforeEach(func() {
		// A registered worker row, not a stand-in for one. The suite has had a
		// real conn and lockFactory since worker_suite_test.go:27-40.
		var err error
		dbWorker, err = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0)).
			SaveWorker(atc.Worker{
				Name:             "test-worker",
				Platform:         "linux",
				ActiveContainers: 0,
				StartTime:        55,
			}, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when K8s config is set", func() {
		It("creates a K8s worker", func() {
			fakeClientset := fake.NewSimpleClientset()
			cfg := jetbridge.NewConfig("test-namespace", "")

			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
			}

			w := factory.NewWorker(logger, dbWorker)
			Expect(w).ToNot(BeNil())
			Expect(w.Name()).To(Equal("test-worker"))

			// Should be a jetbridge.Worker
			_, ok := w.(*jetbridge.Worker)
			Expect(ok).To(BeTrue())
		})
	})
})
