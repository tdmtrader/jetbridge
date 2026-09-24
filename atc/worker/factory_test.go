package worker_test

import (
	"bytes"
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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

	createTask := func(w runtime.Worker, handle string) *jetbridge.Container {
		GinkgoHelper()
		created, _, err := w.FindOrCreateContainer(ctx,
			db.NewFixedHandleContainerOwner(handle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{TeamID: 1, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}},
			nil)
		Expect(err).NotTo(HaveOccurred())
		container, ok := created.(*jetbridge.Container)
		Expect(ok).To(BeTrue())
		return container
	}

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

		// The wiring test: from the pool's worker factory, through the Worker it
		// builds, to a Container that worker creates. Each collaborator is
		// asserted where it is spent -- on the container -- because a worker
		// field is only half the wire.
		//
		// The outage it guards against: NOTHING CALLED SetOutputControls,
		// ANYWHERE. The ATC validated a capability key at startup, refused to
		// run without it, compared it with two other keys for distinctness --
		// and never opened it, because the resolver it exists to build was
		// constructed by no production line. Every jetbridge worker's resolver
		// was nil, so every control call on the output daemon was unreachable.
		// Its sibling hazard: setting the locator rebuilt the storage backend
		// and silently dropped a daemon client set before it.
		It("hands every collaborator it carries through the worker to a created container", func() {
			cfg := jetbridge.NewConfig("test-namespace", "")
			cfg.ArtifactDaemonHostPath = "/var/lib/artifacts"
			clientset := fake.NewSimpleClientset()
			minter, err := executioncontrol.NewCapabilityMinter(
				bytes.Repeat([]byte{7}, executioncontrol.CapabilityKeyBytes),
				time.Minute, nil)
			Expect(err).NotTo(HaveOccurred())

			executor := jetbridge.NewSPDYExecutor(clientset, &rest.Config{Host: "https://kube.invalid"})
			locator := jetbridge.NewArtifactLocator()
			daemonClient := jetbridge.NewDaemonClient(logger, clientset, cfg.Namespace, "artifact-daemon", 7780, nil)
			controls := jetbridge.NewOutputControls(cfg, jetbridge.NewNodeIPResolver(clientset), minter, 7)
			preparer := &recordingPreparer{}

			factory := worker.DefaultFactory{
				DB:                   worker.DB{VolumeRepo: db.NewVolumeRepository(dbConn)},
				K8sClientset:         clientset,
				K8sConfig:            &cfg,
				K8sExecutor:          executor,
				K8sArtifactLocator:   locator,
				K8sDaemonClient:      daemonClient,
				K8sOutputControls:    controls,
				K8sExecutionPreparer: preparer,
			}

			wiring := createTask(factory.NewWorker(logger, dbWorker), "wired-container").Wiring()

			Expect(wiring.Executor).To(BeIdenticalTo(jetbridge.PodExecutor(executor)),
				"no exec-mode I/O: every get, put and check would bake its command into the Pod")
			Expect(wiring.ArtifactLocator).To(BeIdenticalTo(locator),
				"the container records into a private locator the Reaper never drops from")
			Expect(wiring.DaemonClient).To(BeIdenticalTo(daemonClient),
				"the storage backend cannot probe, warm or alias through any artifact daemon")
			Expect(wiring.OutputControls).To(BeIdenticalTo(jetbridge.OutputControlResolver(controls)),
				"the container cannot reach the output daemon on any node, so the capability key "+
					"the ATC refuses to start without is a secret nothing spends")
			Expect(wiring.StartChecked).To(BeTrue(),
				"an exact command would start with no admission check")
			Expect(preparer.prepared).To(Equal([]string{"container", "inputs"}),
				"the owning domain never saw the container it is meant to admit")
		})

		It("leaves every optional collaborator nil when the factory carries none", func() {
			// The control: a deployment with no output plane, no daemon and no
			// admission gate hands every container nils, which is what keeps
			// ordinary execution byte-for-byte what it was.
			cfg := jetbridge.NewConfig("test-namespace", "")
			plain := worker.DefaultFactory{
				DB:           worker.DB{VolumeRepo: db.NewVolumeRepository(dbConn)},
				K8sClientset: fake.NewSimpleClientset(),
				K8sConfig:    &cfg,
			}

			Expect(createTask(plain.NewWorker(logger, dbWorker), "plain-container").Wiring()).
				To(Equal(jetbridge.ContainerWiring{}))
		})
	})
})

// recordingPreparer is the owning domain's admission gate reduced to what the
// wiring test observes: that the worker consulted it. It admits everything.
type recordingPreparer struct{ prepared []string }

func (p *recordingPreparer) PrepareContainer(_ context.Context, _ db.ContainerOwner, _ db.ContainerMetadata, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	p.prepared = append(p.prepared, "container")
	return spec, nil
}

func (p *recordingPreparer) PrepareInputs(_ context.Context, _ db.ContainerOwner, _ string, _ []string, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	p.prepared = append(p.prepared, "inputs")
	return spec, nil
}

func (*recordingPreparer) CheckStart(context.Context, db.ContainerOwner, runtime.ContainerSpec) error {
	return nil
}

func (*recordingPreparer) CheckIntercept(context.Context, string) error { return nil }

func (*recordingPreparer) RecordWitness(context.Context, db.ContainerOwner, executioncontrol.Acknowledgement) error {
	return nil
}
