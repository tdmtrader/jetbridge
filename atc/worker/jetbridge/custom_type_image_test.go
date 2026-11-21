package jetbridge_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The image a step pod runs when the step names a resource type rather than
// an image reference. A custom type whose image was fetched through a type
// other than registry-image (resource -> level-a -> level-b -> registry-image)
// reaches the runtime with only its own name and the fetching type
// (ImageSpec.FetchedImageType): the get produced a rootfs artifact, which a
// pod cannot run. k8s-behavioral spec 6.2 saw that pod pull
// docker.io/library/level-a:latest, an image nobody published, and fail as
// ErrImagePull. Such a step is refused before any pod exists. A type name
// that stands for no fetched image is still pulled by name, as before.
var _ = Describe("A step pod's image for a resource type", func() {
	var (
		ctx           context.Context
		fakeClientset *fake.Clientset
		config        jetbridge.Config
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeClientset = fake.NewSimpleClientset()
		config = jetbridge.NewConfig("ci-namespace", "")
	})

	run := func(containerType db.ContainerType, imageSpec runtime.ImageSpec) error {
		GinkgoHelper()
		database := useJetbridgeDB()
		dbWorker, err := persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		_, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())

		worker := jetbridge.NewWorker(dbWorker, fakeClientset, config, jetbridge.WorkerDeps{Executor: &fakeExecExecutor{}})
		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner("step-under-test"),
			db.ContainerMetadata{Type: containerType},
			runtime.ContainerSpec{
				TeamID:    1,
				TeamName:  "main",
				Type:      containerType,
				ImageSpec: imageSpec,
			},
			&noopDelegate{},
		)
		Expect(err).NotTo(HaveOccurred())

		_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/opt/resource/check"}, runtime.ProcessIO{})
		return err
	}

	pods := func() []corev1.Pod {
		GinkgoHelper()
		list, err := fakeClientset.CoreV1().Pods("ci-namespace").List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		return list.Items
	}

	It("pulls an undeclared type no mapping holds by its name, as before", func() {
		Expect(run(db.ContainerTypeCheck, runtime.ImageSpec{ResourceType: "github-release"})).To(Succeed())
		Expect(pods()).To(HaveLen(1))
		Expect(pods()[0].Spec.Containers[0].Image).To(Equal("github-release"))
	})

	It("refuses a type whose image was fetched through a custom type, naming both, and makes no pod", func() {
		err := run(db.ContainerTypeCheck, runtime.ImageSpec{ResourceType: "level-a", FetchedImageType: "level-b"})
		Expect(err).To(MatchError(And(
			ContainSubstring(`resource type "level-a" has no registry image: its image was produced by resource type "level-b"`),
			ContainSubstring("--kubernetes-base-resource-type level-a=<image>"),
			ContainSubstring("kubernetes.baseResourceTypes"),
		)))
		Expect(pods()).To(BeEmpty(), "no pod may try to pull the type's name as an image")
	})

	It("refuses a custom type declared on a non-registry base type, naming that base type", func() {
		err := run(db.ContainerTypeGet, runtime.ImageSpec{ResourceType: "git-with-ado", FetchedImageType: "docker-image"})
		Expect(err).To(MatchError(ContainSubstring(`resource type "git-with-ado" has no registry image: its image was produced by resource type "docker-image"`)))
		Expect(pods()).To(BeEmpty())
	})

	It("runs the image an operator mapped to a fetched type's name", func() {
		config.ResourceTypeImages = imageresolver.MergeResourceTypeImages([]string{"level-a=registry.example/level-a:1"})
		Expect(run(db.ContainerTypeCheck, runtime.ImageSpec{ResourceType: "level-a", FetchedImageType: "level-b"})).To(Succeed())
		Expect(pods()).To(HaveLen(1))
		Expect(pods()[0].Spec.Containers[0].Image).To(Equal("registry.example/level-a:1"))
	})

	It("refuses a task image_resource fetched through another type, naming that type, and consults no mapping", func() {
		config.ResourceTypeImages = imageresolver.MergeResourceTypeImages([]string{"image=registry.example/never:1"})
		err := run(db.ContainerTypeTask, runtime.ImageSpec{FetchedImageType: "docker-image"})
		Expect(err).To(MatchError(And(
			ContainSubstring(`task image_resource of type "docker-image" yields no registry image`),
			ContainSubstring("use type: registry-image"),
			Not(ContainSubstring("--kubernetes-base-resource-type")),
		)))
		Expect(pods()).To(BeEmpty())
	})

	It("refuses a task whose image: artifact carries no registry reference, saying so", func() {
		err := run(db.ContainerTypeTask, runtime.ImageSpec{ImageArtifact: &fakeArtifact{handle: "rootfs"}})
		Expect(err).To(MatchError(ContainSubstring("the task's image: artifact is not a registry image")))
		Expect(pods()).To(BeEmpty())
	})
})
