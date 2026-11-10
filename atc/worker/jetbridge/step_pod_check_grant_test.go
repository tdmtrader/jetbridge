package jetbridge_test

import (
	"context"

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
// owner=<team>/<pipeline>/resource-type/<type>. The type is read from the
// pipeline's own rows: a check of resource R runs R's type, and a check or
// image fetch named after resource type T (T's own check, or the fetch of T's
// image) runs T's type. Runs, one-off builds, instanced pipelines and base
// types stay ungrantable.
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

	// dind is privileged; child is built on dind (its image is fetched by a
	// dind get), and is not privileged itself.
	pipelineConfig := atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{Name: "dind", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "example/dind"}},
			{Name: "child", Type: "dind", Source: atc.Source{"repository": "example/child"}},
		},
		Resources: atc.ResourceConfigs{
			{Name: "repo", Type: "dind", Source: atc.Source{"uri": "https://example.com/repo"}},
			{Name: "kid", Type: "child", Source: atc.Source{"uri": "https://example.com/kid"}},
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
			grant("name=unit,owner=main/jetbridge/unit,privileged=true"),
		}
	})

	savePipeline := func(team db.Team, ref atc.PipelineRef) db.Pipeline {
		GinkgoHelper()
		saved, _, err := team.SavePipeline(ref, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		return saved
	}

	// inMemoryCheck is the metadata of a container in a check build, which
	// runs in memory and has no builds row: its build is named check, and its
	// pipeline is the checked resource's.
	inMemoryCheck := func(p db.Pipeline, kind db.ContainerType, step string) db.ContainerMetadata {
		return db.ContainerMetadata{
			Type: kind, StepName: step, BuildID: 0, BuildName: db.CheckBuildName,
			PipelineID: p.ID(), PipelineName: p.Name(),
		}
	}

	for _, stepRuntime := range []string{jetbridge.StepRuntimeExec, jetbridge.StepRuntimeJob} {
		stepRuntime := stepRuntime

		Context("on a worker of the "+stepRuntime+" step runtime", func() {
			newWorkerWith := func(mode string, pipelines jetbridge.StepPodPipelines) *jetbridge.Worker {
				config.StepPrivilege = mode
				if stepRuntime == jetbridge.StepRuntimeJob {
					config.StepRuntime = jetbridge.StepRuntimeJob
					config.LauncherImage = "registry.example/jetbridge:1"
					config.StepEnvelopeURL = "http://web.test-namespace.svc:8080/runtime/v1/step-envelope"
					config.ArtifactDaemonHostPath = "/var/concourse/artifacts"
				}
				return jetbridge.NewWorker(dbWorker, clientset, config, jetbridge.WorkerDeps{
					StepPodBuilds:    database.BuildFactory,
					StepPodPipelines: pipelines,
					StepEnvelopes:    jetbridge.NewStepEnvelopes(),
				})
			}
			newWorker := func(mode string) *jetbridge.Worker {
				return newWorkerWith(mode, jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory))
			}

			// run starts one privileged step and returns the pod (or the step
			// Job's pod template) and its annotations; a refusal creates neither.
			run := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata) (*corev1.PodSpec, map[string]string, error) {
				GinkgoHelper()
				spec := runtime.ContainerSpec{
					Type: metadata.Type, TeamID: 1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: true},
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
					pod, annotations, err := run(newWorker(jetbridge.StepPrivilegeGranted), "repo-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"))
					Expect(err).NotTo(HaveOccurred())
					Expect(mainPrivileged(pod)).To(BeTrue())
					Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "dind-checks"))
					Expect(pod.ServiceAccountName).To(Equal("step-default"), "a check grant gives privilege, never a ServiceAccount")
					Expect(pod.AutomountServiceAccountToken).To(PointTo(BeFalse()))
				})

				It("admits the check of a resource type built on the granted type, and the fetch of its image", func() {
					worker := newWorker(jetbridge.StepPrivilegeGranted)
					// child's own check, and the image check and get of child
					// inside kid's check build, all run dind.
					for handle, metadata := range map[string]db.ContainerMetadata{
						"child-check":     inMemoryCheck(pipeline, db.ContainerTypeCheck, "child"),
						"child-image-get": inMemoryCheck(pipeline, db.ContainerTypeGet, "child"),
					} {
						pod, _, err := run(worker, handle, metadata)
						Expect(err).NotTo(HaveOccurred(), handle)
						Expect(mainPrivileged(pod)).To(BeTrue(), handle)
					}
				})

				It("admits a check of a granted type whose check build has a builds row, and a check in a job build", func() {
					resource, found, err := pipeline.Resource("repo")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					checkBuild, created, err := resource.CreateBuild(ctx, true, atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(created).To(BeTrue())
					job, found, err := pipeline.Job("unit")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					jobBuild, err := job.CreateBuild("some-user")
					Expect(err).NotTo(HaveOccurred())

					worker := newWorker(jetbridge.StepPrivilegeGranted)
					for handle, build := range map[string]db.Build{"row-check": checkBuild, "job-check": jobBuild} {
						metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "repo", BuildID: build.ID(), BuildName: build.Name(),
							PipelineID: build.PipelineID(), PipelineName: build.PipelineName(), JobID: build.JobID(), JobName: build.JobName()}
						_, annotations, err := run(worker, handle, metadata)
						Expect(err).NotTo(HaveOccurred(), handle)
						Expect(annotations).To(HaveKeyWithValue("concourse.dev/step-pod-grant", "dind-checks"), handle)
					}
				})

				It("refuses the same type's check in another pipeline and in another team's pipeline of the same name", func() {
					other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other"})
					Expect(err).NotTo(HaveOccurred())
					worker := newWorker(jetbridge.StepPrivilegeGranted)

					_, _, err = run(worker, "other-pipeline", inMemoryCheck(savePipeline(mainTeam, atc.PipelineRef{Name: "elsewhere"}), db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("Owner main/elsewhere/resource-type/dind has no step pod grant")))

					_, _, err = run(worker, "other-team", inMemoryCheck(savePipeline(other, atc.PipelineRef{Name: "jetbridge"}), db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("Owner other/jetbridge/resource-type/dind has no step pod grant")))
				})

				It("refuses a privileged check whose type no grant names", func() {
					config.StepPodGrants = config.StepPodGrants[1:]
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "ungranted", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/resource-type/dind has no step pod grant")))
				})

				It("refuses a privileged check of a base type, which no pipeline declares", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "base", inMemoryCheck(pipeline, db.ContainerTypeCheck, "plain"))
					Expect(err).To(MatchError(ContainSubstring(`"git" is a base resource type`)))
				})

				It("refuses a privileged check that names nothing its pipeline declares", func() {
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "unknown-step", inMemoryCheck(pipeline, db.ContainerTypeCheck, "nothing"))
					Expect(err).To(MatchError(ContainSubstring(`Pipeline main/jetbridge declares no resource or resource type "nothing"`)))
				})

				It("refuses an instanced pipeline's check and a Run's check, though a grant names their pipeline and type", func() {
					instanced := savePipeline(mainTeam, atc.PipelineRef{Name: "jetbridge", InstanceVars: atc.InstanceVars{"branch": "feature"}})
					_, _, err := run(newWorker(jetbridge.StepPrivilegeGranted), "instanced", inMemoryCheck(instanced, db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("is an instanced pipeline")))

					worker := newWorkerWith(jetbridge.StepPrivilegeGranted, runPipelines{
						StepPodPipelines: jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory), runPipelineID: pipeline.ID()})
					_, _, err = run(worker, "run-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("is a Run's pipeline")))
				})

				It("refuses a check in a one-off build, though its step names a granted type", func() {
					oneOff, err := pipeline.CreateOneOffBuild()
					Expect(err).NotTo(HaveOccurred())
					metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "repo", BuildID: oneOff.ID(), BuildName: oneOff.Name(),
						PipelineID: pipeline.ID(), PipelineName: pipeline.Name()}
					_, _, err = run(newWorker(jetbridge.StepPrivilegeGranted), "one-off-check", metadata)
					Expect(err).To(MatchError(ContainSubstring("a one-off, a Run's or an instanced pipeline's build) holds no step pod grant")))
				})

				It("refuses every privileged check when the web cannot look pipelines up", func() {
					_, _, err := run(newWorkerWith(jetbridge.StepPrivilegeGranted, nil), "no-pipelines", inMemoryCheck(pipeline, db.ContainerTypeCheck, "repo"))
					Expect(err).To(MatchError(ContainSubstring("cannot look up the check's pipeline")))
				})
			})

			// The upgrade-safety spec: under any, a check pod is what it was
			// before check eligibility existed, with or without a check grant.
			It("leaves checks unchanged under any", func() {
				pod, annotations, err := run(newWorker(jetbridge.StepPrivilegeAny), "any-check", inMemoryCheck(pipeline, db.ContainerTypeCheck, "plain"))
				Expect(err).NotTo(HaveOccurred())
				Expect(mainPrivileged(pod)).To(BeTrue())
				Expect(annotations).NotTo(HaveKey("concourse.dev/step-pod-grant"))
				Expect(pod.ServiceAccountName).To(Equal("step-default"))
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
	)
})

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
