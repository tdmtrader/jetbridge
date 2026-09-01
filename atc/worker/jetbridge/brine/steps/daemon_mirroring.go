package steps

// Mirroring steps: the executable half of ../features/daemon-mirroring.feature.
//
// THE COPY, AND WHERE IT LANDS. Every artifact a step produces lives on one
// node's disk, and the daemon copies each one to a peer so that losing the
// node does not lose the build. ../features/artifact-daemon.feature has
// carried this as a written-down gap since the migration started: nothing
// anywhere asserted that asking a daemon to mirror causes a copy to exist.
// From the ATC's side it cannot be asserted at all — DaemonClient.TriggerMirror
// returns nil on 202, on non-202, on a transport failure and on a request it
// could not even build, deliberately, so that failing to schedule a copy never
// fails a step that already succeeded.
//
// So the assertion is made at the other end: ask the producer, then READ THE
// ARTIFACT OFF THE PEER. Both are real artifact-daemon processes with storage
// roots of their own, and the only reason that is possible is --kubeconfig:
// peer discovery goes through EndpointSlices, main.go built that client from
// rest.InClusterConfig() alone, and --node-name — which is what wires the
// mirror up at all — made the process exit outside a cluster.
//
// THE TOPOLOGY is the one daemon_cross_node.go established, and this file
// deliberately uses its helpers rather than growing a second copy of them:
//
//	producer — holds the output and is asked to copy it. Started with
//	           --kubeconfig, --node-name, --namespace and a --service-name of
//	           its own, so its peers come from a real EndpointSlice on the
//	           suite's real API server.
//	peer     — the other node's daemon. No --node-name, so it builds no
//	           Kubernetes client, has no peers of its own, and cannot pass a
//	           copy on. It only has to receive and serve.
//
// One TCP forwarder sits between them, and routeToPeer's comment says why: a
// daemon PUTs to peers on its OWN --port (main.go passes *port to NewMirror)
// and binds the wildcard, so two daemons on one host can only be told apart by
// the address they answer on. In a cluster the problem does not exist — every
// pod has its own netns and every daemon is 7780 on its own address. The
// forwarder parses nothing, answers nothing and records nothing; it is the
// network, not a daemon. verifyPeerRoute fetches an artifact only the peer
// holds, through the published address, before a scenario's first step runs —
// so a host that will not let the two listeners coexist says so in one
// sentence instead of leaving six scenarios to fail as "the copy never came".
//
// WHAT IS ASSERTED IS THE ARTIFACT ON THE OTHER NODE'S DISK. Every check reads
// the peer: whether it serves the key, which files came with it, what they say.
// Nothing counts requests, and nothing asks the producer what it thinks it did.
//
// WHY THE DAEMONS ARE STARTED IN THE GIVEN: brine acquires every
// ScopeScenario resource before EVERY scenario in the corpus, so daemons
// registered that way are started and killed once per scenario in the whole
// suite to serve the six here — measured at +70 seconds the first time. The
// API server is the exception and is deliberately NOT started here: it is the
// suite-scoped "real-cluster" resource, already paid for by
// pod-watch-real.feature, so this feature adds nothing to its cost.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	// How long a copy has to arrive. A mirror is scheduled, not performed
	// inline — POST /mirror answers 202 before the tar walk starts — so the
	// arrival is polled. The measured round trip on this topology is well
	// under a second; this is a deadline against a wedged daemon, not an
	// estimate of how long a copy takes.
	mirrorArrivalDeadline = 20 * time.Second

	// How long a scenario watches the peer before it is willing to say no copy
	// was made. Same measurement, with room over it, and it fails the moment
	// one appears rather than at the end.
	mirrorSettleWindow = 2 * time.Second
)

// Mirroring is a producer, the node it copies to, and what the last request
// did.
type Mirroring struct {
	Producer *realDaemon
	Peer     *realDaemon

	// Keys are the artifact keys the last When handed over, in order.
	Keys []string

	// PeerBefore is what the PEER answered for the first of those keys before
	// anything was asked of the producer, so a scenario can show its Then is
	// not describing a state that already held.
	PeerBefore int

	// Trigger is what POST /mirror answered.
	Trigger int

	// Body is the copy a check last read off the peer.
	Body []byte
}

