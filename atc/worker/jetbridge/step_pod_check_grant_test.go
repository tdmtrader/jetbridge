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
	. "github.com/onsi/gomega/gstruct"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Check eligibility under --kubernetes-step-privilege=granted. A privileged
// check pod is admitted only when a privileged step pod grant names the
// resource type whose image it runs, in its own pipeline:
// owner=<team>/<pipeline>/resource-type/<type>. The type is the one the
// step's plan runs, which the engine sets on the container spec
// (StepResourceType): a check of resource R runs R's type; the check of
// resource type T, and the fetch of T's image, run T's type; a task's
// image_resource check runs the image resource's type. Exactly one owner per
// pod. Runs, one-off builds, instanced pipelines and base types stay
// ungrantable.
var _ = Describe("Check eligibility", func() {
	const namespace = "test-namespace"

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		config    jetbridge.Config
		mainTeam  db.Team
		pipeline  db.Pipeline
	)

	// dind and other are privileged; child is built on dind. shared is both a
	// resource and a resource type, its type's parent a base type. A resource
	// named image stands where a task's image_resource check is named.
	pipelineConfig := atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{Name: "dind", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "example/dind"}},
			{Name: "other", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "example/other"}},
			{Name: "child", Type: "dind", Source: atc.Source{"repository": "example/child"}},
			{Name: "shared", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "example/shared"}},
		},
		Resources: atc.ResourceConfigs{
			{Name: "repo", Type: "dind", Source: atc.Source{"uri": "https://example.com/repo"}},
			{Name: "shared", Type: "shared", Source: atc.Source{"uri": "https://example.com/shared"}},
			{Name: "image", Type: "dind", Source: atc.Source{"uri": "https://example.com/image"}},
			{Name: "plain", Type: "git", Source: atc.Source{"uri": "https://example.com/plain"}},
		},
		Jobs: atc.JobConfigs{
			{Name: "unit", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
		},
	}

	grant := func(value string) jetbridge.StepPodGrant {
		GinkgoHelper()
		parsed, err := jetbridge.ParseStepPodGrant(value)
		Expect(err).NotTo(HaveOccurred())
		return parsed
	}

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
		config.ServiceAccount = "step-default"
		config.StepPodGrants = []jetbridge.StepPodGrant{
			grant("name=dind-checks,owner=main/jetbridge/resource-type/dind,privileged=true"),
			grant("name=shared-checks,owner=main/jetbridge/resource-type/shared,privileged=true"),
			grant("name=unit,owner=main/jetbridge/unit,privileged=true"),
		}
	})

	savePipeline := func(team db.Team, ref atc.PipelineRef) db.Pipeline {
		GinkgoHelper()
		saved, _, err := team.SavePipeline(ref, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		return saved
	}

	// inMemoryCheck is the metadata of a container in a check build of a
	// resource, which runs in memory with no builds row: its build is named
	// check, and its pipeline is the checked resource's.
	inMemoryCheck := func(p db.Pipeline, kind db.ContainerType, step string) db.ContainerMetadata {
		return db.ContainerMetadata{
			Type: kind, StepName: step, BuildID: 0, BuildName: db.CheckBuildName,
			PipelineID: p.ID(), PipelineName: p.Name(),
		}
	}

	jobBuildMetadata := func(kind db.ContainerType, step string) db.ContainerMetadata {
		GinkgoHelper()
		job, found, err := pipeline.Job("unit")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())
		return db.ContainerMetadata{Type: kind, StepName: step, BuildID: build.ID(), BuildName: build.Name(),
			PipelineID: build.PipelineID(), PipelineName: build.PipelineName(), JobID: build.JobID(), JobName: build.JobName()}
	}

	for _, stepRuntime := range []string{jetbridge.StepRuntimeExec, jetbridge.StepRuntimeJob} {
		stepRuntime := stepRuntime

		Context("on a worker of the "+stepRuntime+" step runtime", func() {
			newWorkerWith := func(mode string, builds jetbridge.StepPodBuilds, pipelines jetbridge.StepPodPipelines) *jetbridge.Worker {
				config.StepPrivilege = mode
				if stepRuntime == jetbridge.StepRuntimeJob {
					config.StepRuntime = jetbridge.StepRuntimeJob
					config.LauncherImage = "registry.example/jetbridge:1"
					config.StepEnvelopeURL = "http://web.test-namespace.svc:8080/runtime/v1/step-envelope"
					config.ArtifactDaemonHostPath = "/var/concourse/artifacts"
				}
				return jetbridge.NewWorker(dbWorker, clientset, config, jetbridge.WorkerDeps{
					StepPodBuilds:    builds,
					StepPodPipelines: pipelines,
					StepEnvelopes:    jetbridge.NewStepEnvelopes(),
				})
			}
			newWorker := func(mode string) *jetbridge.Worker {
				return newWorkerWith(mode, database.BuildFactory, jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory))
			}

			// runAs starts one step whose plan runs typeName and returns the pod
			// (or the step Job's pod template) and its annotations; a refusal
			// creates neither.
			runAs := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata, typeName string, privileged bool) (*corev1.PodSpec, map[string]string, error) {
				GinkgoHelper()
				spec := runtime.ContainerSpec{
					Type: metadata.Type, TeamID: 1, StepResourceType: typeName,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: privileged},
				}
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
					return nil, nil, err
				}
				if stepRuntime == jetbridge.StepRuntimeJob {
					Expect(jobs.Items).To(HaveLen(1))
					return &jobs.Items[0].Spec.Template.Spec, jobs.Items[0].Spec.Template.Annotations, nil
				}
				Expect(pods.Items).To(HaveLen(1))
				return &pods.Items[0].Spec, pods.Items[0].Annotations, nil
			}
			run := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata, typeName string) (*corev1.PodSpec, map[string]string, error) {
				GinkgoHelper()
				return runAs(worker, handle, metadata, typeName, true)
			}

			mainPrivileged := func(pod *corev1.PodSpec) bool {
				for _, container := range pod.Containers {
					if container.Name == "main" {
						return container.SecurityContext != nil && container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged
					}
				}
				return false
			}

			Describe("under granted", func() {
				It("admits a check of a resource whose privileged type a privileged grant names, and marks the pod with the grant", func() {
					pod, annotations, err := run(newWorker(jetbridge.StepPrivilegeGranted), "repo-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).NotTo(HaveOccurred())
					Expect(mainPrivileged(pod)).To(BeTrue())
					Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "dind-checks"))
					Expect(pod.ServiceAccountName).To(Equal("step-default"), "a check grant gives privilege, never a ServiceAccount")
					Expect(pod.AutomountServiceAccountToken).To(PointTo(BeFalse()))
				})

				It("admits the check of a resource type built on the granted type, and the fetch of its image", func() {
					worker := newWorker(jetbridge.StepPrivilegeGranted)
					// child's own check, and the image get of child inside a
					// check build, both run dind.
					for handle, metadata := range map[string]db.ContainerMetadata{
						"child-check":     inMemoryCheck(pipeline, db.ContainerTypeCheck, "child"),
						"child-image-get": inMemoryCheck(pipeline, db.ContainerTypeGet, "child"),
					} {
						pod, annotations, err := run(worker, handle, metadata, "dind")
						Expect(err).NotTo(HaveOccurred(), handle)
						Expect(mainPrivileged(pod)).To(BeTrue(), handle)
						Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "dind-checks"), handle)
					}
				})

				It("owns a check by the type its plan runs when a resource and a resource type share its name", func() {
					pod, annotations, err := run(newWorker(jetbridge.StepPrivilegeGranted), "shared-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "shared"), "shared")
					Expect(err).NotTo(HaveOccurred())
					Expect(mainPrivileged(pod)).To(BeTrue())
					Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "shared-checks"))
				})

				It("admits a task image_resource check of a granted type in a job build, whatever the pipeline's resource named image is", func() {
					// Only other is granted; the pipeline's resource named image
					// is of dind, which is not.
					config.StepPodGrants = []jetbridge.StepPodGrant{grant("name=other-checks,owner=main/jetbridge/resource-type/other,privileged=true")}
					_, annotations, err := run(newWorker(jetbridge.StepPrivilegeGranted), "image-check", jobBuildMetadata(db.ContainerTypeCheck, "image"), "other")
					Expect(err).NotTo(HaveOccurred())
					Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "other-checks"))
				})

				It("refuses a task image_resource check of an ungranted type, though the pipeline's resource named image is of a granted one", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "image-check-other", jobBuildMetadata(db.ContainerTypeCheck, "image"), "other")
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/resource-type/other has no step pod grant")))
				})

				It("refuses a get in a check build of an ungranted type", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "other-image-get", inMemoryCheck(pipeline, db.ContainerTypeGet, "repo"), "other")
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/resource-type/other has no step pod grant")))
				})

				It("admits a check of a granted type whose check build has a builds row, or an id but no row", func() {
					resource, found, err := pipeline.Resource("repo")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					checkBuild, created, err := resource.CreateBuild(ctx, true, atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					rowCheck := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "repo", BuildID: checkBuild.ID(), BuildName: checkBuild.Name(),
						PipelineID: checkBuild.PipelineID(), PipelineName: checkBuild.PipelineName()}
					idOnly := inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo")
					idOnly.BuildID = 987654321

					worker := newWorker(jetbridge.StepPrivilegeGranted)
					for handle, metadata := range map[string]db.ContainerMetadata{"row-check": rowCheck, "id-only-check": idOnly} {
						_, annotations, err := run(worker, handle, metadata, "dind")
						Expect(err).NotTo(HaveOccurred(), handle)
						Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "dind-checks"), handle)
					}
				})

				It("refuses the same type's check in another pipeline and in another team's pipeline of the same name", func() {
					other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other"})
					Expect(err).NotTo(HaveOccurred())
					worker := newWorker(jetbridge.StepPrivilegeGranted)

					_, _, err = run(worker, "other-pipeline", inMemoryCheck(savePipeline(mainTeam, atc.PipelineRef{Name: "elsewhere"}), db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("Owner main/elsewhere/resource-type/dind has no step pod grant")))

					_, _, err = run(worker, "other-team", inMemoryCheck(savePipeline(other, atc.PipelineRef{Name: "jetbridge"}), db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("Owner other/jetbridge/resource-type/dind has no step pod grant")))
				})

				It("refuses a privileged check whose type no grant names", func() {
					config.StepPodGrants = config.StepPodGrants[1:]
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "ungranted", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/resource-type/dind has no step pod grant")))
				})

				It("refuses a privileged check of a base type, which no pipeline declares", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "base", inMemoryCheck(pipeline, db.ContainerTypeCheck, "plain"), "git")
					Expect(err).To(MatchError(ContainSubstring(`"git" is a base resource type`)))
				})

				It("refuses a privileged check whose spec names no resource type", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "no-type", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "")
					Expect(err).To(MatchError(ContainSubstring("The engine named no resource type for this check step")))
				})

				It("refuses a privileged check that belongs to no pipeline", func() {
					metadata := inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo")
					metadata.PipelineID = 0
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "no-pipeline", metadata, "dind")
					Expect(err).To(MatchError(ContainSubstring("The check belongs to no pipeline")))
				})

				It("refuses a privileged check, creating nothing, when its pipeline cannot be looked up", func() {
					worker := newWorkerWith(jetbridge.StepPrivilegeGranted, database.BuildFactory,
						jetbridge.NewStepPodPipelines(closedJetbridgeCloneConn(), database.LockFactory))
					_, _, err := run(worker, "lookup-fails", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("resolve step pod identity for pipeline")))
				})

				It("looks nothing up for an unprivileged check, so a failing lookup cannot fail it", func() {
					builds := &countingBuilds{StepPodBuilds: db.NewBuildFactory(closedJetbridgeCloneConn(), database.LockFactory, 0, time.Hour)}
					pipelines := &countingPipelines{StepPodPipelines: jetbridge.NewStepPodPipelines(closedJetbridgeCloneConn(), database.LockFactory)}
					worker := newWorkerWith(jetbridge.StepPrivilegeGranted, builds, pipelines)
					metadata := inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo")
					metadata.BuildID = 987654321
					pod, annotations, err := runAs(worker, "plain-check", metadata, "dind", false)
					Expect(err).NotTo(HaveOccurred())
					Expect(mainPrivileged(pod)).To(BeFalse())
					Expect(annotations).NotTo(HaveKey("concourse.dev/step-pod-grant"))
					Expect(builds.calls).To(BeZero())
					Expect(pipelines.calls).To(BeZero())
				})

				It("refuses an instanced pipeline's check and a Run's check, though a grant names their pipeline and type", func() {
					instanced := savePipeline(mainTeam, atc.PipelineRef{Name: "jetbridge", InstanceVars: atc.InstanceVars{"branch": "feature"}})
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "instanced", inMemoryCheck(instanced, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("is an instanced pipeline")))

					worker := newWorkerWith(jetbridge.StepPrivilegeGranted, database.BuildFactory, runPipelines{
						StepPodPipelines: jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory), runPipelineID: pipeline.ID()})
					_, _, err = run(worker, "run-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("is a Run's pipeline")))
				})

				It("refuses a check in a one-off build, though its plan runs a granted type", func() {
					oneOff, err := pipeline.CreateOneOffBuild()
					Expect(err).NotTo(HaveOccurred())
					metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "image", BuildID: oneOff.ID(), BuildName: oneOff.Name(),
						PipelineID: pipeline.ID(), PipelineName: pipeline.Name()}
					_, _, err = run(newWorker(jetbridge.StepPrivilegeGranted), "one-off-check", metadata, "dind")
					Expect(err).To(MatchError(ContainSubstring("a one-off, a Run's or an instanced pipeline's build) holds no step pod grant")))
				})

				It("refuses every privileged check when the web cannot look pipelines up", func() {
					_, _, err := run(newWorkerWith(jetbridge.StepPrivilegeGranted, database.BuildFactory, nil), "no-pipelines", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"), "dind")
					Expect(err).To(MatchError(ContainSubstring("cannot look up the check's pipeline")))
				})
			})

			// The upgrade-safety spec: under any, a check pod is what it was
			// before check eligibility existed, with or without a check grant.
			It("leaves checks unchanged under any, looking nothing up", func() {
				pipelines := &countingPipelines{StepPodPipelines: jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory)}
				pod, annotations, err := run(newWorkerWith(jetbridge.StepPrivilegeAny, database.BuildFactory, pipelines), "any-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "plain"), "git")
				Expect(err).NotTo(HaveOccurred())
				Expect(mainPrivileged(pod)).To(BeTrue())
				Expect(annotations).NotTo(HaveKey("concourse.dev/step-pod-grant"))
				Expect(pod.ServiceAccountName).To(Equal("step-default"))
				Expect(pipelines.calls).To(BeZero())
			})
		})
	}
})

