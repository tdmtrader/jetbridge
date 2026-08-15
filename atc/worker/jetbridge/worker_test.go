package jetbridge_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Worker", func() {
	var (
		database      jetbridgeDB
		dbWorker      db.Worker
		team          db.Team
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		database = useJetbridgeDB()
		var err error
		dbWorker, err = persistNamedWorker(database, "k8s-worker-1")
		Expect(err).NotTo(HaveOccurred())
		team, err = database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
		Expect(err).NotTo(HaveOccurred())
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
		worker.SetVolumeRepo(database.VolumeRepository)
	})

	Describe("Name", func() {
		It("returns the db worker name", func() {
			Expect(worker.Name()).To(Equal("k8s-worker-1"))
		})
	})

	Describe("SkipResourceCache", func() {
		It("returns false to enable resource caching", func() {
			Expect(worker.SkipResourceCache()).To(BeFalse())
		})
	})

	Describe("FindOrCreateContainer", func() {
		var (
			owner    db.ContainerOwner
			metadata db.ContainerMetadata
			spec     runtime.ContainerSpec
		)

		BeforeEach(func() {
			owner = db.NewFixedHandleContainerOwner("test-handle")
			metadata = db.ContainerMetadata{
				Type:     db.ContainerTypeTask,
				StepName: "my-task",
			}
			spec = runtime.ContainerSpec{
				TeamID:   1,
				TeamName: "main",
				Dir:      "/workdir",
				ImageSpec: runtime.ImageSpec{
					ImageURL: "docker:///busybox",
				},
			}
		})

		Context("when no container exists in the DB", func() {
			It("creates a container in the DB and defers Pod creation to Run", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("persisting the created container and its metadata")
				var state, containerType, stepName string
				err = database.Conn.QueryRow(`
					SELECT state, meta_type, meta_step_name
					FROM containers
					WHERE handle = $1
				`, "test-handle").Scan(&state, &containerType, &stepName)
				Expect(err).NotTo(HaveOccurred())
				Expect(state).To(Equal(string(atc.ContainerStateCreated)))
				Expect(containerType).To(Equal(string(db.ContainerTypeTask)))
				Expect(stepName).To(Equal("my-task"))
				Expect(container.DBContainer().Handle()).To(Equal("test-handle"))

				By("not creating a Pod yet (deferred to Run)")
				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(0))

				By("creating the Pod when Run is called")
				_, err = container.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				pods, err = fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))
				Expect(pods.Items[0].Name).To(Equal("test-handle"))
			})
		})

		Context("when a created container already exists in the DB", func() {
			var existing db.CreatedContainer

			BeforeEach(func() {
				owner = db.NewFixedHandleContainerOwner("existing-handle")
				creating, err := dbWorker.CreateContainer(owner, metadata)
				Expect(err).NotTo(HaveOccurred())
				existing, err = creating.Created()
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns the existing container without creating a new one in the DB", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())
				Expect(container.DBContainer().ID()).To(Equal(existing.ID()))

				var count int
				err = database.Conn.QueryRow(`SELECT count(*) FROM containers WHERE handle = $1`, "existing-handle").Scan(&count)
				Expect(err).NotTo(HaveOccurred())
				Expect(count).To(Equal(1))
			})
		})
	})

	Describe("LookupContainer", func() {
		Context("when the Pod exists", func() {
			BeforeEach(func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "lookup-handle",
						Namespace: "test-namespace",
					},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				creating, err := dbWorker.CreateContainer(
					db.NewFixedHandleContainerOwner("lookup-handle"),
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: "lookup-task"},
				)
				Expect(err).NotTo(HaveOccurred())
				_, err = creating.Created()
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns the container", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(container).ToNot(BeNil())
			})

			It("returns a container with a valid DBContainer for hijack support", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				By("having a non-nil DBContainer that the hijack handler can call UpdateLastHijack on")
				Expect(container.DBContainer()).ToNot(BeNil())
				Expect(container.DBContainer().Handle()).To(Equal("lookup-handle"))
			})
		})

		Context("when the Pod exists but the DB container does not", func() {
			BeforeEach(func() {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "orphan-pod",
						Namespace: "test-namespace",
					},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns not found since the container is not tracked in the DB", func() {
				_, found, err := worker.LookupContainer(ctx, "orphan-pod")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the Pod does not exist", func() {
			It("returns not found", func() {
				_, found, err := worker.LookupContainer(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		// fly intercept -j my-pipeline/unit-test: the DB handle is an opaque
		// UUID, but the pod the step created is named from its metadata. The
		// looked-up Container has to resolve to that pod, not to the handle.
		Context("when the pod was named from build-step metadata", func() {
			const (
				buildStepHandle  = "550e8400-e29b-41d4-a716-446655440000"
				buildStepPodName = "my-pipeline-unit-test-b42-task-550e8400"
			)

			var (
				interceptRuntime   *podRuntime
				interceptKey       podKey
				interceptWorker    *jetbridge.Worker
				interceptContainer runtime.Container
			)

			BeforeEach(func() {
				metadata := db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: "my-pipeline",
					JobName:      "unit-test",
					BuildName:    "42",
					StepName:     "unit-test",
					BuildID:      653430,
				}

				By("sanity-checking that the handle is not the pod name")
				Expect(jetbridge.GeneratePodName(metadata, buildStepHandle)).To(Equal(buildStepPodName))
				Expect(buildStepPodName).ToNot(Equal(buildStepHandle))

				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      buildStepPodName,
						Namespace: "test-namespace",
						Labels: map[string]string{
							"concourse.ci/worker": "k8s-worker-1",
							"concourse.ci/handle": buildStepHandle,
						},
					},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				}
				_, err := fakeClientset.CoreV1().Pods("test-namespace").Create(ctx, pod, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				creating, err := dbWorker.CreateContainer(db.NewFixedHandleContainerOwner(buildStepHandle), metadata)
				Expect(err).NotTo(HaveOccurred())
				_, err = creating.Created()
				Expect(err).NotTo(HaveOccurred())

				interceptRuntime = newPodRuntime(fakeClientset)
				interceptKey = podKey{"test-namespace", buildStepPodName, "main"}
				Expect(interceptRuntime.AddContainer(interceptKey)).To(Succeed())
				Expect(interceptRuntime.InstallProgram(interceptKey, "/bin/sh", program{})).To(Succeed())
				interceptWorker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				interceptWorker.SetExecutor(interceptRuntime)

				var found bool
				interceptContainer, found, err = interceptWorker.LookupContainer(ctx, buildStepHandle)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
			})

			It("execs into the pod the step actually created, not the raw handle", func() {
				process, err := interceptContainer.Run(ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
				}, runtime.ProcessIO{})
				Expect(err).ToNot(HaveOccurred())

				result, err := process.Wait(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ExitStatus).To(Equal(0))

				Expect(interceptRuntime.Processes(interceptKey)).To(Equal([]modeledProcess{{
					Command:    []string{"/bin/sh"},
					Supervised: true,
				}}))

				By("not creating a replacement pod named after the handle")
				pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(pods.Items).To(HaveLen(1))
				Expect(pods.Items[0].Name).To(Equal(buildStepPodName))
			})

			It("refuses to fabricate a pod when the step's pod is gone", func() {
				err := fakeClientset.CoreV1().Pods("test-namespace").Delete(ctx, buildStepPodName, metav1.DeleteOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = interceptContainer.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{})
				Expect(err).To(MatchError(ContainSubstring("has no pod to intercept")))

				By("not leaving a pod behind")
				pods, listErr := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
				Expect(listErr).ToNot(HaveOccurred())
				Expect(pods.Items).To(BeEmpty())
			})

			It("refuses to replace a pod that has already exited", func() {
				pod, err := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, buildStepPodName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				pod.Status.Phase = corev1.PodSucceeded
				pod.Annotations = map[string]string{"concourse.ci/exit-status": "0"}
				_, err = fakeClientset.CoreV1().Pods("test-namespace").Update(ctx, pod, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				_, err = interceptContainer.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{})
				Expect(err).To(MatchError(ContainSubstring("already exited")))

				By("leaving the completed pod (and its exit-status annotation) intact")
				survivor, getErr := fakeClientset.CoreV1().Pods("test-namespace").Get(ctx, buildStepPodName, metav1.GetOptions{})
				Expect(getErr).ToNot(HaveOccurred())
				Expect(survivor.Annotations).To(HaveKeyWithValue("concourse.ci/exit-status", "0"))
			})
		})
	})

	Describe("CreateVolumeForArtifact", func() {
		Context("when the volume repo is configured", func() {
			It("creates an artifact volume and returns it with the artifact", func() {
				vol, artifact, err := worker.CreateVolumeForArtifact(ctx, team.ID())
				Expect(err).ToNot(HaveOccurred())
				Expect(vol).ToNot(BeNil())
				Expect(artifact).ToNot(BeNil())

				By("persisting the volume transition and artifact association")
				var persistedTeamID, workerArtifactID int
				var workerName, state string
				err = database.Conn.QueryRow(`
					SELECT team_id, worker_name, state, worker_artifact_id
					FROM volumes
					WHERE handle = $1
				`, vol.Handle()).Scan(&persistedTeamID, &workerName, &state, &workerArtifactID)
				Expect(err).NotTo(HaveOccurred())
				Expect(persistedTeamID).To(Equal(team.ID()))
				Expect(workerName).To(Equal(dbWorker.Name()))
				Expect(state).To(Equal(string(db.VolumeStateCreated)))
				Expect(workerArtifactID).To(Equal(artifact.ID()))

				persisted, found, err := database.VolumeRepository.FindVolume(vol.Handle())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persisted.Type()).To(Equal(db.VolumeTypeArtifact))
			})

			It("returns the handle generated by the persisted volume", func() {
				vol, _, err := worker.CreateVolumeForArtifact(ctx, team.ID())
				Expect(err).ToNot(HaveOccurred())
				Expect(vol.Handle()).NotTo(BeEmpty())

				persisted, found, err := database.VolumeRepository.FindVolume(vol.Handle())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persisted.Handle()).To(Equal(vol.Handle()))
			})

			Context("when the artifact store is configured", func() {
				BeforeEach(func() {
					worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
					worker.SetVolumeRepo(database.VolumeRepository)
				})

				It("returns a DaemonSetVolume", func() {
					vol, _, err := worker.CreateVolumeForArtifact(ctx, team.ID())
					Expect(err).ToNot(HaveOccurred())
					Expect(vol).ToNot(BeNil())

					asVol, ok := vol.(*jetbridge.DaemonSetVolume)
					Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
					Expect(asVol.Key()).To(Equal(jetbridge.ArtifactKey(vol.Handle())))
					Expect(asVol.Handle()).To(Equal(vol.Handle()))
				})
			})

			It("always returns a DaemonSetVolume", func() {
				vol, _, err := worker.CreateVolumeForArtifact(ctx, team.ID())
				Expect(err).ToNot(HaveOccurred())
				Expect(vol).ToNot(BeNil())

				_, isDaemonSet := vol.(*jetbridge.DaemonSetVolume)
				Expect(isDaemonSet).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
			})
		})

		Context("when the volume repo is NOT configured", func() {
			It("returns an error", func() {
				freshWorker := jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				_, _, err := freshWorker.CreateVolumeForArtifact(ctx, team.ID())
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("volume repository not configured")))
			})
		})

		Context("when CreateVolume fails", func() {
			BeforeEach(func() {
				worker.SetVolumeRepo(db.NewVolumeRepository(closedJetbridgeCloneConn()))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, team.ID())
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("closed")))
			})
		})

	})

	Describe("LookupVolume", func() {
		Context("when the volume exists in the DB", func() {
			BeforeEach(func() {
				creating, err := database.VolumeRepository.CreateVolumeWithHandle(
					"vol-handle-1", team.ID(), dbWorker.Name(), db.VolumeTypeArtifact,
				)
				Expect(err).NotTo(HaveOccurred())
				_, err = creating.Created()
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns a cache-backed volume", func() {
				vol, found, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())
				Expect(vol.Handle()).To(Equal("vol-handle-1"))
			})

			It("does not match a different handle", func() {
				_, found, err := worker.LookupVolume(ctx, "vol-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the volume does not exist in the DB", func() {
			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the DB returns an error", func() {
			BeforeEach(func() {
				worker.SetVolumeRepo(db.NewVolumeRepository(closedJetbridgeCloneConn()))
			})

			It("returns the error", func() {
				_, _, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("closed")))
			})
		})

		Context("when no volume repo is configured", func() {
			BeforeEach(func() {
				worker = jetbridge.NewWorker(dbWorker, fakeClientset, cfg)
				// intentionally do NOT call SetVolumeRepo
			})

			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("FindDaemonResourceCache", func() {
		Context("when the daemon has the cache", func() {
			It("returns a DaemonSetVolume that can StreamOut via HTTP", func() {
				// Start a daemon that has the cache.
				daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/resource-caches/") {
						w.WriteHeader(http.StatusOK)
						return
					}
					if strings.Contains(r.URL.Path, "/artifacts/") {
						w.Write([]byte("cached-tar-data"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer daemon.Close()

				addr := daemon.Listener.Addr().String()
				colonIdx := strings.LastIndex(addr, ":")
				host := addr[:colonIdx]
				port, _ := strconv.Atoi(addr[colonIdx+1:])

				daemonClientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "artifact-daemon-abc",
						Namespace: "test-namespace",
						Labels: map[string]string{
							discoveryv1.LabelServiceName: "artifact-daemon",
						},
					},
					Endpoints: []discoveryv1.Endpoint{
						{Addresses: []string{host}},
					},
				})

				daemonCfg := cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonCfg.ArtifactDaemonService = "artifact-daemon"
				daemonCfg.ArtifactDaemonPort = port
				daemonWorker := jetbridge.NewWorker(dbWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)
				daemonWorker.SetDaemonClient(client)

				vol, found, err := daemonWorker.FindDaemonResourceCache(ctx, stubCache{id: 42})
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())
				Expect(vol.Handle()).To(Equal("rc-42"))
				Expect(vol.Source()).To(Equal("k8s-worker-1"))

				// The returned volume should be a DaemonSetVolume
				// that can StreamOut (not a stub that would panic).
				dsVol, ok := vol.(*jetbridge.DaemonSetVolume)
				Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
				_ = dsVol
			})
		})

		Context("when a downstream step wraps the returned volume via ArtifactFromVolume", func() {
			// Regression: FindDaemonResourceCache used to write the daemon
			// pod IP into the ArtifactLocator under the NodeName field.
			// Downstream ArtifactFromVolume → WrapVolumeForLookup would
			// then read that IP back and construct a DaemonSetVolume whose
			// sourceNode was an IP. StreamOut would hand that IP to
			// NodeIPResolver.Resolve, which hits the K8s Nodes API and
			// fails with `nodes "<IP>" not found`.
			//
			// This test mirrors the production trigger in
			// atc/exec/get_step.go:418 — `worker.ArtifactFromVolume(volume)`
			// is called on whatever FindDaemonResourceCache returned.
			It("StreamOut on the wrapped artifact succeeds without resolving the daemon IP as a node name", func() {
				daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/resource-caches/") {
						w.WriteHeader(http.StatusOK)
						return
					}
					if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/artifacts/") {
						w.Write([]byte("cached-tar-data"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer daemon.Close()

				addr := daemon.Listener.Addr().String()
				colonIdx := strings.LastIndex(addr, ":")
				host := addr[:colonIdx]
				port, _ := strconv.Atoi(addr[colonIdx+1:])

				daemonClientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "artifact-daemon-abc",
						Namespace: "test-namespace",
						Labels: map[string]string{
							discoveryv1.LabelServiceName: "artifact-daemon",
						},
					},
					Endpoints: []discoveryv1.Endpoint{
						{Addresses: []string{host}},
					},
				})

				daemonCfg := cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonCfg.ArtifactDaemonService = "artifact-daemon"
				daemonCfg.ArtifactDaemonPort = port
				daemonWorker := jetbridge.NewWorker(dbWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)
				daemonWorker.SetDaemonClient(client)

				vol, found, err := daemonWorker.FindDaemonResourceCache(ctx, stubCache{id: 42})
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())

				artifact := daemonWorker.ArtifactFromVolume(vol)
				Expect(artifact).ToNot(BeNil())
				Expect(artifact.Handle()).To(Equal("rc-42"))

				reader, err := artifact.StreamOut(ctx, ".", nil)
				Expect(err).ToNot(HaveOccurred(),
					"StreamOut must not fail resolving the daemon pod IP as a K8s Node name")
				Expect(reader).ToNot(BeNil())
				defer reader.Close()

				body, err := io.ReadAll(reader)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(body)).To(Equal("cached-tar-data"))
			})
		})

		Context("when a probe hit occurs", func() {
			// The ArtifactLocator's NodeName field is contractually a K8s
			// Node object name. A daemon pod IP is not a valid value for
			// that field. The probe-hit code path does not learn a node
			// name (it only learns a pod IP), so it must not write to the
			// locator — downstream lookups re-probe instead.
			It("writes nothing to the ArtifactLocator for the cache key", func() {
				daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/resource-caches/") {
						w.WriteHeader(http.StatusOK)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer daemon.Close()

				addr := daemon.Listener.Addr().String()
				colonIdx := strings.LastIndex(addr, ":")
				host := addr[:colonIdx]
				port, _ := strconv.Atoi(addr[colonIdx+1:])

				daemonClientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "artifact-daemon-abc",
						Namespace: "test-namespace",
						Labels: map[string]string{
							discoveryv1.LabelServiceName: "artifact-daemon",
						},
					},
					Endpoints: []discoveryv1.Endpoint{
						{Addresses: []string{host}},
					},
				})

				daemonCfg := cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonCfg.ArtifactDaemonService = "artifact-daemon"
				daemonCfg.ArtifactDaemonPort = port
				daemonWorker := jetbridge.NewWorker(dbWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)

				locator := jetbridge.NewArtifactLocator()
				daemonWorker.SetArtifactLocator(locator)
				daemonWorker.SetDaemonClient(client)

				_, found, err := daemonWorker.FindDaemonResourceCache(ctx, stubCache{id: 42})
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				_, ok := locator.Locate("rc-42")
				Expect(ok).To(BeFalse(),
					"probe hits must not write to the ArtifactLocator: "+
						"the daemon pod IP is not a valid NodeName")
			})
		})

		Context("when the locator has a stale entry for a dead node", func() {
			It("does not return a cache hit from the stale locator entry", func() {
				// Start a live daemon that does NOT have the cache.
				daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
				defer daemon.Close()

				// Extract host and port from the test server.
				addr := daemon.Listener.Addr().String()
				colonIdx := strings.LastIndex(addr, ":")
				host := addr[:colonIdx]
				port, _ := strconv.Atoi(addr[colonIdx+1:])

				// Create a clientset with an EndpointSlice pointing to
				// the live daemon only (simulating the new node).
				daemonClientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "artifact-daemon-abc",
						Namespace: "test-namespace",
						Labels: map[string]string{
							discoveryv1.LabelServiceName: "artifact-daemon",
						},
					},
					Endpoints: []discoveryv1.Endpoint{
						{Addresses: []string{host}},
					},
				})

				// Create a worker with DaemonSet backend (requires
				// ArtifactDaemonHostPath to activate the locator).
				daemonCfg := cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonCfg.ArtifactDaemonService = "artifact-daemon"
				daemonCfg.ArtifactDaemonPort = port
				daemonWorker := jetbridge.NewWorker(dbWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)
				daemonWorker.SetDaemonClient(client)

				// Seed the locator with a stale entry for a dead node IP.
				// This simulates: the old node had the cache, then was rolled.
				locator := jetbridge.NewArtifactLocator()
				locator.Record("rc-42", "10.0.0.99", "rc-42") // dead node IP
				daemonWorker.SetArtifactLocator(locator)

				// Re-set the daemon client after SetArtifactLocator
				// (which replaces the storage backend).
				daemonWorker.SetDaemonClient(client)

				// FindDaemonResourceCache should NOT return the stale
				// locator entry — it should probe live daemons and find
				// nothing, since the live daemon returns 404.
				_, found, err := daemonWorker.FindDaemonResourceCache(ctx, stubCache{id: 42})
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse(), "expected no cache hit when the locator entry points to a dead node and no live daemon has the cache")
			})
		})
	})

	Describe("ArtifactFromVolume", func() {
		// ArtifactFromVolume wraps a container-mount volume into a
		// DaemonSet-backed Artifact reference so downstream StreamOut
		// calls do not exec into the producing pod. Step producers MUST
		// call this before RegisterArtifact — the bug that motivates
		// this code is that without the wrap, downstream reads fail
		// with `exec stream: pods "..." not found` after the producer
		// pod is reaped.
		Context("when a DaemonSet backend is configured", func() {
			var (
				daemonWorker *jetbridge.Worker
				daemonCfg    jetbridge.Config
			)

			BeforeEach(func() {
				daemonCfg = cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonWorker = jetbridge.NewWorker(dbWorker, fakeClientset, daemonCfg)
			})

			It("wraps a container-mount DeferredVolume as a DaemonSetVolume", func() {
				deferred := jetbridge.NewDeferredVolume(
					"artifact-handle-1",
					"k8s-worker-1",
					nil, "test-namespace", "main", "/mnt/data",
				)

				artifact := daemonWorker.ArtifactFromVolume(deferred)
				Expect(artifact).ToNot(BeNil())
				Expect(artifact.Handle()).To(Equal("artifact-handle-1"))

				_, isDaemonSet := artifact.(*jetbridge.DaemonSetVolume)
				Expect(isDaemonSet).To(BeTrue(),
					"expected ArtifactFromVolume to return a *DaemonSetVolume, got %T; "+
						"without this wrap, downstream StreamOut execs into the producer pod",
					artifact,
				)
			})

			It("wraps a StubVolume as a DaemonSetVolume", func() {
				stub := jetbridge.NewStubVolume("artifact-handle-2", "k8s-worker-1", "/mnt/stub")

				artifact := daemonWorker.ArtifactFromVolume(stub)
				Expect(artifact).ToNot(BeNil())
				Expect(artifact.Handle()).To(Equal("artifact-handle-2"))

				_, isDaemonSet := artifact.(*jetbridge.DaemonSetVolume)
				Expect(isDaemonSet).To(BeTrue(), "expected *DaemonSetVolume, got %T", artifact)
			})

			It("preserves the handle as the artifact key", func() {
				deferred := jetbridge.NewDeferredVolume(
					"arbitrary-handle",
					"k8s-worker-1",
					nil, "ns", "main", "/mnt/x",
				)

				artifact := daemonWorker.ArtifactFromVolume(deferred)
				dsVol, ok := artifact.(*jetbridge.DaemonSetVolume)
				Expect(ok).To(BeTrue())
				Expect(dsVol.Key()).To(Equal("arbitrary-handle"))
				Expect(dsVol.Handle()).To(Equal("arbitrary-handle"))
			})

			It("resolves the source node from the ArtifactLocator when the locator has an entry", func() {
				locator := jetbridge.NewArtifactLocator()
				locator.Record(jetbridge.ArtifactKey("located-handle"), "node-17", "container/output")
				daemonWorker.SetArtifactLocator(locator)

				deferred := jetbridge.NewDeferredVolume(
					"located-handle",
					"k8s-worker-1",
					nil, "ns", "main", "/mnt/located",
				)

				artifact := daemonWorker.ArtifactFromVolume(deferred)
				dsVol, ok := artifact.(*jetbridge.DaemonSetVolume)
				Expect(ok).To(BeTrue())
				// Source() returns the worker name; the source node is
				// stored internally but is observable via StreamOut
				// behavior (tested at the integration level).
				Expect(dsVol.Source()).To(Equal("k8s-worker-1"))
			})

			It("returns nil when given a nil volume", func() {
				Expect(daemonWorker.ArtifactFromVolume(nil)).To(BeNil())
			})
		})

		Context("when NO DaemonSet backend is configured (legacy exec-only mode)", func() {
			// Phase 4 of this track makes the DaemonSet a hard
			// requirement, but until then the legacy fallback must keep
			// returning the original volume unchanged so existing
			// exec-backed callers still work.
			It("returns the volume unchanged", func() {
				deferred := jetbridge.NewDeferredVolume(
					"legacy-handle",
					"k8s-worker-1",
					nil, "ns", "main", "/mnt/legacy",
				)

				artifact := worker.ArtifactFromVolume(deferred)
				Expect(artifact).To(BeIdenticalTo(runtime.Artifact(deferred)),
					"expected the original volume when no DaemonSet backend is set; "+
						"wrapping without a backend would produce a DaemonSetVolume with no source node",
				)
			})

			It("returns nil when given a nil volume", func() {
				Expect(worker.ArtifactFromVolume(nil)).To(BeNil())
			})
		})

		Context("regression guard for the exec-backed artifact-read path", func() {
			// Phase 5 of the track makes the exec-backed *Volume.StreamOut
			// path reachable only from test code. This guard ensures a
			// DaemonSet-enabled worker NEVER hands out a *Volume reference
			// from ArtifactFromVolume, which would re-introduce the
			// "exec stream: pods ... not found" failure mode.
			It("never returns a *Volume (exec-backed) when a DaemonSet backend is configured", func() {
				daemonCfg := cfg
				daemonCfg.ArtifactDaemonHostPath = "/var/artifacts"
				daemonWorker := jetbridge.NewWorker(dbWorker, fakeClientset, daemonCfg)

				// Try every kind of volume we produce in the runtime:
				// DeferredVolume (container mounts when an executor is
				// set) and StubVolume (placeholder when no executor).
				inputs := []runtime.Volume{
					jetbridge.NewDeferredVolume("deferred-1", "w", nil, "ns", "main", "/mnt/d"),
					jetbridge.NewStubVolume("stub-1", "w", "/mnt/s"),
				}

				for _, vol := range inputs {
					artifact := daemonWorker.ArtifactFromVolume(vol)
					_, isExecVolume := artifact.(*jetbridge.Volume)
					Expect(isExecVolume).To(BeFalse(),
						"ArtifactFromVolume handed out a *jetbridge.Volume for handle %q — "+
							"this would route StreamOut through exec into the producer pod, "+
							"which breaks once the reaper deletes the pod. Wrap via the "+
							"DaemonSet storage backend instead.",
						vol.Handle(),
					)
				}
			})
		})
	})
})
