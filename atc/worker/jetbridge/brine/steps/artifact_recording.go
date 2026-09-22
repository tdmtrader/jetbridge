package steps

// Artifact-recording steps: the executable half of
// ../features/artifact-recording.feature.
//
// What a step leaves behind when it finishes — where its outputs are, who else
// has a copy, and what the next step is told to fetch.
//
// Every daemon here is the production binary, with its own real storage root.
// Mirroring scenarios use real kubelet-run peers with independent stores.
// Their checks observe asynchronous disk arrival before any HTTP read, so
// read-through fallback cannot make an absent mirror look successful.
// Kubernetes validation and persistence are real in every scenario here.
// Envtest has no kubelet; pod shape and host-run fetch scripts are not proof
// of in-cluster container execution.
//
// On the two backends. The worker builds pods through its own storage backend,
// which is unexported and unreachable from here; RecordOutputs and
// RegisterResourceCache are called on a backend this file constructs. The two
// share one *ArtifactLocator, which is not a workaround — the locator IS the
// shared state in production, written when a step finishes and read when the
// next pod is built. A scenario that records outputs and then builds the next
// step's pod is exercising exactly that hand-off.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// stepOutputFileName is the one file a described output holds.
//
// A step's output is a DIRECTORY on the node — that is what the daemon serves,
// what its mirror copies and what a resolve lands — so a Given saying an
// output "holds" some bytes means there is a file in that directory with those
// bytes in it. The name is this fixture's own and nothing asserts on it: the
// checks read the single file out of whatever archive came back.
const stepOutputFileName = "artifact"

// artifactDaemonService is the headless service the ATC discovers daemons
// through.
const artifactDaemonService = "artifact-daemon"

// realNode is the production daemon and the actual address it serves.
type realNode struct {
	Root, URL string
	host      string
	port      int
	store     *liveArtifactStore
	ctx       context.Context
	pod       *corev1.Pod
}

// write is what a step does: it writes a file into the directory it was given,
// and the directory is the output. Nothing tells the daemon it is there.
func (n *realNode) write(rel, content string) error {
	file := filepath.Join(n.Root, rel, stepOutputFileName)
	if n.store != nil {
		return n.store.writeFile(n.ctx, file, content)
	}
	return writeArtifactFile(file, content)
}

// contained turns an absolute path on the node into its location under the
// store root, which is the same question the daemon's own containment check
// asks of a registration or a resolve destination.
func (n *realNode) contained(localPath string) (string, bool) {
	prefix := n.Root + string(filepath.Separator)
	if !strings.HasPrefix(localPath, prefix) {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(localPath, prefix)), true
}

func (n *realNode) request(ctx context.Context, method, path, body string) (int, []byte, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.URL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read the daemon's answer to %s %s: %w", method, path, err)
	}
	return resp.StatusCode, answer, nil
}

// registerAlias is the POST the ATC makes when a step finishes: this key names
// that directory. A daemon that does not hold the path refuses, so the status
// is checked rather than assumed — a silently refused registration would leave
// the scenario asserting against a daemon that never heard of the key.
func (n *realNode) registerAlias(ctx context.Context, key, localPath string) error {
	status, body, err := n.request(ctx, http.MethodPost, "/register",
		fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, localPath))
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf(
			"the daemon refused to register %q as living at %q, answering %d: %s",
			key, localPath, status, abbrev(string(body)))
	}
	return nil
}

// fetchArtifact reads an artifact back out of this daemon over the wire, the
// way any consumer would. The body is a tar; oneFileInTar opens it.
func (n *realNode) fetchArtifact(ctx context.Context, path string) ([]byte, error) {
	status, body, err := n.request(ctx, http.MethodGet, path, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("the daemon answered %d for %s: %s", status, path, abbrev(string(body)))
	}
	return body, nil
}

// oneFileInTar reads the single file out of an archive a daemon served.
//
// What a consumer receives from /artifacts/ is a tar of a directory, and what
// these scenarios describe is the bytes the step wrote into it. An archive
// holding no file, or several, is reported rather than joined: either would
// mean the daemon served a different directory from the one the step wrote to,
// which is the failure half of this family is about.
func oneFileInTar(body []byte) (string, error) {
	tr := tar.NewReader(bytes.NewReader(body))
	var names []string
	var content string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf(
				"the daemon's answer is not a readable tar (%d bytes): %w", len(body), err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		read, err := io.ReadAll(tr)
		if err != nil {
			return "", fmt.Errorf("read %q out of the archive: %w", header.Name, err)
		}
		names = append(names, header.Name)
		content = string(read)
	}
	switch len(names) {
	case 1:
		return content, nil
	case 0:
		return "", fmt.Errorf(
			"the archive that came back holds no file at all, so the directory the daemon " +
				"served is empty and the step reading it finds nothing")
	default:
		return "", fmt.Errorf(
			"the archive that came back holds %d files (%v); the scenario described one output, "+
				"so the daemon served a directory other than the one the step wrote to",
			len(names), names)
	}
}

// -----------------------------------------------------------------------
// Domain states
// -----------------------------------------------------------------------

// ArtifactCluster is a worker whose step data lives on its nodes, the artifact
// index it shares with its storage backend, and the daemons on those nodes.
//
// The producing step and the consuming step are both described into this one
// state, because the whole point of the family is the hand-off between them:
// what one leaves in the index is what the other's pod is built from.
// cachedStepJobID is the job the one scenario that keeps a task cache belongs
// to. Its value is arbitrary and unread: nothing here asserts the key, only
// which tree on the node the cache lands in. It exists because a cache cannot
// be node-local without a job to key it under.
const cachedStepJobID = 7

type ArtifactCluster struct {
	mirrorRecorder *brine.Recorder
	live           *liveArtifactDaemon
	nodeReads      *execObservation
	recordingWire  *daemonWireObservation
	recording      *artifactRecordingObservation
	Ctx            context.Context
	Namespace      string
	Worker         *jetbridge.Worker
	Clientset      kubernetes.Interface
	Backend        *jetbridge.DaemonSetBackend
	Locator        *jetbridge.ArtifactLocator
	DB             JetbridgeDB
	Team           db.Team
	WorkerRow      db.Worker

	// Both stores are owned production-daemon filesystems.
	StoreRoot string
	Node      *realNode
	Peer      *realNode
	NodeName  string

	// CacheIdentity is the job a described task cache belongs to, set by the
	// sentence that asks for a cache. Nil everywhere else, which is what a
	// step with no cache to keep carries in production.
	CacheIdentity *atc.TaskCacheIdentity

	// The producing step under description.
	Handle          string
	Outputs         map[string]string
	Volumes         []*jetbridge.Volume
	Producer        runtime.Container
	ExpectedVolumes map[string]string // mount path -> independently expected handle

	// ProducerDir and ProducerType are the working directory and the kind of
	// step the producer is. They matter together: a get step's working
	// directory IS its output — RecordOutputs files it under the name "dir" —
	// while a task's working directory is scratch and only its named outputs
	// are recorded.
	ProducerDir  string
	ProducerType db.ContainerType

	// Writes are bytes the producing step put somewhere through a mount its
	// own pod gave it. They are not on the node until the pod exists, because
	// where they land is the pod's answer and not the fixture's.
	Writes []producerWrite

	// The consuming step under description.
	Consumer     string
	ConsumerType db.ContainerType
	Inputs       []consumerInput
	// Caches are task caches the consuming step asks to keep BETWEEN builds,
	// which is the whole difference between a cache and a step output.
	Caches []string
	// RanBefore makes the consuming step's container row exist before the run
	// under test, so that run is a RETRY — which is the only shape whose pod
	// carries the cleanup init container.
	RanBefore bool

	// Err is what the last verb reported, so a scenario can assert on it
	// instead of dying.
	Err     error
	Message string
}

type consumerInput struct {
	Key      string
	Path     string
	Artifact runtime.Artifact // retain worker-created references when available
}

// producerWrite is one thing a finished step left behind: the name the ATC
// files it under, the path the STEP saw it at, and what it holds.
//
// The path is the step's own — the mount path, not a host directory — because
// that is all a step knows. Where those bytes actually land on the node is the
// pod's decision, and reading it back out of the pod rather than assuming it
// is the whole point of the family these belong to.
type producerWrite struct {
	Name    string
	Path    string
	Content string
}

// FollowingPod is the pod a later step got. Check steps read its spec, which
// is a real object submitted through a real client — the Kubernetes scheduler
// receives exactly this.
type FollowingPod struct {
	Handle string
	Pod    *corev1.Pod

	// Clientset and Ctx are carried so a check can hold the pod against the
	// nodes that actually exist. A requirement is only wrong relative to the
	// fleet it is asked of, and comparing the pod against a copy of what
	// production was expected to write would assert nothing about that.
	Clientset kubernetes.Interface
	Ctx       context.Context
	// Caches are the paths the step asked to keep, so a check can find the
	// mount by the path the STEP named rather than by the volume-naming
	// convention the backend happens to use.
	Caches []string

	// Node is the daemon on the node this pod landed on, so a check can let
	// the pod's own fetch script run against it; StoreRoot is the store that
	// daemon serves, which is what the pod's hostPaths are held against.
	Node      *realNode
	StoreRoot string
}