func (s Mirroring) key() string {
	if len(s.Keys) == 0 {
		return ""
	}
	return s.Keys[0]
}

// heldByPeer asks the OTHER node's daemon for an artifact by the directory it
// would occupy there. A mirror arrives as PUT /stream-in/<key>, which extracts
// into steps/<key>, so this is the peer's own answer to "do you have it".
func (s Mirroring) heldByPeer(key string) (int, []byte, error) {
	return artifactFrom(s.Peer, key)
}

func (s Mirroring) heldByProducer(key string) (int, []byte, error) {
	return artifactFrom(s.Producer, key)
}

// mirrorHTTP is the only client this file uses, and it has a timeout for a
// reason worth stating: every polling deadline below checks the clock BETWEEN
// requests. On http.DefaultClient — zero Timeout — a daemon that accepts the
// connection and then never answers, or sends headers and stalls mid-body,
// blocks inside the call and the deadline never comes round. The scenario does
// not fail at 20s with its message; the CI job times out with no diagnostic at
// all. That is the exact failure the deadlines were written to prevent.
var mirrorHTTP = &http.Client{Timeout: 15 * time.Second}

func artifactFrom(d *realDaemon, key string) (int, []byte, error) {
	resp, err := mirrorHTTP.Get(d.URL + "/artifacts/steps/" + key)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

func (s Mirroring) askToMirror(key string) (int, error) {
	resp, err := mirrorHTTP.Post(s.Producer.URL+"/mirror", "application/json",
		bytes.NewReader([]byte(fmt.Sprintf(`{"key":%q}`, key))))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// writeOutput puts a file into a step's output directory on the producer's
// disk, which is what a step does. Nothing announces it to anyone.
func (s Mirroring) writeOutput(key, rel, content string) error {
	full := filepath.Join(s.Producer.Root, "steps", filepath.FromSlash(key), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// filesInCopy lists the regular files in the copy last read off the peer.
// Directory headers are not files and are not counted: a scenario that says
// "2 files" means two.
func (s Mirroring) filesInCopy() ([]string, error) {
	tr := tar.NewReader(bytes.NewReader(s.Body))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("the peer's copy is not a readable tar: %w", err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, h.Name)
		}
	}
}

func (s Mirroring) fileInCopy(name string) (string, error) {
	tr := tar.NewReader(bytes.NewReader(s.Body))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			held, _ := s.filesInCopy()
			return "", fmt.Errorf("the copy on the peer has no file %q; it holds %v", name, held)
		}
		if err != nil {
			return "", fmt.Errorf("the peer's copy is not a readable tar: %w", err)
		}
		if h.Name == name {
			body, err := io.ReadAll(tr)
			return string(body), err
		}
	}
}

// ---------------------------------------------------------------------------
// Standing the pair up
// ---------------------------------------------------------------------------

