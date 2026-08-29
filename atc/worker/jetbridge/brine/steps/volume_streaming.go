package steps

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/klauspost/compress/s2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// localExecAdapter is a REAL PodExecutor, not a spy.
//
// It implements the same interface the SPDY executor implements, with one
// behavioral difference we can name: the command runs in a local directory
// instead of inside a pod. Real tar, real filesystem, deterministic and
// synchronous — PHILOSOPHY.md's "test adapters are real adapters", the same
// argument that makes SynchronousTestBus legitimate.
//
// This is the answer to the spy sites. The ginkgo tests assert
// `call.command == ["tar","xf","-","-C","/tmp/build/inputs"]` because a
// RECORDING double is the only thing a recording double can tell you. A
// WORKING double lets the scenario assert what a real consumer of the volume
// port actually experiences: bytes put in come back out.
//
// It records nothing. There is nothing to assert on but the artifact.
type localExecAdapter struct {
	root    string // stands in for the pod's filesystem
	failure string // non-empty: this cluster cannot run commands
}

func (l *localExecAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if l.failure != "" {
		return errors.New(l.failure)
	}
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}

	// Translate the pod-absolute -C target into this adapter's root. The
	// runtime builds the path; we honour it rather than asserting on it.
	translated := make([]string, len(command))
	copy(translated, command)
	for i, arg := range translated {
		if i > 0 && translated[i-1] == "-C" {
			dir := filepath.Join(l.root, filepath.Clean("/"+arg))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("prepare %q: %w", dir, err)
			}
			translated[i] = dir
		}
	}

	cmd := exec.CommandContext(ctx, translated[0], translated[1:]...)
	// macOS bsdtar writes AppleDouble "._name" entries for extended
	// attributes. That is this adapter's platform leaking into the archive,
	// not anything the runtime does, so switch it off at the source rather
	// than filtering it out of the assertion.
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec %v: %w", translated, err)
	}
	return nil
}

