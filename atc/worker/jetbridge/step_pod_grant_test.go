package jetbridge_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Every step pod gets one identity, derived from its build's row and nothing
// else. These specs run real builds through the worker and read the
// ServiceAccount off the pod it creates.
var _ = Describe("Step pod grants", func() {
	const (
		namespace      = "test-namespace"
		defaultAccount = "step-default"
		brineAccount   = "concourse-brine-live"
		releaseAccount = "jetbridge-releaser"
	)

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		config    jetbridge.Config
		mainTeam  db.Team
		pipeline  db.Pipeline
	)

	grant := func(value string) jetbridge.StepPodGrant {
		GinkgoHelper()
		parsed, err := jetbridge.ParseStepPodGrant(value)
		Expect(err).NotTo(HaveOccurred())
		return parsed
	}

	pipelineConfig := atc.Config{Jobs: atc.JobConfigs{
		{Name: "release", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
		{Name: "unit", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
	}, Resources: atc.ResourceConfigs{{Name: "repo", Type: "git", Source: atc.Source{"uri": "https://example.com/repo"}}},
		Prototypes: atc.Prototypes{{Name: "proto", Type: "registry-image", Source: atc.Source{"repository": "busybox"}}}}

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		clientset = fake.NewSimpleClientset()

		mainTeam, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())
		pipeline, _, err = mainTeam.SavePipeline(atc.PipelineRef{Name: "jetbridge"}, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		config = jetbridge.NewConfig(namespace, "")
		config.ServiceAccount = defaultAccount
		config.StepPodGrants = []jetbridge.StepPodGrant{
			grant("name=brine-live,owner=main/one-off,service-account=" + brineAccount),
			grant("name=release,owner=main/jetbridge/release,service-account=" + releaseAccount),
		}
	})

	newWorker := func(builds jetbridge.StepPodBuilds) *jetbridge.Worker {
		return jetbridge.NewWorker(dbWorker, clientset, config, jetbridge.WorkerDeps{StepPodBuilds: builds})
	}

	// runStep creates and starts one step's container and returns the
	// ServiceAccount of the pod it made.
	runStep := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata, spec runtime.ContainerSpec) (string, error) {
		GinkgoHelper()
		if spec.ImageSpec.ImageURL == "" {
			spec.ImageSpec.ImageURL = "docker:///busybox"
		}
		if spec.TeamID == 0 {
			spec.TeamID = 1
		}
		container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata, spec, &noopDelegate{})
		Expect(err).NotTo(HaveOccurred())
		_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
		if err != nil {
			return "", err
		}
		pods, listErr := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + handle})
		Expect(listErr).NotTo(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))
		return pods.Items[0].Spec.ServiceAccountName, nil
	}

	metadataFor := func(build db.Build, kind db.ContainerType) db.ContainerMetadata {
		return db.ContainerMetadata{
			Type:         kind,
			BuildID:      build.ID(),
			BuildName:    build.Name(),
			PipelineID:   build.PipelineID(),
			PipelineName: build.PipelineName(),
			JobID:        build.JobID(),
			JobName:      build.JobName(),
		}
	}

	jobBuild := func(p db.Pipeline, jobName string) db.Build {
		GinkgoHelper()
		job, found, err := p.Job(jobName)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())
		return build
	}

	It("gives a main team one-off and a main pipeline one-off the one-off grant's ServiceAccount", func() {
		worker := newWorker(database.BuildFactory)

		teamOneOff, err := mainTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		Expect(runStep(worker, "team-one-off", metadataFor(teamOneOff, db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(brineAccount))

		pipelineOneOff, err := pipeline.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		Expect(runStep(worker, "pipeline-one-off", metadataFor(pipelineOneOff, db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(brineAccount))
	})

	It("gives the mapped job its grant, and every other job of that pipeline the default", func() {
		worker := newWorker(database.BuildFactory)

		Expect(runStep(worker, "release-task", metadataFor(jobBuild(pipeline, "release"), db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(releaseAccount))
		Expect(runStep(worker, "unit-task", metadataFor(jobBuild(pipeline, "unit"), db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})

	It("gives another team's one-off the default, whatever team and names its spec and metadata claim", func() {
		worker := newWorker(database.BuildFactory)
		other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other"})
		Expect(err).NotTo(HaveOccurred())
		build, err := other.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		metadata := metadataFor(build, db.ContainerTypeTask)
		metadata.PipelineID = pipeline.ID()
		metadata.PipelineName = "jetbridge"
		metadata.JobName = "release"
		spec := runtime.ContainerSpec{TeamID: mainTeam.ID(), TeamName: "main"}

		Expect(runStep(worker, "other-one-off", metadata, spec)).To(Equal(defaultAccount))
	})

	It("gives every container of a main check build the default", func() {
		worker := newWorker(database.BuildFactory)
		resource, found, err := pipeline.Resource("repo")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		check, created, err := resource.CreateBuild(ctx, true, atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		Expect(runStep(worker, "check", metadataFor(check, db.ContainerTypeCheck), runtime.ContainerSpec{})).To(Equal(defaultAccount))
		// The image-fetch get inside a check build: a get container, but the
		// build is a check, so it owns nothing.
		Expect(runStep(worker, "check-image-get", metadataFor(check, db.ContainerTypeGet), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})

	It("gives a main prototype check build the default, though it has no job and no resource", func() {
		// A prototype check build is a main pipeline build with no job and no
		// resource: only its name says it is a check, and without that it would
		// read as a main one-off.
		worker := newWorker(database.BuildFactory)
		prototype, found, err := pipeline.Prototype("proto")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		check, created, err := prototype.CreateBuild(ctx, true, atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(check.JobID()).To(BeZero())
		Expect(check.ResourceID()).To(BeZero())

		Expect(runStep(worker, "prototype-check-task", metadataFor(check, db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})

	It("gives an instanced pipeline's job the default, though its names match a grant", func() {
		worker := newWorker(database.BuildFactory)
		instanced, _, err := mainTeam.SavePipeline(atc.PipelineRef{Name: "jetbridge", InstanceVars: atc.InstanceVars{"branch": "feature"}}, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		Expect(runStep(worker, "instanced-release", metadataFor(jobBuild(instanced, "release"), db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})

	DescribeTable("no supplied field changes a granted build's ServiceAccount",
		func(kind db.ContainerType, spec runtime.ContainerSpec) {
			worker := newWorker(database.BuildFactory)
			build := jobBuild(pipeline, "release")
			metadata := metadataFor(build, kind)
			metadata.JobName = "unit"
			metadata.PipelineName = "elsewhere"

			Expect(runStep(worker, "supplied-"+string(kind), metadata, spec)).To(Equal(releaseAccount))
		},
		Entry("task", db.ContainerTypeTask, runtime.ContainerSpec{
			Env:      []string{"KUBERNETES_SERVICE_ACCOUNT=" + brineAccount, "SERVICE_ACCOUNT=" + brineAccount},
			Dir:      "/tmp/build/workdir",
			TeamName: "other",
		}),
		Entry("get", db.ContainerTypeGet, runtime.ContainerSpec{Env: []string{"KUBERNETES_SERVICE_ACCOUNT=" + brineAccount}}),
		Entry("put", db.ContainerTypePut, runtime.ContainerSpec{Env: []string{"KUBERNETES_SERVICE_ACCOUNT=" + brineAccount}}),
		Entry("task with sidecars", db.ContainerTypeTask, runtime.ContainerSpec{
			Sidecars: []atc.SidecarConfig{{Name: "sidecar", Image: "busybox", Env: []atc.SidecarEnvVar{{Name: "KUBERNETES_SERVICE_ACCOUNT", Value: brineAccount}}}},
		}),
	)

	It("gives a check container the default even inside a granted job's build", func() {
		worker := newWorker(database.BuildFactory)
		Expect(runStep(worker, "check-in-release", metadataFor(jobBuild(pipeline, "release"), db.ContainerTypeCheck), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})

	It("creates no pod when the build cannot be looked up", func() {
		worker := newWorker(db.NewBuildFactory(closedJetbridgeCloneConn(), database.LockFactory, 0, time.Hour))
		build := jobBuild(pipeline, "release")

		container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner("lookup-fails"), metadataFor(build, db.ContainerTypeTask),
			runtime.ContainerSpec{TeamID: 1, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, &noopDelegate{})
		Expect(err).NotTo(HaveOccurred())
		_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
		Expect(err).To(MatchError(ContainSubstring("resolve step pod identity")))

		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).To(BeEmpty())
	})

	It("leaves every pod on the configured ServiceAccount when no grant is configured", func() {
		config.StepPodGrants = nil
		worker := newWorker(database.BuildFactory)
		oneOff, err := mainTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		Expect(runStep(worker, "no-grants", metadataFor(oneOff, db.ContainerTypeTask), runtime.ContainerSpec{})).To(Equal(defaultAccount))
	})
})

var _ = Describe("ParseStepPodGrant", func() {
	DescribeTable("refuses",
		func(value, reason string) {
			_, err := jetbridge.ParseStepPodGrant(value)
			Expect(err).To(MatchError(ContainSubstring(reason)))
		},
		Entry("an unknown key", "name=a,owner=main/p/j,service-account=sa,privileged=true", `unknown key "privileged"`),
		Entry("another team's one-off", "name=a,owner=other/one-off,service-account=sa", "only team main's one-off"),
		Entry("a two-segment job", "name=a,owner=main/p,service-account=sa", "want <team>/<pipeline>/<job>"),
		Entry("a non-identifier segment", "name=a,owner=main/../j,service-account=sa", `".." is not an identifier`),
		Entry("an invalid ServiceAccount", "name=a,owner=main/p/j,service-account=Not_A_Name", "service-account"),
		Entry("a repeated key", "name=a,name=b,owner=main/p/j,service-account=sa", "name given twice"),
	)

	It("refuses two grants with one owner or one name", func() {
		one, err := jetbridge.ParseStepPodGrant("name=a,owner=main/p/j,service-account=sa")
		Expect(err).NotTo(HaveOccurred())
		sameOwner, err := jetbridge.ParseStepPodGrant("name=b,owner=main/p/j,service-account=other")
		Expect(err).NotTo(HaveOccurred())
		sameName, err := jetbridge.ParseStepPodGrant("name=a,owner=main/p/k,service-account=other")
		Expect(err).NotTo(HaveOccurred())

		Expect(jetbridge.ValidateStepPodGrants([]jetbridge.StepPodGrant{one, sameOwner})).To(MatchError(ContainSubstring("both name owner main/p/j")))
		Expect(jetbridge.ValidateStepPodGrants([]jetbridge.StepPodGrant{one, sameName})).To(MatchError(ContainSubstring(`"a" is configured twice`)))
	})
})