// mirroringGiven builds one of the three Givens. They differ in the producer's
// boot flags and in whether it has been told its own address — both read once
// at startup, which is why each is a Given of its own rather than something
// said about a daemon already running.
func mirroringGiven(pattern string, toldItsOwnAddress bool, producerArgs ...string) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, Mirroring](
		pattern,
		[]string{"real-cluster"},
		func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (Mirroring, error) {
			rc, ok := res.Get("real-cluster").(*realCluster)
			if !ok {
				return Mirroring{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
			}
			cfg := rc.env.Config
			if cfg == nil {
				return Mirroring{}, fmt.Errorf("the real control plane started without a rest config")
			}

			host := routableIPv4()
			if host == "" {
				return Mirroring{}, fmt.Errorf(
					"this host has no routable IPv4 address, and a copy has to travel to one: a real " +
						"API server refuses loopback and link-local addresses in an EndpointSlice")
			}

			// POD_IP is how a daemon learns which address is its own, and the
			// producer inherits this process's environment. A stray POD_IP
			// here would quietly turn every mirroring scenario into the
			// self-skip one below — a whole feature green for the wrong
			// reason — so it is refused rather than tolerated.
			if !toldItsOwnAddress && os.Getenv("POD_IP") != "" {
				return Mirroring{}, fmt.Errorf(
					"POD_IP is set in this process's environment (%q). The producer inherits it, treats "+
						"that address as its own and skips it when choosing peers, so no copy would ever "+
						"be made and every scenario here would be green for the wrong reason",
					os.Getenv("POD_IP"))
			}

			ctx := context.Background()
			uniq := strconv.FormatInt(time.Now().UnixNano(), 36)
			nodeName := "mirror-node-" + uniq
			// A service name per scenario: every daemon lists EndpointSlices
			// by service label, so a slice left behind by a neighbouring
			// scenario would otherwise become one of this producer's peers.
			service := "artifact-daemon-mirror-" + uniq

			dir, err := os.MkdirTemp("", "brine-mirroring-*")
			if err != nil {
				return Mirroring{}, fmt.Errorf("temp dir for the kubeconfig: %w", err)
			}
			rec.RegisterDisposer(func() { _ = os.RemoveAll(dir) })

			kubeconfig := filepath.Join(dir, "kubeconfig")
			api := clientcmdapi.NewConfig()
			api.Clusters["e"] = &clientcmdapi.Cluster{
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
				InsecureSkipTLSVerify:    cfg.CAData == nil,
			}
			api.AuthInfos["e"] = &clientcmdapi.AuthInfo{
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			}
			api.Contexts["e"] = &clientcmdapi.Context{Cluster: "e", AuthInfo: "e", Namespace: "default"}
			api.CurrentContext = "e"
			if err := clientcmd.WriteToFile(*api, kubeconfig); err != nil {
				return Mirroring{}, fmt.Errorf("write the kubeconfig: %w", err)
			}

			// The producer labels its node at startup and exits(1) if it
			// cannot, so the node has to be there first.
			if _, err := rc.Clientset.CoreV1().Nodes().Create(ctx,
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}},
				metav1.CreateOptions{}); err != nil {
				return Mirroring{}, fmt.Errorf("create node %q: %w", nodeName, err)
			}
			rec.RegisterDisposer(func() {
				_ = rc.Clientset.CoreV1().Nodes().Delete(
					context.Background(), nodeName, metav1.DeleteOptions{})
			})

			// The other node's daemon. No --node-name, so it has no peers of
			// its own and cannot pass a copy on: what arrives there arrived
			// from the producer.
			peer, err := startRealDaemon()
			if err != nil {
				return Mirroring{}, fmt.Errorf("start the other node's daemon: %w", err)
			}
			rec.RegisterDisposer(func() { _ = peer.stop() })

			args := append([]string{
				"--listen-address", "127.0.0.1",
				"--kubeconfig", kubeconfig,
				"--node-name", nodeName,
				"--namespace", "default",
				"--service-name", service,
			}, producerArgs...)

			// The one thing that cannot be passed as a flag. brine runs one
			// scenario at a time in a process — a parallel run is a separate
			// process — so the window in which this is set is a window nothing
			// else can start a daemon in. Restored either way, so a scenario
			// that fails does not leave it behind for the rest of the corpus.
			if toldItsOwnAddress {
				prev, had := os.LookupEnv("POD_IP")
				if err := os.Setenv("POD_IP", host); err != nil {
					return Mirroring{}, fmt.Errorf("set POD_IP for the producer: %w", err)
				}
				defer func() {
					if had {
						_ = os.Setenv("POD_IP", prev)
					} else {
						_ = os.Unsetenv("POD_IP")
					}
				}()
			}

			producer, err := startRealDaemon(args...)
			if err != nil {
				return Mirroring{}, fmt.Errorf("start the producing node's daemon: %w", err)
			}
			rec.RegisterDisposer(func() { _ = producer.stop() })

			producerPort, err := daemonPort(producer)
			if err != nil {
				return Mirroring{}, err
			}
			peerPort, err := daemonPort(peer)
			if err != nil {
				return Mirroring{}, err
			}

			route, err := routeToPeer(
				net.JoinHostPort(host, strconv.Itoa(producerPort)),
				net.JoinHostPort("127.0.0.1", strconv.Itoa(peerPort)))
			if err != nil {
				return Mirroring{}, err
			}
			rec.RegisterDisposer(func() { _ = route.Close() })

			if _, err := rc.Clientset.DiscoveryV1().EndpointSlices("default").Create(ctx,
				&discoveryv1.EndpointSlice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      service,
						Namespace: "default",
						Labels:    map[string]string{discoveryv1.LabelServiceName: service},
					},
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{host}}},
				}, metav1.CreateOptions{}); err != nil {
				return Mirroring{}, fmt.Errorf("publish the peer's endpoints: %w", err)
			}
			rec.RegisterDisposer(func() {
				_ = rc.Clientset.DiscoveryV1().EndpointSlices("default").Delete(
					context.Background(), service, metav1.DeleteOptions{})
			})

			if err := verifyPeerRoute(peer, host, producerPort); err != nil {
				return Mirroring{}, err
			}

			return Mirroring{Producer: producer, Peer: peer}, nil
		},
	)
}

