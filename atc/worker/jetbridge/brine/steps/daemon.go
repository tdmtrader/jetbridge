package steps

// Artifact-daemon steps: the executable half of ../features/artifact-daemon.feature.
//
// These scenarios drive the REAL artifact daemon — the binary built from
// cmd/artifact-daemon, run as a process with its own storage root on a free
// port (see realdaemon.go for how, and for why it is started in a Given rather
// than owned by the resource plane). What the daemon holds is what a step's
// output actually is: files on a node's disk, plus the POST /register alias
// the ATC writes for the outputs it produced.
//
// It used to be a hand-written http.Server answering the daemon's routes out
// of a map. That double was honest about being one and it recorded nothing —
// no gotPath, no gotMethod, no probeHits — and for asking what the ATC does
// with an answer it was the right tool. What it could not do is tell you the
// answer was RIGHT, and three of the sentences below turned out to assert its
// implementation rather than the daemon's:
//
//   - "the archive holds X containing Y" read back a tar the double had been
//     handed pre-built by tarOfMembers. Nothing said that the daemon, asked
//     for a directory, produces a tar whose members carry their relative
//     paths — so "ci/task.yml" was a name the FIXTURE chose. It is now a name
//     the daemon's own walk produced.
//   - "it holds the resource cache X" and "it holds the artifact X" wrote into
//     two different maps, so a probe could name a daemon that could not serve
//     the bytes and the scenario would still pass. On the real daemon both are
//     one POST /register: the route the probe answers from and the route the
//     fetch reads from resolve the SAME alias. That is what makes "fetchable
//     from the daemon the probe names" mean anything, and it is why those two
//     sentences are now one.
//   - the mirrored copy the peer-fallback scenarios read was a map entry under
//     a "steps/" string key. It is now a directory under steps/ on disk, which
//     is where PUT /stream-in extracts a mirror — so a regression in the
//     daemon's filesystem branch fails these scenarios instead of passing
//     them.
//
// A divergence the double taught wrongly, now gone: it advertised the
// durable-tier header on EVERY route. The daemon advertises it from
// /resource-caches only (advertiseDurableTier is called by the two
// resource-cache handlers and nowhere else). No scenario asserted the
// difference, but a reader would have taken the double's version for the
// contract; the durable scenarios now learn the capability from the route that
// really carries it, and would fail if that route stopped carrying it.
//
// ONE double survives, for one scenario, and the scenario says why in the
// feature file: a daemon that answers /resolve while holding nothing locally
// cannot be built from the real binary. Peer-served resolve needs DAEMON-side
// peer discovery, which main.go builds only from rest.InClusterConfig() and
// cannot be pointed anywhere outside a cluster. A real daemon with no peers
// simply misses — which reproduces the wire signature of that scenario and not
// its situation, and loses the regression it exists to catch.
//
// The domain-state structs are declared here rather than in domain.go so this
// family can be read, and reviewed, as one file.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// -----------------------------------------------------------------------
// Domain states
// -----------------------------------------------------------------------

// DaemonPlan is a cluster with an artifact daemon in it, under description.
// Steps take DaemonPlan in and out — the live state's type does not change —
// so a scenario may say what the daemon holds, who produced the artifact and
// what the consumer is asking for in any order.
type DaemonPlan struct {
	Ctx       context.Context
	Namespace string
	Service   string

	// Port is the single port the ATC reaches every daemon on, because that is
	// how the deployment works: one DaemonSet, one containerPort. It is the
	// running daemon's real port, so requests really are dialled.
	Port     int
	DaemonIP string

	// Root is the daemon's storage path — the node's disk. Empty when the
	// scenario has no daemon at all.
	Root string

	// IPs is what the EndpointSlice publishes. It may name addresses that
	// nothing answers on.
	IPs   []string
	Nodes map[string]string

	SourceNode string
	KnownIP    string
	UseKnownIP bool
	Fallback   bool

	SubPath string
	Gzip    bool
}