var _ = Describe("ParseStepPodGrant with a resource-type owner", func() {
	It("accepts a privileged grant naming a pipeline's resource type", func() {
		grant, err := jetbridge.ParseStepPodGrant("name=dind-checks,owner=main/jetbridge/resource-type/dind,privileged=true")
		Expect(err).NotTo(HaveOccurred())
		Expect(grant.Owner).To(Equal(jetbridge.StepPodOwner{Team: "main", Pipeline: "jetbridge", ResourceType: "dind"}))
		Expect(grant.Owner.String()).To(Equal("main/jetbridge/resource-type/dind"))
	})

	DescribeTable("refuses",
		func(value, reason string) {
			_, err := jetbridge.ParseStepPodGrant(value)
			Expect(err).To(MatchError(ContainSubstring(reason)))
		},
		Entry("one without privileged=true", "name=a,owner=main/p/resource-type/dind,privileged=false,service-account=sa", "a resource-type owner's grant must declare privileged=true"),
		Entry("one with a ServiceAccount", "name=a,owner=main/p/resource-type/dind,privileged=true,service-account=sa", "a resource-type owner's grant names no service-account"),
		Entry("a four-segment owner of another form", "name=a,owner=main/p/job/dind,privileged=true", "want <team>/<pipeline>/<job>"),
		Entry("a one-off resource-type owner", "name=a,owner=main/one-off/resource-type/dind,privileged=true", "a resource-type owner names a pipeline"),
		Entry("a type name that is not a lowercase identifier", "name=a,owner=main/p/resource-type/Dind,privileged=true", `"Dind" is not an identifier`),
	)
})

// countingPipelines counts the pipeline lookups a worker makes.
type countingPipelines struct {
	jetbridge.StepPodPipelines
	calls int
}

func (p *countingPipelines) Pipeline(id int) (db.Pipeline, bool, error) {
	p.calls++
	return p.StepPodPipelines.Pipeline(id)
}

// runPipelines reports one pipeline as a Run's.
type runPipelines struct {
	jetbridge.StepPodPipelines
	runPipelineID int
}

func (p runPipelines) Pipeline(id int) (db.Pipeline, bool, error) {
	pipeline, found, err := p.StepPodPipelines.Pipeline(id)
	if err != nil || !found || id != p.runPipelineID {
		return pipeline, found, err
	}
	return runPipeline{pipeline}, true, nil
}

type runPipeline struct{ db.Pipeline }

func (runPipeline) PipelineRunID() (int, bool) { return 7, true }
