package steps

// Cross-node steps: the executable half of ../features/daemon-cross-node.feature.
//
// THE DAEMON'S OWN PEER FALLBACK, which is a different thing from the ATC's.
// artifact-daemon.feature covers the ATC asking daemon X and, on a miss,
// asking daemon Y itself. This covers what happens one layer down: a consumer
// asks the daemon ON ITS OWN NODE, that daemon holds nothing, and it goes and
// gets the bytes from the daemon that does — EndpointSlice discovery, HEAD
// probe, GET of a tar, extraction, promotion by rename, all inside the daemon.
//
// Until the --kubeconfig flag landed, none of that was reachable from outside
// a cluster and three feature files say so in as many words: peer discovery
// goes through EndpointSlices, main.go built that client from
// rest.InClusterConfig() alone, and client-go hardcodes the service-account
// token path. Two real daemons could not find each other. They can now.
//
// THE TOPOLOGY, and the one piece of scaffolding in it.
//
// Two real artifact-daemon PROCESSES, each with its own storage root:
//
//	peer   — holds the artifact. No --node-name, so it builds no Kubernetes
//	         client and has no peers of its own. It only has to serve.
//	local  — the daemon the consumer talks to. Started with --kubeconfig,
//	         --node-name and --namespace, so its PeerResolver is wired to the
//	         real API server the suite already runs (the "real-cluster"
//	         resource, envtest). --mirror-replicas 0, so the only cross-node
//	         machinery in play is the fallback under test.
//
// A real EndpointSlice publishes the peer's address, and the local daemon
// reads it live on every probe — there is no informer or cache to prime.
//
// THE SCAFFOLDING is one TCP forwarder, and it is worth being exact about why
// it has to exist. A daemon derives the port it probes peers on from its OWN
// --port (main.go passes *port to NewPeerResolver), and it binds the wildcard.
// So for the local daemon to reach the peer, the peer must answer on the
// LOCAL daemon's port number — and two processes on one host cannot both hold
// one wildcard port. In a cluster this problem does not exist: every pod has
// its own network namespace and every daemon is 7780 on its own address.
//
// The forwarder restores that. It binds the published address at the local
// daemon's port and moves bytes to the peer's port. A specific-address
// listener is separate from the daemon's loopback-only listener, so the
// local daemon never sees these connections. The forwarder
// reads nothing, answers nothing and counts nothing: it is the network, not a
// daemon. Both ends of every request in this file are real daemon processes.
//
// The local daemon is deliberately bound to loopback in this single-host
// harness, leaving the routable address available to the forwarder. Production
// keeps the default wildcard bind. verifyPeerRoute fetches, through the
// published address, an artifact only the peer holds before any scenario runs.
//
// WHY THE DAEMONS ARE STARTED IN THE GIVEN and not registered as brine
// resources: brine acquires every ScopeScenario resource before EVERY
// scenario in the suite, so a daemon registered that way is started and
// killed once per scenario in the whole corpus to serve the six here. That
// was measured at +70 seconds when the first real-daemon scenarios were
// wired up. The API server is the exception and is deliberately NOT started
// here: it is the suite-scoped "real-cluster" resource, already paid for by
// pod-watch-real.feature, and this feature adds nothing to its cost.
//
// WHAT IS ASSERTED IS THE OUTCOME. Every check below reads the destination
// directory the consumer named: which bytes are in it, whether a link is
// still a link, how many bytes long a file is. Nothing counts requests. The
// scenario that has to distinguish "served its own copy" from "served the
// peer's" does it by giving the two nodes DIFFERENT bytes for the same key
// and naming which arrived, because a probe count would pass equally against
// a daemon that fetched from the peer and then threw the answer away.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// CrossNode is two running daemons and the answer the local one gave.
type CrossNode struct {
	// Peer is the daemon on the OTHER node: the one holding artifacts.
	Peer *realDaemon
	// Local is the daemon on THIS node: the one the consumer talks to, and
	// the only one wired to the API server.
	Local *realDaemon

	// Dest is the directory the last resolve was told to write to. The
	// checks read it; the miss scenario asserts it was never created.
	Dest string

	Status int
	Body   string
	Err    error

	// Digests maps a path inside the artifact to the sha256 of the bytes the
	// PEER wrote there, for the artifact whose content is too large to quote
	// in a sentence. A map rather than a plain field because brine passes the
	// state by value between steps: the header is copied, the backing store is
	// shared, so a Given can record something a later check reads.
	Digests map[string]string
}