// FetchOutcome is what happened when the pod's fetch init container ran: the
// status the kubelet reads, and what it printed on the way.
//
// The kubelet's whole decision about whether the step may start is that one
// number. A fetch that could not deliver every input and still exits 0 is a
// pod that proceeds to the step's own command with a workspace missing the
// files the pipeline promised it — which surfaces later as a task failing on
// a file it was handed, nowhere near the fetch that never happened.
type FetchOutcome struct {
	Ctx       context.Context
	Handle    string
	Pod       *corev1.Pod
	Node      *realNode
	StoreRoot string
	ExitCode  int
	Output    string
}

// -----------------------------------------------------------------------
// Preamble
// -----------------------------------------------------------------------

func applyArtifactConfig(cfg *jetbridge.Config, root string, port int) {
	cfg.ArtifactDaemonHostPath = root
	cfg.ArtifactDaemonPort = port
	cfg.ArtifactDaemonService = artifactDaemonService
	cfg.ArtifactHelperImage = "alpine:latest"
}

// Both single-node and real peer fixtures use the same production worker,
// backend, locator and database wiring.
func wireArtifactCluster(in ArtifactCluster, executor jetbridge.PodExecutor) (ArtifactCluster, error) {
	team, err := in.DB.TeamFactory.CreateTeam(atc.Team{Name: "artifact-recording"})
	if err != nil {
		return ArtifactCluster{}, fmt.Errorf("create team: %w", err)
	}
	in.Team = team
	cfg := jetbridge.NewConfig(in.Namespace, "")
	port := 0
	if in.Node != nil {
		port = in.Node.port
	}
	applyArtifactConfig(&cfg, in.StoreRoot, port)
	cfg.ArtifactDaemonService = in.daemonService()
	if in.live != nil {
		cfg.ArtifactHelperImage = "busybox:1.37.0"
		cfg.PodStartupTimeout, cfg.PodSchedulingTimeout = 30*time.Second, 30*time.Second
	}
	in.Worker = jetbridge.NewWorker(in.WorkerRow, in.Clientset, cfg)
	in.Worker.SetVolumeRepo(in.DB.VolumeRepository)
	in.Worker.SetExecutor(executor)
	in.Locator = jetbridge.NewArtifactLocator()
	addresses := map[string]bool{}
	if in.Node != nil {
		addresses[net.JoinHostPort(in.Node.host, strconv.Itoa(in.Node.port))] = true
	}
	// Both the backend and the discovery client retain transports built here.
	// Observe their construction once; each recording action selects its own
	// request interval without replacing any of the fixture's clients.
	in.recordingWire, err = observeDaemonConstruction(addresses, func() {
		client := jetbridge.NewDaemonClient(
			lagertest.NewTestLogger("brine-artifact-recording"),
			in.Clientset, in.Namespace, cfg.ArtifactDaemonService, cfg.ArtifactDaemonPort, nil,
		)
		in.Backend = jetbridge.NewDaemonSetBackend(cfg, in.Locator, jetbridge.NewNodeIPResolver(in.Clientset))
		in.Backend.SetDaemonClient(client)
		in.Worker.SetArtifactLocator(in.Locator)
		in.Worker.SetDaemonClient(client)
	})
	if err != nil {
		return ArtifactCluster{}, err
	}
	in.Outputs = map[string]string{}
	in.ExpectedVolumes = map[string]string{}
	in.ProducerDir = "/tmp/build"
	in.ProducerType = db.ContainerTypeTask
	return in, nil
}

// outputMountPath is where an output of the described step is mounted. The
// name is what the daemon key is built from, so the two travel together.
//
// It is the ATC's own rule, not a convenience: atc/exec/task_step.go's
// resolvePath returns an ABSOLUTE output name unchanged and hangs a relative
// one off the build directory. A task that wants to capture a directory its
// image owns — a Postgres data directory at /data — names the output for that
// path and gives it no path: of its own, and the mount lands there. Joining
// such a name onto /tmp/build here would describe a step no pipeline can
// write, and the scenarios built on it would be asserting about nothing.
func outputMountPath(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return "/tmp/build/" + name
}

func (c ArtifactCluster) producerSpec() runtime.ContainerSpec {
	outputs := runtime.OutputPaths{}
	for name, path := range c.Outputs {
		outputs[name] = path
	}
	return runtime.ContainerSpec{
		Dir:     c.ProducerDir,
		Outputs: outputs,
		Type:    c.ProducerType,
	}
}

// -----------------------------------------------------------------------
// Steps
// -----------------------------------------------------------------------

// ArtifactRecordingDefinitions is the single entry point this file exports.
func ArtifactRecordingDefinitions() []brine.StepDefinition {
	defs := artifactClusterDefinitions()
	defs = append(defs, liveArtifactRecordingDefinitions()...)
	defs = append(defs, artifactRecordDefinitions()...)
	defs = append(defs, artifactPodDefinitions()...)
	defs = append(defs, artifactSchedulingDefinitions()...)
	defs = append(defs, artifactLookupDefinitions()...)
	defs = append(defs, artifactLayoutDefinitions()...)
	defs = append(defs, artifactFetchRunDefinitions()...)
	return defs
}

func artifactClusterDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The daemon is started HERE rather than as a scenario-scoped
		// resource, and the difference is 70 seconds of suite: brine acquires
		// every ScopeScenario resource before EVERY scenario, so a daemon
		// registered that way would be built, started and killed for the whole
		// corpus to serve this feature. Started in the Given that asks for one,
		// with its kill on the Recorder, it is lazy and drains at scenario end
		// on pass, on failure and on SIGTERM. ../steps/realdaemon.go measured
		// this; the note is repeated because it is easy to undo.
		brine.DefineMapUsing[brine.Empty, ArtifactCluster](
			"a jetbridge worker whose step outputs stay on the node that ran them",
			[]string{"jetbridge-db", "real-cluster"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ArtifactCluster, error) {
				return newArtifactCluster(res, rec, false)
			},
		),

		Refine[ArtifactCluster]("the step {string} ran on node {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Handle, in.NodeName = a.String(0), a.String(1)
				return in
			}),

		// The output is on the node's disk before anything records it —
		// which is the real order of events: the step wrote it through the
		// hostPath mount, and only then did the process finish. On a real
		// daemon that is a directory under the store's steps tree with a file
		// in it, and nothing has told the daemon it is there.
		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"its output {string} is the volume {string} holding {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				name, _ := p.GetString(0)
				handle, _ := p.GetString(1)
				content, _ := p.GetString(2)

				in.Outputs[name] = outputMountPath(name)
				in.ExpectedVolumes[outputMountPath(name)] = handle
				// path.Join, because this fixture stands in for the kubelet:
				// the directory it makes is the one the pod's hostPath names,
				// and that is built by joining. Concatenating instead would
				// put an output named "/data" at steps/<handle>//data — the
				// production defect, reproduced in the fixture, where it would
				// cancel out rather than show.
				if err := in.Node.write(path.Join("steps", in.Handle, name), content); err != nil {
					return ArtifactCluster{}, err
				}
				return in, nil
			},
		),

		// A cache the daemon has been told about: the bytes sit under their
		// own path and the registry answers to the cache key. That split is
		// what a registered resource cache actually looks like — the alias
		// names a get step's output directory, not a directory called after
		// the cache — and here it is the daemon's own registry saying so,
		// because the registration goes over the wire and its refusal would
		// be reported rather than swallowed.
		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the node's daemon holds the artifact {string} containing {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				key, _ := p.GetString(0)
				content, _ := p.GetString(1)
				if in.Node == nil {
					return ArtifactCluster{}, fmt.Errorf(
						"this scenario has no real daemon to hold a cache")
				}

				rel := "steps/cached/" + key
				if err := in.Node.write(rel, content); err != nil {
					return ArtifactCluster{}, err
				}
				if err := in.Node.registerAlias(in.Ctx, key, filepath.Join(in.Node.Root, rel)); err != nil {
					return ArtifactCluster{}, err
				}
				return in, nil
			},
		),

		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"an artifact volume {string} the worker can look up",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ArtifactCluster{}, fmt.Errorf("expected a volume handle parameter")
				}
				creating, err := in.DB.VolumeRepository.CreateVolumeWithHandle(
					handle, in.Team.ID(), in.WorkerRow.Name(), db.VolumeTypeArtifact)
				if err != nil {
					return ArtifactCluster{}, fmt.Errorf("create volume %q: %w", handle, err)
				}
				if _, err := creating.Created(); err != nil {
					return ArtifactCluster{}, fmt.Errorf("transition volume %q: %w", handle, err)
				}
				return in, nil
			},
		),
	}
}

func artifactRecordDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the worker records the outputs using equivalent handle {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				handle, ok := p.GetString(0)
				if !ok || handle == in.Handle || filepath.Clean(handle) != in.Handle || len(in.Outputs) == 0 {
					return in, fmt.Errorf("expected a noncanonical spelling of the same producer handle")
				}
				if _, err := prepareProducer(&in); err != nil {
					return in, err
				}
				// The pod and its files are real and unchanged. Only the spelling
				// passed to RecordOutputs differs. All paths remain in this root.
				for name := range in.Outputs {
					status, _, err := in.Node.request(in.Ctx, http.MethodPost, "/mirror",
						fmt.Sprintf("{\"key\":%q}", handle+"/"+name))
					if err != nil || status != http.StatusBadRequest {
						return in, fmt.Errorf("real mirror must reject the noncanonical key: status=%d error=%v", status, err)
					}
				}
				in.Handle = handle
				if err := in.observeRecording("", func() {
					defer func() {
						if failure := recover(); failure != nil {
							in.Err = fmt.Errorf("recording panicked after mirror refusal: %v", failure)
						}
					}()
					in.Backend.RecordOutputs(in.Ctx, in.Handle, in.NodeName, in.Volumes, in.producerSpec())
				}); err != nil {
					return in, err
				}
				return in, nil
			},
		),
		CheckThat[ArtifactCluster]("recording succeeds despite the mirror refusal",
			func(in ArtifactCluster) error {
				if in.Err != nil {
					return in.Err
				}
				return in.requireRecording()
			}),

		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the worker records where the step's outputs went",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				if _, err := prepareProducer(&in); err != nil {
					return in, err
				}
				if err := in.observeRecording("", func() {
					in.Backend.RecordOutputs(in.Ctx, in.Handle, in.NodeName, in.Volumes, in.producerSpec())
				}); err != nil {
					return in, err
				}
				return in, nil
			},
		),

		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the worker registers the resource cache {string} for that step's output",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				key, ok := p.GetString(0)
				if !ok {
					return ArtifactCluster{}, fmt.Errorf("expected a cache key parameter")
				}
				if _, err := prepareProducer(&in); err != nil {
					return in, err
				}
				volume, err := producerVolumeAt(in, in.ProducerDir)
				if err != nil {
					return in, err
				}
				if err := in.observeRecording(key, func() {
					in.Err = in.Backend.RegisterResourceCache(in.Ctx, key, "", volume.Handle(), in.NodeName)
				}); err != nil {
					return in, err
				}
				in.Message = ""
				if in.Err != nil {
					in.Message = in.Err.Error()
				}
				return in, nil
			},
		),

		CheckThat[ArtifactCluster]("registering the cache succeeded",
			func(in ArtifactCluster) error {
				if in.Err != nil {
					return fmt.Errorf(
						"registering the cache failed, so the next build re-runs the get step: %s",
						in.Message)
				}
				return nil
			}),

		// The read a later step's web process performs: the worker wraps the
		// artifact for lookup and streams it. It only arrives if the index
		// remembers which node holds it AND that node's daemon was told the
		// key names that directory.
		//
		// What arrives is a tar of that directory, which is the runtime.Volume
		// contract, so the file is read out of it rather than the body being
		// compared whole.
		CheckStringFor[ArtifactCluster]("the output {string} reads back as {string}",
			"the artifact's contents",
			func(in ArtifactCluster, handle string) (string, error) {
				if len(in.Volumes) == 0 {
					return "", fmt.Errorf("producer returned no volumes")
				}
				for _, volume := range in.Volumes {
					if volume.Source() != in.WorkerRow.Name() || !volume.HasExecutor() {
						return "", fmt.Errorf("producer volume %q lost worker or executor binding", volume.Handle())
					}
				}
				vol := in.Backend.WrapVolumeForLookup(
					in.Ctx, jetbridge.ArtifactKey(handle), handle, in.WorkerRow.Name(), nil)
				stream, err := vol.StreamOut(in.Ctx, ".", nil)
				if err != nil {
					return "", fmt.Errorf("reading %q back: %w", handle, err)
				}
				defer stream.Close()
				body, err := io.ReadAll(stream)
				if err != nil {
					return "", fmt.Errorf("draining %q: %w", handle, err)
				}
				if in.live != nil {
					if err := in.nodeReads.requireNodeRead(in.NodeName); err != nil {
						return "", err
					}
				}
				return oneFileInTar(body)
			}),

		// Keeps its own body: three parameters, and the failure has to say
		// what losing the copy costs.
		brine.DefineCheck[ArtifactCluster](
			"the independent peer holds a copy of the output {string} containing {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an output name and its contents")
				}
				if in.Peer == nil {
					return fmt.Errorf("no independent peer was set up, so nothing could hold a copy")
				}
				if err := in.requireRecording(); err != nil {
					return err
				}
				key := in.Handle + "/" + name
				// GET can fetch from the producer on a miss. First require the
				// mirror itself to have delivered the file to the peer's disk.
				ctx, cancel := context.WithTimeout(in.Ctx, 10*time.Second)
				defer cancel()
				path := filepath.Join(in.Peer.Root, "steps", key, stepOutputFileName)
				if err := waitForPeerFile(ctx, in.Peer, path, want); err != nil {
					return fmt.Errorf("the independent peer has no completed mirror of %q: %w", key, err)
				}
				body, err := in.Peer.fetchArtifact(ctx, "/artifacts/steps/"+key)
				if err != nil {
					return fmt.Errorf(
						"the independent peer has no readable copy of %q: %w", key, err)
				}
				got, err := oneFileInTar(body)
				if err != nil {
					return fmt.Errorf("the copy of %q on the independent peer: %w", key, err)
				}
				if got != want {
					return fmt.Errorf("expected the copy of %q to be %q, got %q", key, want, got)
				}
				return nil
			},
		),
	}
}

func artifactPodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Refine[ArtifactCluster]("a later step {string} takes the artifact {string} at {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Consumer = a.String(0)
				in.ConsumerType = db.ContainerTypeTask
				in.Inputs = append(in.Inputs, consumerInput{Key: a.String(1), Path: a.String(2)})
				return in
			}),

		Refine[ArtifactCluster]("it also takes the artifact {string} at {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Inputs = append(in.Inputs, consumerInput{Key: a.String(0), Path: a.String(1)})
				return in
			}),

		Refine[ArtifactCluster]("a later step {string} takes no inputs",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Consumer, in.ConsumerType = a.String(0), db.ContainerTypeTask
				return in
			}),

		Refine[ArtifactCluster]("a later check {string} takes no inputs",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Consumer, in.ConsumerType = a.String(0), db.ContainerTypeCheck
				return in
			}),

		brine.DefineMap[ArtifactCluster, FollowingPod](
			"that step's pod is built",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (FollowingPod, error) {
				var inputs []runtime.Input
				for _, input := range in.Inputs {
					// Restored or external references use the real backend lookup.
					// Locally produced inputs use the worker's returned volume, as
					// task output registration does in production.
					artifact := input.Artifact
					for _, volume := range in.Volumes {
						if artifact == nil && volume.Handle() == input.Key {
							artifact = in.Worker.ArtifactFromVolume(volume)
							break
						}
					}
					if artifact == nil {
						artifact = in.Backend.WrapVolumeForLookup(
							in.Ctx, jetbridge.ArtifactKey(input.Key), input.Key, in.WorkerRow.Name(), nil)
					}
					inputs = append(inputs, runtime.Input{
						Artifact:        artifact,
						DestinationPath: input.Path,
					})
				}

				spec := runtime.ContainerSpec{
					TeamID:            in.Team.ID(),
					Dir:               "/tmp/build/workdir",
					ImageSpec:         runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs:            inputs,
					Caches:            in.Caches,
					TaskCacheIdentity: in.CacheIdentity,
					Type:              in.ConsumerType,
				}

				owner := db.NewFixedHandleContainerOwner(in.Consumer)
				metadata := db.ContainerMetadata{Type: in.ConsumerType}

				// A step whose container row already exists is a RETRY, and
				// only a retry's pod clears the workspace its last attempt
				// left. Creating the row is how production gets there too:
				// the second FindOrCreateContainer finds the first one.
				if in.RanBefore {
					if _, _, err := in.Worker.FindOrCreateContainer(
						in.Ctx, owner, metadata, spec, nil,
					); err != nil {
						return FollowingPod{}, fmt.Errorf(
							"pre-create container %q: %w", in.Consumer, err)
					}
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					owner,
					metadata,
					spec,
					nil,
				)
				if err != nil {
					return FollowingPod{}, fmt.Errorf("find or create container %q: %w", in.Consumer, err)
				}
				if _, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{},
				); err != nil {
					return FollowingPod{}, fmt.Errorf("run container %q: %w", in.Consumer, err)
				}

				pods, err := in.Clientset.CoreV1().Pods(in.Namespace).
					List(in.Ctx, metav1.ListOptions{})
				if err != nil {
					return FollowingPod{}, fmt.Errorf("list pods: %w", err)
				}
				if len(pods.Items) != 1 {
					return FollowingPod{}, fmt.Errorf("expected exactly 1 pod, found %d", len(pods.Items))
				}
				pod := pods.Items[0]
				return FollowingPod{
					Handle:    in.Consumer,
					Pod:       &pod,
					Clientset: in.Clientset,
					Ctx:       in.Ctx,
					Caches:    in.Caches,
					Node:      in.Node,
					StoreRoot: in.StoreRoot,
				}, nil
			},
		),

		CheckThat[FollowingPod]("the step's inputs are fetched by one init container in one request",
			func(in FollowingPod) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				var fetchers []string
				for _, c := range in.Pod.Spec.InitContainers {
					if c.Name != "cleanup-stale" {
						fetchers = append(fetchers, c.Name)
					}
				}
				if len(fetchers) != 1 {
					return fmt.Errorf(
						"expected the pod for %q to fetch every input with ONE init container; it has %d (%v). "+
							"One per input is one image pull and one round trip per input, serially, "+
							"before the step's own command starts",
						in.Handle, len(fetchers), fetchers)
				}
				command, err := fetchCommand(in)
				if err != nil {
					return err
				}
				if !strings.Contains(command, "/resolve-batch") {
					return fmt.Errorf(
						"the pod for %q does not use the batch endpoint at all: %s",
						in.Handle, abbrev(command))
				}
				// Counting the PAYLOADS is the assertion; naming the endpoint
				// is not. A first version of this check only looked for the
				// string "/resolve-batch", and an audit demonstrated the hole
				// by rewriting daemonResolveBatchCommand to emit one wget per
				// item — each still posting to /resolve-batch, inside the same
				// single init container — which is precisely the serial
				// round-trip-per-input regression this scenario is about. All
				// eleven scenarios stayed green.
				if n := strings.Count(command, "PAYLOAD='"); n != 1 {
					return fmt.Errorf(
						"the pod for %q builds %d request payloads, so it fetches its inputs one at a "+
							"time; a ten-input task pays that ten times over, serially, before the "+
							"step's own command starts",
						in.Handle, n)
				}
				return nil
			}),

		CheckContains[FollowingPod]("that fetch asks the daemon for {string}",
			"the batch of keys the pod asks for in one request",
			fetchPayload),

		// Keeps its own body: the assertion is that the text appears NOWHERE
		// in the request, which no comparison combinator expresses.
		brine.DefineCheck[FollowingPod](
			"that fetch does not ask the daemon for {string}",
			func(in FollowingPod, p brine.Params, _ *brine.Recorder) error {
				unwanted, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a key parameter")
				}
				command, err := fetchPayload(in)
				if err != nil {
					return err
				}
				if strings.Contains(command, unwanted) {
					return fmt.Errorf(
						"the pod for %q asks the daemon for %q — its own volume handle, which no "+
							"daemon has ever heard of — instead of the directory the producing step "+
							"recorded: %s", in.Handle, unwanted, abbrev(command))
				}
				return nil
			},
		),

		CheckString[FollowingPod]("the pod prefers the node {string}",
			"the node the pod prefers",
			preferredNode),

		CheckThat[FollowingPod]("the pod expresses no preference about where it runs",
			func(in FollowingPod) error {
				terms, err := preferredTerms(in)
				if err != nil {
					return err
				}
				if len(terms) != 0 {
					return fmt.Errorf(
						"the pod for %q is steered toward a node although it reads nothing from one; "+
							"a preference derived from no inputs is a preference for an arbitrary node, "+
							"and it costs the scheduler its freedom to balance", in.Handle)
				}
				return nil
			}),

		CheckThat[FollowingPod]("the pod is not given the node's artifact store",
			func(in FollowingPod) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for _, v := range in.Pod.Spec.Volumes {
					if v.HostPath != nil && v.HostPath.Path == in.StoreRoot {
						return fmt.Errorf(
							"the pod for %q mounts %q as volume %q, which is every step's outputs on "+
								"this node — a check would get read and write access to work it has "+
								"nothing to do with", in.Handle, in.StoreRoot, v.Name)
					}
				}
				return nil
			}),

		CheckThat[FollowingPod]("the pod is given the node's artifact store",
			func(in FollowingPod) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for _, v := range in.Pod.Spec.Volumes {
					if v.HostPath != nil && v.HostPath.Path == in.StoreRoot {
						return nil
					}
				}
				return fmt.Errorf(
					"the pod for %q does not mount %q, so its init containers have nowhere to put the "+
						"inputs they fetch", in.Handle, in.StoreRoot)
			}),
	}
}