// DaemonFetch is what a consumer got when it read an artifact — or what it was
// told instead. The error is a value so failure is assertable rather than
// fatal to the scenario.
type DaemonFetch struct {
	Raw     []byte
	Gzipped bool
	IsTar   bool
	Files   map[string]string
	Entries int

	Handle string
	Worker string

	Err     error
	Message string
}

// ProbeOutcome is everything a probe told its caller. Note what it does NOT
// carry: how many requests went out, or where they went.
type ProbeOutcome struct {
	Found          bool
	IP             string
	DurableCapable bool
	EndpointCount  int
}

// -----------------------------------------------------------------------
// Putting artifacts on the daemon's disk
// -----------------------------------------------------------------------

// Two locations, because the ATC reads through two different routes and one
// daemon here has to stand in for both ends of them.
//
//   - A copy the PRODUCER holds is reached at /artifacts/{key}, which resolves
//     through the registry: the ATC registers the volume key against the
//     step's output directory, and the daemon finds nothing at that key on
//     disk and falls back to the alias. producedPath is where such an output
//     sits. On a real cluster it would be steps/{handle}/{output} on the
//     producer's node — it is a subtree of its own here only because the same
//     process is also playing the peer, whose copy is at the path below.
//
//   - A copy a PEER holds is reached at /artifacts/steps/{key} and read
//     straight off the disk, because a peer receives mirrored data through PUT
//     /stream-in — which extracts to steps/{key} — and never gets the
//     producer-side alias.
//
// Giving the two copies different contents is what lets a scenario say which
// of the two routes a consumer actually read.

func (p DaemonPlan) producedPath(key string) string {
	return filepath.Join(p.Root, "produced", key)
}

func (p DaemonPlan) mirrorPath(key string) string {
	return filepath.Join(p.Root, "steps", key)
}

func (p DaemonPlan) baseURL() string {
	return fmt.Sprintf("http://%s:%d", p.DaemonIP, p.Port)
}

// writeArtifactFile writes one file, creating the directories above it.
func writeArtifactFile(full, content string) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create %q: %w", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", full, err)
	}
	return nil
}

// register is the ATC's own move: POST /register, mapping a key to a path the
// daemon can already see on its disk. The daemon refuses a key whose path is
// not there, so a fixture that writes nothing cannot pretend to hold anything.
func (p DaemonPlan) register(key, path string) error {
	body := fmt.Sprintf("{\"key\":%q,\"local_path\":%q}", key, path)
	resp, err := http.Post(p.baseURL()+"/register", "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("register %q with the daemon: %w", key, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("the daemon refused to register %q at %q: %d %s",
			key, path, resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	return nil
}

// requireDaemon reports the missing daemon in the scenario's own terms rather
// than as a path error three steps later.
func (p DaemonPlan) requireDaemon(what string) error {
	if p.Root == "" {
		return fmt.Errorf("there is no artifact daemon for %s", what)
	}
	return nil
}

// -----------------------------------------------------------------------
// Wiring the ATC side up from the plan
// -----------------------------------------------------------------------

// cluster builds the fake Kubernetes the ATC discovers daemons and nodes
// through. fake.Clientset is a real implementation of the client interface
// whose behavioral property is deterministic delivery; it is not the subject
// of any assertion here. It is also the only half of discovery that can be
// faked from outside a cluster — the daemon's own peer discovery cannot, which
// is what keeps one scenario on a double.
func (p DaemonPlan) cluster() (*fake.Clientset, error) {
	cs := fake.NewSimpleClientset()

	endpoints := make([]discoveryv1.Endpoint, 0, len(p.IPs))
	for _, ip := range p.IPs {
		endpoints = append(endpoints, discoveryv1.Endpoint{Addresses: []string{ip}})
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Service + "-brine",
			Namespace: p.Namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: p.Service},
		},
		Endpoints: endpoints,
	}
	if _, err := cs.DiscoveryV1().EndpointSlices(p.Namespace).Create(p.Ctx, slice, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("publish daemon endpoints: %w", err)
	}

	for name, ip := range p.Nodes {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
			},
		}
		if _, err := cs.CoreV1().Nodes().Create(p.Ctx, node, metav1.CreateOptions{}); err != nil {
			return nil, fmt.Errorf("create node %q: %w", name, err)
		}
	}

	return cs, nil
}

