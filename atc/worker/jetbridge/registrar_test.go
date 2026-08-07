package jetbridge_test

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	concourse "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Registrar", func() {
	var (
		ctx           context.Context
		database      jetbridgeDB
		fakeClientset *fake.Clientset
		cfg           jetbridge.Config
		registrar     *jetbridge.Registrar
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")

		testLogger := lagertest.NewTestLogger("registrar")
		registrar = jetbridge.NewRegistrar(testLogger, fakeClientset, cfg, database.WorkerFactory)
	})

	reloadWorker := func() db.Worker {
		GinkgoHelper()
		worker, found, err := database.WorkerFactory.GetWorker(registrar.WorkerName())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		reloaded, err := worker.Reload()
		Expect(err).ToNot(HaveOccurred())
		Expect(reloaded).To(BeTrue())
		return worker
	}

	Describe("Register", func() {
		It("saves a worker to the database with the correct attributes", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker := reloadWorker()

			By("using a name derived from the namespace")
			Expect(savedWorker.Name()).To(Equal("k8s-test-namespace"))
			Expect(savedWorker.Version()).ToNot(BeNil())
			Expect(*savedWorker.Version()).To(Equal(concourse.WorkerVersion))

			By("setting the platform to linux")
			Expect(savedWorker.Platform()).To(Equal("linux"))

			By("setting state to running")
			Expect(savedWorker.State()).To(Equal(db.WorkerStateRunning))

			By("persisting the complete global-worker identity")
			Expect(savedWorker.ActiveContainers()).To(Equal(0))
			Expect(savedWorker.ActiveVolumes()).To(Equal(0))
			Expect(savedWorker.Tags()).To(BeEmpty())
			Expect(savedWorker.TeamID()).To(Equal(0))
			Expect(savedWorker.TeamName()).To(BeEmpty())
			Expect(savedWorker.StartTime().Unix()).To(Equal(int64(0)))
			Expect(savedWorker.Ephemeral()).To(BeFalse())

			By("using a non-zero TTL")
			Expect(savedWorker.ExpiresAt()).To(BeTemporally(">", time.Now()))
			Expect(savedWorker.ExpiresAt()).To(BeTemporally("<", time.Now().Add(time.Minute)))
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

			Expect(reloadWorker().ActiveContainers()).To(Equal(1))
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

			Expect(reloadWorker().ActiveContainers()).To(Equal(1))
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

			Expect(reloadWorker().ActiveContainers()).To(Equal(3))
		})

		It("reports zero active containers when no Pods exist", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(reloadWorker().ActiveContainers()).To(Equal(0))
		})

		It("propagates SaveWorker errors", func() {
			closedConn := closedJetbridgeCloneConn()
			closedFactory := db.NewWorkerFactory(
				closedConn,
				db.NewStaticWorkerCache(lagertest.NewTestLogger("closed-worker-cache"), closedConn, 0),
			)
			registrar = jetbridge.NewRegistrar(
				lagertest.NewTestLogger("registrar"), fakeClientset, cfg, closedFactory,
			)

			err := registrar.Register(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("saving worker"))
		})
	})

	Describe("Heartbeat", func() {
		It("calls SaveWorker to refresh the TTL", func() {
			Expect(registrar.Register(ctx)).To(Succeed())
			_, err := database.Conn.Exec(
				`UPDATE workers SET expires = NOW() - INTERVAL '1 minute' WHERE name = $1`,
				registrar.WorkerName(),
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(registrar.Heartbeat(ctx)).To(Succeed())
			Expect(reloadWorker().ExpiresAt()).To(BeTemporally(">", time.Now()))
		})
	})

	Describe("ResourceTypes registration", func() {
		It("registers default resource types when no overrides are set", func() {
			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker := reloadWorker()
			typeNames := make([]string, len(savedWorker.ResourceTypes()))
			for i, rt := range savedWorker.ResourceTypes() {
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
			registrar = jetbridge.NewRegistrar(testLogger, fakeClientset, cfg, database.WorkerFactory)

			err := registrar.Register(ctx)
			Expect(err).ToNot(HaveOccurred())

			savedWorker := reloadWorker()
			typeMap := make(map[string]string)
			for _, rt := range savedWorker.ResourceTypes() {
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
