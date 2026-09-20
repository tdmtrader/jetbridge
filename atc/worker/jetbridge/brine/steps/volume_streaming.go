package steps

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VolumeStreamingDefinitions expresses volume behavior as artifact movement.
// Nothing here names tar, exec, a pod, or ExecAttrs.
func VolumeStreamingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, VolumeSet](
			"a volume {string} mounted at {string} with {string} binding",
			[]string{"span-capture"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (VolumeSet, error) {
				capture, ok := resources.Get("span-capture").(SpanCapture)
				if !ok {
					return VolumeSet{}, fmt.Errorf("volume tracing resource is %T", resources.Get("span-capture"))
				}
				capture, err := capture.ready()
				if err != nil {
					return VolumeSet{}, err
				}
				set, err := newLiveVolumeSet(rec, capture)
				if err != nil {
					return VolumeSet{}, err
				}
				return addVolume(set, p)
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"another volume {string} mounted at {string} with {string} binding",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				return addVolume(in, p)
			},
		),

		TransformUsing[brine.Empty, VolumeSet](
			"volume {string} sits on a cluster that cannot run commands",
			[]string{"real-cluster"},
			func(_ brine.Empty, a Args, res brine.Resources) (VolumeSet, error) {
				in := newVolumeSet()
				cluster, err := getRealCluster(res)
				if err != nil {
					return VolumeSet{}, err
				}
				name := a.String(0)
				ns, err := cluster.Clientset.CoreV1().Namespaces().Create(in.Ctx,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "volume-error-"}},
					metav1.CreateOptions{})
				if err != nil {
					return VolumeSet{}, fmt.Errorf("create volume-error namespace: %w", err)
				}
				// No kubelet is needed to reject exec into an absent pod. Verify
				// that premise against the API, then use the production transport.
				podName := name + "-pod"
				if _, err := cluster.Clientset.CoreV1().Pods(ns.Name).Get(in.Ctx, podName,
					metav1.GetOptions{}); !apierrors.IsNotFound(err) {
					return VolumeSet{}, fmt.Errorf("expected absent pod %q: %v", podName, err)
				}
				volume := jetbridge.NewDeferredVolume(
					name+"-handle", "k8s-worker-1",
					jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.RESTConfig),
					ns.Name, "main", "/tmp/build/inputs",
				)
				volume.SetPodName(podName)
				in.Volumes[name] = volume
				return in, nil
			},
		),

		Transform[VolumeSet, VolumeSet](
			"a file {string} containing {string} is put into volume {string} at {string} using {string}",
			func(in VolumeSet, a Args) (VolumeSet, error) {
				volName := a.String(2)

				volume, err := in.volume(volName)
				if err != nil {
					return VolumeSet{}, err
				}
				archive, err := tarOfOneFile(a.String(0), a.String(1))
				if err != nil {
					return VolumeSet{}, err
				}
				var encoding compression.Compression
				switch a.String(4) {
				case "gzip":
					encoding = compression.NewGzipCompression()
				case "raw":
					decompressed, err := compression.NewGzipCompression().NewReader(io.NopCloser(archive))
					if err != nil {
						return VolumeSet{}, err
					}
					defer decompressed.Close()
					archive = decompressed
				default:
					return VolumeSet{}, fmt.Errorf("unsupported upload encoding %q", a.String(4))
				}
				expected, err := plainTarOfOneFile(a.String(0), a.String(1))
				if err != nil {
					return VolumeSet{}, err
				}
				if err := volume.StreamIn(in.Ctx, a.String(3), encoding, 0, archive); err != nil {
					return VolumeSet{}, fmt.Errorf("stream into %q: %w", volName, err)
				}
				if err := in.requireVolumeBytes(volName, a.String(3), "stdin", expected); err != nil {
					return VolumeSet{}, err
				}
				return in, nil
			},
		),

		// The user story the volume-to-volume ginkgo test was really about:
		// one step's output becomes the next step's input.
		Transform[VolumeSet, VolumeSet](
			"the contents of volume {string} are moved into volume {string}",
			func(in VolumeSet, a Args) (VolumeSet, error) {
				if err := moveVolumeBytes(in, a.String(0), a.String(1)); err != nil {
					return VolumeSet{}, err
				}
				return in, nil
			},
		),

		Transform[VolumeSet, VolumeRead](
			"volume {string} is opened then drained with and without compression",
			func(in VolumeSet, a Args) (VolumeRead, error) {
				volume, err := in.volume(a.String(0))
				if err != nil {
					return VolumeRead{}, err
				}
				out := VolumeRead{}
				for _, encoding := range []struct {
					name        string
					compression compression.Compression
				}{
					{"raw", nil}, {"gzip", compression.NewGzipCompression()},
				} {
					stream, openErr := volume.StreamOut(in.Ctx, ".", encoding.compression)
					attempt := volumeReadAttempt{encoding: encoding.name, openErr: openErr}
					if stream != nil {
						if openErr == nil {
							_, attempt.readErr = io.ReadAll(stream)
						}
						attempt.closeErr = stream.Close()
					} else if openErr == nil {
						attempt.openErr = fmt.Errorf("StreamOut returned no reader")
					}
					out.readAttempts = append(out.readAttempts, attempt)
				}
				return out, nil
			}),

		// Reading is an attempt, so that failure is assertable rather than
		// fatal to the scenario.
		Transform[VolumeSet, VolumeRead](
			"volume {string} is read from {string}",
			func(in VolumeSet, a Args) (VolumeRead, error) {
				volume, err := in.volume(a.String(0))
				if err != nil {
					return VolumeRead{}, err
				}

				var out VolumeRead
				if in.execTrace != nil {
					out = readVolumeBytes(in, a.String(0), a.String(1))
				} else {
					out = readArtifactFiles(in.Ctx, volume, a.String(1))
				}
				out.source = &in
				return out, nil
			},
		),

		Transform[VolumeSet, VolumeRead](
			"a file is put into volume {string}",
			func(in VolumeSet, a Args) (VolumeRead, error) {
				volume, err := in.volume(a.String(0))
				if err != nil {
					return VolumeRead{}, err
				}
				archive, err := tarOfOneFile("probe.txt", "probe")
				if err != nil {
					return VolumeRead{}, err
				}
				writeErr := volume.StreamIn(in.Ctx, ".", compression.NewGzipCompression(), 0, archive)
				if writeErr != nil {
					return VolumeRead{Err: writeErr, Message: writeErr.Error()}, nil
				}
				return VolumeRead{}, nil
			},
		),

		CheckStringFor[VolumeRead]("the artifact {string} containing {string} is there",
			"the artifact's contents",
			func(in VolumeRead, name string) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("reading the volume failed: %w", in.Err)
				}
				if in.remote != nil {
					if err := in.remote.requireBytesAndRetries(); err != nil {
						return "", err
					}
				}
				got, found := in.Files[name]
				if !found {
					names := make([]string, 0, len(in.Files))
					for n := range in.Files {
						names = append(names, n)
					}
					return "", fmt.Errorf("expected %q, found %v", name, names)
				}
				return got, nil
			}),

		// Keeps its own body: the match is case-INSENSITIVE, which CheckContains
		// is not, and the failure must have happened at all.
		Assert[VolumeRead](
			"it fails rather than panicking, saying {string}",
			func(in VolumeRead, args Args) error {
				want := args.String(0)

				if len(in.readAttempts) != 0 {
					for _, attempt := range in.readAttempts {
						if attempt.openErr != nil {
							return fmt.Errorf("%s stream did not open successfully: %w", attempt.encoding, attempt.openErr)
						}
						if attempt.closeErr != nil {
							return fmt.Errorf("%s reader did not close: %w", attempt.encoding, attempt.closeErr)
						}
						if attempt.readErr == nil {
							return fmt.Errorf("%s reader swallowed the exec failure", attempt.encoding)
						}
						if !containsFold(attempt.readErr.Error(), want) {
							return fmt.Errorf("%s reader error must mention %q, got %q", attempt.encoding, want, attempt.readErr.Error())
						}
					}
					return nil
				}
				if in.Err == nil {
					return fmt.Errorf("expected a failure mentioning %q, but it succeeded", want)
				}
				if !containsFold(in.Message, want) {
					return fmt.Errorf("expected the failure to mention %q, got %q", want, in.Message)
				}
				return nil
			},
		),
	}
}