func (p DaemonPlan) config() jetbridge.Config {
	return jetbridge.Config{Namespace: p.Namespace, ArtifactDaemonPort: p.Port}
}

func (p DaemonPlan) daemonClient(cs *fake.Clientset) *jetbridge.DaemonClient {
	return jetbridge.NewDaemonClient(
		lagertest.NewTestLogger("brine-daemon"),
		cs, p.Namespace, p.Service, p.Port, nil,
	)
}

// volume builds the volume the way the runtime builds it: from a known daemon
// address after a cache probe, or from the node the artifact was recorded on.
func (p DaemonPlan) volume(key string, cs *fake.Clientset) *jetbridge.DaemonSetVolume {
	var vol *jetbridge.DaemonSetVolume
	if p.UseKnownIP {
		vol = jetbridge.NewDaemonSetVolumeFromIP(key, key, "k8s-worker-1", p.KnownIP, p.config())
	} else {
		vol = jetbridge.NewDaemonSetVolume(
			key, key, "k8s-worker-1", nil, p.SourceNode,
			p.config(), jetbridge.NewNodeIPResolver(cs),
		)
	}
	if p.Fallback {
		vol.SetDaemonClient(p.daemonClient(cs))
	}
	return vol
}

// read is the one consumer action every volume scenario ends in: ask for the
// artifact, drain what comes back, and put the outcome — bytes or error — into
// a state a check step can read.
func (p DaemonPlan) read(vol *jetbridge.DaemonSetVolume) DaemonFetch {
	out := DaemonFetch{Handle: vol.Handle(), Worker: vol.Source()}

	path := p.SubPath
	if path == "" {
		path = "."
	}
	var enc compression.Compression
	if p.Gzip {
		enc = compression.NewGzipCompression()
	}

	stream, err := vol.StreamOut(p.Ctx, path, enc)
	if err != nil {
		out.Err, out.Message = err, err.Error()
		return out
	}
	raw, readErr := io.ReadAll(stream)
	_ = stream.Close()
	if readErr != nil {
		out.Err, out.Message = readErr, readErr.Error()
		return out
	}

	out.Raw = raw
	out.Gzipped, out.IsTar, out.Files, out.Entries = decodeArchive(raw)
	return out
}

// -----------------------------------------------------------------------
// Starting one
// -----------------------------------------------------------------------

// startDaemonPlan runs a daemon and describes the cluster it is the only
// member of. Its kill goes on the Recorder, which drains at scenario end on
// pass, on failure and on SIGTERM; it is deliberately NOT a brine resource,
// because every ScopeScenario resource is acquired for EVERY scenario in the
// suite — measured at 70 seconds when the daemon was wired that way.
func startDaemonPlan(rec *brine.Recorder, extraArgs ...string) (DaemonPlan, error) {
	d, err := startRealDaemon(extraArgs...)
	if err != nil {
		return DaemonPlan{}, err
	}
	rec.RegisterDisposer(func() { _ = d.stop() })

	host, port, err := hostPortOfURL(d.URL)
	if err != nil {
		return DaemonPlan{}, err
	}

	return DaemonPlan{
		Ctx:       context.Background(),
		Namespace: "cicd",
		Service:   "artifact-daemon",
		Port:      port,
		DaemonIP:  host,
		Root:      d.Root,
		IPs:       []string{host},
		Nodes:     map[string]string{},
	}, nil
}

// -----------------------------------------------------------------------
// Steps
// -----------------------------------------------------------------------

