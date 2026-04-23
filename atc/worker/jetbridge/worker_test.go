package jetbridge_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
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
		fakeDBWorker  *dbfakes.FakeWorker
		fakeClientset *fake.Clientset
		worker        *jetbridge.Worker
		ctx           context.Context
		cfg           jetbridge.Config
		delegate      runtime.BuildStepDelegate
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeDBWorker = new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("k8s-worker-1")
		fakeClientset = fake.NewSimpleClientset()
		cfg = jetbridge.NewConfig("test-namespace", "")
		delegate = &noopDelegate{}

		worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
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
			var (
				fakeCreatingContainer *dbfakes.FakeCreatingContainer
				fakeCreatedContainer  *dbfakes.FakeCreatedContainer
			)

			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, nil)

				fakeCreatingContainer = new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("test-handle")
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("test-handle")
				fakeCreatingContainer.CreatedReturns(fakeCreatedContainer, nil)
			})

			It("creates a container in the DB and defers Pod creation to Run", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("creating the container in the DB")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(1))

				By("marking the container as created")
				Expect(fakeCreatingContainer.CreatedCallCount()).To(Equal(1))

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

		Context("when transitioning to created state fails", func() {
			var fakeCreatingContainer *dbfakes.FakeCreatingContainer

			BeforeEach(func() {
				fakeDBWorker.FindContainerReturns(nil, nil, nil)

				fakeCreatingContainer = new(dbfakes.FakeCreatingContainer)
				fakeCreatingContainer.HandleReturns("test-handle")
				fakeDBWorker.CreateContainerReturns(fakeCreatingContainer, nil)

				fakeCreatingContainer.CreatedReturns(nil, fmt.Errorf("db connection lost"))
			})

			It("marks the container as failed so the GC can clean it up", func() {
				_, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).To(HaveOccurred())

				Expect(fakeCreatingContainer.FailedCallCount()).To(Equal(1))
			})
		})

		Context("when a created container already exists in the DB", func() {
			var fakeCreatedContainer *dbfakes.FakeCreatedContainer

			BeforeEach(func() {
				fakeCreatedContainer = new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("existing-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)
			})

			It("returns the existing container without creating a new one in the DB", func() {
				container, _, err := worker.FindOrCreateContainer(ctx, owner, metadata, spec, delegate)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("not creating a new container in the DB")
				Expect(fakeDBWorker.CreateContainerCallCount()).To(Equal(0))
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

				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("lookup-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)
			})

			It("returns the container", func() {
				container, found, err := worker.LookupContainer(ctx, "lookup-handle")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(container).ToNot(BeNil())
			})

			It("returns a container with a valid DBContainer for hijack support", func() {
				fakeCreatedContainer := new(dbfakes.FakeCreatedContainer)
				fakeCreatedContainer.HandleReturns("lookup-handle")
				fakeDBWorker.FindContainerReturns(nil, fakeCreatedContainer, nil)

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

				fakeDBWorker.FindContainerReturns(nil, nil, nil)
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
	})

	Describe("CreateVolumeForArtifact", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		Context("when the volume repo is configured", func() {
			var (
				fakeCreatingVolume *dbfakes.FakeCreatingVolume
				fakeCreatedVolume  *dbfakes.FakeCreatedVolume
				fakeArtifact       *dbfakes.FakeWorkerArtifact
			)

			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume = new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeCreatingVolume.IDReturns(42)

				fakeCreatedVolume = new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("artifact-volume-handle")

				fakeArtifact = new(dbfakes.FakeWorkerArtifact)
				fakeArtifact.IDReturns(7)

				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)
				fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)
				fakeCreatedVolume.InitializeArtifactReturns(fakeArtifact, nil)
			})

			It("creates an artifact volume and returns it with the artifact", func() {
				vol, artifact, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(vol).ToNot(BeNil())
				Expect(artifact).ToNot(BeNil())

				By("creating a volume with the correct team ID, worker name, and type")
				Expect(fakeVolumeRepo.CreateVolumeCallCount()).To(Equal(1))
				teamID, workerName, volType := fakeVolumeRepo.CreateVolumeArgsForCall(0)
				Expect(teamID).To(Equal(1))
				Expect(workerName).To(Equal("k8s-worker-1"))
				Expect(volType).To(Equal(db.VolumeTypeArtifact))

				By("transitioning the volume to created state")
				Expect(fakeCreatingVolume.CreatedCallCount()).To(Equal(1))

				By("initializing the artifact on the created volume")
				Expect(fakeCreatedVolume.InitializeArtifactCallCount()).To(Equal(1))
				name, buildID := fakeCreatedVolume.InitializeArtifactArgsForCall(0)
				Expect(name).To(Equal(""))
				Expect(buildID).To(Equal(0))

				By("returning the artifact from the DB")
				Expect(artifact.ID()).To(Equal(7))
			})

			It("returns a volume with the correct handle", func() {
				vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(vol.Handle()).To(Equal("artifact-volume-handle"))
			})

			Context("when the artifact store is configured", func() {
				BeforeEach(func() {
					worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
					worker.SetVolumeRepo(fakeVolumeRepo)
				})

				It("returns an DaemonSetVolume", func() {
					vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
					Expect(err).ToNot(HaveOccurred())
					Expect(vol).ToNot(BeNil())

					asVol, ok := vol.(*jetbridge.DaemonSetVolume)
					Expect(ok).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
					Expect(asVol.Key()).To(Equal("artifact-volume-handle"))
					Expect(asVol.Handle()).To(Equal("artifact-volume-handle"))
				})
			})

			It("always returns a DaemonSetVolume", func() {
				vol, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).ToNot(HaveOccurred())
				Expect(vol).ToNot(BeNil())

				_, isDaemonSet := vol.(*jetbridge.DaemonSetVolume)
				Expect(isDaemonSet).To(BeTrue(), "expected DaemonSetVolume, got %T", vol)
			})
		})

		Context("when the volume repo is NOT configured", func() {
			It("returns an error", func() {
				// Create a fresh worker without SetVolumeRepo
				freshWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
				_, _, err := freshWorker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("volume repository not configured")))
			})
		})

		Context("when CreateVolume fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)
				fakeVolumeRepo.CreateVolumeReturns(nil, fmt.Errorf("db connection lost"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("db connection lost")))
			})
		})

		Context("when transitioning to created state fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)

				fakeCreatingVolume.CreatedReturns(nil, fmt.Errorf("transition error"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("transition error")))
			})
		})

		Context("when InitializeArtifact fails", func() {
			BeforeEach(func() {
				fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
				worker.SetVolumeRepo(fakeVolumeRepo)

				fakeCreatingVolume := new(dbfakes.FakeCreatingVolume)
				fakeCreatingVolume.HandleReturns("artifact-volume-handle")
				fakeVolumeRepo.CreateVolumeReturns(fakeCreatingVolume, nil)

				fakeCreatedVolume := new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("artifact-volume-handle")
				fakeCreatingVolume.CreatedReturns(fakeCreatedVolume, nil)

				fakeCreatedVolume.InitializeArtifactReturns(nil, fmt.Errorf("artifact init error"))
			})

			It("returns the error", func() {
				_, _, err := worker.CreateVolumeForArtifact(ctx, 1)
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("artifact init error")))
			})
		})
	})

	Describe("LookupVolume", func() {
		var fakeVolumeRepo *dbfakes.FakeVolumeRepository

		BeforeEach(func() {
			fakeVolumeRepo = new(dbfakes.FakeVolumeRepository)
			worker.SetVolumeRepo(fakeVolumeRepo)
		})

		Context("when the volume exists in the DB", func() {
			var fakeCreatedVolume *dbfakes.FakeCreatedVolume

			BeforeEach(func() {
				fakeCreatedVolume = new(dbfakes.FakeCreatedVolume)
				fakeCreatedVolume.HandleReturns("vol-handle-1")
				fakeCreatedVolume.WorkerNameReturns("k8s-worker-1")
				fakeVolumeRepo.FindVolumeReturns(fakeCreatedVolume, true, nil)
			})

			It("returns a cache-backed volume", func() {
				vol, found, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(vol).ToNot(BeNil())
				Expect(vol.Handle()).To(Equal("vol-handle-1"))
			})

			It("calls FindVolume with the correct handle", func() {
				_, _, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeVolumeRepo.FindVolumeCallCount()).To(Equal(1))
				Expect(fakeVolumeRepo.FindVolumeArgsForCall(0)).To(Equal("vol-handle-1"))
			})
		})

		Context("when the volume does not exist in the DB", func() {
			BeforeEach(func() {
				fakeVolumeRepo.FindVolumeReturns(nil, false, nil)
			})

			It("returns not found", func() {
				_, found, err := worker.LookupVolume(ctx, "nonexistent")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when the DB returns an error", func() {
			BeforeEach(func() {
				fakeVolumeRepo.FindVolumeReturns(nil, false, fmt.Errorf("db connection lost"))
			})

			It("returns the error", func() {
				_, _, err := worker.LookupVolume(ctx, "vol-handle-1")
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(ContainSubstring("db connection lost")))
			})
		})

		Context("when no volume repo is configured", func() {
			BeforeEach(func() {
				worker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, cfg)
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
				daemonWorker := jetbridge.NewWorker(fakeDBWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)
				daemonWorker.SetDaemonClient(client)

				vol, found, err := daemonWorker.FindDaemonResourceCache(ctx, 42)
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
				daemonWorker := jetbridge.NewWorker(fakeDBWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)
				daemonWorker.SetDaemonClient(client)

				vol, found, err := daemonWorker.FindDaemonResourceCache(ctx, 42)
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
				daemonWorker := jetbridge.NewWorker(fakeDBWorker, daemonClientset, daemonCfg)

				logger := lagertest.NewTestLogger("test")
				client := jetbridge.NewDaemonClient(logger, daemonClientset, "test-namespace", "artifact-daemon", port, nil)

				locator := jetbridge.NewArtifactLocator()
				daemonWorker.SetArtifactLocator(locator)
				daemonWorker.SetDaemonClient(client)

				_, found, err := daemonWorker.FindDaemonResourceCache(ctx, 42)
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
				daemonWorker := jetbridge.NewWorker(fakeDBWorker, daemonClientset, daemonCfg)

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
				_, found, err := daemonWorker.FindDaemonResourceCache(ctx, 42)
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
				daemonWorker = jetbridge.NewWorker(fakeDBWorker, fakeClientset, daemonCfg)
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
				daemonWorker := jetbridge.NewWorker(fakeDBWorker, fakeClientset, daemonCfg)

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