// VolumeStreamingDefinitions expresses volume behavior as artifact movement.
// Nothing here names tar, exec, a pod, or ExecAttrs.
func VolumeStreamingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, VolumeSet](
			"a volume {string} mounted at {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				set := VolumeSet{
					Volumes: map[string]*jetbridge.Volume{},
					Ctx:     context.Background(),
				}
				return addVolume(set, p)
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"another volume {string} mounted at {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				return addVolume(in, p)
			},
		),

		// VT-05: a stub volume has no executor and cannot perform I/O.
		Refine[VolumeSet]("a stub volume {string} with no cluster behind it",
			func(in VolumeSet, a Args) VolumeSet {
				name := a.String(0)
				in.Volumes[name] = jetbridge.NewStubVolume(name+"-handle", "k8s-worker-1", "/tmp/stub")
				return in
			}),

		brine.DefineMap[VolumeSet, VolumeSet](
			"volume {string} sits on a cluster that cannot run commands",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, ok := p.GetString(0)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected a volume name parameter")
				}
				root, err := os.MkdirTemp("", "brine-volume")
				if err != nil {
					return VolumeSet{}, fmt.Errorf("create volume root: %w", err)
				}
				volume := jetbridge.NewDeferredVolume(
					name+"-handle", "k8s-worker-1",
					&localExecAdapter{root: root, failure: "exec failed: pod terminated"},
					"test-namespace", "main", "/tmp/build/inputs",
				)
				volume.SetPodName(name + "-pod")
				in.Volumes[name] = volume
				return in, nil
			},
		),

		brine.DefineMap[VolumeSet, VolumeSet](
			"a file {string} containing {string} is put into volume {string} at {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, _ := p.GetString(0)
				content, _ := p.GetString(1)
				volName, _ := p.GetString(2)
				destPath, ok := p.GetString(3)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected four parameters")
				}

				volume, err := in.volume(volName)
				if err != nil {
					return VolumeSet{}, err
				}
				archive, err := tarOfOneFile(name, content)
				if err != nil {
					return VolumeSet{}, err
				}
				if err := volume.StreamIn(in.Ctx, destPath, compression.NewGzipCompression(), 0, archive); err != nil {
					return VolumeSet{}, fmt.Errorf("stream into %q: %w", volName, err)
				}
				return in, nil
			},
		),

		// The user story the volume-to-volume ginkgo test was really about:
		// one step's output becomes the next step's input.
		brine.DefineMap[VolumeSet, VolumeSet](
			"the contents of volume {string} are moved into volume {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				srcName, _ := p.GetString(0)
				dstName, ok := p.GetString(1)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected two volume name parameters")
				}

				src, err := in.volume(srcName)
				if err != nil {
					return VolumeSet{}, err
				}
				dst, err := in.volume(dstName)
				if err != nil {
					return VolumeSet{}, err
				}

				stream, err := src.StreamOut(in.Ctx, ".", compression.NewGzipCompression())
				if err != nil {
					return VolumeSet{}, fmt.Errorf("stream out of %q: %w", srcName, err)
				}
				defer stream.Close()

				if err := dst.StreamIn(in.Ctx, ".", compression.NewGzipCompression(), 0, stream); err != nil {
					return VolumeSet{}, fmt.Errorf("stream into %q: %w", dstName, err)
				}
				return in, nil
			},
		),

		// Reading is an attempt, so that failure is assertable rather than
		// fatal to the scenario.
		brine.DefineMap[VolumeSet, VolumeRead](
			"volume {string} is read from {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volName, _ := p.GetString(0)
				srcPath, ok := p.GetString(1)
				if !ok {
					return VolumeRead{}, fmt.Errorf("expected two parameters")
				}

				volume, err := in.volume(volName)
				if err != nil {
					return VolumeRead{}, err
				}

				stream, streamErr := volume.StreamOut(in.Ctx, srcPath, compression.NewGzipCompression())
				if streamErr != nil {
					return VolumeRead{Err: streamErr, Message: streamErr.Error()}, nil
				}
				defer stream.Close()

				files, readErr := filesInGzippedTar(stream)
				if readErr != nil {
					return VolumeRead{Err: readErr, Message: readErr.Error()}, nil
				}
				return VolumeRead{Files: files}, nil
			},
		),

		// StreamIn must decompress what the Streamer hands it. Every other
		// streaming scenario uses gzip, and gzip CANNOT witness that step:
		// bsdtar auto-detects it, and libarchive auto-detects zstd too, so
		// with the decompressor removed tar still extracts the archive and
		// every one of those scenarios keeps passing. Verified on this host —
		// bsdtar 3.5.3 accepted both encodings undecompressed.
		//
		// S2 settles it. libarchive has no Snappy reader, so an
		// undecompressed S2 stream is refused outright, and Concourse offers
		// s2 as a compression option. The assertion is about the runtime doing
		// the work, not about the extractor being clever.
		brine.DefineMap[VolumeSet, VolumeSet](
			"a file {string} containing {string} is put into volume {string} compressed with s2",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeSet, error) {
				name, _ := p.GetString(0)
				content, _ := p.GetString(1)
				volName, ok := p.GetString(2)
				if !ok {
					return VolumeSet{}, fmt.Errorf("expected a name, content and volume")
				}
				volume, err := in.volume(volName)
				if err != nil {
					return VolumeSet{}, err
				}

				plain, err := tarOfOneFile(name, content)
				if err != nil {
					return VolumeSet{}, err
				}
				raw, err := io.ReadAll(plain)
				if err != nil {
					return VolumeSet{}, fmt.Errorf("read tar: %w", err)
				}

				// compression.Compression only reads; the Streamer compresses
				// with the same library on the way in, so this does too.
				enc := compression.NewS2Compression()
				var packed bytes.Buffer
				w := s2.NewWriter(&packed)
				if _, err := w.Write(raw); err != nil {
					return VolumeSet{}, fmt.Errorf("s2 write: %w", err)
				}
				if err := w.Close(); err != nil {
					return VolumeSet{}, fmt.Errorf("close s2 writer: %w", err)
				}

				if err := volume.StreamIn(in.Ctx, ".", enc, 0, &packed); err != nil {
					return VolumeSet{}, fmt.Errorf("stream in: %w", err)
				}
				return in, nil
			},
		),

		brine.DefineMap[VolumeSet, VolumeRead](
			"a file is put into volume {string}",
			func(in VolumeSet, p brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volName, ok := p.GetString(0)
				if !ok {
					return VolumeRead{}, fmt.Errorf("expected a volume name parameter")
				}
				volume, err := in.volume(volName)
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
		brine.DefineCheck[VolumeRead](
			"it fails rather than panicking, saying {string}",
			func(in VolumeRead, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
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

func addVolume(set VolumeSet, p brine.Params) (VolumeSet, error) {
	name, _ := p.GetString(0)
	mountPath, ok := p.GetString(1)
	if !ok {
		return VolumeSet{}, fmt.Errorf("expected a name and a mount path")
	}

	root, err := os.MkdirTemp("", "brine-volume")
	if err != nil {
		return VolumeSet{}, fmt.Errorf("create volume root: %w", err)
	}

	volume := jetbridge.NewDeferredVolume(
		name+"-handle", "k8s-worker-1",
		&localExecAdapter{root: root},
		"test-namespace", "main", mountPath,
	)
	volume.SetPodName(name + "-pod")
	set.Volumes[name] = volume
	return set, nil
}

// VolumeIdentityDefinitions covers the rest of volume_test.go — a volume's
// identity, its source worker, and the database row behind it.
//
// Identity is what the artifact repository keys on, so a volume that reported
// the wrong handle would hand the next step somebody else's artifact.
func VolumeIdentityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, VolumeIdentity](
			"a persisted volume on this worker",
			[]string{"jetbridge-db"},
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
				creating, err := database.VolumeRepository.CreateVolume(
					team.ID(), dbWorker.Name(), db.VolumeTypeArtifact)
				if err != nil {
					return VolumeIdentity{}, fmt.Errorf("create volume: %w", err)
				}
				created, err := creating.Created()
				if err != nil {
					return VolumeIdentity{}, fmt.Errorf("mark volume created: %w", err)
				}

				root, err := os.MkdirTemp("", "brine-identity")
				if err != nil {
					return VolumeIdentity{}, fmt.Errorf("temp dir: %w", err)
				}
				vol := jetbridge.NewVolume(created, &localExecAdapter{root: root},
					"identity-pod", "test-namespace", "main", "/tmp/build/inputs")

				daemonVol := jetbridge.NewDaemonSetVolume(
					"key", "runtime-handle", dbWorker.Name(), created, "",
					jetbridge.NewConfig("test-namespace", ""), nil)

				return VolumeIdentity{
					Volume: vol, DaemonVolume: daemonVol,
					DBHandle: created.Handle(), WorkerName: dbWorker.Name(),
				}, nil
			},
		),

		CheckThat[VolumeIdentity]("the volume identifies itself by its database handle",
			func(in VolumeIdentity) error {
				if in.Volume.Handle() != in.DBHandle {
					return fmt.Errorf(
						"expected the volume to identify as %q — the handle the artifact repository keys on — got %q",
						in.DBHandle, in.Volume.Handle())
				}
				return nil
			}),

		CheckThat[VolumeIdentity]("the volume names the worker it lives on",
			func(in VolumeIdentity) error {
				if in.Volume.Source() != in.WorkerName {
					return fmt.Errorf("expected the volume to name worker %q, got %q",
						in.WorkerName, in.Volume.Source())
				}
				return nil
			}),

		// The DB row is what survives a web restart; a volume that lost it
		// would be invisible to garbage collection.
		CheckThat[VolumeIdentity]("both volume kinds still carry their database row",
			func(in VolumeIdentity) error {
				if in.Volume.DBVolume() == nil {
					return fmt.Errorf("the deferred volume lost its database row")
				}
				if in.DaemonVolume.DBVolume() == nil {
					return fmt.Errorf("the daemonset volume lost its database row")
				}
				if got := in.DaemonVolume.DBVolume().Handle(); got != in.DBHandle {
					return fmt.Errorf("expected the daemonset volume's row to be %q, got %q", in.DBHandle, got)
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

// The volumes above move bytes through a pod's own filesystem. The ones below
// move them across the network, from the artifact daemon on the node that
// produced them — which is where every input a step did not produce itself
// comes from.
//
// The daemon here is a REAL http.Server speaking the daemon's wire contract,
// the same argument localExecAdapter makes for exec. Its ONE named
// behavioural difference is how it treats a connection: it may drop the first
// few, drop every one, or answer with an internal error. It records nothing an
// assertion reads. The counter behind "drops the first N" decides what the
// server DOES; no scenario asks it what it saw.
//
// The Go tests these replace reached inside the struct — they built a
// DaemonSetVolume by literal, swapped in a transport that rewrote every URL to
// the test server, and one of them finished by asserting the handler's own
// attempt counter. Here the node is a real Node object in the cluster, the
// address is resolved out of it the way production resolves it, and the
// assertion is on what the consumer got: the artifact, or the failure.

// remoteArtifactNode is the node the artifact was produced on, and the only
// one in the cluster. Its address is the live server's, so the fetch really
// is dialled.
const (
	remoteArtifactNode = "producer-node"

	// remoteArtifactKey is the artifact under discussion. It is the key the
	// daemon is addressed by, so a failure that does not mention it did not
	// say which artifact went missing.
	remoteArtifactKey = "step-output"
)

// RemoteArtifact is an artifact on another node's daemon, described but not
// yet fetched. Refinements adjust how that node's daemon behaves, so a
// scenario says what it holds and how it misbehaves in either order.
type RemoteArtifact struct {
	Ctx      context.Context
	Key      string
	FileName string
	Content  string

	// DropFirst, NeverAnswers and ServerError are the daemon's behaviour, not
	// a record of what it was asked.
	DropFirst    int
	NeverAnswers bool
	ServerError  bool

	// Forgotten means the web restarted and lost which node produced this
	// artifact, and no daemon discovery is configured to find it again. There
	// is no daemon at all in that case — that is the point.
	Forgotten bool
}

// daemon starts the node's artifact daemon and builds the volume the runtime
// would build for an artifact recorded on that node. The returned func stops
// the daemon.
func (r RemoteArtifact) daemon() (*jetbridge.DaemonSetVolume, func(), error) {
	if r.Forgotten {
		cfg := jetbridge.NewConfig("test-namespace", "")
		return jetbridge.NewDaemonSetVolume(
			r.Key, r.Key, "k8s-worker-1", nil, "", cfg, nil,
		), func() {}, nil
	}

	body, err := plainTarOfOneFile(r.FileName, r.Content)
	if err != nil {
		return nil, nil, err
	}
	server := httptest.NewServer(r.handler(body))

	addr, ok := server.Listener.Addr().(*net.TCPAddr)
	if !ok {
		server.Close()
		return nil, nil, fmt.Errorf("the daemon is listening on %T, not TCP", server.Listener.Addr())
	}

	clientset := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: remoteArtifactNode},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: addr.IP.String()},
			},
		},
	})

	cfg := jetbridge.NewConfig("test-namespace", "")
	cfg.ArtifactDaemonPort = addr.Port

	return jetbridge.NewDaemonSetVolume(
		r.Key, r.Key, "k8s-worker-1", nil, remoteArtifactNode,
		cfg, jetbridge.NewNodeIPResolver(clientset),
	), server.Close, nil
}