// readArtifactFiles observes the same gzip archive contract for exec-backed volumes
// and daemon-backed artifacts. Setup errors remain fatal at the caller; read
// errors remain scenario state, so failure steps can inspect them.
func readArtifactFiles(ctx context.Context, volume runtime.Artifact, path string) VolumeRead {
	stream, streamErr := volume.StreamOut(ctx, path, compression.NewGzipCompression())
	if streamErr != nil {
		return VolumeRead{Err: streamErr, Message: streamErr.Error()}
	}
	defer stream.Close()

	files, readErr := filesInGzippedTar(stream)
	if readErr != nil {
		return VolumeRead{Err: readErr, Message: readErr.Error()}
	}
	return VolumeRead{Files: files}
}

// Failure-only Givens need no live cluster. Mounted volumes opt into the live tier.
func newVolumeSet() VolumeSet {
	return VolumeSet{Volumes: map[string]*jetbridge.Volume{}, Ctx: context.Background()}
}

// VolumeIdentityDefinitions checks constructor identity, owning workers and
// the persisted artifact associations. Streaming has its own live scenarios.
func VolumeIdentityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, VolumeIdentity](
			"two persisted volumes on this worker",
			[]string{"jetbridge-db", "real-cluster"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (VolumeIdentity, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return VolumeIdentity{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return VolumeIdentity{}, err
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "volume-identity"})
				if err != nil {
					return VolumeIdentity{}, fmt.Errorf("create team: %w", err)
				}
				cluster, err := getRealCluster(res)
				if err != nil {
					return VolumeIdentity{}, err
				}
				executor := jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.RESTConfig)
				out := VolumeIdentity{WorkerName: dbWorker.Name(), TeamID: team.ID()}
				for _, spec := range []struct{ handle, pod, mount, artifact string }{
					{"vol-handle-123", "test-pod", "/tmp/build/inputs", "volume-test-artifact"},
					{"vol-handle-456", "other-pod", "/tmp/build/outputs", "volume-test-artifact-2"},
				} {
					creating, err := database.VolumeRepository.CreateVolumeWithHandle(
						spec.handle, team.ID(), dbWorker.Name(), db.VolumeTypeArtifact)
					if err != nil {
						return VolumeIdentity{}, fmt.Errorf("create volume: %w", err)
					}
					created, err := creating.Created()
					if err != nil {
						return VolumeIdentity{}, fmt.Errorf("mark volume created: %w", err)
					}
					artifact, err := created.InitializeArtifact(spec.artifact, 0)
					if err != nil {
						return VolumeIdentity{}, fmt.Errorf("initialize volume artifact: %w", err)
					}
					if artifact.ID() <= 0 {
						return VolumeIdentity{}, fmt.Errorf("volume artifact has no persisted identity")
					}
					row, found, err := database.VolumeRepository.FindVolume(spec.handle)
					if err != nil || !found {
						return VolumeIdentity{}, fmt.Errorf("reload volume %q: found=%t, error=%v", spec.handle, found, err)
					}
					// Identity-only construction: the real executor never runs.
					volume := jetbridge.NewVolume(row, executor,
						spec.pod, "test-namespace", "main", spec.mount)
					out.Volumes = append(out.Volumes, VolumeIdentityRow{
						Volume: volume, DBVolume: row, DBHandle: spec.handle, Artifact: artifact,
					})
				}
				out.DaemonVolume = jetbridge.NewDaemonSetVolume(
					"key", "runtime-handle", dbWorker.Name(), out.Volumes[0].DBVolume, "",
					jetbridge.NewConfig("test-namespace", ""), nil)
				return out, nil
			},
		),

		CheckThat[VolumeIdentity]("the volumes retain their handles, worker and database rows",
			func(in VolumeIdentity) error {
				if len(in.Volumes) != 2 {
					return fmt.Errorf("identity comparison requires two persisted volumes, got %d", len(in.Volumes))
				}
				for _, entry := range in.Volumes {
					if entry.Volume.Handle() != entry.DBHandle {
						return fmt.Errorf(
							"expected the volume to identify as %q — the handle the artifact repository keys on — got %q",
							entry.DBHandle, entry.Volume.Handle())
					}
					if entry.Volume.Source() != in.WorkerName {
						return fmt.Errorf("expected the volume to name worker %q, got %q",
							in.WorkerName, entry.Volume.Source())
					}
					if entry.Volume.DBVolume() == nil {
						return fmt.Errorf("the deferred volume lost its database row")
					}
					if entry.Volume.DBVolume() != entry.DBVolume {
						return fmt.Errorf("the exec-backed volume replaced its original database object")
					}
					if entry.DBVolume.Handle() != entry.DBHandle {
						return fmt.Errorf("expected persisted handle %q, got %q", entry.DBHandle, entry.DBVolume.Handle())
					}
					artifactVolume, found, err := entry.Artifact.Volume(in.TeamID)
					if err != nil || !found {
						return fmt.Errorf("resolve artifact %d: found=%t, error=%v", entry.Artifact.ID(), found, err)
					}
					if artifactVolume.Handle() != entry.Volume.Handle() {
						return fmt.Errorf("artifact %d resolves to %q, but its runtime volume reports %q",
							entry.Artifact.ID(), artifactVolume.Handle(), entry.Volume.Handle())
					}
				}
				if in.Volumes[0].Volume.Handle() == in.Volumes[1].Volume.Handle() {
					return fmt.Errorf("independent volumes report the same runtime handle")
				}
				first := in.Volumes[0]
				if in.DaemonVolume.DBVolume() == nil {
					return fmt.Errorf("the daemonset volume lost its database row")
				}
				if in.DaemonVolume.DBVolume() != first.DBVolume {
					return fmt.Errorf("the daemonset volume replaced its original database object")
				}
				if got := in.DaemonVolume.DBVolume().TeamID(); got != in.TeamID {
					return fmt.Errorf("expected the row to belong to team %d, got %d", in.TeamID, got)
				}
				if got := in.DaemonVolume.DBVolume().Type(); got != db.VolumeTypeArtifact {
					return fmt.Errorf("expected an artifact row, got volume type %q", got)
				}
				if got := in.DaemonVolume.DBVolume().Handle(); got != first.DBHandle {
					return fmt.Errorf("expected the daemonset volume's row to be %q, got %q", first.DBHandle, got)
				}
				if got := in.DaemonVolume.DBVolume().WorkerName(); got != in.WorkerName {
					return fmt.Errorf("expected the row to name worker %q, got %q", in.WorkerName, got)
				}
				return nil
			}),
	}
}

