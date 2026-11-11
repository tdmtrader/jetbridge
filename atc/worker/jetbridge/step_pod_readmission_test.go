package jetbridge_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		// configure, when set, adjusts the next web's Config; shape adjusts
		// every step's ContainerSpec.
		configure func(*jetbridge.Config)
		shape     func(*runtime.ContainerSpec)
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
		configure, shape = nil, nil

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
				if configure != nil {
					configure(&config)
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
				if shape != nil {
					shape(&spec)
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

			// mutate changes what the cluster holds for the step: the pod's
			// spec, or the step Job's pod template -- what a mutating webhook
			// does between the web's request and the stored object.
			mutate := func(handle string, change func(*corev1.PodSpec)) {
				GinkgoHelper()
				switch object := existing(handle).(type) {
				case *corev1.Pod:
					change(&object.Spec)
					_, err := clientset.CoreV1().Pods(namespace).Update(ctx, object, metav1.UpdateOptions{})
					Expect(err).NotTo(HaveOccurred())
				case *batchv1.Job:
					change(&object.Spec.Template.Spec)
					_, err := clientset.BatchV1().Jobs(namespace).Update(ctx, object, metav1.UpdateOptions{})
					Expect(err).NotTo(HaveOccurred())
				default:
					Fail("the step has nothing in the cluster to change")
				}
			}

			hostPaths := func(handle string) []string {
				GinkgoHelper()
				var spec *corev1.PodSpec
				switch object := existing(handle).(type) {
				case *corev1.Pod:
					spec = &object.Spec
				case *batchv1.Job:
					spec = &object.Spec.Template.Spec
				}
				Expect(spec).NotTo(BeNil())
				var paths []string
				for _, volume := range spec.Volumes {
					if volume.HostPath != nil {
						paths = append(paths, volume.HostPath.Path)
					}
				}
				return paths
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
				DescribeTable("deletes a privileged pod even when the step now composes an unprivileged one, says the step no longer asks for privilege, and makes no fresh pod",
					func(handle string, step func() db.ContainerMetadata) {
						metadata := step()
						uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), handle, metadata)

						err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), handle, metadata, false)
						Expect(err).To(MatchError(ContainSubstring(`container "main" is privileged`)))
						Expect(err).To(MatchError(ContainSubstring("privilege its step no longer asks for")))
						Expect(err).To(MatchError(ContainSubstring("deleted")))
						Expect(err).NotTo(MatchError(ContainSubstring("cannot resolve the check's owner")))
						Expect(err).NotTo(MatchError(ContainSubstring("must run unprivileged")))
						expectDeleted(handle, uid)
					},
					Entry("a task", "now-unprivileged-task", func() db.ContainerMetadata { return jobBuild("unit") }),
					Entry("a repeated check", "now-unprivileged-check", inMemoryCheck),
				)

				// Re-admission judges privilege only. What a mutating webhook
				// added (an init container that adds NET_ADMIN, a host path
				// outside the step's directories), and host paths the web's
				// configuration has since moved, were the API server's and the
				// creating web's to judge; an existing object is never deleted
				// for them.
				It("hands back a granted pod a webhook gave a capability-adding init container and a foreign host path", func() {
					metadata := jobBuild("dind")
					made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "injected", metadata)
					mutate("injected", func(pod *corev1.PodSpec) {
						pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "mesh-run",
							VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/run/mesh"}}})
						pod.InitContainers = append(pod.InitContainers, corev1.Container{Name: "istio-init", Image: "mesh/proxy",
							SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "NET_RAW"}}},
							VolumeMounts:    []corev1.VolumeMount{{Name: "mesh-run", MountPath: "/run/mesh"}}})
					})

					Expect(start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "injected", metadata, true)).To(Succeed())
					Expect(existing("injected")).NotTo(BeNil())
					Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
					Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
				})

				It("still deletes an over-grant pod a webhook also changed, naming only its privilege", func() {
					metadata := jobBuild("unit")
					uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "injected-over-grant", metadata)
					mutate("injected-over-grant", func(pod *corev1.PodSpec) {
						pod.InitContainers = append([]corev1.Container{{Name: "istio-init", Image: "mesh/proxy",
							SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}}}},
							pod.InitContainers...)
					})

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "injected-over-grant", metadata, true)
					expectRefusal(err, "main/jetbridge/unit")
					Expect(err).NotTo(MatchError(ContainSubstring("capability")))
					expectDeleted("injected-over-grant", uid)
				})

				It("hands back a granted pod whose cache host path the web's configuration has since moved", func() {
					metadata := jobBuild("dind")
					shape = func(spec *runtime.ContainerSpec) {
						spec.TaskCacheIdentity = &atc.TaskCacheIdentity{TeamID: 1, TemplatePipelineID: pipeline.ID(), RunJobName: "dind"}
						spec.Caches = []string{"/root/.cache"}
					}
					configure = func(config *jetbridge.Config) { config.CacheHostPath = "/var/lib/jetbridge-cache" }
					made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "moved-cache", metadata)
					Expect(hostPaths("moved-cache")).To(ContainElement(HavePrefix("/var/lib/jetbridge-cache/")), "the first web made no cache host path")

					configure = func(config *jetbridge.Config) {
						config.CacheHostPath = "/srv/jetbridge-cache"
						if config.ArtifactDaemonHostPath != "" {
							config.ArtifactDaemonHostPath = "/srv/jetbridge-artifacts"
						}
					}
					Expect(start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "moved-cache", metadata, true)).To(Succeed())
					Expect(existing("moved-cache")).NotTo(BeNil())
					Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
					Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
				})

				It("leaves the pod in place when the step's build cannot be looked up", func() {
					metadata := jobBuild("unit")
					made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "lookup-fails", metadata)

					broken := db.NewBuildFactory(closedJetbridgeCloneConn(), database.LockFactory, 0, time.Hour)
					err := start(web(jetbridge.StepPrivilegeGranted, broken), "lookup-fails", metadata, true)
					Expect(err).To(MatchError(ContainSubstring("resolve step pod identity")))
					Expect(existing("lookup-fails")).NotTo(BeNil())
					Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
					Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
				})

				It("fails the step, saying the delete failed, when the over-grant pod cannot be deleted", func() {
					metadata := jobBuild("unit")
					made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "undeletable", metadata)
					clientset.PrependReactor("delete", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "x", errors.New("the Role cannot delete"))
					})

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "undeletable", metadata, true)
					Expect(err).To(MatchError(ContainSubstring("deleting it failed")))
					Expect(err).To(MatchError(ContainSubstring("the Role cannot delete")))
					Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/unit has no step pod grant")))
					Expect(existing("undeletable")).NotTo(BeNil())
				})

				It("takes a UID Conflict on the delete as the over-grant pod being gone already", func() {
					metadata := jobBuild("unit")
					made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "replaced-since", metadata)
					clientset.PrependReactor("delete", "*", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
						return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "x", errors.New("UID precondition failed"))
					})

					err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "replaced-since", metadata, true)
					expectRefusal(err, "main/jetbridge/unit")
					Expect(err).NotTo(MatchError(ContainSubstring("deleting it failed")))
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

				if stepRuntime == jetbridge.StepRuntimeExec {
					// fly intercept is not a step: a looked-up container has
					// no spec to resolve a check's owner from, so it is
					// deliberately not re-admitted (MIGRATING.md says to delete
					// the privileged pods hack/step-pod-audit lists instead).
					It("still lets fly intercept into a privileged pod made under any, and deletes nothing", func() {
						metadata := jobBuild("unit")
						made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "intercepted", metadata)

						container, found, err := web(jetbridge.StepPrivilegeGranted, database.BuildFactory).LookupContainer(ctx, "intercepted")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						_, err = container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{})
						Expect(err).NotTo(HaveOccurred())
						Expect(existing("intercepted")).NotTo(BeNil())
						Expect(deletes.preconditionsOf("pods")).To(BeEmpty())
					})
				}

				if stepRuntime == jetbridge.StepRuntimeJob {
					// finish marks the step Job complete, as its controller
					// does once its pod exits.
					finish := func(handle string) {
						GinkgoHelper()
						job := existing(handle).(*batchv1.Job)
						job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
						_, err := clientset.BatchV1().Jobs(namespace).Update(ctx, job, metav1.UpdateOptions{})
						Expect(err).NotTo(HaveOccurred())
					}

					It("leaves a finished step Job when the step's own composition is refused: nothing would follow it", func() {
						metadata := jobBuild("unit")
						made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "finished-refused", metadata)
						finish("finished-refused")

						err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "finished-refused", metadata, true)
						Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/unit has no step pod grant")))
						Expect(err).NotTo(MatchError(ContainSubstring("deleted")))
						Expect(existing("finished-refused")).NotTo(BeNil())
						Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
					})

					// A composition that fails for a reason of its own still
					// retires a running Job the privilege rule refuses, and
					// leaves one it admits.
					Describe("when the step's composition fails for a reason unrelated to privilege", func() {
						unrelated := func(config *jetbridge.Config) { config.ArtifactDaemonHostPath = "" }
						getStep := func(job string) db.ContainerMetadata {
							metadata := jobBuild(job)
							metadata.Type, metadata.StepName = db.ContainerTypeGet, "repo"
							return metadata
						}

						It("deletes a running over-grant Job", func() {
							metadata := getStep("unit")
							uid := made(web(jetbridge.StepPrivilegeAny, database.BuildFactory), "unrelated-over-grant", metadata)

							configure = unrelated
							err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory), "unrelated-over-grant", metadata, true)
							Expect(err).To(MatchError(ContainSubstring("Owner main/jetbridge/unit has no step pod grant")))
							Expect(err).To(MatchError(ContainSubstring("deleted")))
							expectDeleted("unrelated-over-grant", uid)
						})

						It("leaves a running Job its grant covers, and fails with the composition's own error", func() {
							metadata := getStep("dind")
							made(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "unrelated-granted", metadata)

							configure = unrelated
							err := start(web(jetbridge.StepPrivilegeGranted, database.BuildFactory, dindGrant), "unrelated-granted", metadata, true)
							Expect(err).To(MatchError(ContainSubstring("needs the artifact daemon host path")))
							Expect(existing("unrelated-granted")).NotTo(BeNil())
							Expect(deletes.preconditionsOf("jobs")).To(BeEmpty())
						})
					})
				}
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