// handler answers the one route the artifact daemon answers for a step
// artifact and 404s everything else, so the bytes arriving at all is what
// proves the right artifact was asked for.
func (r RemoteArtifact) handler(body []byte) http.Handler {
	var connections atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.NeverAnswers || (r.DropFirst > 0 && int(connections.Add(1)) <= r.DropFirst) {
			// Hijack and close: a connection that dies after the request was
			// written, which is what a daemon pod being rescheduled looks
			// like from the ATC. Go's transport does not silently retry a
			// fresh connection, so this really does reach the runtime.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, hjErr := hj.Hijack(); hjErr == nil {
					_ = conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
		if r.ServerError {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("artifact daemon: disk is gone"))
			return
		}
		if req.URL.Path != "/artifacts/"+r.Key {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(body)
	})
}

// ---------------------------------------------------------------------------
// Where a step is placed, and which volumes it is handed
// ---------------------------------------------------------------------------

// InputPlacement is a worker that records where artifacts live, the inputs a
// step is about to take, and — once it has been scheduled — the pod the
// Kubernetes scheduler will read.
type InputPlacement struct {
	Cluster Cluster
	Locator *jetbridge.ArtifactLocator
	Inputs  []runtime.Input
	Pod     *corev1.Pod
}

// VolumeGapDefinitions covers the parts of a volume's life the scenarios above
// do not reach: fetching one across the network, writing one that has nowhere
// to go, and being placed near the ones a step already needs.
func VolumeGapDefinitions() []brine.StepDefinition {
	defs := remoteArtifactDefinitions()
	defs = append(defs, inputPlacementDefinitions()...)
	return defs
}

func remoteArtifactDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, RemoteArtifact](
			"an artifact on another node holding the file {string} containing {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (RemoteArtifact, error) {
				name, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return RemoteArtifact{}, fmt.Errorf("expected a file name and its contents")
				}
				return RemoteArtifact{
					Ctx: context.Background(), Key: remoteArtifactKey,
					FileName: name, Content: content,
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

		// Reading is an attempt, so a failure is assertable rather than fatal
		// to the scenario — the same shape the exec-backed reads above use,
		// and it lands in the same state, so it can reuse their checks.
		brine.DefineMap[RemoteArtifact, VolumeRead](
			"the next step fetches the artifact from that node",
			func(in RemoteArtifact, _ brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volume, stop, err := in.daemon()
				if err != nil {
					return VolumeRead{}, err
				}
				defer stop()

				// gzip is what Streamer.StreamFile asks for, and it is what
				// makes the answer readable as an archive rather than as an
				// opaque body.
				stream, streamErr := volume.StreamOut(in.Ctx, ".", compression.NewGzipCompression())
				if streamErr != nil {
					return VolumeRead{Err: streamErr, Message: streamErr.Error()}, nil
				}
				defer stream.Close()

				files, readErr := filesInGzippedTar(stream)
				if readErr != nil {
					return VolumeRead{Err: readErr, Message: readErr.Error()}, nil
				}
				return VolumeRead{Files: files}, nil
			},
		),

		brine.DefineMap[RemoteArtifact, VolumeRead](
			"the step writes its output into that artifact",
			func(in RemoteArtifact, _ brine.Params, _ *brine.Recorder) (VolumeRead, error) {
				volume, stop, err := in.daemon()
				if err != nil {
					return VolumeRead{}, err
				}
				defer stop()

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
				if in.Err == nil {
					return fmt.Errorf(
						"expected the read to fail, but it succeeded and handed back %d files (%v) — "+
							"a step that cannot reach its input must be told so, not given an empty "+
							"directory it will then fail on with no explanation",
						len(in.Files), sortedFileNames(in.Files))
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
				if !strings.Contains(in.Message, "500") {
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

func sortedFileNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func inputPlacementDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, InputPlacement](
			"a jetbridge worker that places steps near their inputs",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (InputPlacement, error) {
				locator := jetbridge.NewArtifactLocator()
				cluster, err := NewCluster(res,
					WithConfig(func(cfg *jetbridge.Config) {
						cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
					}),
					WithVolumeRepo(),
					WithTeam(),
					WithArtifactLocator(locator),
				)
				if err != nil {
					return InputPlacement{}, err
				}
				return InputPlacement{Cluster: cluster, Locator: locator}, nil
			},
		),

		// A real artifact volume, with a real database handle, recorded where
		// the producing step left it. Nothing here stands in for an artifact:
		// the handle the scheduler is asked about is the one the volume
		// reports.
		brine.DefineMap[InputPlacement, InputPlacement](
			"an input artifact that already lives on node {string}",
			func(in InputPlacement, p brine.Params, _ *brine.Recorder) (InputPlacement, error) {
				node, ok := p.GetString(0)
				if !ok {
					return InputPlacement{}, fmt.Errorf("expected a node name parameter")
				}
				volume, _, err := in.Cluster.Worker.CreateVolumeForArtifact(
					in.Cluster.Ctx, in.Cluster.TeamID)
				if err != nil {
					return InputPlacement{}, fmt.Errorf("create artifact volume: %w", err)
				}
				in.Locator.Record(jetbridge.ArtifactKey(volume.Handle()), node, "")
				in.Inputs = append(in.Inputs, runtime.Input{
					Artifact:        volume,
					DestinationPath: fmt.Sprintf("/tmp/build/workdir/input-%d", len(in.Inputs)),
				})
				return in, nil
			},
		),

		brine.DefineMap[InputPlacement, InputPlacement](
			"the step is scheduled",
			func(in InputPlacement, _ brine.Params, _ *brine.Recorder) (InputPlacement, error) {
				const handle = "placed-step"
				container, _, err := in.Cluster.Worker.FindOrCreateContainer(
					in.Cluster.Ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    in.Cluster.TeamID,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Inputs:    in.Inputs,
					},
					&noopDelegate{},
				)
				if err != nil {
					return InputPlacement{}, fmt.Errorf("find or create container: %w", err)
				}
				if _, err := container.Run(in.Cluster.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
					runtime.ProcessIO{},
				); err != nil {
					return InputPlacement{}, fmt.Errorf("run step: %w", err)
				}

				pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					Get(in.Cluster.Ctx, handle, metav1.GetOptions{})
				if err != nil {
					return InputPlacement{}, fmt.Errorf("get pod %q: %w", handle, err)
				}
				in.Pod = pod
				return in, nil
			},
		),

		// The preference is what keeps a step's inputs off the network. A pod
		// with no preference is scheduled anywhere, and every input then
		// crosses between nodes to reach it.
		CheckString[InputPlacement]("the step prefers to run on node {string}",
			"the node the step prefers",
			func(in InputPlacement) (string, error) {
				if in.Pod == nil {
					return "", fmt.Errorf("no pod was created")
				}
				aff := in.Pod.Spec.Affinity
				if aff == nil || aff.NodeAffinity == nil ||
					len(aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
					return "", fmt.Errorf(
						"the pod carries no scheduling preference, so it may be placed anywhere the " +
							"artifact cache is ready — including a node holding none of its inputs, " +
							"which then all cross the network")
				}
				var named []string
				for _, term := range aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
					for _, expr := range term.Preference.MatchExpressions {
						if expr.Key == "kubernetes.io/hostname" {
							named = append(named, expr.Values...)
						}
					}
				}
				if len(named) != 1 {
					return "", fmt.Errorf(
						"expected the pod to prefer exactly one node, it names %v", named)
				}
				return named[0], nil
			}),
	}
}

// plainTarOfOneFile builds the uncompressed tar an artifact daemon serves off
// a node's disk. tarOfOneFile gzips, which is what a StreamIn caller hands
// over; the daemon's own body is raw.
func plainTarOfOneFile(name, content string) ([]byte, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, fmt.Errorf("write tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return raw.Bytes(), nil
}
