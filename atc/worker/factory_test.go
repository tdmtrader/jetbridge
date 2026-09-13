package worker_test

import (
	"bytes"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
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

		It("gives the worker the output daemon's control resolver when the plane is configured", func() {
			// NOTHING CALLED SetOutputControls, ANYWHERE. The ATC validated a
			// capability key at startup, refused to run without it, compared
			// it with two other keys for distinctness -- and never opened it,
			// because the resolver it exists to build was constructed by no
			// production line. Every jetbridge worker's resolver was nil, so
			// every control call on the output daemon was unreachable.
			cfg := jetbridge.NewConfig("test-namespace", "")
			minter, err := executioncontrol.NewCapabilityMinter(
				bytes.Repeat([]byte{7}, executioncontrol.CapabilityKeyBytes),
				time.Minute, nil)
			Expect(err).NotTo(HaveOccurred())

			fakeClientset := fake.NewSimpleClientset()
			factory := worker.DefaultFactory{
				K8sClientset: fakeClientset,
				K8sConfig:    &cfg,
				K8sOutputControls: jetbridge.NewOutputControls(cfg,
					jetbridge.NewNodeIPResolver(fakeClientset), minter, 7),
			}

			built, ok := factory.NewWorker(logger, dbWorker).(*jetbridge.Worker)
			Expect(ok).To(BeTrue())
			Expect(built.OutputControls()).NotTo(BeNil(),
				"the worker cannot reach the output daemon on any node, so the capability key "+
					"the ATC refuses to start without is a secret nothing spends")

			// The control: a deployment with no output plane still hands every
			// worker a nil resolver, which is what keeps ordinary execution
			// byte-for-byte what it was.
			plain := worker.DefaultFactory{K8sClientset: fakeClientset, K8sConfig: &cfg}
			ordinary, ok := plain.NewWorker(logger, dbWorker).(*jetbridge.Worker)
			Expect(ok).To(BeTrue())
			Expect(ordinary.OutputControls()).To(BeNil())
		})
	})
})