// DaemonDefinitions expresses the artifact daemon's contract as artifact
// arrival. Nothing here names a URL, a verb, or a request count.
func DaemonDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, DaemonPlan](
			"an artifact daemon",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (DaemonPlan, error) {
				return startDaemonPlan(rec)
			},
		),

		// A separate Given rather than a refinement of the one above, because
		// the durable tier is a boot flag: --durable-store decides it before
		// the first request, and a running daemon cannot acquire one. The
		// store is a directory of its own so the node's storage root stays
		// what the node holds.
		brine.DefineMap[brine.Empty, DaemonPlan](
			"an artifact daemon with a durable tier",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (DaemonPlan, error) {
				store, err := os.MkdirTemp("", "brine-durable-store-*")
				if err != nil {
					return DaemonPlan{}, fmt.Errorf("create durable store: %w", err)
				}
				rec.RegisterDisposer(func() { _ = os.RemoveAll(store) })
				return startDaemonPlan(rec, "--durable-store=filesystem", "--durable-path", store)
			},
		),

		brine.DefineMap[brine.Empty, DaemonPlan](
			"a cluster with no artifact daemons",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				return DaemonPlan{
					Ctx:       context.Background(),
					Namespace: "cicd",
					Service:   "artifact-daemon",
					Port:      7780,
					Nodes:     map[string]string{},
				}, nil
			},
		),

		// THE ONE DOUBLE LEFT IN THIS FILE, and the reason is in the scenario
		// that uses it: the real binary cannot be made to answer /resolve
		// while holding nothing locally, because a peer-served resolve needs
		// the daemon's own EndpointSlice discovery and that is built from
		// rest.InClusterConfig() alone. A real daemon with no peers misses on
		// /resolve too, which would reproduce the wire signature of the
		// scenario and not its situation.
		//
		// It answers /resolve enthusiastically and 404s everything else,
		// including the HEAD /resource-caches/ the probe actually sends. A hit
		// therefore means one thing: the probe fell back to /resolve again.
		brine.DefineMap[brine.Empty, DaemonPlan](
			"a daemon that answers resolve requests but holds nothing locally",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (DaemonPlan, error) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/resolve" || r.URL.Path == "/resolve-batch" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"status":"ok","method":"registry"}`))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				rec.RegisterDisposer(server.Close)

				host, port, err := hostAndPort(server)
				if err != nil {
					return DaemonPlan{}, err
				}
				return DaemonPlan{
					Ctx:       context.Background(),
					Namespace: "cicd",
					Service:   "artifact-daemon",
					Port:      port,
					DaemonIP:  host,
					IPs:       []string{host},
					Nodes:     map[string]string{},
				}, nil
			},
		),

		// --- what the daemon holds ---

		// A flat artifact file, which the daemon serves back byte for byte.
		// This is the shape that makes "the artifact arrives as ..." an
		// assertion about pass-through rather than about tar.
		brine.DefineMap[DaemonPlan, DaemonPlan](
			"it holds the artifact {string} containing {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				key, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return DaemonPlan{}, fmt.Errorf("expected an artifact key and its contents")
				}
				if err := in.requireDaemon("the artifact " + key); err != nil {
					return DaemonPlan{}, err
				}
				path := in.producedPath(key)
				if err := writeArtifactFile(path, content); err != nil {
					return DaemonPlan{}, err
				}
				return in, in.register(key, path)
			},
		),

		// A step's output as it really is: a directory. The daemon tars it on
		// the way out, so the member names in the answer are the daemon's own.
		brine.DefineMap[DaemonPlan, DaemonPlan](
			"it holds the artifact {string} containing the file {string} with {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				return addProducedFile(in, p)
			},
		),

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"the artifact {string} also contains the file {string} with {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				return addProducedFile(in, p)
			},
		),

		// A copy a peer received by mirror: on its disk under steps/, with no
		// alias, because a peer never gets the producer's registration.
		brine.DefineMap[DaemonPlan, DaemonPlan](
			"it holds a mirrored copy of the artifact {string} containing the file {string} with {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				key, _ := p.GetString(0)
				name, _ := p.GetString(1)
				content, ok := p.GetString(2)
				if !ok {
					return DaemonPlan{}, fmt.Errorf("expected an artifact key, a file name and its contents")
				}
				if err := in.requireDaemon("the mirrored copy of " + key); err != nil {
					return DaemonPlan{}, err
				}
				return in, writeArtifactFile(filepath.Join(in.mirrorPath(key), name), content)
			},
		),

		// --- the shape of the cluster ---

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"the daemon address is published twice",
			func(in DaemonPlan, _ brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				if in.DaemonIP == "" {
					return DaemonPlan{}, fmt.Errorf("there is no daemon address to publish")
				}
				in.IPs = append(append([]string{}, in.IPs...), in.DaemonIP)
				return in, nil
			},
		),

		// 203.0.113.99 is TEST-NET-3: reserved for documentation, routed
		// nowhere. It behaves exactly like a daemon pod that has stopped
		// answering, which is the point.
		Refine[DaemonPlan]("a second daemon address that never answers",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.IPs = append(append([]string{}, in.IPs...), "203.0.113.99")
				return in
			}),

		// --- what the ATC remembers about where the artifact came from ---

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"the artifact was produced on node {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				node, ok := p.GetString(0)
				if !ok {
					return DaemonPlan{}, fmt.Errorf("expected a node name parameter")
				}
				if in.DaemonIP == "" {
					return DaemonPlan{}, fmt.Errorf("no daemon is running for node %q to point at", node)
				}
				nodes := map[string]string{}
				for k, v := range in.Nodes {
					nodes[k] = v
				}
				nodes[node] = in.DaemonIP
				in.Nodes = nodes
				in.SourceNode = node
				return in, nil
			},
		),

		// The node is still recorded against the artifact; it is simply not in
		// the cluster any more. Spot preemption, a crash, a drain. The daemon
		// process stays up: it is the peer now, and the only thing that
		// disappeared is the ATC's way of resolving the producer's address.
		Refine[DaemonPlan]("the node that produced the artifact has left the cluster",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.SourceNode = "node-1"
				in.Nodes = map[string]string{}
				return in
			}),

		// A web restart wipes the in-memory locator, so artifacts wrapped
		// afterwards carry no producing node at all.
		Refine[DaemonPlan]("no producing node was ever recorded",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.SourceNode = ""
				return in
			}),

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"the ATC already knows the daemon address",
			func(in DaemonPlan, _ brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				if in.DaemonIP == "" {
					return DaemonPlan{}, fmt.Errorf("there is no daemon address to know")
				}
				in.UseKnownIP, in.KnownIP = true, in.DaemonIP
				return in, nil
			},
		),

		Refine[DaemonPlan]("the ATC recorded an empty daemon address",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.UseKnownIP, in.KnownIP = true, ""
				return in
			}),

		Refine[DaemonPlan]("the ATC can fall back to other daemons",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.Fallback = true
				return in
			}),

		// --- what the consumer is asking for ---

		Refine[DaemonPlan]("the consumer asks for the sub-path {string}",
			func(in DaemonPlan, a Args) DaemonPlan {
				in.SubPath = a.String(0)
				return in
			}),

		Refine[DaemonPlan]("the consumer asks for a gzip-compressed stream",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.Gzip = true
				return in
			}),

		// --- reading ---

		brine.DefineMap[DaemonPlan, DaemonFetch](
			"a consumer reads the artifact {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonFetch, error) {
				key, ok := p.GetString(0)
				if !ok {
					return DaemonFetch{}, fmt.Errorf("expected an artifact key parameter")
				}
				cs, err := in.cluster()
				if err != nil {
					return DaemonFetch{}, err
				}
				return in.read(in.volume(key, cs)), nil
			},
		),

		// The production path for a resource-cache hit, end to end: probe,
		// bind a volume to whatever the probe named, stream. A probe that
		// returns an address nothing can be fetched from fails here.
		brine.DefineMap[DaemonPlan, DaemonFetch](
			"a consumer fetches the resource cache {string} from wherever the probe finds it",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonFetch, error) {
				key, ok := p.GetString(0)
				if !ok {
					return DaemonFetch{}, fmt.Errorf("expected a cache key parameter")
				}
				cs, err := in.cluster()
				if err != nil {
					return DaemonFetch{}, err
				}

				probe, found := in.daemonClient(cs).ProbeResourceCache(in.Ctx, key)
				if !found {
					return DaemonFetch{
						Err:     fmt.Errorf("no daemon reported holding the resource cache %q", key),
						Message: fmt.Sprintf("no daemon reported holding the resource cache %q", key),
					}, nil
				}

				bound := in
				bound.UseKnownIP, bound.KnownIP, bound.Fallback = true, probe.IP, false
				return bound.read(bound.volume(key, cs)), nil
			},
		),

		// --- probing ---

		brine.DefineMap[DaemonPlan, ProbeOutcome](
			"the ATC probes for the resource cache {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (ProbeOutcome, error) {
				key, ok := p.GetString(0)
				if !ok {
					return ProbeOutcome{}, fmt.Errorf("expected a cache key parameter")
				}
				cs, err := in.cluster()
				if err != nil {
					return ProbeOutcome{}, err
				}

				probe, found := in.daemonClient(cs).ProbeResourceCache(in.Ctx, key)
				return ProbeOutcome{
					Found:          found,
					IP:             probe.IP,
					DurableCapable: probe.DurableCapable,
					EndpointCount:  len(probe.Endpoints),
				}, nil
			},
		),

		brine.DefineMap[DaemonPlan, ProbeOutcome](
			"the ATC probes for a mirrored copy of {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (ProbeOutcome, error) {
				key, ok := p.GetString(0)
				if !ok {
					return ProbeOutcome{}, fmt.Errorf("expected an artifact key parameter")
				}
				cs, err := in.cluster()
				if err != nil {
					return ProbeOutcome{}, err
				}

				ip, found, probeErr := in.daemonClient(cs).ProbeStepArtifact(in.Ctx, key)
				if probeErr != nil {
					return ProbeOutcome{}, fmt.Errorf("probe for %q: %w", key, probeErr)
				}
				return ProbeOutcome{Found: found, IP: ip}, nil
			},
		),

		// -------------------------------------------------------------------
		// Checks
		// -------------------------------------------------------------------

		CheckString[DaemonFetch]("the artifact arrives as {string}",
			"the artifact",
			func(in DaemonFetch) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the read failed: %s", in.Message)
				}
				return string(in.Raw), nil
			}),

		CheckThat[DaemonFetch]("the read succeeds",
			func(in DaemonFetch) error {
				if in.Err != nil {
					return fmt.Errorf("expected the read to succeed, got %q", in.Message)
				}
				return nil
			}),

		// These two match the message case-insensitively. CheckContains is
		// case-sensitive, so they keep their own bodies rather than quietly
		// narrowing what a scenario is allowed to say — and the second is not
		// a CheckNotMember either: it is a substring of one message, folded,
		// not equality against a member of a collection.
		brine.DefineCheck[DaemonFetch](
			"the read fails saying {string}",
			func(in DaemonFetch, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected a failure mentioning %q, but the read succeeded with %d bytes", want, len(in.Raw))
				}
				if !containsFold(in.Message, want) {
					return fmt.Errorf("expected the failure to mention %q, got %q", want, in.Message)
				}
				return nil
			},
		),

		brine.DefineCheck[DaemonFetch](
			"the read does not fail saying {string}",
			func(in DaemonFetch, p brine.Params, _ *brine.Recorder) error {
				unwanted, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected the read to have failed, but it succeeded")
				}
				if containsFold(in.Message, unwanted) {
					return fmt.Errorf("expected the failure not to mention %q, got %q", unwanted, in.Message)
				}
				return nil
			},
		),

		// Two fields in one sentence: the combinators compare one value, and
		// this has to fail on either half, so it keeps its own body.
		brine.DefineCheck[DaemonFetch](
			"the volume reports the handle {string} on worker {string}",
			func(in DaemonFetch, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				worker, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a worker name")
				}
				if in.Handle != handle {
					return fmt.Errorf("expected handle %q, got %q", handle, in.Handle)
				}
				if in.Worker != worker {
					return fmt.Errorf("expected worker %q, got %q", worker, in.Worker)
				}
				return nil
			},
		),

		CheckThat[DaemonFetch]("the stream is gzip compressed",
			func(in DaemonFetch) error {
				if in.Err != nil {
					return fmt.Errorf("the read failed: %s", in.Message)
				}
				if !in.Gzipped {
					return fmt.Errorf("expected a gzip-compressed stream, got %d bytes that are not gzip", len(in.Raw))
				}
				return nil
			}),

		// "Not compressed" is only worth anything if the bytes are usable as
		// they stand, so this asserts both halves: not gzip, and a tar the
		// consumer can read directly.
		CheckThat[DaemonFetch]("the stream is not compressed",
			func(in DaemonFetch) error {
				if in.Err != nil {
					return fmt.Errorf("the read failed: %s", in.Message)
				}
				if in.Gzipped {
					return fmt.Errorf("expected uncompressed bytes, got a gzip stream")
				}
				if !in.IsTar {
					return fmt.Errorf("expected the bytes to be a tar the consumer can read directly, got %d bytes that are not", len(in.Raw))
				}
				return nil
			}),

		// The named file is the key, its contents the expectation. The getter
		// keeps the original's not-found error, which names every entry that IS
		// in the archive — the one thing worth seeing when a name misses.
		CheckStringFor[DaemonFetch]("the archive holds {string} containing {string}",
			"the archive entry",
			func(in DaemonFetch, name string) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the read failed: %s", in.Message)
				}
				if !in.IsTar {
					return "", fmt.Errorf("expected an archive, got %d bytes that do not parse as tar", len(in.Raw))
				}
				got, found := in.Files[name]
				if !found {
					names := make([]string, 0, len(in.Files))
					for n := range in.Files {
						names = append(names, n)
					}
					return "", fmt.Errorf("expected %q in the archive, found %v", name, names)
				}
				return got, nil
			}),

		CheckThat[DaemonFetch]("the archive holds that entry and nothing else",
			func(in DaemonFetch) error {
				if in.Err != nil {
					return fmt.Errorf("the read failed: %s", in.Message)
				}
				if in.Entries != 1 {
					return fmt.Errorf("expected exactly one entry in the archive, got %d: %v", in.Entries, in.Files)
				}
				return nil
			}),

		// Not CheckCount: the number being asserted is the tar's entry count,
		// and Files is keyed by name, so a repeated name would make len(Files)
		// smaller than the archive really is. The count stays the one the
		// decoder reported; the detail carries the entries the original
		// message listed, which is what a miscount is diagnosed from.
		CheckInt[DaemonFetch]("the archive holds exactly {int} entries",
			"the number of entries in the archive",
			func(in DaemonFetch) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("the read failed: %s", in.Message)
				}
				if !in.IsTar {
					return 0, fmt.Errorf("expected an archive, got %d bytes that do not parse as tar", len(in.Raw))
				}
				return in.Entries, nil
			},
			func(in DaemonFetch) string { return fmt.Sprintf("entries: %v", in.Files) }),

		CheckThat[DaemonFetch]("the archive is empty",
			func(in DaemonFetch) error {
				if in.Err != nil {
					return fmt.Errorf("the read failed: %s", in.Message)
				}
				if len(in.Raw) == 0 {
					return fmt.Errorf("expected an empty archive, got no bytes at all")
				}
				if !in.IsTar {
					return fmt.Errorf("expected an empty archive, got %d bytes that do not parse as tar", len(in.Raw))
				}
				if in.Entries != 0 {
					return fmt.Errorf("expected an empty archive, got %d entries: %v", in.Entries, in.Files)
				}
				return nil
			}),

		CheckThat[ProbeOutcome]("the probe reports a miss",
			func(in ProbeOutcome) error {
				if in.Found {
					return fmt.Errorf("expected a miss, but a daemon at %q reported holding it", in.IP)
				}
				return nil
			}),

		CheckThat[ProbeOutcome]("the probe names no daemon",
			func(in ProbeOutcome) error {
				if in.IP != "" {
					return fmt.Errorf("expected no daemon address, got %q", in.IP)
				}
				return nil
			}),

		CheckThat[ProbeOutcome]("the daemon is known to have a durable tier",
			func(in ProbeOutcome) error {
				if !in.DurableCapable {
					return fmt.Errorf("the probe did not learn the daemon has a durable tier")
				}
				return nil
			}),

		CheckThat[ProbeOutcome]("the daemon is not known to have a durable tier",
			func(in ProbeOutcome) error {
				if in.DurableCapable {
					return fmt.Errorf("the probe credited a durable tier to a daemon that never advertised one")
				}
				return nil
			}),

		CheckInt[ProbeOutcome]("the probe carries back {int} daemon addresses",
			"the number of daemon addresses carried back",
			func(in ProbeOutcome) (int, error) {
				return in.EndpointCount, nil
			}),
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// addProducedFile puts one more file in a produced artifact's directory and
// (re-)registers the alias. Registering after every file rather than once is
// deliberate: POST /register refuses a path that is not on disk, so the
// registration has to follow the write, and RegisterAlias is idempotent.
func addProducedFile(in DaemonPlan, p brine.Params) (DaemonPlan, error) {
	key, _ := p.GetString(0)
	name, _ := p.GetString(1)
	content, ok := p.GetString(2)
	if !ok {
		return DaemonPlan{}, fmt.Errorf("expected an artifact key, a file name and its contents")
	}
	if err := in.requireDaemon("the artifact " + key); err != nil {
		return DaemonPlan{}, err
	}

	dir := in.producedPath(key)
	if err := writeArtifactFile(filepath.Join(dir, name), content); err != nil {
		return DaemonPlan{}, err
	}
	return in, in.register(key, dir)
}

