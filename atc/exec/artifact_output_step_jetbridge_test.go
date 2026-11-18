package exec_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/artifactserver"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// `fly execute -o` is a task followed by an artifact output step, and then a
// download through the artifact server. On the JetBridge runtime a task's
// output is a step volume the artifact daemon holds by its artifact key: no
// volume row describes it. The artifact output step used to call
// InitializeArtifact on that missing row and the web panicked, so the build
// errored after a task that exited 0.
//
// Everything below is the production path -- the jetbridge worker the
// default worker factory builds, its task container's output mount, the
// worker's ArtifactFromVolume, the artifact output step, the worker pool and
// the artifact server's download handler -- against a real database. Only the
// node's artifact daemon is an httptest server serving the output's tar.
var _ = Describe("ArtifactOutputStep on a JetBridge step output", func() {
	const (
		namespace  = "test-ns"
		workerName = "k8s-test-ns"
		outputName = "my-output"
		workDir    = "/tmp/build/one-off"
		outputPath = workDir + "/" + outputName
	)

	var (
		ctx      context.Context
		fixture  *execDBFixture
		team     db.Team
		oneOff   db.Build
		pool     worker.Pool
		artifact runtime.Artifact
		rewrap   func() runtime.Artifact
		state    exec.RunState

		outputHandle string

		daemonMu      sync.Mutex
		daemonFetches []string
	)

	outputTar := func() []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		body := "OUTPUT_DATA\n"
		Expect(tw.WriteHeader(&tar.Header{Name: "result.txt", Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(body))})).To(Succeed())
		_, err := tw.Write([]byte(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Close()).To(Succeed())
		return buf.Bytes()
	}

	BeforeEach(func() {
		ctx = context.Background()
		fixture = useExecDB()
		daemonFetches = nil

		var err error
		team, err = fixture.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
		Expect(err).NotTo(HaveOccurred())
		oneOff, err = team.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())

		archive := outputTar()
		daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/artifacts/"+outputHandle:
				daemonMu.Lock()
				daemonFetches = append(daemonFetches, r.URL.Path)
				daemonMu.Unlock()
				_, _ = w.Write(archive)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		DeferCleanup(daemon.Close)

		parsed, err := url.Parse(daemon.URL)
		Expect(err).NotTo(HaveOccurred())
		port, err := strconv.Atoi(parsed.Port())
		Expect(err).NotTo(HaveOccurred())
		host := parsed.Hostname()

		clientset := fake.NewSimpleClientset(
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
				Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
					{Type: corev1.NodeInternalIP, Address: host},
				}},
			},
			&discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "artifact-daemon-fixture",
					Namespace: namespace,
					Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
				},
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
			},
		)

		capabilityKey := make([]byte, 32)
		for i := range capabilityKey {
			capabilityKey[i] = byte(i + 1)
		}
		cfg := jetbridge.NewConfig(namespace, "")
		cfg.ArtifactDaemonHostPath = "/artifact-store"
		cfg.ArtifactDaemonResolveCapabilityKey = capabilityKey
		cfg.ArtifactDaemonPort = port
		cfg.ArtifactDaemonService = "artifact-daemon"
		cfg.ArtifactHelperImage = "alpine:latest"

		locator := jetbridge.NewArtifactLocator()
		volumeRepo := db.NewVolumeRepository(fixture.Conn)
		workerDB := worker.DB{
			WorkerFactory: fixture.WorkerFactory,
			TeamFactory:   fixture.TeamFactory,
			VolumeRepo:    volumeRepo,
			LockFactory:   fixture.LockFactory,
		}
		factory := worker.DefaultFactory{
			DB:                 workerDB,
			K8sClientset:       clientset,
			K8sConfig:          &cfg,
			K8sArtifactLocator: locator,
			K8sDaemonClient: jetbridge.NewDaemonClient(
				lagertest.NewTestLogger("daemon-client"), clientset, namespace, "artifact-daemon", port, nil),
		}
		pool = worker.NewPool(factory, workerDB)

		savedWorker, err := fixture.WorkerFactory.SaveWorker(atc.Worker{
			Name: workerName, Platform: "linux", State: string(db.WorkerStateRunning),
		}, 0)
		Expect(err).NotTo(HaveOccurred())
		k8sWorker := factory.NewWorker(testLogger, savedWorker)

		By("making the one-off's task container, whose output is a step volume")
		container, mounts, err := k8sWorker.FindOrCreateContainer(
			ctx,
			db.NewBuildStepContainerOwner(oneOff.ID(), "task-plan", team.ID()),
			db.ContainerMetadata{Type: db.ContainerTypeTask, WorkingDirectory: workDir},
			runtime.ContainerSpec{
				TeamID:    team.ID(),
				Type:      db.ContainerTypeTask,
				Dir:       workDir,
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Outputs:   runtime.OutputPaths{outputName: outputPath},
			},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		var outputVolume runtime.Volume
		for _, mount := range mounts {
			if mount.MountPath == outputPath {
				outputVolume = mount.Volume
			}
		}
		Expect(outputVolume).NotTo(BeNil())
		Expect(outputVolume.DBVolume()).To(BeNil(), "a step volume has no volume row; that is the case under test")
		outputHandle = outputVolume.Handle()

		By("recording where the finished task's output lives, as RecordOutputs does")
		containerHandle := container.DBContainer().Handle()
		locator.RecordStepOutput(containerHandle, outputHandle, "node-1", containerHandle+"/"+outputName)

		By("registering the output the way the task step does")
		rewrap = func() runtime.Artifact { return k8sWorker.ArtifactFromVolume(outputVolume) }
		artifact = rewrap()
		state = exec.NewRunState(noopStepper, vars.StaticVariables{})
		state.ArtifactRepository().RegisterArtifact(build.ArtifactName(outputName), artifact, false)
	})

	It("initializes the build's artifact and serves the output for download", func() {
		step := exec.NewArtifactOutputStep(
			atc.Plan{ID: "output-plan", ArtifactOutput: &atc.ArtifactOutputPlan{Name: outputName}},
			oneOff,
			pool,
		)
		ok, err := step.Run(ctx, state)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		artifacts, err := oneOff.Artifacts()
		Expect(err).NotTo(HaveOccurred())
		Expect(artifacts).To(HaveLen(1))
		Expect(artifacts[0].Name()).To(Equal(outputName))

		artifactVolume, found, err := team.FindVolumeForWorkerArtifact(artifacts[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(artifactVolume.Handle()).To(Equal(outputHandle), "the row names the artifact key the daemon holds")
		Expect(artifactVolume.WorkerName()).To(Equal(workerName))
		Expect(artifactVolume.TeamID()).To(Equal(team.ID()))

		By("downloading it through the artifact server, as fly execute -o does")
		server := artifactserver.NewServer(lagertest.NewTestLogger("artifact-server"), pool)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/?:artifact_id="+strconv.Itoa(artifacts[0].ID()), nil)
		server.GetArtifact(team).ServeHTTP(recorder, request)
		Expect(recorder.Code).To(Equal(http.StatusOK))

		gz := compression.NewGzipCompression()
		raw, err := gz.NewReader(io.NopCloser(bytes.NewReader(recorder.Body.Bytes())))
		Expect(err).NotTo(HaveOccurred())
		tr := tar.NewReader(raw)
		hdr, err := tr.Next()
		Expect(err).NotTo(HaveOccurred())
		Expect(hdr.Name).To(Equal("result.txt"))
		body, err := io.ReadAll(tr)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("OUTPUT_DATA\n"))

		daemonMu.Lock()
		defer daemonMu.Unlock()
		Expect(daemonFetches).To(ConsistOf("/artifacts/" + outputHandle))
	})

	It("is idempotent when the step runs again for the same output", func() {
		step := exec.NewArtifactOutputStep(
			atc.Plan{ID: "output-plan", ArtifactOutput: &atc.ArtifactOutputPlan{Name: outputName}},
			oneOff,
			pool,
		)
		_, err := step.Run(ctx, state)
		Expect(err).NotTo(HaveOccurred())

		By("re-registering a fresh wrap of the same step volume, as a resumed build would")
		again := exec.NewRunState(noopStepper, vars.StaticVariables{})
		again.ArtifactRepository().RegisterArtifact(build.ArtifactName(outputName), rewrap(), false)
		ok, err := step.Run(ctx, again)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})
})