// artifactSchedulingDefinitions is the placement half: where the scheduler is
// allowed to put a step, and what the pod expects to find on the node it lands
// on.
//
// Everything here reads the POD, and that is the honest limit of it. A hostPath
// type, a mount's read-only flag and a node-selector term are instructions to a
// kubelet and a scheduler, and no kubelet or scheduler is running in this
// fixture — the pod object below is a real one, submitted through a real
// client, but nothing consumes it. Two of these checks push past a plain field
// read anyway: the affinity check evaluates the requirement AGAINST THE NODES
// THAT EXIST, which is the question the scheduler asks and the only way a
// requirement nothing can satisfy is visible at all; and the cache check
// resolves the directory through the pod's own mount wiring, from the path the
// step named, rather than trusting the volume-naming convention.
func artifactSchedulingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		Transform[ArtifactCluster, ArtifactCluster]("an input artifact is recorded on node {string}",
			func(in ArtifactCluster, a Args) (ArtifactCluster, error) {
				nodeName := a.String(0)
				if nodeName == "" {
					return in, fmt.Errorf("expected a recorded node name")
				}
				volume, _, err := in.Worker.CreateVolumeForArtifact(in.Ctx, in.Team.ID())
				if err != nil {
					return in, fmt.Errorf("create input artifact: %w", err)
				}
				key := jetbridge.ArtifactKey(volume.Handle())
				in.Locator.Record(key, nodeName, key)
				in.Inputs = append(in.Inputs, consumerInput{
					Key: volume.Handle(), Artifact: volume,
					Path: fmt.Sprintf("/tmp/build/workdir/input-%d", len(in.Inputs)),
				})
				return in, nil
			}),
		// Only the index and submitted pod are under test here. No daemon,
		// node status, mirrored bytes or running task is needed for placement.
		brine.DefineMapUsing[brine.Empty, ArtifactCluster](
			"a jetbridge worker placing step {string} from recorded artifact locations",
			[]string{"jetbridge-db", "real-cluster"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (ArtifactCluster, error) {
				handle, ok := p.GetString(0)
				if !ok || handle == "" {
					return ArtifactCluster{}, fmt.Errorf("expected a consuming step handle")
				}
				api, err := getRealCluster(res)
				if err != nil {
					return ArtifactCluster{}, err
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ArtifactCluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				ctx := context.Background()
				ns, err := api.Clientset.CoreV1().Namespaces().Create(ctx,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "artifact-placement-"}}, metav1.CreateOptions{})
				if err != nil {
					return ArtifactCluster{}, err
				}
				registerNamespacePodCleanup(rec, api.Clientset, ns.Name)
				row, err := database.PersistNamedWorker("artifact-placement-worker")
				if err != nil {
					return ArtifactCluster{}, err
				}
				return wireArtifactCluster(ArtifactCluster{
					Ctx: ctx, Namespace: ns.Name, Clientset: api.Clientset,
					DB: database, WorkerRow: row, StoreRoot: "/brine/artifacts",
					Consumer: handle, ConsumerType: db.ContainerTypeTask,
				}, jetbridge.NewSPDYExecutor(api.Clientset, api.RESTConfig))
			},
		),

		Refine[ArtifactCluster]("it keeps a task cache at {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Caches = append(in.Caches, a.String(0))
				// A task cache belongs to a job. Since 0d336e062b the
				// ContainerSpec's TaskCacheIdentity is the only thing that
				// selects node-local cache storage, and a step without one
				// gets an emptyDir that dies with the pod — which is the
				// right answer for a one-off build and the wrong one for the
				// step this sentence describes, whose whole point is a cache
				// that outlives the pod and has to land somewhere on the node.
				in.CacheIdentity = &atc.TaskCacheIdentity{JobID: cachedStepJobID}
				return in
			}),

		Refine[ArtifactCluster]("that step has run here before",
			func(in ArtifactCluster, _ Args) ArtifactCluster {
				in.RanBefore = true
				return in
			}),

		// The scheduler's own question, asked of the fleet that exists. A
		// requirement is never wrong on its own — it is wrong relative to the
		// nodes it is asked of, and a term naming a value no daemon writes
		// reads exactly like the correct one until you hold it against a node.
		CheckThat[FollowingPod]("the node running the artifact daemon can accept the pod",
			func(in FollowingPod) error {
				terms, err := requiredNodeTerms(in)
				if err != nil {
					return err
				}
				if in.Clientset == nil {
					return fmt.Errorf("no cluster was carried forward with the pod")
				}
				nodes, err := in.Clientset.CoreV1().Nodes().List(in.Ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("list nodes: %w", err)
				}
				if len(nodes.Items) == 0 {
					return fmt.Errorf(
						"the cluster has no nodes, so there is nothing to hold the pod for %q "+
							"against and this would pass without asserting anything", in.Handle)
				}
				var rejected []string
				for _, node := range nodes.Items {
					ok, err := nodeSatisfies(node, terms)
					if err != nil {
						return err
					}
					if ok {
						return nil
					}
					rejected = append(rejected, fmt.Sprintf("%s %v", node.Name, node.Labels))
				}
				return fmt.Errorf(
					"the pod for %q demands %s, and no node running an artifact daemon carries "+
						"that: %v. Nothing in the fleet can ever satisfy it, so every build pod "+
						"stays Pending until it times out and the only symptom is \"node(s) "+
						"didn't match Pod's node affinity\"",
					in.Handle, describeTerms(terms), rejected)
			}),

		// The first time a step runs on a node, none of its directories are
		// there — they are named after a container handle that is new every
		// run. Asserted over every one the pod carries, because the working
		// directory, the inputs and the outputs all come from the same call.
		CheckThat[FollowingPod]("every directory the pod expects on the node is created if it is missing",
			func(in FollowingPod) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				found := 0
				for _, v := range in.Pod.Spec.Volumes {
					if v.HostPath == nil {
						continue
					}
					found++
					if v.HostPath.Type != nil && *v.HostPath.Type == corev1.HostPathDirectoryOrCreate {
						continue
					}
					got := "unset"
					if v.HostPath.Type != nil {
						got = string(*v.HostPath.Type)
					}
					return fmt.Errorf(
						"the pod for %q requires %q to already exist on the node (hostPath type "+
							"%q) rather than asking for it to be created. The step's directories "+
							"under it are named after a container handle that is new every run, so "+
							"there is no node in the cluster where they do exist: the kubelet "+
							"refuses the pod with a hostPath type check failure and the step fails "+
							"before its command runs, with an error that never mentions artifacts",
						in.Handle, v.HostPath.Path, got)
				}
				if found == 0 {
					return fmt.Errorf(
						"the pod for %q keeps nothing on the node at all, so this asserts nothing "+
							"— the worker is not storing step data on its nodes", in.Handle)
				}
				return nil
			}),

		// A task cache and a step's outputs are the same thing on disk and
		// opposite things in lifetime. The steps tree is build-scoped: the
		// daemon's sweeper deletes every child of it past the TTL and its
		// mirror copies what it finds there to the peers.
		CheckThat[FollowingPod]("the task cache is filed apart from the step data on that node",
			func(in FollowingPod) error {
				if len(in.Caches) == 0 {
					return fmt.Errorf(
						"the step for %q asked to keep no cache, so this asserts nothing", in.Handle)
				}
				stepsTree := in.StoreRoot + "/steps/"
				cachesTree := in.StoreRoot + "/caches/"
				for _, cachePath := range in.Caches {
					hostDir, err := hostDirForMountPath(in, cachePath)
					if err != nil {
						return err
					}
					if strings.HasPrefix(hostDir, stepsTree) {
						return fmt.Errorf(
							"the cache the step keeps at %q lives on the node at %q, inside %q — "+
								"the tree the daemon treats as build data. Its sweeper deletes "+
								"every child of that tree once it is older than the TTL, and its "+
								"mirror copies what it finds there to every peer. The cache is "+
								"swept between builds instead of kept, so a cached build is never "+
								"faster than an uncached one and nothing anywhere reports an error",
							cachePath, hostDir, stepsTree)
					}
					if !strings.HasPrefix(hostDir, cachesTree) {
						return fmt.Errorf(
							"the cache the step keeps at %q lives on the node at %q, outside %q, "+
								"where nothing that manages task caches will find it",
							cachePath, hostDir, cachesTree)
					}
				}
				return nil
			}),

		// The cleanup container's whole body is an rm over the node's artifact
		// store. It reaches the store through a mount, and a read-only mount
		// makes the rm fail.
		CheckThat[FollowingPod]("the step's cleanup can really delete what the last attempt left",
			func(in FollowingPod) error {
				mount, err := storeMountOf(in, "cleanup-stale")
				if err != nil {
					return err
				}
				if mount.ReadOnly {
					return fmt.Errorf(
						"the pod for %q gives its cleanup container the node's artifact store "+
							"read-only at %q, so the rm it exists to perform fails with EROFS. "+
							"The init container exits non-zero and every retry of every reused "+
							"step dies before its command runs; swallow that and the retry meets "+
							"its own half-written outputs instead — the \"destination path "+
							"already exists\" failure the cleanup is there to prevent",
						in.Handle, mount.MountPath)
				}
				return nil
			}),

		// The contrast that keeps the check above honest: the same store is
		// mounted twice, and the OTHER mount is read-only on purpose. The
		// fetch container does not write the artifacts itself — it asks the
		// daemon to — so "make every mount writable" is not the fix.
		CheckThat[FollowingPod]("the fetch of its inputs still cannot write there",
			func(in FollowingPod) error {
				mount, err := storeMountOf(in, "fetch-inputs")
				if err != nil {
					return err
				}
				if !mount.ReadOnly {
					return fmt.Errorf(
						"the pod for %q lets the container that fetches its inputs write to the "+
							"whole node's artifact store at %q. It never needs to — it posts the "+
							"batch and the daemon does the writing — so this is every other "+
							"step's outputs handed, writable, to a container running a helper "+
							"image on someone else's behalf",
						in.Handle, mount.MountPath)
				}
				return nil
			}),
	}
}

// requiredNodeTerms is what the pod DEMANDS of the node it lands on.
//
// A pod with no requirement is reported rather than treated as "matches
// everything", which is what an absent node selector means to Kubernetes and
// the opposite of what this family is about: without it the scheduler may put
// a step on a node with no artifact daemon, where its inputs cannot be fetched
// at all.
func requiredNodeTerms(in FollowingPod) ([]corev1.NodeSelectorTerm, error) {
	if in.Pod == nil {
		return nil, fmt.Errorf("no pod was created")
	}
	aff := in.Pod.Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil ||
		aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil ||
		len(aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
		return nil, fmt.Errorf(
			"the pod for %q demands nothing of the node it lands on, so the scheduler may place "+
				"it anywhere — including a node with no artifact daemon, where the step cannot "+
				"read its inputs at all", in.Handle)
	}
	return aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms, nil
}

// nodeSatisfies answers the question the kube-scheduler asks of a node: do its
// labels satisfy any ONE of the pod's required terms? Terms are OR-ed and the
// expressions inside a term are AND-ed, which is Kubernetes' own rule.
//
// An operator this does not implement is REPORTED, never skipped. A matcher
// that quietly ignored a term it could not read would answer "yes" to a
// requirement it never evaluated, which is precisely the failure this check
// exists to catch.
func nodeSatisfies(node corev1.Node, terms []corev1.NodeSelectorTerm) (bool, error) {
	for _, term := range terms {
		if len(term.MatchFields) > 0 {
			return false, fmt.Errorf(
				"the pod selects on node fields (%v), which this check does not evaluate",
				term.MatchFields)
		}
		if len(term.MatchExpressions) == 0 {
			continue
		}
		satisfied := true
		for _, expr := range term.MatchExpressions {
			ok, err := labelsSatisfy(node.Labels, expr)
			if err != nil {
				return false, err
			}
			if !ok {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true, nil
		}
	}
	return false, nil
}

func labelsSatisfy(labels map[string]string, expr corev1.NodeSelectorRequirement) (bool, error) {
	value, present := labels[expr.Key]
	switch expr.Operator {
	case corev1.NodeSelectorOpIn:
		if !present {
			return false, nil
		}
		for _, want := range expr.Values {
			if value == want {
				return true, nil
			}
		}
		return false, nil
	case corev1.NodeSelectorOpNotIn:
		if !present {
			return true, nil
		}
		for _, want := range expr.Values {
			if value == want {
				return false, nil
			}
		}
		return true, nil
	case corev1.NodeSelectorOpExists:
		return present, nil
	case corev1.NodeSelectorOpDoesNotExist:
		return !present, nil
	default:
		return false, fmt.Errorf(
			"the pod requires a node with the %q operator, which this check does not evaluate",
			expr.Operator)
	}
}

// describeTerms renders a requirement the way a failure needs to read it,
// keeping Kubernetes' own grouping: expressions inside a term are AND-ed and
// the terms themselves are OR-ed. A message that flattened the two would
// misreport which half of a compound requirement went unmet.
func describeTerms(terms []corev1.NodeSelectorTerm) string {
	var described []string
	for _, term := range terms {
		var exprs []string
		for _, expr := range term.MatchExpressions {
			exprs = append(exprs, fmt.Sprintf("%s %s %v", expr.Key, expr.Operator, expr.Values))
		}
		if len(exprs) > 0 {
			described = append(described, strings.Join(exprs, " and "))
		}
	}
	if len(described) == 0 {
		return "nothing"
	}
	return "a node with " + strings.Join(described, ", or ")
}

// hostDirForMountPath resolves where on the NODE a path inside the step comes
// from, following the pod's own wiring: the mount the step sees at that path
// names a volume, and the volume names a directory. Going through the mount
// rather than the volume's name is the point — the step named the path, and
// nothing else in the scenario has to know what the backend called the volume.
func hostDirForMountPath(in FollowingPod, mountPath string) (string, error) {
	return podHostDir(in.Pod, in.Handle, mountPath)
}

// podHostDir is the same resolution against a pod on its own, so the producing
// step's pod — which is not a FollowingPod, it is the step that went first —
// can be asked the same question.
func podHostDir(pod *corev1.Pod, handle, mountPath string) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("no pod was created")
	}
	name := ""
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.MountPath == mountPath {
				name = m.Name
			}
		}
	}
	if name == "" {
		return "", fmt.Errorf(
			"the pod for %q mounts nothing at %q, so the step cannot see it at all",
			handle, mountPath)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name != name {
			continue
		}
		if v.HostPath == nil {
			return "", fmt.Errorf(
				"what the pod for %q gives the step at %q is not a directory on the node, so it "+
					"is lost with the pod and cannot be a cache at all", handle, mountPath)
		}
		return v.HostPath.Path, nil
	}
	return "", fmt.Errorf(
		"the pod for %q mounts volume %q at %q but declares no such volume",
		handle, name, mountPath)
}