// ---------------------------------------------------------------------------
// Artifacts that live on another node
// ---------------------------------------------------------------------------

// Remote artifacts use production daemons and real Kubernetes discovery.
// Faults affect actual TCP connections or the daemon's owned filesystem;
// no HTTP handler implements artifact serving for the tests.

// remoteArtifactKey identifies the artifact in requests and diagnostics.
const remoteArtifactKey = "step-output"

// RemoteArtifact is an artifact on another node's daemon, described but not
// yet fetched. Refinements adjust how that node's daemon behaves, so a
// scenario says what it holds and how it misbehaves in either order.
type RemoteArtifact struct {
	Ctx         context.Context
	Key         string
	FileName    string
	Content     string
	expectedRaw []byte
	trace       *daemonWireObservation
	nodeReads   *execObservation
	nodeName    string

	// Transport drops and unreadable storage are injected at real boundaries.
	// These settings are not request records.
	DropFirst    int
	NeverAnswers bool
	ServerError  bool

	// Forgotten means the web restarted and lost which node produced this
	// artifact, and no daemon discovery is configured to find it again. There
	// is no daemon at all in that case — that is the point.
	Forgotten bool

	// Refused means the producing node is STILL IN THE CLUSTER and its
	// address still resolves — there is simply nothing listening on the
	// daemon port, so the connection is refused. That is a different
	// situation from Forgotten and from a node that has left, and it reaches
	// the runtime down a different branch: those two never produce a URL to
	// ask, this one produces a URL, asks it, and is turned away.
	Refused bool

	// Mirror is what a peer daemon holds under the mirrored path, empty when
	// no peer has a copy. It is deliberately different text from Content so a
	// scenario can name WHICH copy the consumer received.
	Mirror string

	// Fallback means the ATC is configured to discover the other daemons and
	// ask them. Without it there is nowhere to fall back to and the recorded
	// source's failure is final.
	Fallback bool
}

