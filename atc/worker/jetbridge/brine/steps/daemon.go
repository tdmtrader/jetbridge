package steps

// Artifact-daemon steps: the executable half of ../features/artifact-daemon.feature.
//
// The double here is a REAL http.Server speaking the artifact daemon's wire
// contract — PHILOSOPHY.md's "test adapters are real adapters", and the same
// argument volume_streaming.go's localExecAdapter makes for exec. Its
// behavioral difference is one sentence: it holds its artifacts in a map
// instead of on a node's disk.
//
// It records NOTHING. There is no `gotPath`, no `gotMethod`, no `probeHits`.
// That is deliberate: the two ginkgo suites this replaces already had a real
// server and still asserted against captures taken inside the handler, which
// is the recording-double problem with better transport. Every assertion below
// is on what came back out — the artifact, the error, or nothing.
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
	"strconv"
	"strings"
	"sync"

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

// DaemonPlan is a cluster with artifact daemons in it, under description.
// Refinement steps take DaemonPlan in and out — the live state's type does not
// change — so a scenario may say what the daemon holds, who produced the
// artifact and what the consumer is asking for in any order.
type DaemonPlan struct {
	Ctx       context.Context
	Namespace string
	Service   string

	// Port is the single port the ATC reaches every daemon on, because that is
	// how the deployment works: one DaemonSet, one containerPort. It is the
	// live server's real port, so requests really are dialled.
	Port     int
	DaemonIP string
	Server   *httptest.Server
	Store    *daemonStore

	// IPs is what the EndpointSlice publishes. It may name addresses that
	// nothing answers on.
	IPs   []string
	Nodes map[string]string

	// Archives keeps member order so a multi-file artifact can be built up
	// across several Given steps.
	Archives map[string][]tarMember

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
// The daemon double
// -----------------------------------------------------------------------

type tarMember struct {
	Name    string
	Content string
}

// daemonStore is what one artifact daemon has. The handler reads it at request
// time, so Given steps may keep adding to it after the server is listening.
type daemonStore struct {
	mu sync.Mutex

	caches        map[string]bool
	artifacts     map[string][]byte
	stepArtifacts map[string][]byte

	durableTier bool
	resolves    bool
}

func newDaemonStore() *daemonStore {
	return &daemonStore{
		caches:        map[string]bool{},
		artifacts:     map[string][]byte{},
		stepArtifacts: map[string][]byte{},
	}
}

// handler answers the routes the artifact daemon answers, and 404s everything
// else — including a route asked for with the wrong shape. A client that
// addresses the wrong key gets nothing, which is how the scenarios can assert
// on arrival instead of on the request.
func (s *daemonStore) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Advertised on every status, not just 200 — a daemon that misses on
		// this key is still the daemon that could warm it.
		if s.durableTier {
			w.Header().Set(jetbridge.DurableTierHeader, "enabled")
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			if s.caches[strings.TrimPrefix(r.URL.Path, "/resource-caches/")] {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.URL.Path == "/resolve" || r.URL.Path == "/resolve-batch":
			if !s.resolves {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","method":"registry"}`))

		// The route the real daemon answers, so a client that posts here is
		// not answered with a 404 it would never see in production. Nothing
		// asks it: see the note in ../features/artifact-daemon.feature about
		// the mirror trigger having no scenario.
		case r.URL.Path == "/mirror":
			w.WriteHeader(http.StatusAccepted)

		case strings.HasPrefix(r.URL.Path, "/artifacts/steps/"):
			serveArtifactBody(w, s.stepArtifacts[strings.TrimPrefix(r.URL.Path, "/artifacts/steps/")])

		case strings.HasPrefix(r.URL.Path, "/artifacts/"):
			serveArtifactBody(w, s.artifacts[strings.TrimPrefix(r.URL.Path, "/artifacts/")])

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func serveArtifactBody(w http.ResponseWriter, body []byte) {
	if body == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// -----------------------------------------------------------------------
// Wiring the ATC side up from the plan
// -----------------------------------------------------------------------

// cluster builds the fake Kubernetes the ATC discovers daemons and nodes
// through. fake.Clientset is a real implementation of the client interface
// whose behavioral property is deterministic delivery; it is not the subject
// of any assertion here.
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

// close shuts the daemon down. Every When step calls it, because the resource
// plane cannot own an httptest server that a step created — see the note in
// the migration report. Close is idempotent, so a scenario that already took
// the daemon away is fine.
func (p DaemonPlan) close() {
	if p.Server != nil {
		p.Server.Close()
	}
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
// Steps
// -----------------------------------------------------------------------

// DaemonDefinitions expresses the artifact daemon's contract as artifact
// arrival. Nothing here names a URL, a verb, or a request count.
func DaemonDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, DaemonPlan](
			"an artifact daemon",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				store := newDaemonStore()
				server := httptest.NewServer(store.handler())

				host, port, err := hostAndPort(server)
				if err != nil {
					server.Close()
					return DaemonPlan{}, err
				}

				return DaemonPlan{
					Ctx:       context.Background(),
					Namespace: "cicd",
					Service:   "artifact-daemon",
					Port:      port,
					DaemonIP:  host,
					Server:    server,
					Store:     store,
					IPs:       []string{host},
					Nodes:     map[string]string{},
					Archives:  map[string][]tarMember{},
				}, nil
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
					Store:     newDaemonStore(),
					Nodes:     map[string]string{},
					Archives:  map[string][]tarMember{},
				}, nil
			},
		),

		// --- what the daemon holds ---

		Refine[DaemonPlan]("it holds the artifact {string} containing {string}",
			func(in DaemonPlan, a Args) DaemonPlan {
				in.Store.put(&in.Store.artifacts, a.String(0), []byte(a.String(1)))
				return in
			}),

		Refine[DaemonPlan]("it holds a mirrored copy of the artifact {string} containing {string}",
			func(in DaemonPlan, a Args) DaemonPlan {
				in.Store.put(&in.Store.stepArtifacts, a.String(0), []byte(a.String(1)))
				return in
			}),

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"it holds the archive {string} containing the file {string} with {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				return addArchiveMember(in, p)
			},
		),

		brine.DefineMap[DaemonPlan, DaemonPlan](
			"the archive {string} also contains the file {string} with {string}",
			func(in DaemonPlan, p brine.Params, _ *brine.Recorder) (DaemonPlan, error) {
				return addArchiveMember(in, p)
			},
		),

		Refine[DaemonPlan]("it holds the resource cache {string}",
			func(in DaemonPlan, a Args) DaemonPlan {
				key := a.String(0)
				in.Store.mu.Lock()
				in.Store.caches[key] = true
				in.Store.mu.Unlock()
				return in
			}),

		Refine[DaemonPlan]("it advertises a durable tier",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.Store.mu.Lock()
				in.Store.durableTier = true
				in.Store.mu.Unlock()
				return in
			}),

		Refine[DaemonPlan]("it answers resolve requests but holds nothing locally",
			func(in DaemonPlan, _ Args) DaemonPlan {
				in.Store.mu.Lock()
				in.Store.resolves = true
				in.Store.mu.Unlock()
				return in
			}),

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
		// the cluster any more. Spot preemption, a crash, a drain.
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
				defer in.close()

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
				defer in.close()

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
				defer in.close()

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
				defer in.close()

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

		// --- mirroring ---

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

func (s *daemonStore) put(into *map[string][]byte, key string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	(*into)[key] = body
}

func addArchiveMember(in DaemonPlan, p brine.Params) (DaemonPlan, error) {
	key, _ := p.GetString(0)
	name, _ := p.GetString(1)
	content, ok := p.GetString(2)
	if !ok {
		return DaemonPlan{}, fmt.Errorf("expected an archive key, a file name and its contents")
	}

	archives := map[string][]tarMember{}
	for k, v := range in.Archives {
		archives[k] = append([]tarMember{}, v...)
	}
	archives[key] = append(archives[key], tarMember{Name: name, Content: content})
	in.Archives = archives

	body, err := tarOfMembers(archives[key])
	if err != nil {
		return DaemonPlan{}, err
	}
	in.Store.put(&in.Store.artifacts, key, body)
	return in, nil
}

func tarOfMembers(members []tarMember) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		hdr := &tar.Header{Name: m.Name, Size: int64(len(m.Content)), Mode: 0o644}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write tar header for %q: %w", m.Name, err)
		}
		if _, err := tw.Write([]byte(m.Content)); err != nil {
			return nil, fmt.Errorf("write tar body for %q: %w", m.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return buf.Bytes(), nil
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