// storeMountOf finds how one init container reaches the node's artifact store.
//
// The volume is identified by the directory it points at rather than by name,
// so the check is about the store the daemon serves and not about a constant
// production could rename.
func storeMountOf(in FollowingPod, container string) (corev1.VolumeMount, error) {
	if in.Pod == nil {
		return corev1.VolumeMount{}, fmt.Errorf("no pod was created")
	}
	storeVolume := ""
	for _, v := range in.Pod.Spec.Volumes {
		if v.HostPath != nil && v.HostPath.Path == in.StoreRoot {
			storeVolume = v.Name
		}
	}
	if storeVolume == "" {
		return corev1.VolumeMount{}, fmt.Errorf(
			"the pod for %q does not carry the node's artifact store at all", in.Handle)
	}
	for _, c := range in.Pod.Spec.InitContainers {
		if c.Name != container {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.Name == storeVolume {
				return m, nil
			}
		}
		return corev1.VolumeMount{}, fmt.Errorf(
			"the %s container in the pod for %q cannot reach the node's artifact store at all",
			container, in.Handle)
	}
	return corev1.VolumeMount{}, fmt.Errorf(
		"the pod for %q has no %s init container", in.Handle, container)
}

func artifactLookupDefinitions() []brine.StepDefinition {
	return append(artifactLookupContractDefinitions(),
		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the ATC cannot go looking for daemons it was not told about",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				if err := in.unpublishDaemons(); err != nil {
					return in, err
				}
				return in, nil
			},
		),
	)
}

// lookupVolume is the production entry point a consumer uses: the worker finds
// the row and hands back whatever its storage backend decided to wrap it in.
func lookupVolume(in ArtifactCluster, handle string) (runtime.Volume, error) {
	vol, found, err := in.Worker.LookupVolume(in.Ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("look up %q: %w", handle, err)
	}
	if !found {
		return nil, fmt.Errorf("no volume %q is in the database", handle)
	}
	return vol, nil
}

// oneResourceCache creates the resource cache a get step would have produced.
// The worker has to offer the type before a cache for it can exist, which is
// the same row the registrar writes.
func oneResourceCache(in ArtifactCluster) (db.ResourceCache, error) {
	if _, err := in.DB.WorkerFactory.SaveWorker(atc.Worker{
		Name: in.WorkerRow.Name(), Platform: "linux", Version: "1.2.3",
		State: string(db.WorkerStateRunning),
		ResourceTypes: []atc.WorkerResourceType{{
			Type: "mock", Image: "some-image", Version: "some-version",
		}},
	}, 0); err != nil {
		return nil, fmt.Errorf("save worker with resource types: %w", err)
	}
	build, err := in.Team.CreateOneOffBuild()
	if err != nil {
		return nil, fmt.Errorf("create one-off build: %w", err)
	}
	cache, err := db.NewResourceCacheFactory(in.DB.Conn, in.DB.LockFactory).
		FindOrCreateResourceCache(
			db.ForBuild(build.ID()),
			"mock",
			atc.Version{"version": "1"},
			atc.Source{"uri": "example.invalid"},
			nil,
			nil,
		)
	if err != nil {
		return nil, fmt.Errorf("find or create resource cache: %w", err)
	}
	return cache, nil
}

// fetchCommand is the request the pod's init container will make. It is the
// pod spec's own text, not a record of anything: the scheduler and the kubelet
// read exactly this.
func fetchCommand(in FollowingPod) (string, error) {
	container, err := fetchContainer(in)
	if err != nil {
		return "", err
	}
	return strings.Join(container.Command, " "), nil
}

// fetchContainer is the init container the kubelet will run before the step.
// The checks that only need its script go through fetchCommand; the ones that
// need what the kubelet puts in its ENVIRONMENT, or that run the script, need
// the container itself.
func fetchContainer(in FollowingPod) (corev1.Container, error) {
	if in.Pod == nil {
		return corev1.Container{}, fmt.Errorf("no pod was created")
	}
	for _, c := range in.Pod.Spec.InitContainers {
		if c.Name == "fetch-inputs" {
			return c, nil
		}
	}
	return corev1.Container{}, fmt.Errorf(
		"the pod for %q has no init container to fetch its inputs, so the step starts against an "+
			"empty workspace and fails on a file it was handed", in.Handle)
}

// fetchPayload returns the ONE request body the fetch init container posts.
//
// Asking whether a key appears anywhere in the script is a weaker question
// than asking whether it is in the batch: a script looping one request per
// item mentions every key too. This returns the single payload so that
// "asks the daemon for X" means X travelled in the same request as the others.
func fetchPayload(in FollowingPod) (string, error) {
	command, err := fetchCommand(in)
	if err != nil {
		return "", err
	}
	const marker = "PAYLOAD='"
	if n := strings.Count(command, marker); n != 1 {
		return "", fmt.Errorf(
			"expected the pod for %q to build exactly one request payload, found %d",
			in.Handle, n)
	}
	rest := command[strings.Index(command, marker)+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return "", fmt.Errorf("the pod for %q has an unterminated request payload", in.Handle)
	}
	return rest[:end], nil
}

func preferredTerms(in FollowingPod) ([]corev1.PreferredSchedulingTerm, error) {
	if in.Pod == nil {
		return nil, fmt.Errorf("no pod was created")
	}
	aff := in.Pod.Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil {
		return nil, nil
	}
	return aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution, nil
}

func preferredNode(in FollowingPod) (string, error) {
	terms, err := preferredTerms(in)
	if err != nil {
		return "", err
	}
	var named []string
	for _, term := range terms {
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key != "kubernetes.io/hostname" {
				continue
			}
			if term.Weight <= 0 || expr.Operator != corev1.NodeSelectorOpIn {
				return "", fmt.Errorf("pod %q does not positively prefer its named node: weight=%d operator=%s",
					in.Handle, term.Weight, expr.Operator)
			}
			named = append(named, expr.Values...)
		}
	}
	if len(named) != 1 {
		return "", fmt.Errorf("pod %q must prefer exactly one node, got %v", in.Handle, named)
	}
	return named[0], nil
}

// -----------------------------------------------------------------------
// Where a step's data actually lands
// -----------------------------------------------------------------------
//
// Everything above describes a step's outputs as though the fixture knew where
// they were: a Given puts bytes at steps/<handle>/<output> and the checks read
// them back. That is the layout production is SUPPOSED to use, written down
// twice — once in the pod and once in the fixture — so the two can never
// disagree and the scenarios cannot see it when production's two halves do.
//
// They are separate halves. The pod's hostPath comes from container.go's
// Container.buildVolumeMounts, which names the subdirectory after the output
// (`subdir = outName`); the daemon key comes from storage_daemonset.go's
// RecordOutputs, which derives it independently from the same spec. Nothing
// joins them. Rename the subdirectory on one side and every read by handle
// 404s on a node that has the bytes.
//
// Not to be confused with Worker.buildVolumeMountsForSpec (worker.go), which
// builds the runtime.VolumeMount list and knows nothing about hostPath
// subdirectories.
//
// The steps below take the layout from the POD instead. The producing step's
// pod is built, its mounts are followed to the directories they point at, and
// the bytes are written THERE — which is what a step does: it writes into the
// mount it was given and the kubelet decides where that lands. Then the ATC
// records where it thinks the outputs went, and the read either resolves or
// does not. Two derivations, one assertion, and no third copy of the layout in
// the fixture to keep them agreeing.

