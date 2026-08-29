package steps

// Artifact-recording steps: the executable half of
// ../features/artifact-recording.feature.
//
// What a step leaves behind when it finishes — where its outputs are, who else
// has a copy, and what the next step is told to fetch.
//
// THE DOUBLES ARE REAL DAEMONS. Each one is an http.Server speaking the routes
// the ATC's storage layer actually calls: POST /register, POST /mirror,
// GET/HEAD /artifacts/ and HEAD /resource-caches/. Two named behavioural
// differences and no others: a node's storage tree is a map rather than a
// filesystem, and a mirror has landed on the peer by the time the request
// returns, where the real daemon schedules it and answers 202 first.
//
// They record NOTHING — no gotKey, no mirrorCount, no requests channel. That
// is the rule ../steps/daemon.go's header states, and it is why the ordering
// halves of two ginkgo tests are NOT here: see the DISPOSITION notes in the
// feature file. Every assertion below is on what a later fetch brought back,
// what the pod carries, or what the database holds.
//
// ../features/artifact-daemon.feature says in prose that asking a daemon to
// mirror has no scenario anywhere in brine, and names the two ways to close
// it: a double that records the keys it was asked for, or one that ACTUALLY
// MIRRORS so the copy can be fetched afterwards. It prefers the second, and
// notes that doing it honestly needs a PEER, because a real daemon mirrors to
// its peers rather than to itself. That is what is here — two servers, and the
// copy is fetched over HTTP from the second one.
//
// On the two backends. The worker builds pods through its own storage backend,
// which is unexported and unreachable from here; RecordOutputs and
// RegisterResourceCache are called on a backend this file constructs. The two
// share one *ArtifactLocator, which is not a workaround — the locator IS the
// shared state in production, written when a step finishes and read when the
// next pod is built. A scenario that records outputs and then builds the next
// step's pod is exercising exactly that hand-off.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// artifactStoreRoot is the hostPath the artifact daemon serves on every node.
const artifactStoreRoot = "/artifact-store"

// artifactDaemonService is the headless service the ATC discovers daemons
// through.
const artifactDaemonService = "artifact-daemon"

// artifactCacheLabelKey and artifactCacheLabelValue are what the daemon writes
// on its OWN node when it comes up — cmd/artifact-daemon's NodeLabeler, whose
// -label-key defaults to this key and whose value is always "ready".
//
// They are here so a node in this fixture is labelled the way a node with a
// running daemon is labelled, and nothing more. No check reads them to compare
// against the pod: the pod is held against the NODE, which is the only way to
// see a requirement that no node in the fleet can satisfy.
const (
	artifactCacheLabelKey   = "concourse.dev/artifact-cache"
	artifactCacheLabelValue = "ready"
)

// -----------------------------------------------------------------------
// The daemon
// -----------------------------------------------------------------------

// storeDaemon is one node's artifact daemon.
//
// disk is the node's storage tree, keyed the way the daemon keys it: a path
// relative to the storage root, so a step output lives at
// "steps/<handle>/<output>". aliases is the daemon's registry — the map POST
// /register writes and HEAD /resource-caches/ answers from. Keeping them apart
// is not decoration: it is what makes "the bytes are on this node" and "this
// node answers to this name" two different questions, which is the whole
// difference between the lookup paths below.
//
// The server outlives the scenario. daemon.go closes its server in every When
// step because every read there happens before the When returns; here the
// reads are in the Then steps — a check FETCHES from the peer — so there is no
// point at which closing is safe, and the resource plane cannot own a server a
// step created. Two listeners per scenario, released when the adapter exits.
type storeDaemon struct {
	mu      sync.Mutex
	root    string
	disk    map[string]string
	aliases map[string]string
	peers   []*storeDaemon

	server *httptest.Server
	host   string
	port   int
}

func newStoreDaemon(root string) (*storeDaemon, error) {
	d := &storeDaemon{
		root:    root,
		disk:    map[string]string{},
		aliases: map[string]string{},
	}
	d.server = httptest.NewServer(d.handler())
	host, port, err := hostAndPort(d.server)
	if err != nil {
		d.server.Close()
		return nil, err
	}
	d.host, d.port = host, port
	return d, nil
}

func (d *storeDaemon) put(key, content string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disk[key] = content
}

func (d *storeDaemon) registerAlias(key, target string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.aliases[key] = target
}

// contained turns the absolute local path the ATC sends into the key this
// daemon files data under, refusing anything outside its own storage root the
// way the real handler's containment check does.
func (d *storeDaemon) contained(localPath string) (string, bool) {
	prefix := d.root + "/"
	if !strings.HasPrefix(localPath, prefix) {
		return "", false
	}
	return strings.TrimPrefix(localPath, prefix), true
}