// decodeArchive reads what arrived the way a consumer would: gunzip if it is
// gzip, then unpack if it is a tar. Anything else comes back as-is, which is
// what makes "the artifact arrives as ..." able to talk about plain bytes.
func decodeArchive(raw []byte) (gzipped bool, isTar bool, files map[string]string, entries int) {
	payload := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gzipped = true
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return true, false, nil, 0
		}
		defer gr.Close()
		out, err := io.ReadAll(gr)
		if err != nil {
			return true, false, nil, 0
		}
		payload = out
	}

	files = map[string]string{}
	tr := tar.NewReader(bytes.NewReader(payload))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return gzipped, true, files, entries
		}
		if err != nil {
			return gzipped, false, nil, 0
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return gzipped, false, nil, 0
		}
		files[hdr.Name] = string(body)
		entries++
	}
}

func hostAndPort(server *httptest.Server) (string, int, error) {
	host, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		return "", 0, fmt.Errorf("split daemon address: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("parse daemon port: %w", err)
	}
	return host, port, nil
}

// hostPortOfURL splits the address a started daemon reported.
func hostPortOfURL(raw string) (string, int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, fmt.Errorf("parse daemon URL %q: %w", raw, err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", 0, fmt.Errorf("split daemon address %q: %w", u.Host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("parse daemon port %q: %w", portStr, err)
	}
	return host, port, nil
}
