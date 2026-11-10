package jetbridge_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The privilege gate at creation. --kubernetes-step-privilege=granted admits a
// privileged step pod only under a step pod grant that declares
// privileged=true; any, the default, admits privilege as the task or resource
// type asks, whatever the grants say. Each spec runs real builds through a
// worker of each step runtime and reads the pod, or the step Job's pod
// template, it created.
var _ = Describe("The privilege gate", func() {
	const (
		namespace      = "test-namespace"
		defaultAccount = "step-default"
		releaseAccount = "jetbridge-releaser"
	)

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		config    jetbridge.Config
		pipeline  db.Pipeline
	)

	pipelineConfig := atc.Config{Jobs: atc.JobConfigs{
		{Name: "dind", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
		{Name: "release", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
		{Name: "unit", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
	}, Resources: atc.ResourceConfigs{{Name: "repo", Type: "git", Source: atc.Source{"uri": "https://example.com/repo"}}}}

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		clientset = fake.NewSimpleClientset()

		mainTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())
		pipeline, _, err = mainTeam.SavePipeline(atc.PipelineRef{Name: "jetbridge"}, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		config = jetbridge.NewConfig(namespace, "")
		config.ServiceAccount = defaultAccount
		for _, value := range []string{
			"name=dind,owner=main/jetbridge/dind,privileged=true",
			"name=release,owner=main/jetbridge/release,service-account=" + releaseAccount,
		} {
			grant, err := jetbridge.ParseStepPodGrant(value)
			Expect(err).NotTo(HaveOccurred())
			config.StepPodGrants = append(config.StepPodGrants, grant)
		}
	})

	jobBuild := func(name string) db.Build {
		GinkgoHelper()
		job, found, err := pipeline.Job(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())
		return build
	}

	checkBuild := func() db.Build {
		GinkgoHelper()
		resource, found, err := pipeline.Resource("repo")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build, created, err := resource.CreateBuild(ctx, true, atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return build
	}

	metadataFor := func(build db.Build, kind db.ContainerType) db.ContainerMetadata {
		return db.ContainerMetadata{
			Type: kind, StepName: "s", BuildID: build.ID(), BuildName: build.Name(),
			PipelineID: build.PipelineID(), PipelineName: build.PipelineName(),
			JobID: build.JobID(), JobName: build.JobName(),
		}
	}

	for _, stepRuntime := range []string{jetbridge.StepRuntimeExec, jetbridge.StepRuntimeJob} {
		stepRuntime := stepRuntime

		Context("on a worker of the "+stepRuntime+" step runtime", func() {
			newWorker := func(mode string) *jetbridge.Worker {
				config.StepPrivilege = mode
				if stepRuntime == jetbridge.StepRuntimeJob {
					config.StepRuntime = jetbridge.StepRuntimeJob
					config.LauncherImage = "registry.example/jetbridge:1"
					config.StepEnvelopeURL = "http://web.test-namespace.svc:8080/runtime/v1/step-envelope"
					config.ArtifactDaemonHostPath = "/var/concourse/artifacts"
				}
				return jetbridge.NewWorker(dbWorker, clientset, config, jetbridge.WorkerDeps{
					StepPodBuilds: database.BuildFactory,
					StepEnvelopes: jetbridge.NewStepEnvelopes(),
				})
			}

			// run creates and starts one step's container and returns the pod
			// spec it created: the pod itself, or the step Job's pod template.
			run := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata, spec runtime.ContainerSpec) (*corev1.PodSpec, error) {
				GinkgoHelper()
				spec.Type = metadata.Type
				if spec.ImageSpec.ImageURL == "" {
					spec.ImageSpec.ImageURL = "docker:///busybox"
				}
				spec.TeamID = 1
				container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata, spec, &noopDelegate{})
				Expect(err).NotTo(HaveOccurred())
				_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
				selector := metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + handle}
				pods, listErr := clientset.CoreV1().Pods(namespace).List(ctx, selector)
				Expect(listErr).NotTo(HaveOccurred())
				jobs, listErr := clientset.BatchV1().Jobs(namespace).List(ctx, selector)
				Expect(listErr).NotTo(HaveOccurred())
				if err != nil {
					Expect(pods.Items).To(BeEmpty(), "a refused step created a Pod")
					Expect(jobs.Items).To(BeEmpty(), "a refused step created a Job")
					return nil, err
				}
				if stepRuntime == jetbridge.StepRuntimeJob {
					Expect(jobs.Items).To(HaveLen(1))
					Expect(pods.Items).To(BeEmpty())
					return &jobs.Items[0].Spec.Template.Spec, nil
				}
				Expect(pods.Items).To(HaveLen(1))
				return &pods.Items[0].Spec, nil
			}

			privilegedSpec := func() runtime.ContainerSpec {
				return runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{Privileged: true}}
			}

			mainOf := func(pod *corev1.PodSpec) corev1.Container {
				GinkgoHelper()
				for _, container := range pod.Containers {
					if container.Name == "main" {
						return container
					}
				}
				Fail("the pod has no main container")
				return corev1.Container{}
			}

			isPrivileged := func(container corev1.Container) bool {
				return container.SecurityContext != nil && container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged
			}

			Describe("under granted", func() {
				DescribeTable("refuses privilege outside a privileged grant, naming the owner and the grant it lacks, and creates nothing",
					func(job string, kind db.ContainerType, step string, want string) {
						worker := newWorker(jetbridge.StepPrivilegeGranted)
						metadata := metadataFor(jobBuild(job), kind)
						metadata.StepName = step
						_, err := run(worker, "refused-"+job+"-"+step, metadata, privilegedSpec())
						Expect(err).To(MatchError(ContainSubstring(`container "main" is privileged`)))
						Expect(err).To(MatchError(ContainSubstring(want)))
						Expect(err).To(MatchError(ContainSubstring("--kubernetes-step-privilege=granted")))
					},
					Entry("a task of a job with no grant", "unit", db.ContainerTypeTask, "t", "Owner main/jetbridge/unit has no step pod grant"),
					Entry("a get of a job with no grant", "unit", db.ContainerTypeGet, "repo", "Owner main/jetbridge/unit has no step pod grant"),
					Entry("a put of a job with no grant", "unit", db.ContainerTypePut, "push", "Owner main/jetbridge/unit has no step pod grant"),
					// An image fetch is a get in the task's own build, privileged
					// when the image resource's type is.
					Entry("an image fetch of a job with no grant", "unit", db.ContainerTypeGet, "image", "Owner main/jetbridge/unit has no step pod grant"),
					Entry("a task under a grant that does not declare privileged=true", "release", db.ContainerTypeTask, "t",
						`Owner main/jetbridge/release's step pod grant "release" does not declare privileged=true`),
				)

				DescribeTable("runs privileged inside a privileged=true grant",
					func(kind db.ContainerType) {
						worker := newWorker(jetbridge.StepPrivilegeGranted)
						pod, err := run(worker, "granted-"+string(kind), metadataFor(jobBuild("dind"), kind), privilegedSpec())
						Expect(err).NotTo(HaveOccurred())
						Expect(isPrivileged(mainOf(pod))).To(BeTrue())
					},
					Entry("a task", db.ContainerTypeTask),
					Entry("a get", db.ContainerTypeGet),
					Entry("a put", db.ContainerTypePut),
				)

				It("refuses a privileged check, which holds no step pod grant, even in a privileged grant's build", func() {
					worker := newWorker(jetbridge.StepPrivilegeGranted)
					_, err := run(worker, "refused-check", metadataFor(checkBuild(), db.ContainerTypeCheck), privilegedSpec())
					Expect(err).To(MatchError(ContainSubstring("A check holds no step pod grant")))

					_, err = run(worker, "refused-check-in-dind", metadataFor(jobBuild("dind"), db.ContainerTypeCheck), privilegedSpec())
					Expect(err).To(MatchError(ContainSubstring("A check holds no step pod grant")))
				})

				It("admits an unprivileged step of any kind outside every grant", func() {
					worker := newWorker(jetbridge.StepPrivilegeGranted)
					for handle, metadata := range map[string]db.ContainerMetadata{
						"plain-task":  metadataFor(jobBuild("unit"), db.ContainerTypeTask),
						"plain-get":   metadataFor(jobBuild("unit"), db.ContainerTypeGet),
						"plain-check": metadataFor(checkBuild(), db.ContainerTypeCheck),
					} {
						pod, err := run(worker, handle, metadata, runtime.ContainerSpec{})
						Expect(err).NotTo(HaveOccurred(), handle)
						Expect(isPrivileged(mainOf(pod))).To(BeFalse(), handle)
					}
				})

				It("keeps a sidecar unprivileged inside a privileged grant", func() {
					worker := newWorker(jetbridge.StepPrivilegeGranted)
					spec := privilegedSpec()
					spec.Sidecars = []atc.SidecarConfig{{Name: "docker", Image: "docker:dind"}}
					pod, err := run(worker, "granted-sidecar", metadataFor(jobBuild("dind"), db.ContainerTypeTask), spec)
					Expect(err).NotTo(HaveOccurred())
					Expect(isPrivileged(mainOf(pod))).To(BeTrue())

					var sidecar *corev1.Container
					for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
						if container.Name == "docker" {
							sidecar = &container
						}
					}
					Expect(sidecar).NotTo(BeNil(), "the sidecar is in the pod")
					Expect(isPrivileged(*sidecar)).To(BeFalse())
					Expect(sidecar.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
					Expect(*sidecar.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
				})
			})

			// The upgrade-safety spec: a deployment that set privileged: true on
			// a grant, and never chose granted, sees exactly what it saw before
			// the gate existed.
			Describe("under any, with privileged grants configured", func() {
				for _, mode := range []string{"", jetbridge.StepPrivilegeAny} {
					mode := mode
					It("admits every privileged step as before, inside and outside the grants (mode "+map[string]string{"": "unset", "any": "any"}[mode]+")", func() {
						worker := newWorker(mode)
						for handle, metadata := range map[string]db.ContainerMetadata{
							"any-unit-task":    metadataFor(jobBuild("unit"), db.ContainerTypeTask),
							"any-unit-get":     metadataFor(jobBuild("unit"), db.ContainerTypeGet),
							"any-unit-put":     metadataFor(jobBuild("unit"), db.ContainerTypePut),
							"any-release-task": metadataFor(jobBuild("release"), db.ContainerTypeTask),
							"any-dind-task":    metadataFor(jobBuild("dind"), db.ContainerTypeTask),
							"any-check":        metadataFor(checkBuild(), db.ContainerTypeCheck),
						} {
							pod, err := run(worker, handle, metadata, privilegedSpec())
							Expect(err).NotTo(HaveOccurred(), handle)
							Expect(isPrivileged(mainOf(pod))).To(BeTrue(), handle)
						}
					})
				}
			})
		})
	}
})
