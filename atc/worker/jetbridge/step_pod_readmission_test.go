package jetbridge_test

import (
	"context"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Re-admission. A step pod, or a step Job, that already exists when a step
// comes back to it -- a live pause pod reused, a pod reattached to after a web
// restart, a step Job resumed -- passes the step pod admission again under the
// web's privilege mode and the step pod grants it has now, resolved the way
// creation resolves them. A pod made under any, or under a grant since
// removed, is deleted (a step Job by its UID) and the step fails naming the
// grant it lacks; it is never handed back, and no fresh pod replaces it.
// Under any nothing is re-admitted and nothing more is looked up.
//
// Each spec runs a step on one web's worker, then the same step again on a
// second web's worker over the same database and cluster: the second web is
// the restarted, reconfigured one.
var _ = Describe("Re-admission of an existing step pod", func() {
	const (
		namespace      = "test-namespace"
		defaultAccount = "step-default"
	)

	var (
		ctx       context.Context
		database  jetbridgeDB
		dbWorker  db.Worker
		clientset *fake.Clientset
		pipeline  db.Pipeline
		deletes   *recordedDeletes
	)

	pipelineConfig := atc.Config{
		ResourceTypes: atc.ResourceTypes{
			{Name: "dind", Type: "registry-image", Privileged: true, Source: atc.Source{"repository": "example/dind"}},
		},
		Resources: atc.ResourceConfigs{{Name: "repo", Type: "dind", Source: atc.Source{"uri": "https://example.com/repo"}}},
		Jobs: atc.JobConfigs{
			{Name: "dind", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
			{Name: "unit", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "t", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}}}}}},
		},
	}

	const (
		dindGrant   = "name=dind,owner=main/jetbridge/dind,privileged=true"
		checksGrant = "name=dind-checks,owner=main/jetbridge/resource-type/dind,privileged=true"
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		clientset = fake.NewSimpleClientset()
		deletes = recordDeletes(clientset)

		mainTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())
		pipeline, _, err = mainTeam.SavePipeline(atc.PipelineRef{Name: "jetbridge"}, pipelineConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
	})

	jobBuild := func(name string) db.ContainerMetadata {
		GinkgoHelper()
		job, found, err := pipeline.Job(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("some-user")
		Expect(err).NotTo(HaveOccurred())
		return db.ContainerMetadata{
			Type: db.ContainerTypeTask, StepName: "t", BuildID: build.ID(), BuildName: build.Name(),
			PipelineID: build.PipelineID(), PipelineName: build.PipelineName(), JobID: build.JobID(), JobName: build.JobName(),
		}
	}

	// inMemoryCheck is a check of resource repo, which runs as an in-memory
	// build: no builds row, its pipeline the checked resource's. A repeated
	// check reuses its container, and so its live pause pod.
	inMemoryCheck := func() db.ContainerMetadata {
		return db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "repo", BuildName: db.CheckBuildName,
			PipelineID: pipeline.ID(), PipelineName: pipeline.Name()}
	}

	for _, stepRuntime := range []string{jetbridge.StepRuntimeExec, jetbridge.StepRuntimeJob} {
		stepRuntime := stepRuntime

		Context("on a worker of the "+stepRuntime+" step runtime", func() {
			// web builds one web's worker: its privilege mode and its grants.
			web := func(mode string, builds jetbridge.StepPodBuilds, grants ...string) *jetbridge.Worker {
				GinkgoHelper()
				config := jetbridge.NewConfig(namespace, "")
				config.ServiceAccount = defaultAccount
				config.StepPrivilege = mode
				for _, value := range grants {
					grant, err := jetbridge.ParseStepPodGrant(value)
					Expect(err).NotTo(HaveOccurred())
					config.StepPodGrants = append(config.StepPodGrants, grant)
				}
				deps := jetbridge.WorkerDeps{
					StepPodBuilds:    builds,
					StepPodPipelines: jetbridge.NewStepPodPipelines(database.Conn, database.LockFactory),
					StepEnvelopes:    jetbridge.NewStepEnvelopes(),
				}
				if stepRuntime == jetbridge.StepRuntimeJob {
					config.StepRuntime = jetbridge.StepRuntimeJob
					config.LauncherImage = "registry.example/jetbridge:1"
					config.StepEnvelopeURL = "http://web.test-namespace.svc:8080/runtime/v1/step-envelope"
					config.ArtifactDaemonHostPath = "/var/concourse/artifacts"
				} else {
					// The exec runtime reuses a live pause pod only with an
					// exec transport; idleExecutor execs nothing.
					deps.Executor = idleExecutor{}
				}
				return jetbridge.NewWorker(dbWorker, clientset, config, deps)
			}

			start := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata, privileged bool) error {
				GinkgoHelper()
				spec := runtime.ContainerSpec{
					Type: metadata.Type, TeamID: 1,
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox", Privileged: privileged},
				}
				if metadata.Type == db.ContainerTypeCheck {
					spec.StepResourceType = "dind"
				}
				container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata, spec, &noopDelegate{})
				Expect(err).NotTo(HaveOccurred())
				_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{})
				return err
			}

			// existing is the step's pod (exec) or step Job (job), as the
			// cluster has it; nil once it is gone.
			existing := func(handle string) metav1.Object {
				GinkgoHelper()
				selector := metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + handle}
				if stepRuntime == jetbridge.StepRuntimeJob {
					jobs, err := clientset.BatchV1().Jobs(namespace).List(ctx, selector)
					Expect(err).NotTo(HaveOccurred())
					Expect(len(jobs.Items)).To(BeNumerically("<=", 1))
					if len(jobs.Items) == 0 {
						return nil
					}
					return &jobs.Items[0]
				}
				pods, err := clientset.CoreV1().Pods(namespace).List(ctx, selector)
				Expect(err).NotTo(HaveOccurred())
				Expect(len(pods.Items)).To(BeNumerically("<=", 1))
				if len(pods.Items) == 0 {
					return nil
				}
				return &pods.Items[0]
			}

			// made runs the step on the first web and gives what it made a
			// UID, as the API server would have; the fake one sets none.
			made := func(worker *jetbridge.Worker, handle string, metadata db.ContainerMetadata) types.UID {
				GinkgoHelper()
				Expect(start(worker, handle, metadata, true)).To(Succeed())
				uid := types.UID("uid-" + handle)
				switch object := existing(handle).(type) {
				case *corev1.Pod:
					object.UID = uid
					_, err := clientset.CoreV1().Pods(namespace).Update(ctx, object, metav1.UpdateOptions{})
					Expect(err).NotTo(HaveOccurred())
				default:
					job := existing(handle)
					Expect(job).NotTo(BeNil(), "the first web made no step Job")
					stepJob, err := clientset.BatchV1().Jobs(namespace).Get(ctx, job.GetName(), metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					stepJob.UID = uid
					_, err = clientset.BatchV1().Jobs(namespace).Update(ctx, stepJob, metav1.UpdateOptions{})
					Expect(err).NotTo(HaveOccurred())
				}
				return uid
			}

			expectDeleted := func(handle string, uid types.UID) {
				GinkgoHelper()
				Expect(existing(handle)).To(BeNil(), "the over-grant step pod is still there")
				resource := "pods"
				if stepRuntime == jetbridge.StepRuntimeJob {
					resource = "jobs"
				}
				Expect(deletes.preconditionsOf(resource)).To(ContainElement(uid), "the delete was not held to the UID the step came back to")
			}

			expectRefusal := func(err error, owner string) {
				GinkgoHelper()
				Expect(err).To(MatchError(ContainSubstring(`container "main" is privileged`)))
				Expect(err).To(MatchError(ContainSubstring("Owner " + owner + " has no step pod grant")))
				Expect(err).To(MatchError(ContainSubstring("--kubernetes-step-privilege=granted")))
				Expect(err).To(MatchError(ContainSubstring("deleted")))
			}

			Describe("under granted", func() {
				It("deletes a privileged pod made under any, and fails the step naming the grant it lacks", func() {
					metadata := jobBuild("unit")
					uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "made-under-any", metadata)

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "made-under-any", metadata, true)
					expectRefusal(err, "main/jetbridge/unit")
					expectDeleted("made-under-any", uid)
				})

				It("deletes a privileged pod whose grant has since been removed", func() {
					metadata := jobBuild("dind")
					uid := made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "grant-removed", metadata)

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "grant-removed", metadata, true)
					expectRefusal(err, "main/jetbridge/dind")
					expectDeleted("grant-removed", uid)
				})

				// What runs is the pod that exists, not the pod the step would
				// compose now: one whose step no longer asks for privilege is
				// still privileged, and could have begun the command.
				It("deletes a privileged pod even when the step now composes an unprivileged one, and makes no fresh pod", func() {
					metadata := jobBuild("unit")
					uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "now-unprivileged", metadata)

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "now-unprivileged", metadata, false)
					expectRefusal(err, "main/jetbridge/unit")
					expectDeleted("now-unprivileged", uid)
				})

				It("hands back a privileged pod a privileged grant still covers, and leaves it", func() {
					metadata := jobBuild("dind")
					made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "still-granted", metadata)

					Expect(start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "still-granted", metadata, true)).To(Succeed())
					Expect(existing("still-granted")).NotTo(BeNil())
					Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
					Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
				})

				It("re-admits a repeated check through check eligibility: its resource-type grant covers it, and without one it is deleted", func() {
					uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "repeated-check", inMemoryCheck())

					Expect(start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, checksGrant), "repeated-check", inMemoryCheck(), true)).To(Succeed())
					Expect(existing("repeated-check")).NotTo(BeNil(), "a covered check pod was deleted")

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "repeated-check", inMemoryCheck(), true)
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/resource-type/dind has no step pod grant")))
					Expect(err).To(MatchError(ContainSubstring("deleted")))
					expectDeleted("repeated-check", uid)
				})

				It("looks the step's build up once when it comes back", func() {
					metadata := jobBuild("dind")
					made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "counted-granted", metadata)

					builds := &countingBuilds{StepPodBuilds: database.BuildFactory}
					Expect(start(web(jetbridge.StepPrivilegeGranted, builds, dindGrant), "counted-granted", metadata, true)).To(Succeed())
					Expect(builds.calls).To(Equal(1))
				})
			})

			// The upgrade-safety pin: under any, a pod that comes back is handed
			// back as before -- privileged outside every grant -- and coming
			// back costs no lookup it did not cost before.
			Describe("under any", func() {
				for _, mode := range []string{"", jetbridge.StepPrivilegeAny} {
					mode := mode
					It("hands back a privileged pod outside every grant and deletes nothing (mode "+map[string]string{"": "unset", "any": "any"}[mode]+")", func() {
						metadata := jobBuild("unit")
						made(web(mode, database.BuildFactory, dindGrant), "any-reuse", metadata)

						builds := &countingBuilds{StepPodBuilds: database.BuildFactory}
						Expect(start(web(mode, builds, dindGrant), "any-reuse", metadata, true)).To(Succeed())
						Expect(builds.calls).To(Equal(1), "coming back looked the build up more than before")
						Expect(existing("any-reuse")).NotTo(BeNil())
						Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
						Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
					})
				}

				It("looks nothing up for a step that comes back with no grant configured", func() {
					metadata := jobBuild("unit")
					made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "any-no-grants", metadata)

					builds := &countingBuilds{StepPodBuilds: database.BuildFactory}
					Expect(start(web(jetbridge.StepPrivilegeAny, builds), "any-no-grants", metadata, true)).To(Succeed())
					Expect(builds.calls).To(BeZero())
					Expect(existing("any-no-grants")).NotTo(BeNil())
				})
			})
		})
	}
})

// recordedDeletes is every pod and Job delete a fake clientset was asked for,
// by the UID precondition each carried ("" for none). The fake object tracker
// ignores preconditions, so this is how a spec sees one.
type recordedDeletes struct {
	mu    sync.Mutex
	byUID map[string][]types.UID
}

func recordDeletes(clientset *fake.Clientset) *recordedDeletes {
	recorded := &recordedDeletes{byUID: map[string][]types.UID{}}
	clientset.PrependReactor("delete", "*", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		deleteAction, ok := action.(k8stesting.DeleteActionImpl)
		if !ok {
			return false, nil, nil
		}
		var uid types.UID
		if preconditions := deleteAction.DeleteOptions.Preconditions; preconditions != nil && preconditions.UID != nil {
			uid = *preconditions.UID
		}
		recorded.mu.Lock()
		recorded.byUID[action.GetResource().Resource] = append(recorded.byUID[action.GetResource().Resource], uid)
		recorded.mu.Unlock()
		return false, nil, nil
	})
	return recorded
}

func (r *recordedDeletes) preconditionsOf(resource string) []types.UID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.UID(nil), r.byUID[resource]...)
}