// getStepWorkDir is where a get step's pod puts its working directory. A get
// step has no named outputs — the directory it fetched into IS the output, and
// RecordOutputs files it under the name "dir".
const getStepWorkDir = "/tmp/build/get"

func artifactLayoutDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// A step whose pod could not be found when it finished — the node
		// lookup failed, or the pod was already gone. The outputs are on a
		// node's disk either way; what is missing is the name of the node.
		Refine[ArtifactCluster]("the step {string} ran on a node the worker could not identify",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Handle, in.NodeName = a.String(0), ""
				return in
			}),

		// Declares an output WITHOUT putting anything on the node. Where the
		// bytes go is the pod's answer, and the pod does not exist yet.
		Refine[ArtifactCluster]("it writes {string} to its output {string} in the volume {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				content, name, handle := a.String(0), a.String(1), a.String(2)
				path := outputMountPath(name)
				in.Outputs[name] = path
				in.ExpectedVolumes[path] = handle
				in.Writes = append(in.Writes,
					producerWrite{Name: name, Path: path, Content: content})
				return in
			}),

		// The get-step shape of the same thing. There is no output name to
		// give: the working directory is the output, and the ATC's name for it
		// is "dir".
		Refine[ArtifactCluster]("it fetched {string} into its working directory, which is the volume {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				content, handle := a.String(0), a.String(1)
				in.ExpectedVolumes[in.ProducerDir] = handle
				in.Writes = append(in.Writes,
					producerWrite{Name: "dir", Path: in.ProducerDir, Content: content})
				return in
			}),

		// The step runs: its pod is built, and what it writes lands wherever
		// its own mounts point. Nothing here chooses a directory.
		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"the bytes reached the node through the mounts the step's own pod gave it",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				if len(in.Writes) == 0 {
					return ArtifactCluster{}, fmt.Errorf(
						"the step wrote nothing, so nothing would reach the node and the read " +
							"afterwards would be asserting against an empty fixture")
				}
				if in.Node == nil {
					return ArtifactCluster{}, fmt.Errorf(
						"this scenario has no real node for the step's bytes to land on")
				}
				pod, err := buildProducerPod(&in)
				if err != nil {
					return ArtifactCluster{}, err
				}
				for _, write := range in.Writes {
					hostDir, err := podHostDir(pod, in.Handle, write.Path)
					if err != nil {
						return ArtifactCluster{}, fmt.Errorf(
							"the step %q writes its output %q at %q: %w",
							in.Handle, write.Name, write.Path, err)
					}
					rel, contained := in.Node.contained(hostDir)
					if !contained {
						return ArtifactCluster{}, fmt.Errorf(
							"the pod for %q keeps its output %q at %q, which is outside the "+
								"artifact store at %q — the node's daemon serves nothing from "+
								"there, so the output is unreachable however it is recorded",
							in.Handle, write.Name, hostDir, in.StoreRoot)
					}
					if in.live != nil {
						_, err = in.live.store.exec(in.Ctx, pod.Name, []string{"sh", "-ec", "mkdir -p \"$1\"; cat > \"$1/$2\"", "write-producer-output", write.Path, stepOutputFileName}, strings.NewReader(write.Content))
					} else {
						err = in.Node.write(rel, write.Content)
					}
					if err != nil {
						return ArtifactCluster{}, err
					}
				}
				return in, nil
			},
		),
	}
}

// prepareProducer gets volumes from the same worker factory used by task/get
// steps. Feature handles are expectations, never constructor arguments. Keep
// scratch mounts too: RecordOutputs must decide which paths are outputs.
func prepareProducer(in *ArtifactCluster) (runtime.Container, error) {
	if in.Producer != nil {
		return in.Producer, nil
	}
	if len(in.ExpectedVolumes) == 0 {
		return nil, fmt.Errorf("producer %q declares no volume expectations", in.Handle)
	}
	spec := in.producerSpec()
	spec.TeamID = in.Team.ID()
	spec.ImageSpec = runtime.ImageSpec{ImageURL: "docker:///busybox"}
	if in.live != nil {
		spec.ImageSpec.ImageURL = "busybox:1.37.0"
	}
	container, mounts, err := in.Worker.FindOrCreateContainer(
		in.Ctx, db.NewFixedHandleContainerOwner(in.Handle),
		db.ContainerMetadata{Type: in.ProducerType}, spec, nil)
	if err != nil {
		return nil, fmt.Errorf("create producer %q: %w", in.Handle, err)
	}
	for _, mount := range mounts {
		volume, ok := mount.Volume.(*jetbridge.Volume)
		if !ok || !volume.HasExecutor() {
			return nil, fmt.Errorf("producer %q returned a non-executable volume at %q", in.Handle, mount.MountPath)
		}
		in.Volumes = append(in.Volumes, volume)
	}
	for path, handle := range in.ExpectedVolumes {
		volume, err := producerVolumeAt(*in, path)
		if err != nil {
			return nil, err
		}
		if volume.Handle() != handle {
			return nil, fmt.Errorf("producer %q volume at %q: expected handle %q, got %q",
				in.Handle, path, handle, volume.Handle())
		}
	}
	in.Producer = container
	return container, nil
}

func producerVolumeAt(in ArtifactCluster, path string) (*jetbridge.Volume, error) {
	var found *jetbridge.Volume
	for _, volume := range in.Volumes {
		if volume.MountPath() != path {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("producer %q returned duplicate volumes at %q", in.Handle, path)
		}
		found = volume
	}
	if found == nil {
		return nil, fmt.Errorf("producer %q returned no volume at %q", in.Handle, path)
	}
	return found, nil
}

// buildProducerPod starts the producer far enough to inspect its actual pod.
func buildProducerPod(in *ArtifactCluster) (*corev1.Pod, error) {
	container, err := prepareProducer(in)
	if err != nil {
		return nil, err
	}
	if _, err := container.Run(in.Ctx,
		runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{},
	); err != nil {
		return nil, fmt.Errorf("run the producing container %q: %w", in.Handle, err)
	}

	pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{LabelSelector: "concourse.ci/handle=" + in.Handle})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf(
			"expected the producing step %q to have exactly 1 pod, found %d",
			in.Handle, len(pods.Items))
	}
	pod := pods.Items[0]
	if in.live != nil {
		ready, err := awaitLiveStoragePod(in.Ctx, in.live.store, &pod)
		if err != nil {
			return nil, err
		}
		if ready.Spec.NodeName != in.NodeName {
			return nil, fmt.Errorf("producer ran on %q instead of artifact node %q", ready.Spec.NodeName, in.NodeName)
		}
		if err := validatePodMounts(ready); err != nil {
			return nil, err
		}
		return ready, nil
	}
	return &pod, nil
}

// -----------------------------------------------------------------------
// Running the fetch the pod would run
// -----------------------------------------------------------------------
//
// The init container's script is production's own text and a real shell reads
// it, the way supervisor_script_test.go reads the supervisor's. What the
// kubelet does with the result is the whole assertion: a non-zero exit stops
// the pod before the step's command, a zero exit lets it through.
//
// The daemon answering is the actual artifact-daemon binary, asked with the
// pod's own payload, and that is what makes these two scenarios worth running:
// the batch it is handed names keys the ATC derived and destinations the POD
// derived, and the daemon has to find the first on its own disk and land a
// DIRECTORY on the second. A map keyed by "steps/"+key answers both without
// either derivation being right.
//
// The unchanged script runs under actual BusyBox sh/wget/sleep. It makes its
// own HTTP requests, reads their real responses, and waits through retries.
// The fixture only prepares the owned destination directories, which the
// kubelet would create before running an init container. There is no kubelet
// here; this proves the script/daemon boundary, not pod execution.

func artifactFetchRunDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[FollowingPod, FetchOutcome](
			"the node's daemon answers its fetch",
			func(in FollowingPod, _ brine.Params, _ *brine.Recorder) (FetchOutcome, error) {
				if in.Node == nil {
					return FetchOutcome{}, fmt.Errorf(
						"no node daemon was carried forward with the pod for %q", in.Handle)
				}
				container, err := fetchContainer(in)
				if err != nil {
					return FetchOutcome{}, err
				}
				argv := container.Command
				if len(argv) != 3 || argv[1] != "-c" {
					return FetchOutcome{}, fmt.Errorf(
						"the pod for %q does not fetch its inputs with a shell script (%v), so "+
							"there is nothing here to run", in.Handle, argv)
				}
				payload, err := fetchPayload(in)
				if err != nil {
					return FetchOutcome{}, err
				}

				// The kubelet's half. Every hostPath the pod declares is
				// DirectoryOrCreate, so by the time an init container runs its
				// input directories exist on the node; the daemon walks to a
				// destination's parent through its storage root and refuses
				// one that is not there. Without this the batch would fail for
				// a reason no scenario is about.
				if err := makeFetchDestinations(in.Node, payload); err != nil {
					return FetchOutcome{}, fmt.Errorf(
						"preparing the destinations the pod for %q named: %w", in.Handle, err)
				}

				// Local requests and nine real two-second retry waits fit this
				// budget. A timeout is a fixture failure, never an expected fetch refusal.
				ctx, cancel := context.WithTimeout(in.Ctx, 30*time.Second)
				defer cancel()
				out, runErr := runBusyboxScript(ctx, argv, []string{"HOST_IP=" + in.Node.host})

				exitCode := 0
				if runErr != nil {
					var exitErr *exec.ExitError
					if !errors.As(runErr, &exitErr) {
						return FetchOutcome{}, fmt.Errorf(
							"running the fetch script for %q: %w (output: %s)",
							in.Handle, runErr, abbrev(string(out)))
					}
					exitCode = exitErr.ExitCode()
				}

				return FetchOutcome{
					Ctx:       in.Ctx,
					Handle:    in.Handle,
					Pod:       in.Pod,
					Node:      in.Node,
					StoreRoot: in.StoreRoot,
					ExitCode:  exitCode,
					Output:    string(out),
				}, nil
			},
		),

		CheckThat[FetchOutcome]("the fetch succeeded, so the step starts",
			func(in FetchOutcome) error {
				if in.ExitCode != 0 {
					return fmt.Errorf(
						"the fetch of %q's inputs exited %d, so the kubelet never starts the "+
							"step's own command and the build fails on the fetch: %s",
						in.Handle, in.ExitCode, abbrev(in.Output))
				}
				return nil
			}),

		CheckThat[FetchOutcome]("the fetch failed, so the step never starts",
			func(in FetchOutcome) error {
				if in.ExitCode == 0 {
					return fmt.Errorf(
						"the daemon could not deliver every input, and the fetch of %q's inputs "+
							"exited 0 anyway. The kubelet reads that as success and starts the "+
							"step against a workspace missing the files the pipeline promised "+
							"it, so the build fails later on a file it was handed — nowhere near "+
							"the fetch that never happened, and with nothing in the log to "+
							"connect the two: %s",
						in.Handle, abbrev(in.Output))
				}
				// The daemon answers a batch with its worst item's status: 404
				// when an artifact is simply not on this node, 422 when one
				// that is here is refused, 500 when resolving it broke. This
				// scenario's missing "vol-lost" is the 404 case; what the
				// step must not do is retry it or start.
				if !strings.Contains(in.Output, "server returned error: HTTP/1.1 404") {
					return fmt.Errorf("fetch exited %d without the daemon's 404 for the missing artifact: %s",
						in.ExitCode, abbrev(in.Output))
				}
				return nil
			}),

		// What the step will actually read, fetched from the node over the
		// wire. The path named is the step's own mount path; where that comes
		// from on the node is the pod's answer, followed rather than assumed.
		CheckStringFor[FetchOutcome]("what the step finds at {string} is {string}",
			"what the step reads",
			func(in FetchOutcome, mountPath string) (string, error) {
				hostDir, err := podHostDir(in.Pod, in.Handle, mountPath)
				if err != nil {
					return "", err
				}
				rel, contained := in.Node.contained(hostDir)
				if !contained {
					return "", fmt.Errorf(
						"the pod for %q reads %q from %q, outside the artifact store at %q, so "+
							"the daemon could not have put anything there",
						in.Handle, mountPath, hostDir, in.StoreRoot)
				}
				body, err := in.Node.fetchArtifact(in.Ctx, "/artifacts/"+rel)
				if err != nil {
					return "", fmt.Errorf(
						"the step's workspace at %q holds nothing the daemon will serve, so the "+
							"fetch exited 0 without landing the input there: %w", mountPath, err)
				}
				return oneFileInTar(body)
			}),

		// The address the init container dials, held against the node it is
		// running on. A pod reaches its OWN node's daemon: the kubelet puts the
		// node's IP in HOST_IP through the downward API, because a daemon
		// serving a hostPath can only be the one on the machine holding it.
		CheckThat[FollowingPod]("the fetch dials the daemon on the node the pod lands on",
			func(in FollowingPod) error {
				container, err := fetchContainer(in)
				if err != nil {
					return err
				}
				if in.Node == nil {
					return fmt.Errorf("no node daemon was carried forward with the pod")
				}
				script := container.Command[len(container.Command)-1]

				address, found := scriptAssignment(script, "DAEMON")
				if !found {
					return fmt.Errorf(
						"the pod for %q builds no daemon address at all: %s",
						in.Handle, abbrev(script))
				}
				_, host, port, parsed := splitDaemonAddress(address)
				if !parsed {
					return fmt.Errorf(
						"the pod for %q dials %q, which is not an address of the form "+
							"scheme://host:port", in.Handle, address)
				}
				if host != "${HOST_IP}" {
					return fmt.Errorf(
						"the pod for %q dials its daemon at %q, so every input fetch goes to %q "+
							"instead of to the node the pod landed on. The artifact daemon is a "+
							"DaemonSet reached on the node's own address — the kubelet supplies "+
							"it in HOST_IP — and nothing answers on %q inside the step's own "+
							"network namespace. Every fetch exhausts its retry budget and no "+
							"step with an input ever starts",
						in.Handle, address, host, host)
				}
				if port != "${PORT}" {
					return fmt.Errorf(
						"the pod for %q dials port %q rather than the port the script resolved "+
							"for the daemon", in.Handle, port)
				}
				configured, found := scriptAssignment(script, "PORT")
				if !found {
					return fmt.Errorf("the pod for %q resolves no daemon port", in.Handle)
				}
				if want := strconv.Itoa(in.Node.port); configured != want {
					return fmt.Errorf(
						"the pod for %q dials port %s, but the daemon on its node listens on %s, "+
							"so the fetch reaches nothing", in.Handle, configured, want)
				}

				for _, env := range container.Env {
					if env.Name != "HOST_IP" {
						continue
					}
					if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil {
						return fmt.Errorf(
							"the pod for %q sets HOST_IP to a fixed value rather than reading it "+
								"from the node it lands on, so every pod dials the same address "+
								"whatever node the scheduler chose", in.Handle)
					}
					if got := env.ValueFrom.FieldRef.FieldPath; got != "status.hostIP" {
						return fmt.Errorf(
							"the pod for %q takes HOST_IP from %q, which is not the address of "+
								"the node it is running on", in.Handle, got)
					}
					return nil
				}
				return fmt.Errorf(
					"the pod for %q never learns its node's address: its fetch container has no "+
						"HOST_IP, so the address it dials expands to nothing", in.Handle)
			}),
	}
}

// makeFetchDestinations creates, on the node, the input directories the pod
// declared — the kubelet's work, done here because no kubelet is running.
//
// It reads them out of the pod's OWN payload rather than deriving them a
// second time. Deriving them here would be a third copy of the layout the two
// halves of production already derive independently, and the whole point of
// this family is that nothing in the fixture holds those two together.
func makeFetchDestinations(node *realNode, payload string) error {
	var batch struct {
		Items []struct {
			Dest string `json:"dest"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(payload), &batch); err != nil {
		return fmt.Errorf("the pod's request payload is not readable JSON (%s): %w",
			abbrev(payload), err)
	}
	if len(batch.Items) == 0 {
		return fmt.Errorf("the pod's request payload asks for nothing: %s", abbrev(payload))
	}
	for _, item := range batch.Items {
		if _, contained := node.contained(item.Dest); !contained {
			return fmt.Errorf(
				"the pod asks the daemon to deliver an input to %q, outside the node's artifact "+
					"store at %q — the daemon refuses that, and no kubelet would have made it "+
					"either", item.Dest, node.Root)
		}
		if err := os.MkdirAll(item.Dest, 0o755); err != nil {
			return fmt.Errorf("make the input directory %q on the node: %w", item.Dest, err)
		}
	}
	return nil
}

// scriptAssignment reads the VALUE a shell script assigns to a variable, so a
// check can compare that value instead of asking whether some text appears
// somewhere in the script. Quotes around the value are the shell's, not part
// of it.
func scriptAssignment(script, name string) (string, bool) {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, name+"=") {
			continue
		}
		value := strings.TrimPrefix(line, name+"=")
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		return value, true
	}
	return "", false
}

// splitDaemonAddress breaks scheme://host:port apart. The host may still be an
// unexpanded shell variable, which is the point: what a pod dials is decided
// when the kubelet runs it, not when the ATC writes the script.
func splitDaemonAddress(address string) (scheme, host, port string, ok bool) {
	i := strings.Index(address, "://")
	if i < 0 {
		return "", "", "", false
	}
	scheme, rest := address[:i], address[i+len("://"):]
	j := strings.LastIndex(rest, ":")
	if j < 0 {
		return "", "", "", false
	}
	return scheme, rest[:j], rest[j+1:], true
}