// peerArtifact is where an artifact named by key lives on the peer's disk.
// "Putting an artifact on a daemon" is writing files under <root>/steps/, and
// an artifact is a DIRECTORY — /artifacts/ tars it on the way out.
func (s CrossNode) peerArtifact(key string) string {
	return filepath.Join(s.Peer.Root, "steps", filepath.FromSlash(key))
}

func (s CrossNode) localArtifact(key string) string {
	return filepath.Join(s.Local.Root, "steps", filepath.FromSlash(key))
}

// resolved is a path inside the directory the consumer asked for.
func (s CrossNode) resolved(rel string) string {
	return filepath.Join(s.Dest, filepath.FromSlash(rel))
}

// answer describes the last resolve, for the failure message of a check that
// went looking for a file and found nothing.
func (s CrossNode) answer() string {
	if s.Err != nil {
		return "the resolve did not complete: " + s.Err.Error()
	}
	return fmt.Sprintf("the resolve answered %d: %s", s.Status, abbrev(s.Body))
}

// missing explains a file that is not where the consumer asked for it.
//
// The two cases read the same to os.Open and are entirely different defects:
// nothing arrived at all, or the artifact arrived MISSING A NAME — which is
// the shape a dropped tar entry has, and the one worth naming, because the
// first version of this message said "the artifact was not delivered" for
// both and sent the reader looking for a transfer that had in fact happened.
// The resolve's own answer is NOT repeated here: the two combinator-backed
// checks carry it as their detail, which prints on a mismatch as well as on a
// miss. Only the digest check, which has no detail hook, adds it itself.
func (s CrossNode) missing(rel string, err error) error {
	entries, readErr := os.ReadDir(s.Dest)
	if readErr != nil {
		return fmt.Errorf("nothing was delivered to %s: %w", s.Dest, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return fmt.Errorf("the artifact arrived without %q: %s holds %v", rel, s.Dest, names)
}

// putCrossNodeFile puts one file into an artifact directory.
func putCrossNodeFile(dir, rel string, content []byte) error {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// syntheticBytes fills n bytes with a deterministic, non-repeating stream.
//
// Non-repeating matters: an artifact of one byte repeated survives truncation
// at any offset and a duplicated block, and both are exactly what a broken
// stream produces. With this, size and digest together say the whole thing
// arrived in the right order.
func syntheticBytes(n int) []byte {
	b := make([]byte, n)
	x := uint64(0x9E3779B97F4A7C15)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

// routableIPv4 returns an address an EndpointSlice will accept AND a listener
// can bind. A real API server refuses loopback, unspecified and link-local
// addresses in an EndpointSlice — validation no fake clientset performs, and
// the first thing envtest caught when two real daemons were first pointed at
// each other.
//
// Deliberately not named nonLoopbackIPv4: peerproof_test.go has one of those,
// and a _test.go file in this same package would collide with it.
func routableIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// daemonPort reads the port a started daemon is listening on.
func daemonPort(d *realDaemon) (int, error) {
	_, port, err := net.SplitHostPort(strings.TrimPrefix(d.URL, "http://"))
	if err != nil {
		return 0, fmt.Errorf("read the port out of %q: %w", d.URL, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf("port %q in %q is not a number: %w", port, d.URL, err)
	}
	return n, nil
}

// routeToPeer binds listenAddr and forwards every connection to targetAddr.
//
// This is the network between two nodes, not a stand-in for either of them:
// it never parses a request, never produces a response and never records
// anything. See the file header for why it is unavoidable — the daemon probes
// peers on its own --port and binds the wildcard, so on one host the two
// daemons can only be told apart by the address they answer on.
func routeToPeer(listenAddr, targetAddr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s to publish the peer there: %w", listenAddr, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // the listener was closed at scenario end
			}
			go forwardConn(conn, targetAddr)
		}
	}()
	return ln, nil
}

func forwardConn(client net.Conn, targetAddr string) {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	// Half-close in each direction as it drains, so the peer sees the end of a
	// request body and the daemon sees the end of a response.
	done := make(chan struct{}, 2)
	copyThenCloseWrite := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyThenCloseWrite(upstream, client)
	go copyThenCloseWrite(client, upstream)
	<-done
	<-done
}

// verifyPeerRoute proves, before any scenario runs, that the address published
// for the peer actually reaches the peer.
//
// Without this the whole family degrades into "the artifact did not arrive",
// which is the same symptom as a broken fallback and would send the next
// person to read the daemon.
func verifyPeerRoute(peer *realDaemon, host string, port int) error {
	const key = "__peer_route_check__"
	dir := filepath.Join(peer.Root, "steps", key)
	if err := putCrossNodeFile(dir, "probe.txt", []byte("route")); err != nil {
		return fmt.Errorf("write the route probe onto the peer: %w", err)
	}
	defer os.RemoveAll(dir)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/artifacts/steps/%s", host, port, key))
	if err != nil {
		return fmt.Errorf("the address published for the peer (%s:%d) is not reachable: %w", host, port, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"the address published for the peer (%s:%d) answered %d for an artifact only the peer holds — "+
				"the request reached the ASKING daemon instead. Two daemons on one host are told apart only "+
				"by the address they answer on, because the daemon probes peers on its own --port and binds "+
				"the wildcard, and this host is routing the connection to the wildcard listener",
			host, port, resp.StatusCode)
	}
	return nil
}

// DaemonCrossNodeDefinitions drives two real artifact-daemon processes that
// can see each other through a real API server.
func DaemonCrossNodeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, CrossNode](
			"two real artifact daemons, this node's and another node's",
			[]string{"real-cluster"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (CrossNode, error) {
				rc, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return CrossNode{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}
				cfg := rc.env.Config
				if cfg == nil {
					return CrossNode{}, fmt.Errorf("the real control plane started without a rest config")
				}

				host := routableIPv4()
				if host == "" {
					return CrossNode{}, fmt.Errorf(
						"this host has no routable IPv4 address, and peer discovery needs one: a real API " +
							"server refuses loopback and link-local addresses in an EndpointSlice")
				}

				ctx := context.Background()
				uniq := strconv.FormatInt(time.Now().UnixNano(), 36)
				nodeName := "cross-node-" + uniq
				// A service name of its own per scenario. Every daemon lists
				// EndpointSlices by service label, so a slice left behind by a
				// neighbouring scenario would otherwise become one of this
				// daemon's peers.
				service := "artifact-daemon-" + uniq

				// A kubeconfig the daemon can be pointed at. --node-name is
				// what wires peer discovery at all, and it makes the daemon
				// build a Kubernetes client, so --kubeconfig must come with it.
				dir, err := os.MkdirTemp("", "brine-cross-node-*")
				if err != nil {
					return CrossNode{}, fmt.Errorf("temp dir for the kubeconfig: %w", err)
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
					return CrossNode{}, fmt.Errorf("write the kubeconfig: %w", err)
				}

				// The daemon labels its node at startup and exits(1) if it
				// cannot, so the node has to be there first.
				if _, err := rc.Clientset.CoreV1().Nodes().Create(ctx,
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}},
					metav1.CreateOptions{}); err != nil {
					return CrossNode{}, fmt.Errorf("create node %q: %w", nodeName, err)
				}
				rec.RegisterDisposer(func() {
					_ = rc.Clientset.CoreV1().Nodes().Delete(
						context.Background(), nodeName, metav1.DeleteOptions{})
				})

				peer, err := startRealDaemon()
				if err != nil {
					return CrossNode{}, fmt.Errorf("start the other node's daemon: %w", err)
				}
				rec.RegisterDisposer(func() { _ = peer.stop() })

				local, err := startRealDaemon(
					"--listen-address", "127.0.0.1",
					"--kubeconfig", kubeconfig,
					"--node-name", nodeName,
					"--namespace", "default",
					"--service-name", service,
					// Nothing here mirrors, and leaving the outbound mirror on
					// would put a second cross-node mechanism in the same
					// scenario as the one under test.
					"--mirror-replicas", "0",
				)
				if err != nil {
					return CrossNode{}, fmt.Errorf("start this node's daemon: %w", err)
				}
				rec.RegisterDisposer(func() { _ = local.stop() })

				localPort, err := daemonPort(local)
				if err != nil {
					return CrossNode{}, err
				}
				peerPort, err := daemonPort(peer)
				if err != nil {
					return CrossNode{}, err
				}

				route, err := routeToPeer(
					net.JoinHostPort(host, strconv.Itoa(localPort)),
					net.JoinHostPort("127.0.0.1", strconv.Itoa(peerPort)))
				if err != nil {
					return CrossNode{}, err
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
					return CrossNode{}, fmt.Errorf("publish the peer's endpoints: %w", err)
				}
				rec.RegisterDisposer(func() {
					_ = rc.Clientset.DiscoveryV1().EndpointSlices("default").Delete(
						context.Background(), service, metav1.DeleteOptions{})
				})

				if err := verifyPeerRoute(peer, host, localPort); err != nil {
					return CrossNode{}, err
				}

				return CrossNode{Peer: peer, Local: local, Digests: map[string]string{}}, nil
			},
		),

		brine.DefineMap[CrossNode, CrossNode](
			"the other node's daemon holds {string} containing {string} at {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				key, err := paramAt("the other node's daemon holds {string} containing {string} at {string}", p, 0)
				if err != nil {
					return in, err
				}
				content, err := paramAt("the other node's daemon holds {string} containing {string} at {string}", p, 1)
				if err != nil {
					return in, err
				}
				rel, err := paramAt("the other node's daemon holds {string} containing {string} at {string}", p, 2)
				if err != nil {
					return in, err
				}
				if err := putCrossNodeFile(in.peerArtifact(key), rel, []byte(content)); err != nil {
					return in, fmt.Errorf("put %q on the other node: %w", key, err)
				}
				in.Digests[rel] = sha256Hex([]byte(content))
				return in, nil
			},
		),

		brine.DefineMap[CrossNode, CrossNode](
			"the other node's daemon holds {string} containing {int} megabytes at {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				const pattern = "the other node's daemon holds {string} containing {int} megabytes at {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				megabytes, err := intAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				rel, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				content := syntheticBytes(megabytes << 20)
				if err := putCrossNodeFile(in.peerArtifact(key), rel, content); err != nil {
					return in, fmt.Errorf("put %q on the other node: %w", key, err)
				}
				in.Digests[rel] = sha256Hex(content)
				return in, nil
			},
		),

		// A link INSIDE the artifact, relative to a sibling: the shape a build
		// produces and the shape the extractor is allowed to materialize.
		brine.DefineMap[CrossNode, CrossNode](
			"that artifact has a link {string} pointing at {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				const pattern = "that artifact has a link {string} pointing at {string}"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				target, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				dir, err := lastPeerArtifact(in)
				if err != nil {
					return in, err
				}
				return in, os.Symlink(filepath.FromSlash(target), filepath.Join(dir, filepath.FromSlash(name)))
			},
		),

		// A hard link: one file, two names in the same artifact. node_modules
		// and package caches are full of them.
		brine.DefineMap[CrossNode, CrossNode](
			"that artifact names the file {string} a second time as {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				const pattern = "that artifact names the file {string} a second time as {string}"
				existing, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				second, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				dir, err := lastPeerArtifact(in)
				if err != nil {
					return in, err
				}
				if err := os.Link(
					filepath.Join(dir, filepath.FromSlash(existing)),
					filepath.Join(dir, filepath.FromSlash(second)),
				); err != nil {
					return in, fmt.Errorf("hard-link %q as %q: %w", existing, second, err)
				}
				in.Digests[second] = in.Digests[existing]
				return in, nil
			},
		),

		brine.DefineMap[CrossNode, CrossNode](
			"this node's daemon already holds {string} containing {string} at {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				const pattern = "this node's daemon already holds {string} containing {string} at {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				rel, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				if err := putCrossNodeFile(in.localArtifact(key), rel, []byte(content)); err != nil {
					return in, fmt.Errorf("put %q on this node: %w", key, err)
				}
				return in, nil
			},
		),

		// The consumer's request. POST /resolve is what an ATC step's
		// fetch-inputs container issues against the daemon on its own node.
		brine.DefineMap[CrossNode, CrossNode](
			"a consumer asks this node's daemon to resolve {string}",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) (CrossNode, error) {
				key, err := paramAt("a consumer asks this node's daemon to resolve {string}", p, 0)
				if err != nil {
					return in, err
				}

				// The destination is contained by the daemon's own storage
				// root, which /resolve requires, and its PARENT has to exist:
				// the local copy path builds a sibling temp directory with
				// os.MkdirTemp(filepath.Dir(dest)) and fails without one.
				in.Dest = filepath.Join(in.Local.Root, "resolved", strings.ReplaceAll(key, "/", "-"))
				if err := os.MkdirAll(filepath.Dir(in.Dest), 0o755); err != nil {
					return in, fmt.Errorf("create the destination's parent: %w", err)
				}

				// Generous, but bounded: a hung fetch must fail the scenario
				// rather than the suite. The daemon's own peer fetch client
				// allows three minutes.
				client := &http.Client{Timeout: 90 * time.Second}
				body := fmt.Sprintf(`{"key":%q,"dest":%q}`, key, in.Dest)
				resp, err := client.Post(in.Local.URL+"/resolve", "application/json", strings.NewReader(body))
				if err != nil {
					in.Err, in.Status, in.Body = err, 0, ""
					return in, nil
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				in.Status, in.Body, in.Err = resp.StatusCode, string(raw), err
				return in, nil
			},
		),

		CheckStringFor[CrossNode]("the resolved artifact's {string} reads {string}",
			"the resolved file",
			func(in CrossNode, rel string) (string, error) {
				b, err := os.ReadFile(in.resolved(rel))
				if err != nil {
					return "", in.missing(rel, err)
				}
				return string(b), nil
			},
			func(in CrossNode) string { return in.answer() }),

		// Readlink refuses a regular file, so this asserts BOTH that the entry
		// is still a link and where it points. An extractor that materialized
		// a copy, or a producer that followed the link on the way out, fails
		// here rather than passing quietly on identical content.
		CheckStringFor[CrossNode]("the resolved artifact's {string} is a link to {string}",
			"the link target",
			func(in CrossNode, rel string) (string, error) {
				target, err := os.Readlink(in.resolved(rel))
				if err != nil {
					return "", fmt.Errorf("%s is not a link that arrived intact: %w", rel, err)
				}
				return filepath.ToSlash(target), nil
			},
			func(in CrossNode) string { return in.answer() }),

		CheckIntFor[CrossNode]("the resolved artifact's {string} is {int} bytes",
			"the resolved file's size",
			func(in CrossNode, rel string) (int, error) {
				info, err := os.Stat(in.resolved(rel))
				if err != nil {
					return 0, in.missing(rel, err)
				}
				return int(info.Size()), nil
			},
			func(in CrossNode) string { return in.answer() }),

		// Size alone would pass on a file of the right length holding the
		// wrong bytes, which is what a stream reassembled out of order is.
		brine.DefineCheck[CrossNode](
			"the resolved artifact's {string} is byte-for-byte what the other node wrote",
			func(in CrossNode, p brine.Params, _ *brine.Recorder) error {
				rel, err := paramAt("the resolved artifact's {string} is byte-for-byte what the other node wrote", p, 0)
				if err != nil {
					return err
				}
				want, ok := in.Digests[rel]
				if !ok {
					return fmt.Errorf("no step in this scenario wrote %q onto the other node, so there is "+
						"nothing to compare against", rel)
				}
				f, err := os.Open(in.resolved(rel))
				if err != nil {
					return fmt.Errorf("%w (%s)", in.missing(rel, err), in.answer())
				}
				defer f.Close()
				h := sha256.New()
				if _, err := io.Copy(h, f); err != nil {
					return fmt.Errorf("read the delivered %q: %w", rel, err)
				}
				if got := hex.EncodeToString(h.Sum(nil)); got != want {
					return fmt.Errorf("the delivered %q is not the file the other node wrote: sha256 %s, expected %s",
						rel, got, want)
				}
				return nil
			},
		),

		CheckInt[CrossNode]("the resolve is refused with {int}",
			"the daemon's status",
			func(in CrossNode) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Status, nil
			},
			func(in CrossNode) string { return "body: " + abbrev(in.Body) }),

		CheckContains[CrossNode]("the refusal names {string}",
			"the daemon's refusal",
			func(in CrossNode) (string, error) {
				if in.Status >= 200 && in.Status < 300 {
					return "", fmt.Errorf("expected a refusal, the daemon answered %d", in.Status)
				}
				return in.Body, nil
			}),

		// The status alone cannot say this. A daemon that reported a miss
		// after leaving a half-extracted tree at the destination has still
		// handed the next step something to trip over.
		CheckThat[CrossNode]("nothing was left at the destination", func(in CrossNode) error {
			if in.Dest == "" {
				return fmt.Errorf("no resolve was asked for, so there is no destination to look at")
			}
			entries, err := os.ReadDir(in.Dest)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("look at the destination %s: %w", in.Dest, err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return fmt.Errorf("expected nothing at the destination %s, found %v", in.Dest, names)
		}),
	}
}

// lastPeerArtifact finds the artifact a link step is being asked to modify.
//
// The sentences read "that artifact", meaning the one the previous Given put
// on the peer, and the state does not carry a cursor. Rather than adding one —
// which every OTHER step would then have to maintain correctly — this reads
// the peer's own steps/ directory, which has exactly one artifact in these
// scenarios, and says so plainly when it does not.
func lastPeerArtifact(in CrossNode) (string, error) {
	root := filepath.Join(in.Peer.Root, "steps")
	var dirs []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || path == root {
			return err
		}
		// An artifact is the directory a key names, and the keys these
		// scenarios use are two segments deep ("linked/output").
		if rel, relErr := filepath.Rel(root, path); relErr == nil && len(strings.Split(rel, string(filepath.Separator))) == 2 {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("look for the artifact on the other node: %w", err)
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("\"that artifact\" is ambiguous: the other node holds %d artifacts (%v). "+
			"Name it in the step that puts it there, and add a link to that one", len(dirs), dirs)
	}
	return dirs[0], nil
}