// notePeerBefore records what the peer said about a key before the producer
// was asked to do anything with it. Every arrival check downstream is only
// worth reading beside this.
func (s Mirroring) notePeerBefore(key string) (Mirroring, error) {
	status, _, err := s.heldByPeer(key)
	if err != nil {
		return s, fmt.Errorf("asking the peer for %q before the mirror: %w", key, err)
	}
	s.Keys = []string{key}
	s.PeerBefore = status
	return s, nil
}

// ---------------------------------------------------------------------------
// The steps
// ---------------------------------------------------------------------------

// DaemonMirroringDefinitions drives a real daemon's outbound mirror between two
// real daemon processes.
func DaemonMirroringDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// --mirror-replicas=-1 is "every peer this daemon can find", which
		// here is the one address published for the other node. The per-peer
		// timeout is cut from five minutes so a wedged PUT fails inside the
		// scenario rather than outliving it.
		mirroringGiven(
			"a daemon that mirrors, and the node it mirrors to",
			false, "--mirror-replicas", "-1", "--mirror-timeout", "30s"),

		mirroringGiven(
			"a daemon with its mirror switched off, and the node it would mirror to",
			false, "--mirror-replicas", "0"),

		// Told its own address the way the chart tells it: POD_IP, from the
		// downward API.
		mirroringGiven(
			"a daemon told the peer's address is its own, and the node it would mirror to",
			true, "--mirror-replicas", "-1", "--mirror-timeout", "30s"),

		// What a step does: write files into its output directory on the node.
		// Nothing tells any daemon they are there.
		brine.DefineMap[Mirroring, Mirroring](
			"the producer's disk holds the output {string} with the file {string} reading {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				const pattern = "the producer's disk holds the output {string} with the file {string} reading {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				rel, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				return in, in.writeOutput(key, rel, content)
			},
		),

		// A step that produced nothing still has an output directory and still
		// registers a volume the next step will ask for.
		brine.DefineMap[Mirroring, Mirroring](
			"the producer's disk holds the empty output {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				key, err := paramAt("the producer's disk holds the empty output {string}", p, 0)
				if err != nil {
					return in, err
				}
				dir := filepath.Join(in.Producer.Root, "steps", filepath.FromSlash(key))
				return in, os.MkdirAll(dir, 0o755)
			},
		),

		// The realistic tail of a step: several outputs finished together.
		brine.DefineMap[Mirroring, Mirroring](
			"the producer's disk holds {int} outputs, each with a file of its own",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				const pattern = "the producer's disk holds {int} outputs, each with a file of its own"
				n, err := intAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				if n <= 0 {
					return in, fmt.Errorf("step %q asks for %d outputs", pattern, n)
				}
				in.Keys = nil
				for i := 0; i < n; i++ {
					key := fmt.Sprintf("build-%d/result", i)
					if err := in.writeOutput(key, "f.txt", fmt.Sprintf("output %d", i)); err != nil {
						return in, err
					}
					in.Keys = append(in.Keys, key)
				}
				return in, nil
			},
		),

		// POST /mirror, with the peer's answer for the same key taken first.
		brine.DefineMap[Mirroring, Mirroring](
			"the ATC asks it to mirror {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				key, err := paramAt("the ATC asks it to mirror {string}", p, 0)
				if err != nil {
					return in, err
				}
				in, err = in.notePeerBefore(key)
				if err != nil {
					return in, err
				}
				in.Trigger, err = in.askToMirror(key)
				if err != nil {
					return in, fmt.Errorf("POST /mirror for %q: %w", key, err)
				}
				return in, nil
			},
		),

		// Every key handed over before any of them is waited on: the queue is
		// what the scenario is about, so filling it is the point.
		brine.DefineMap[Mirroring, Mirroring](
			"the ATC asks it to mirror all {int} of them at once",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				const pattern = "the ATC asks it to mirror all {int} of them at once"
				n, err := intAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				if n != len(in.Keys) {
					return in, fmt.Errorf("step %q names %d outputs, but %d were written",
						pattern, n, len(in.Keys))
				}
				// Refused here rather than asserted later: if the peer already
				// held one of these, the arrival that follows would prove
				// nothing.
				for _, key := range in.Keys {
					status, _, err := in.heldByPeer(key)
					if err != nil {
						return in, fmt.Errorf("asking the peer for %q before the mirror: %w", key, err)
					}
					if status != http.StatusNotFound {
						return in, fmt.Errorf(
							"the peer already answered %d for %q before any mirror was asked for", status, key)
					}
				}
				for _, key := range in.Keys {
					in.Trigger, err = in.askToMirror(key)
					if err != nil {
						return in, fmt.Errorf("POST /mirror for %q: %w", key, err)
					}
				}
				return in, nil
			},
		),

		// The path nobody asks for: a producer mirrors what is streamed into
		// it on its own, which is how an ordinary step's output leaves the
		// node it was made on. The header a mirrored write carries is
		// deliberately absent here — this is the ATC writing, not a peer.
		brine.DefineMap[Mirroring, Mirroring](
			"a step streams the output {string} into the producer with the file {string} reading {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				const pattern = "a step streams the output {string} into the producer with the file {string} reading {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				rel, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				in, err = in.notePeerBefore(key)
				if err != nil {
					return in, err
				}

				body, err := tarOfOneFile(rel, content)
				if err != nil {
					return in, err
				}
				req, err := http.NewRequest(http.MethodPut, in.Producer.URL+"/stream-in/"+key, body)
				if err != nil {
					return in, err
				}
				req.Header.Set("Content-Type", "application/x-tar")
				resp, err := mirrorHTTP.Do(req)
				if err != nil {
					return in, fmt.Errorf("PUT /stream-in/%s: %w", key, err)
				}
				defer resp.Body.Close()
				answer, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusCreated {
					return in, fmt.Errorf("streaming %q into the producer answered %d: %s",
						key, resp.StatusCode, abbrev(string(answer)))
				}
				return in, nil
			},
		),

		// The arrival, polled: a mirror is scheduled and not performed inline,
		// so POST /mirror has answered long before the tar walk starts.
		brine.DefineMap[Mirroring, Mirroring](
			"the copy arrives on the peer under the key {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) (Mirroring, error) {
				key, err := paramAt("the copy arrives on the peer under the key {string}", p, 0)
				if err != nil {
					return in, err
				}
				deadline := time.Now().Add(mirrorArrivalDeadline)
				last := 0
				for time.Now().Before(deadline) {
					status, body, err := in.heldByPeer(key)
					if err != nil {
						return in, fmt.Errorf("asking the peer for the copy of %q: %w", key, err)
					}
					if status == http.StatusOK {
						in.Body = body
						return in, nil
					}
					last = status
					time.Sleep(25 * time.Millisecond)
				}
				onProducer, _, _ := in.heldByProducer(key)
				return in, fmt.Errorf(
					"no copy of %q reached the peer within %s (it kept answering %d). The producer "+
						"answers %d for its own copy, so the source %s",
					key, mirrorArrivalDeadline, last, onProducer,
					map[bool]string{
						true:  "was there to be copied",
						false: "was not there either",
					}[onProducer == http.StatusOK])
			},
		),

		// The negative half, and what makes the positives mean anything: a
		// daemon that copied everything to everyone passes every arrival above
		// and fails here.
		brine.DefineCheck[Mirroring](
			"no copy ever arrives on the peer under the key {string}",
			func(in Mirroring, p brine.Params, _ *brine.Recorder) error {
				key, err := paramAt("no copy ever arrives on the peer under the key {string}", p, 0)
				if err != nil {
					return err
				}
				started := time.Now()
				for time.Since(started) < mirrorSettleWindow {
					status, _, err := in.heldByPeer(key)
					if err != nil {
						return fmt.Errorf("asking the peer for a copy of %q: %w", key, err)
					}
					if status == http.StatusOK {
						return fmt.Errorf(
							"the peer was holding a copy of %q %s after the request; this producer was "+
								"not supposed to make one",
							key, time.Since(started).Round(time.Millisecond))
					}
					time.Sleep(25 * time.Millisecond)
				}
				return nil
			},
		),

		CheckThat[Mirroring]("every one of those copies arrives on the peer", func(in Mirroring) error {
			if len(in.Keys) == 0 {
				return fmt.Errorf("no outputs were handed over")
			}
			deadline := time.Now().Add(mirrorArrivalDeadline)
			var missing []string
			for time.Now().Before(deadline) {
				missing = nil
				for _, key := range in.Keys {
					status, _, err := in.heldByPeer(key)
					if err != nil {
						return fmt.Errorf("asking the peer for the copy of %q: %w", key, err)
					}
					if status != http.StatusOK {
						missing = append(missing, key)
					}
				}
				if len(missing) == 0 {
					return nil
				}
				time.Sleep(25 * time.Millisecond)
			}
			return fmt.Errorf("%d of %d copies never reached the peer within %s: %v",
				len(missing), len(in.Keys), mirrorArrivalDeadline, missing)
		}),

		// 202 whatever happens next. The ATC discards the answer, so a daemon
		// that refused the request would be failing mirrors invisibly.
		CheckThat[Mirroring]("the daemon accepted the mirror request", func(in Mirroring) error {
			if in.Trigger != http.StatusAccepted {
				return fmt.Errorf("expected the producer to accept the mirror with 202, it answered %d",
					in.Trigger)
			}
			return nil
		}),

		CheckThat[Mirroring]("the peer held nothing under that key before", func(in Mirroring) error {
			if in.PeerBefore != http.StatusNotFound {
				return fmt.Errorf(
					"the peer already answered %d for %q before the producer was asked for anything; "+
						"the arrival that follows would prove nothing",
					in.PeerBefore, in.key())
			}
			return nil
		}),

		// The liveness half of a negative scenario: no copy was made, and the
		// output is still readable on the node that made it. Without this a
		// producer that had died — or eaten the artifact — would pass.
		CheckThat[Mirroring]("the producer still serves the output it was asked to mirror", func(in Mirroring) error {
			status, _, err := in.heldByProducer(in.key())
			if err != nil {
				return fmt.Errorf("asking the producer for its own copy of %q: %w", in.key(), err)
			}
			if status != http.StatusOK {
				return fmt.Errorf("expected the producer to still serve steps/%s, it answered %d",
					in.key(), status)
			}
			return nil
		}),

		CheckCount[Mirroring]("the copy carries {int} files",
			"files in the copy on the peer",
			func(in Mirroring) ([]string, error) { return in.filesInCopy() }),

		CheckStringFor[Mirroring]("the copy's file at {string} reads {string}",
			"the copied file",
			func(in Mirroring, name string) (string, error) { return in.fileInCopy(name) }),
	}
}
