package jetbridge_test

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Registrar", func() {
	var (
		ctx              context.Context
		fakeClientset    *fake.Clientset
		fakeWorkerFactory *dbfakes.FakeWorkerFactory
		cfg              jetbridge.Config
		registrar        *jetbridge.Registrar
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
		fakeWorkerFactory = new(dbfakes.FakeWorkerFactory)
		cfg = jetbridge.NewConfig("test-namespace", "")

		testLogger := lagertest.NewTestLogger("registrar")
		registrar = jetbridge.NewRegistrar(testLogger, fakeClientset, cfg, fakeWorkerFactory)
	})

	Describe("Register", func() {
		It("saves a worker to the database with the correct attributes", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeWorkerFactory.SaveWorkerCallCount()).To(Equal(1))
			savedWorker, ttl := fakeWorkerFactory.SaveWorkerArgsForCall(0)

			By("using a name derived from the namespace")
			Expect(savedWorker.Name).To(ContainSubstring("test-namespace"))

			By("setting the platform to linux")
			Expect(savedWorker.Platform).To(Equal("linux"))

			By("setting state to running")
			Expect(savedWorker.State).To(Equal("running"))

			By("using a non-zero TTL")
			Expect(ttl).To(BeNumerically(">", 0))
		})

		It("reports active containers by counting Pods in the namespace", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-pod",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"concourse.ci/worker": "k8s-test-namespace",
					},
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			err = registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(1))
		})

		It("only counts Pods with the worker label", func() {
			labelledPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "labelled-pod",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"concourse.ci/worker": "k8s-test-namespace",
					},
				},
			}
			unlabelledPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unlabelled-pod",
					Namespace: "test-namespace",
				},
			}
			_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, labelledPod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())
			_, err = fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, unlabelledPod, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			err = registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(1))
		})

		It("counts multiple labelled Pods", func() {
			for i := 0; i < 3; i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("pod-%d", i),
						Namespace: "test-namespace",
						Labels: map[string]string{
							"concourse.ci/worker": "k8s-test-namespace",
						},
					},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			}

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(3))
		})

		It("reports zero active containers when no Pods exist", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(savedWorker.ActiveContainers).To(Equal(0))
		})

		It("propagates SaveWorker errors", func() {
			fakeWorkerFactory.SaveWorkerReturns(nil, fmt.Errorf("db connection lost"))

			err := registrar.Register(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("db connection lost"))
		})
	})

	Describe("Heartbeat", func() {
		It("calls SaveWorker to refresh the TTL", func() {
			err := registrar.Heartbeat(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeWorkerFactory.SaveWorkerCallCount()).To(Equal(1))
			_, ttl := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			Expect(ttl).To(BeNumerically(">", 0))
		})
	})

	Describe("ResourceTypes registration", func() {
		It("registers default resource types when no overrides are set", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			typeNames := make([]string, len(savedWorker.ResourceTypes))
			for i, rt := range savedWorker.ResourceTypes {
				typeNames[i] = rt.Type
			}
			Expect(typeNames).To(ContainElements("git", "registry-image", "time", "s3"))
		})

		It("registers custom resource types when overrides are set", func() {
			cfg.ResourceTypeImages = jetbridge.MergeResourceTypeImages([]string{
				"git=my-registry/custom-git",
				"custom-type=my-registry/custom",
			})
			testLogger := lagertest.NewTestLogger("registrar")
			registrar = jetbridge.NewRegistrar(testLogger, fakeClientset, cfg, fakeWorkerFactory)

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker, _ := fakeWorkerFactory.SaveWorkerArgsForCall(0)
			typeMap := make(map[string]string)
			for _, rt := range savedWorker.ResourceTypes {
				typeMap[rt.Type] = rt.Image
			}

			By("overridden type uses custom image")
			Expect(typeMap).To(HaveKeyWithValue("git", "my-registry/custom-git"))

			By("new type is added")
			Expect(typeMap).To(HaveKeyWithValue("custom-type", "my-registry/custom"))

			By("other defaults still present")
			Expect(typeMap).To(HaveKeyWithValue("registry-image", "concourse/registry-image-resource"))
		})
	})

	Describe("WorkerName", func() {
		It("returns a deterministic name based on the namespace", func() {
			name := registrar.WorkerName()
			Expect(name).To(Equal("k8s-test-namespace"))
		})
	})
})

// workerVersion is used for testing; in production this comes from the binary.
func init() {
	// Ensure the fake worker factory returns no error by default.
	// The FakeWorkerFactory's SaveWorkerReturns is set to nil, nil by default.
}