func (d *storeDaemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/register":
			d.serveRegister(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/mirror":
			d.serveMirror(w, r)
		case strings.HasPrefix(r.URL.Path, "/artifacts/"):
			d.serveArtifact(w, r, strings.TrimPrefix(r.URL.Path, "/artifacts/"))
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			d.serveCacheProbe(w, strings.TrimPrefix(r.URL.Path, "/resource-caches/"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// serveRegister mirrors the real handler's decisive property: a daemon whose
// node does not hold the path answers 404 rather than claiming the alias.
// Without that, every daemon would accept every registration and "which node
// can serve this" would stop meaning anything.
func (d *storeDaemon) serveRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key       string `json:"key"`
		LocalPath string `json:"local_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" || req.LocalPath == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rel, ok := d.contained(req.LocalPath)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, held := d.disk[rel]; !held {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	d.aliases[req.Key] = rel
	w.WriteHeader(http.StatusCreated)
}

// serveMirror copies what this node holds under steps/<key> onto its peers.
//
// A key this node does not hold is still a 202: the real daemon schedules the
// mirror and reports nothing about whether it found anything, which is what
// makes "the copy arrived" the only assertable outcome.
func (d *storeDaemon) serveMirror(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	content, held := d.disk["steps/"+req.Key]
	peers := append([]*storeDaemon(nil), d.peers...)
	d.mu.Unlock()

	if held {
		for _, peer := range peers {
			peer.put("steps/"+req.Key, content)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// serveArtifact answers for a path on disk first and for a registered alias
// second, which is the order the real daemon resolves in.
func (d *storeDaemon) serveArtifact(w http.ResponseWriter, r *http.Request, key string) {
	d.mu.Lock()
	content, held := d.disk[key]
	if !held {
		if target, aliased := d.aliases[key]; aliased {
			content, held = d.disk[target]
		}
	}
	d.mu.Unlock()

	if !held {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(content))
	}
}

// serveCacheProbe answers from the registry only. A daemon holding the bytes
// under some other path has not been told it is this cache, and says so.
func (d *storeDaemon) serveCacheProbe(w http.ResponseWriter, key string) {
	d.mu.Lock()
	target, aliased := d.aliases[key]
	_, held := d.disk[target]
	d.mu.Unlock()

	if !aliased || !held {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// fetch reads an artifact back out of this daemon over the wire, the way any
// consumer would.
func (d *storeDaemon) fetch(ctx context.Context, path string) (string, error) {
	url := fmt.Sprintf("http://%s:%d%s", d.host, d.port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request for %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", url, err)
	}
	return string(body), nil
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
type ArtifactCluster struct {
	Ctx       context.Context
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Backend   *jetbridge.DaemonSetBackend
	Locator   *jetbridge.ArtifactLocator
	DB        JetbridgeDB
	Team      db.Team
	WorkerRow db.Worker

	// Node is the daemon on the node that ran the step; Peer is a second
	// node's daemon, present only when a scenario asked for one.
	Node     *storeDaemon
	Peer     *storeDaemon
	NodeName string

	// The producing step under description.
	Handle  string
	Outputs map[string]string
	Volumes []*jetbridge.Volume

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
	Key  string
	Path string
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
	Clientset *fake.Clientset
	Ctx       context.Context
	// Caches are the paths the step asked to keep, so a check can find the
	// mount by the path the STEP named rather than by the volume-naming
	// convention the backend happens to use.
	Caches []string
}

// ArtifactLookup is what a lookup produced: the bytes, or the failure, or the
// database association initialising the volume as a resource cache wrote.
type ArtifactLookup struct {
	Content     string
	Association *db.UsedWorkerResourceCache
	Err         error
	Message     string
}

// -----------------------------------------------------------------------
// Preamble
// -----------------------------------------------------------------------

func applyArtifactConfig(cfg *jetbridge.Config, port int) {
	cfg.ArtifactDaemonHostPath = artifactStoreRoot
	cfg.ArtifactDaemonPort = port
	cfg.ArtifactDaemonService = artifactDaemonService
	cfg.ArtifactHelperImage = "alpine:latest"
}

// newArtifactCluster stands up the node, its daemon, and a worker wired to
// both.
//
// The daemon comes first because its port is the port the whole cluster is
// configured with: one DaemonSet, one containerPort, so the ATC reaches every
// daemon on the same one.
func newArtifactCluster(res brine.Resources) (ArtifactCluster, error) {
	node, err := newStoreDaemon(artifactStoreRoot)
	if err != nil {
		return ArtifactCluster{}, err
	}

	cluster, err := NewCluster(res,
		WithNamespace("test-namespace"),
		WithConfig(func(cfg *jetbridge.Config) { applyArtifactConfig(cfg, node.port) }),
		WithVolumeRepo(),
	)
	if err != nil {
		node.server.Close()
		return ArtifactCluster{}, err
	}

	ctx := cluster.Ctx
	nodeName := "node-1"
	if _, err := cluster.Clientset.CoreV1().Nodes().Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			// The daemon standing up above labels its own node ready. A node
			// in this cluster is a node with a daemon on it, so it carries
			// what that daemon would have written.
			Labels: map[string]string{artifactCacheLabelKey: artifactCacheLabelValue},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: node.host}},
		},
	}, metav1.CreateOptions{}); err != nil {
		return ArtifactCluster{}, fmt.Errorf("create node %q: %w", nodeName, err)
	}

	if _, err := cluster.Clientset.DiscoveryV1().
		EndpointSlices(cluster.Namespace).
		Create(ctx, &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      artifactDaemonService + "-brine",
				Namespace: cluster.Namespace,
				Labels:    map[string]string{discoveryv1.LabelServiceName: artifactDaemonService},
			},
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{node.host}}},
		}, metav1.CreateOptions{}); err != nil {
		return ArtifactCluster{}, fmt.Errorf("publish daemon endpoints: %w", err)
	}

	team, err := cluster.DB.TeamFactory.CreateTeam(atc.Team{Name: "artifact-recording"})
	if err != nil {
		return ArtifactCluster{}, fmt.Errorf("create team: %w", err)
	}

	cfg := jetbridge.NewConfig(cluster.Namespace, "")
	applyArtifactConfig(&cfg, node.port)

	locator := jetbridge.NewArtifactLocator()
	client := jetbridge.NewDaemonClient(
		lagertest.NewTestLogger("brine-artifact-recording"),
		cluster.Clientset, cluster.Namespace, artifactDaemonService, node.port, nil,
	)
	backend := jetbridge.NewDaemonSetBackend(cfg, locator, jetbridge.NewNodeIPResolver(cluster.Clientset))
	backend.SetDaemonClient(client)

	cluster.Worker.SetArtifactLocator(locator)
	cluster.Worker.SetDaemonClient(client)

	return ArtifactCluster{
		Ctx:       ctx,
		Namespace: cluster.Namespace,
		Worker:    cluster.Worker,
		Clientset: cluster.Clientset,
		Backend:   backend,
		Locator:   locator,
		DB:        cluster.DB,
		Team:      team,
		WorkerRow: cluster.DBWorker,
		Node:      node,
		NodeName:  nodeName,
		Outputs:   map[string]string{},
	}, nil
}

// outputMountPath is where an output of the described step is mounted. The
// name is what the daemon key is built from, so the two travel together.
func outputMountPath(name string) string {
	return "/tmp/build/" + name
}

func (c ArtifactCluster) producerSpec() runtime.ContainerSpec {
	outputs := runtime.OutputPaths{}
	for name, path := range c.Outputs {
		outputs[name] = path
	}
	return runtime.ContainerSpec{
		Dir:     "/tmp/build",
		Outputs: outputs,
		Type:    db.ContainerTypeTask,
	}
}

// -----------------------------------------------------------------------
// Steps
// -----------------------------------------------------------------------

// ArtifactRecordingDefinitions is the single entry point this file exports.
func ArtifactRecordingDefinitions() []brine.StepDefinition {
	defs := artifactClusterDefinitions()
	defs = append(defs, artifactRecordDefinitions()...)
	defs = append(defs, artifactPodDefinitions()...)
	defs = append(defs, artifactSchedulingDefinitions()...)
	defs = append(defs, artifactLookupDefinitions()...)
	return defs
}

func artifactClusterDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ArtifactCluster](
			"a jetbridge worker whose step outputs stay on the node that ran them",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ArtifactCluster, error) {
				return newArtifactCluster(res)
			},
		),

		// A peer is a whole second daemon, on its own address, because a
		// daemon mirrors to its peers and not to itself. It is deliberately
		// NOT published in the EndpointSlice: peer discovery is the daemon's
		// own business, and the ATC never needs to know the peers to ask for
		// a mirror.
		brine.DefineMap[ArtifactCluster, ArtifactCluster](
			"a second node whose daemon can hold mirrored copies",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				peer, err := newStoreDaemon(artifactStoreRoot)
				if err != nil {
					return ArtifactCluster{}, err
				}
				in.Node.mu.Lock()
				in.Node.peers = append(in.Node.peers, peer)
				in.Node.mu.Unlock()
				in.Peer = peer
				return in, nil
			},
		),

		Refine[ArtifactCluster]("the step {string} ran on node {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Handle, in.NodeName = a.String(0), a.String(1)
				return in
			}),

		// The output is on the node's disk before anything records it —
		// which is the real order of events: the step wrote it through the
		// hostPath mount, and only then did the process finish.
		Refine[ArtifactCluster]("its output {string} is the volume {string} holding {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				name, handle, content := a.String(0), a.String(1), a.String(2)
				in.Outputs[name] = outputMountPath(name)
				in.Volumes = append(in.Volumes,
					jetbridge.NewStubVolume(handle, in.WorkerRow.Name(), outputMountPath(name)))
				in.Node.put("steps/"+in.Handle+"/"+name, content)
				return in
			}),

		// A cache the daemon has been told about: the bytes sit under their
		// own path and the registry answers to the cache key. That split is
		// what a registered resource cache actually looks like — the alias
		// names a get step's output directory, not a directory called after
		// the cache.
		Refine[ArtifactCluster]("the node's daemon holds the resource cache {string} containing {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				key, content := a.String(0), a.String(1)
				in.Node.put("steps/cached/"+key, content)
				in.Node.registerAlias(key, "steps/cached/"+key)
				return in
			}),

		Refine[ArtifactCluster]("the worker already knows the cache {string} is on node {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Locator.Record(a.String(0), a.String(1), a.String(0))
				return in
			}),

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
			"the worker records where the step's outputs went",
			func(in ArtifactCluster, _ brine.Params, _ *brine.Recorder) (ArtifactCluster, error) {
				in.Backend.RecordOutputs(in.Ctx, in.Handle, in.NodeName, in.Volumes, in.producerSpec())
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
				// A get step's output volume is named after its container
				// with a "-dir" suffix, which is how the backend derives the
				// directory the cache actually lives in.
				in.Err = in.Backend.RegisterResourceCache(in.Ctx, key, "", in.Handle+"-dir", in.NodeName)
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
		CheckStringFor[ArtifactCluster]("the output {string} reads back as {string}",
			"the artifact's contents",
			func(in ArtifactCluster, handle string) (string, error) {
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
				return string(body), nil
			}),

		// Keeps its own body: three parameters, and the failure has to say
		// what losing the copy costs.
		brine.DefineCheck[ArtifactCluster](
			"the other node holds a copy of the output {string} containing {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an output name and its contents")
				}
				if in.Peer == nil {
					return fmt.Errorf("no second node was set up, so nothing could hold a copy")
				}
				key := in.Handle + "/" + name
				got, err := in.Peer.fetch(in.Ctx, "/artifacts/steps/"+key)
				if err != nil {
					return fmt.Errorf(
						"the second node has no copy of %q, so losing the node that produced it "+
							"loses the build's output and forces a rerun: %w", key, err)
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
					inputs = append(inputs, runtime.Input{
						Artifact:        jetbridge.NewStubVolume(input.Key, in.WorkerRow.Name(), input.Path),
						DestinationPath: input.Path,
					})
				}

				spec := runtime.ContainerSpec{
					TeamID:    in.Team.ID(),
					Dir:       "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					Inputs:    inputs,
					Caches:    in.Caches,
					Type:      in.ConsumerType,
				}

				owner := db.NewFixedHandleContainerOwner(in.Consumer)
				metadata := db.ContainerMetadata{Type: in.ConsumerType}

				// A step whose container row already exists is a RETRY, and
				// only a retry's pod clears the workspace its last attempt
				// left. Creating the row is how production gets there too:
				// the second FindOrCreateContainer finds the first one.
				if in.RanBefore {
					if _, _, err := in.Worker.FindOrCreateContainer(
						in.Ctx, owner, metadata, spec, &noopDelegate{},
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
					&noopDelegate{},
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
					if v.HostPath != nil && v.HostPath.Path == artifactStoreRoot {
						return fmt.Errorf(
							"the pod for %q mounts %q as volume %q, which is every step's outputs on "+
								"this node — a check would get read and write access to work it has "+
								"nothing to do with", in.Handle, artifactStoreRoot, v.Name)
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
					if v.HostPath != nil && v.HostPath.Path == artifactStoreRoot {
						return nil
					}
				}
				return fmt.Errorf(
					"the pod for %q does not mount %q, so its init containers have nowhere to put the "+
						"inputs they fetch", in.Handle, artifactStoreRoot)
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

		Refine[ArtifactCluster]("the worker already knows the artifact {string} is on node {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				handle, node := a.String(0), a.String(1)
				in.Locator.Record(jetbridge.ArtifactKey(handle), node, handle)
				return in
			}),

		Refine[ArtifactCluster]("it keeps a task cache at {string}",
			func(in ArtifactCluster, a Args) ArtifactCluster {
				in.Caches = append(in.Caches, a.String(0))
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
				stepsTree := artifactStoreRoot + "/steps/"
				cachesTree := artifactStoreRoot + "/caches/"
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
	if in.Pod == nil {
		return "", fmt.Errorf("no pod was created")
	}
	name := ""
	for _, c := range in.Pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.MountPath == mountPath {
				name = m.Name
			}
		}
	}
	if name == "" {
		return "", fmt.Errorf(
			"the pod for %q mounts nothing at %q, so the step cannot see it at all",
			in.Handle, mountPath)
	}
	for _, v := range in.Pod.Spec.Volumes {
		if v.Name != name {
			continue
		}
		if v.HostPath == nil {
			return "", fmt.Errorf(
				"what the pod for %q gives the step at %q is not a directory on the node, so it "+
					"is lost with the pod and cannot be a cache at all", in.Handle, mountPath)
		}
		return v.HostPath.Path, nil
	}
	return "", fmt.Errorf(
		"the pod for %q mounts volume %q at %q but declares no such volume",
		in.Handle, name, mountPath)
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
		if v.HostPath != nil && v.HostPath.Path == artifactStoreRoot {
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
	return []brine.StepDefinition{

		brine.DefineMap[ArtifactCluster, ArtifactLookup](
			"the worker looks up the volume {string}",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactLookup, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ArtifactLookup{}, fmt.Errorf("expected a volume handle parameter")
				}
				vol, err := lookupVolume(in, handle)
				if err != nil {
					return ArtifactLookup{Err: err, Message: err.Error()}, nil
				}
				stream, err := vol.StreamOut(in.Ctx, ".", nil)
				if err != nil {
					return ArtifactLookup{Err: err, Message: err.Error()}, nil
				}
				defer stream.Close()
				body, err := io.ReadAll(stream)
				if err != nil {
					return ArtifactLookup{Err: err, Message: err.Error()}, nil
				}
				return ArtifactLookup{Content: string(body)}, nil
			},
		),

		brine.DefineMap[ArtifactCluster, ArtifactLookup](
			"the worker looks up the volume {string} and initialises it as a resource cache",
			func(in ArtifactCluster, p brine.Params, _ *brine.Recorder) (ArtifactLookup, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ArtifactLookup{}, fmt.Errorf("expected a volume handle parameter")
				}
				cache, err := oneResourceCache(in)
				if err != nil {
					return ArtifactLookup{}, err
				}
				vol, err := lookupVolume(in, handle)
				if err != nil {
					return ArtifactLookup{Err: err, Message: err.Error()}, nil
				}
				association, err := vol.InitializeResourceCache(in.Ctx, cache)
				out := ArtifactLookup{Association: association}
				if err != nil {
					out.Err, out.Message = err, err.Error()
				}
				return out, nil
			},
		),

		CheckString[ArtifactLookup]("the artifact comes back as {string}",
			"the artifact's contents",
			func(in ArtifactLookup) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the lookup could not read the artifact: %s", in.Message)
				}
				return in.Content, nil
			}),

		CheckThat[ArtifactLookup]("the cache is recorded against the worker in the database",
			func(in ArtifactLookup) error {
				if in.Err != nil {
					return fmt.Errorf("initialising the cache failed: %s", in.Message)
				}
				if in.Association == nil {
					return fmt.Errorf(
						"nothing was written associating this worker with the resource cache. The " +
							"volume that came back carries no database row, so InitializeResourceCache " +
							"silently did nothing and the next build re-runs the get step")
				}
				return nil
			}),
	}
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
	if in.Pod == nil {
		return "", fmt.Errorf("no pod was created")
	}
	for _, c := range in.Pod.Spec.InitContainers {
		if c.Name == "fetch-inputs" {
			return strings.Join(c.Command, " "), nil
		}
	}
	return "", fmt.Errorf(
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
	for _, term := range terms {
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" && len(expr.Values) > 0 {
				return expr.Values[0], nil
			}
		}
	}
	return "", fmt.Errorf(
		"the pod for %q asks for no particular node, so the scheduler is free to put it anywhere "+
			"and the fetch of its inputs crosses the network instead of reading the local disk",
		in.Handle)
}