// refusedPort returns a loopback port with nothing listening on it: a listener
// is opened to reserve a free port and closed again, so the dial that follows
// is really refused by the kernel rather than black-holed. A black hole would
// take the same production branch, but it would cost a full client timeout on
// each of the three attempts instead of nothing at all.
func refusedPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a port to close: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		l.Close()
		return 0, fmt.Errorf("reserved a %T, not a TCP address", l.Addr())
	}
	if err := l.Close(); err != nil {
		return 0, fmt.Errorf("close the reserved port: %w", err)
	}
	return addr.Port, nil
}

// ---------------------------------------------------------------------------
// Where a step is placed, and which volumes it is handed
// ---------------------------------------------------------------------------

// RemoteArtifactDefinitions covers fetching node-local artifacts and refusing
// writes when no daemon can receive them. Placement uses the shared real
// artifact-recording vocabulary.
func RemoteArtifactDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[brine.Empty, RemoteArtifact](
			"an artifact on another node holding the file {string} containing {string}",
			func(_ brine.Empty, a Args) (RemoteArtifact, error) {
				return RemoteArtifact{
					Ctx: context.Background(), Key: remoteArtifactKey,
					FileName: a.String(0), Content: a.String(1),
				}, nil
			},
		),

		// The restart case: the locator is in memory, so a web that restarted
		// no longer knows which node produced this artifact. With daemon
		// discovery configured the runtime probes for it; with none, there is
		// nowhere to send anything.
		brine.DefineMap[brine.Empty, RemoteArtifact](
			"an artifact whose producing node the web has forgotten, and no way to look it up",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (RemoteArtifact, error) {
				return RemoteArtifact{
					Ctx: context.Background(), Key: remoteArtifactKey, Forgotten: true,
				}, nil
			},
		),

		Refine[RemoteArtifact]("that node's daemon drops the first {int} connections",
			func(in RemoteArtifact, a Args) RemoteArtifact {
				in.DropFirst = a.Int(0)
				return in
			}),

		Refine[RemoteArtifact]("that node's daemon never completes a connection",
			func(in RemoteArtifact, _ Args) RemoteArtifact {
				in.NeverAnswers = true
				return in
			}),

		Refine[RemoteArtifact]("that node's daemon is failing and answers every request with an internal error",
			func(in RemoteArtifact, _ Args) RemoteArtifact {
				in.ServerError = true
				return in
			}),

		// The node has NOT left. It is in the cluster, it resolves, and the
		// address the runtime builds out of it is a real one — there is
		// simply nothing listening on the daemon port, because the daemon
		// pod is gone or has not come back yet. Every other fallback
		// scenario in these features takes the artifact's recorded node
		// away, which fails before a URL exists; this one produces the URL,
		// dials it, and is turned away.
		Refine[RemoteArtifact]("that node is still in the cluster, and its daemon port refuses the connection",
			func(in RemoteArtifact, _ Args) RemoteArtifact {
				in.Refused = true
				return in
			}),

		// A peer that received the mirror. The text it holds is deliberately
		// not the producer's, so a scenario can name which copy arrived
		// rather than only that something did.
		Refine[RemoteArtifact]("a peer daemon holds a mirrored copy of it containing {string}",
			func(in RemoteArtifact, a Args) RemoteArtifact {
				in.Mirror = a.String(0)
				return in
			}),

		Refine[RemoteArtifact]("the ATC can ask the other daemons for a mirrored copy",
			func(in RemoteArtifact, _ Args) RemoteArtifact {
				in.Fallback = true
				return in
			}),

		// Reading is an attempt, so a failure is assertable rather than fatal
		// to the scenario — the same shape the exec-backed reads above use,
		// and it lands in the same state, so it can reuse their checks.
		brine.DefineMap[RemoteArtifact, VolumeRead](
			"the next step fetches the artifact from that node",
			func(in RemoteArtifact, _ brine.Params, rec *brine.Recorder) (VolumeRead, error) {
				volume, err := in.daemon(rec)
				if err != nil {
					return VolumeRead{}, err
				}

				// Preserve both opaque raw delivery and the gzip StreamFile route.
				return in.read(volume), nil
			},
		),

		brine.DefineMap[RemoteArtifact, VolumeRead](
			"the step writes its output into that artifact",
			func(in RemoteArtifact, _ brine.Params, rec *brine.Recorder) (VolumeRead, error) {
				volume, err := in.daemon(rec)
				if err != nil {
					return VolumeRead{}, err
				}

				archive, err := tarOfOneFile("result.json", "built ok")
				if err != nil {
					return VolumeRead{}, err
				}
				writeErr := volume.StreamIn(in.Ctx, ".", compression.NewGzipCompression(), 0, archive)
				if writeErr != nil {
					return VolumeRead{Err: writeErr, Message: writeErr.Error()}, nil
				}
				return VolumeRead{}, nil
			},
		),

		// Keeps its own body: it is about an ABSENCE of success, which no
		// comparison combinator states. A transport failure that came back as
		// a successful read of nothing is the outcome this exists to catch,
		// and "it succeeded and here is what it handed over" is the message
		// that diagnoses it.
		CheckThat[VolumeRead]("the read fails rather than handing back an empty artifact",
			func(in VolumeRead) error {
				if in.remote != nil {
					if in.remote.observationErr != nil {
						return in.remote.observationErr
					}
					if len(in.readAttempts) != 2 {
						return fmt.Errorf("expected raw and gzip open attempts")
					}
					for _, attempt := range in.readAttempts {
						if attempt.openErr == nil {
							return fmt.Errorf("%s StreamOut must fail at open, not merely decode an invalid archive", attempt.encoding)
						}
					}
				}
				if in.Err == nil {
					return fmt.Errorf(
						"expected the read to fail, but it succeeded and handed back %d files (%v) — "+
							"a step that cannot reach its input must be told so, not given an empty "+
							"directory it will then fail on with no explanation",
						len(in.Files), sortedKeys(in.Files))
				}
				return nil
			}),

		// "Gone" and "broken" are different situations and a build log that
		// confuses them sends the operator to the wrong place: a missing
		// artifact is a pipeline bug, a failing daemon is an outage.
		CheckThat[VolumeRead]("the failure says the daemon is broken rather than that the artifact is gone",
			func(in VolumeRead) error {
				if in.Err == nil {
					return fmt.Errorf("expected the read to fail against a failing daemon, but it succeeded")
				}
				if !strings.Contains(in.Message, "unexpected status 500") {
					return fmt.Errorf(
						"expected the failure to carry the daemon's status so an operator can see it is "+
							"an outage, got %q", in.Message)
				}
				if containsFold(in.Message, "not found") {
					return fmt.Errorf(
						"expected a failing daemon to be reported as a failure, not as a missing "+
							"artifact — that sends the operator to look for a pipeline bug that is not "+
							"there; got %q", in.Message)
				}
				return nil
			}),

		// The one outcome that separates "the producer was asked and refused,
		// and then everyone else was asked too" from "the producer was asked,
		// refused, and that was the end of it". Both fail; only one of them
		// searched. A raw refusal reaching the operator IS the diagnosis that
		// the search never happened, because the peer-miss error replaces it.
		CheckThat[VolumeRead]("the failure names the node and its peers rather than the refused connection",
			func(in VolumeRead) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected the read to fail once neither the node nor any peer had the " +
							"artifact, but it succeeded")
				}
				if !containsFold(in.Message, "or any peer") {
					return fmt.Errorf(
						"expected the failure to say the artifact was on neither the node nor any "+
							"peer — which is the only thing in the message that tells an operator "+
							"the other daemons were asked at all; got %q", in.Message)
				}
				if containsFold(in.Message, "connection refused") {
					return fmt.Errorf(
						"expected the producer's refused connection to be superseded by the peer "+
							"search, not handed over as the diagnosis — a raw refusal sends the "+
							"operator to the network for an artifact that is simply nowhere, and "+
							"means the search was skipped; got %q", in.Message)
				}
				return nil
			}),

		// The Go test this replaces pinned the exact sentence production
		// prints. What an operator needs from a build log is narrower and more
		// durable: that there is a failure, that it says a daemon is missing,
		// and that it says WHICH artifact — a step has many volumes and a
		// message that names none of them locates nothing.
		CheckThat[VolumeRead]("the write fails rather than reporting an output that never left the web",
			func(in VolumeRead) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected the write to fail, but it reported success — the step then carries " +
							"on believing its output landed, and the next step reads an empty directory")
				}
				if !containsFold(in.Message, "daemon") {
					return fmt.Errorf(
						"expected the failure to name the daemon it could not find, got %q", in.Message)
				}
				if !strings.Contains(in.Message, remoteArtifactKey) {
					return fmt.Errorf(
						"expected the failure to name the artifact %q it could not deliver, so the "+
							"operator knows which of the step's volumes went nowhere; got %q",
						remoteArtifactKey, in.Message)
				}
				return nil
			}),
	}
}
