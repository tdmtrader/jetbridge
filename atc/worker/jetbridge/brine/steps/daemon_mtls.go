package steps

// Daemon mTLS and warm ownership: the executable half of
// ../features/daemon-mtls.feature.
//
// Two families live here, and they share one conviction with daemon.go — the
// double is a REAL implementation with one named behavioural difference, and it
// records NOTHING. There is no gotClientCert, no restores counter, no
// warmedNode field written by a handler. Every assertion below is on what a
// production call handed back: the artifact bytes, the refusal text, or the
// node a real probe reports the cache is now resident on.
//
// Family 1 — mTLS. The suite this replaces (daemon_tls_test.go) reached into
// `client.Transport.(*http.Transport).TLSClientConfig` and counted
// Certificates, checked RootCAs != nil, compared ServerName to a string. Those
// are fields. A certificate that is configured but never presented, or a
// ServerName copied into a config that is then never used to verify anything,
// passes all of them. The daemon here is a real TLS server that only serves
// clients it can authenticate, holding a certificate that names the headless
// service and not the loopback address it is actually dialled at — which is
// exactly the deployed shape, and the shape the "certificate is valid for
// 127.0.0.1, not <podIP>" regression was about. The observable is whether the
// artifact arrives.
//
// Family 2 — which node owns a warm. warmOwners keys its rendezvous hash on
// the NODE NAME rather than the pod IP, so that a DaemonSet rolling update —
// every pod IP replaced at once, the commonest churn event in the cluster —
// does not move who owns a key and invalidate every warmed multi-gigabyte
// cache at the same moment. Observing that needs more than one daemon, and a
// daemon has to be reachable at an address of its own.
//
// One listener serves both nodes, as two VIRTUAL HOSTS. That is the named
// behavioural difference: in the cluster each node's daemon is its own pod
// with its own address, and here they are one process distinguished by the
// address they were addressed AS — which is a thing real HTTP servers do, and
// which keeps the ATC's choice of daemon expressed the only way it is ever
// expressed, in the address it dialled. Each virtual host keeps its own node's
// disk; the durable bucket behind them is shared, because it is one bucket.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// -----------------------------------------------------------------------
// Domain states — mTLS
// -----------------------------------------------------------------------

// MTLSPlan is a TLS-speaking artifact daemon and the ATC that will talk to it,
// under description. Nothing is wired until the When step: whether the daemon
// demands a client certificate, and which names its own certificate carries,
// have to be decided before a listener exists.
type MTLSPlan struct {
	Ctx       context.Context
	Namespace string
	Service   string

	// Artifacts is what the daemon holds. A key it does not hold is a 404,
	// which is how a scenario can tell arrival from mere connectivity.
	Artifacts map[string]string

	// RequireClientCert makes the daemon refuse anyone it cannot authenticate,
	// which is the whole of what "mTLS" means on the wire.
	RequireClientCert bool

	// CertOmitsAddress narrows the daemon's own certificate to the service DNS
	// name alone, dropping the address it is dialled at. That is the deployed
	// shape — pod IPs are handed out at schedule time and can be in no SAN —
	// and it is what makes the ATC's ServerName load-bearing. A scenario that
	// is about something else leaves it off, so the certificate names both and
	// the hostname question cannot be what decided the outcome.
	CertOmitsAddress bool

	// ATCHasCerts and ATCCertsMissing are the two configurations an operator
	// actually ends up in: the certificate files are where the chart put them,
	// or a bad rollout means they are not there at all.
	ATCHasCerts     bool
	ATCCertsMissing bool
}

// MTLSFetch is what the consumer got. The error is a value, so a refusal is
// assertable rather than fatal to the scenario.
type MTLSFetch struct {
	Raw     []byte
	Err     error
	Message string
}

// -----------------------------------------------------------------------
// The TLS material
// -----------------------------------------------------------------------

// mtlsMaterial is one small PKI: a CA, a server certificate the daemon serves,
// and a client certificate the ATC presents. Real certificates verified by
// real Go TLS — there is no other way to make "the handshake succeeded" mean
// anything.
type mtlsMaterial struct {
	caPEM      []byte
	clientCert []byte
	clientKey  []byte
	server     tls.Certificate
	clientPool *x509.CertPool
}

func mintMTLSMaterial(dnsName string, ips []net.IP) (mtlsMaterial, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("generate CA key: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "brine-daemon-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("create CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("parse CA certificate: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverPEM, serverKeyPEM, err := signLeaf(ca, caKey, 2, "daemon-server",
		x509.ExtKeyUsageServerAuth, []string{dnsName}, ips)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("server certificate: %w", err)
	}
	serverCert, err := tls.X509KeyPair(serverPEM, serverKeyPEM)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("assemble server key pair: %w", err)
	}

	clientPEM, clientKeyPEM, err := signLeaf(ca, caKey, 3, "atc-client",
		x509.ExtKeyUsageClientAuth, nil, nil)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("client certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return mtlsMaterial{}, fmt.Errorf("the minted CA certificate does not parse as PEM")
	}

	return mtlsMaterial{
		caPEM:      caPEM,
		clientCert: clientPEM,
		clientKey:  clientKeyPEM,
		server:     serverCert,
		clientPool: pool,
	}, nil
}

func signLeaf(
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	serial int64,
	commonName string,
	usage x509.ExtKeyUsage,
	dnsNames []string,
	ips []net.IP,
) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		nil
}

// writeATCCredentials puts the client certificate and the daemon CA where the
// ATC's config says they are. The chart mounts them from a Secret; here they
// are files in a directory that is removed when the scenario ends.
func writeATCCredentials(m mtlsMaterial) (dir string, cfgCert, cfgKey, cfgCA string, err error) {
	dir, err = os.MkdirTemp("", "brine-daemon-mtls-")
	if err != nil {
		return "", "", "", "", fmt.Errorf("make certificate directory: %w", err)
	}
	cfgCert = filepath.Join(dir, "client.crt")
	cfgKey = filepath.Join(dir, "client.key")
	cfgCA = filepath.Join(dir, "ca.crt")
	for path, body := range map[string][]byte{
		cfgCert: m.clientCert,
		cfgKey:  m.clientKey,
		cfgCA:   m.caPEM,
	} {
		if writeErr := os.WriteFile(path, body, 0o600); writeErr != nil {
			os.RemoveAll(dir)
			return "", "", "", "", fmt.Errorf("write %s: %w", path, writeErr)
		}
	}
	return dir, cfgCert, cfgKey, cfgCA, nil
}

// -----------------------------------------------------------------------
// The TLS daemon double
// -----------------------------------------------------------------------

// mtlsArtifactHandler answers the one route these scenarios use, and 404s
// everything else — so a client that asks for a key the daemon does not have
// gets nothing, and arrival means arrival.
func mtlsArtifactHandler(artifacts map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/artifacts/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, held := artifacts[strings.TrimPrefix(r.URL.Path, "/artifacts/")]
		if !held {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

// serve starts the daemon described by the plan and returns it along with the
// TLS material it was built from.
func (p MTLSPlan) serve() (*httptest.Server, mtlsMaterial, error) {
	dnsName := fmt.Sprintf("%s.%s.svc", p.Service, p.Namespace)

	var ips []net.IP
	if !p.CertOmitsAddress {
		ips = []net.IP{net.ParseIP("127.0.0.1")}
	}
	material, err := mintMTLSMaterial(dnsName, ips)
	if err != nil {
		return nil, mtlsMaterial{}, err
	}

	server := httptest.NewUnstartedServer(mtlsArtifactHandler(p.Artifacts))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{material.server},
	}
	if p.RequireClientCert {
		server.TLS.ClientCAs = material.clientPool
		server.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	}
	server.StartTLS()

	return server, material, nil
}

// config is the ATC side: TLS on, and the certificate paths in whichever of
// the two states the scenario described.
func (p MTLSPlan) config(port int, cert, key, ca string) jetbridge.Config {
	return jetbridge.Config{
		Namespace:                p.Namespace,
		ArtifactDaemonService:    p.Service,
		ArtifactDaemonPort:       port,
		ArtifactDaemonTLSEnabled: true,
		ArtifactDaemonTLSCert:    cert,
		ArtifactDaemonTLSKey:     key,
		ArtifactDaemonTLSCACert:  ca,
	}
}

// -----------------------------------------------------------------------
// Domain states — warm ownership
// -----------------------------------------------------------------------

// WarmRollPlan is a two-node cluster with a durable bucket behind it, plus
// what each warm so far was answered with. The Whens take it in and out, so a
// scenario can warm, let the cluster change underneath, and warm again.
type WarmRollPlan struct {
	Ctx      context.Context
	Daemons  *rollingDaemons
	Server   *httptest.Server
	Cluster  *fake.Clientset
	Client   *jetbridge.DaemonClient
	Backend  *jetbridge.DaemonSetBackend
	Rolled   bool
	Observed []warmObservation
}

// warmObservation is one warm's outcome: whether a cache came back at all, and
// which node a subsequent probe reports is now holding it. Both halves are
// production output — FindResourceCache's own answer, and ProbeResourceCache's
// — not anything a handler wrote down.
type warmObservation struct {
	Served bool
	Node   string
}

// rollingDaemons is the artifact daemon on every node, as one listener.
//
// Each address is a pod; nodeFor says which node's pod is answering at that
// address today, which is the only thing a DaemonSet roll actually changes.
// disk is per node, because a node's hostPath belongs to the node and survives
// its pod. store is the durable bucket, shared, because there is one bucket.
type rollingDaemons struct {
	mu      sync.Mutex
	nodeFor map[string]string
	disk    map[string]map[string]string
	store   map[string]string
}

func newRollingDaemons() *rollingDaemons {
	return &rollingDaemons{
		nodeFor: map[string]string{},
		disk:    map[string]map[string]string{},
		store:   map[string]string{},
	}
}

// handler answers as whichever node's daemon it was addressed as. A request
// naming an address no pod answers on is a 404, exactly as it would be if
// nothing were listening there.
//
// There is no /artifacts route: no scenario in this family reads the bytes.
// What is being described is which node ends up holding the cache, and a
// second scenario re-proving that a warmed cache is readable would only repeat
// step-closing.feature.
func (d *rollingDaemons) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()

		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		node, answering := d.nodeFor[host]
		if !answering {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Capability rides every response at any status, which is how the ATC
		// learns the cluster can warm at all.
		w.Header().Set(jetbridge.DurableTierHeader, "enabled")

		switch {
		case strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
			if _, held := d.disk[node][key]; held {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPost && r.URL.Path == "/durable/restore":
			var body struct {
				Key        string `json:"key"`
				DurableKey string `json:"durable_key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)

			content, inStore := d.store[body.DurableKey]
			if !inStore {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// A restore makes its own answer true: the object lands on THIS
			// node's disk, which is what makes "where did it land" a question
			// the cluster can be asked afterwards.
			if d.disk[node] == nil {
				d.disk[node] = map[string]string{}
			}
			d.disk[node][body.Key] = content
			w.WriteHeader(http.StatusCreated)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// The two addresses the two pods answer on.
//
// Two spellings of loopback, because a second listener cannot have a second
// address: the ATC reaches every daemon on ONE port — that is how the
// DaemonSet is deployed, one containerPort — and a machine this suite has to
// run on unattended cannot be relied on to let anything bind a loopback alias.
// Two names for one listener is what is left, and it is enough: an address is
// all the ATC ever has to go on, and the daemon behind each one keeps its own
// node's disk.
//
// The roll hands the same two addresses back out the other way round rather
// than inventing new ones. That is a faithful roll — pods come back with
// addresses from a pool, and an address a departing pod held can be handed to
// the pod that replaces it on another node — and it is the sharper form of the
// question: production must follow the NODE, not the address, and here the two
// answers differ.
const (
	warmAddrOne = "127.0.0.1"
	warmAddrTwo = "localhost"
)

const (
	warmNodeOne = "node-a"
	warmNodeTwo = "node-b"
)

func (p WarmRollPlan) nodeOn(addr string) string {
	if p.Rolled {
		if addr == warmAddrOne {
			return warmNodeTwo
		}
		return warmNodeOne
	}
	if addr == warmAddrOne {
		return warmNodeOne
	}
	return warmNodeTwo
}

// publish writes the EndpointSlice the DaemonSet's headless Service would,
// naming each pod's address and the node it runs on.
func (p WarmRollPlan) publish() *discoveryv1.EndpointSlice {
	ready := true
	endpoints := make([]discoveryv1.Endpoint, 0, 2)
	for _, addr := range []string{warmAddrOne, warmAddrTwo} {
		node := p.nodeOn(addr)
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses:  []string{addr},
			NodeName:   &node,
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-brine",
			Namespace: "cicd",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: endpoints,
	}
}

func warmRollConfig(port int) jetbridge.Config {
	return jetbridge.Config{
		Namespace:                 "cicd",
		ArtifactDaemonService:     "artifact-daemon",
		ArtifactDaemonPort:        port,
		ArtifactDaemonHostPath:    "/artifact-store",
		ArtifactDaemonWarmTimeout: 5 * time.Second,
	}
}

// warm runs the consumer's action and then asks the cluster where the cache
// ended up. The second question is a plain production probe — the same call
// the next get step on any other web would make — so "which node owns this
// key" is answered by the runtime, not by the fixture.
func (p WarmRollPlan) warm(cacheKey, durableKey string) WarmRollPlan {
	obs := warmObservation{}

	_, found := p.Backend.FindResourceCache(p.Ctx, cacheKey, durableKey, "k8s-worker-1")
	obs.Served = found
	if found {
		probe, hit := p.Client.ProbeResourceCache(p.Ctx, cacheKey)
		if hit {
			obs.Node = probe.Node
		}
	}

	p.Observed = append(append([]warmObservation{}, p.Observed...), obs)
	return p
}

// -----------------------------------------------------------------------
// Steps
// -----------------------------------------------------------------------

// DaemonMTLSDefinitions covers the mTLS data plane and which node a durable
// warm lands on. Nothing here names a transport field, a URL or a request
// count.
func DaemonMTLSDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// --- mTLS: describing the daemon and the ATC ---

		brine.DefineMap[brine.Empty, MTLSPlan](
			"an artifact daemon serving over TLS",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (MTLSPlan, error) {
				return MTLSPlan{
					Ctx:       context.Background(),
					Namespace: "cicd",
					Service:   "artifact-daemon",
					Artifacts: map[string]string{},
				}, nil
			},
		),

		Refine[MTLSPlan]("it only serves clients whose certificate it can verify",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.RequireClientCert = true
				return in
			}),

		// The deployed shape. The chart issues the daemon a certificate for the
		// headless service; the address the ATC dials is a pod IP handed out
		// at schedule time, and is in no SAN.
		Refine[MTLSPlan]("its certificate names only the service, not the address it is dialled at",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.CertOmitsAddress = true
				return in
			}),

		Refine[MTLSPlan]("the daemon holds the artifact {string} containing {string}",
			func(in MTLSPlan, a Args) MTLSPlan {
				artifacts := map[string]string{}
				for k, v := range in.Artifacts {
					artifacts[k] = v
				}
				artifacts[a.String(0)] = a.String(1)
				in.Artifacts = artifacts
				return in
			}),

		Refine[MTLSPlan]("the ATC has its client certificate and the daemon's CA",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.ATCHasCerts = true
				return in
			}),

		Refine[MTLSPlan]("the ATC is configured for mTLS but its certificate files are not there",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.ATCCertsMissing = true
				return in
			}),

		// --- mTLS: reading ---

		// Everything is wired here, because the daemon's certificate and its
		// client-auth policy are settled by the Givens and a listener cannot
		// be started before them.
		brine.DefineMap[MTLSPlan, MTLSFetch](
			"a consumer reads the artifact {string} over mTLS",
			func(in MTLSPlan, p brine.Params, _ *brine.Recorder) (MTLSFetch, error) {
				key, ok := p.GetString(0)
				if !ok {
					return MTLSFetch{}, fmt.Errorf("expected an artifact key parameter")
				}

				server, material, err := in.serve()
				if err != nil {
					return MTLSFetch{}, err
				}
				defer server.Close()

				host, port, err := hostAndPort(server)
				if err != nil {
					return MTLSFetch{}, err
				}

				cert, keyPath, ca := "", "", ""
				switch {
				case in.ATCHasCerts:
					dir, c, k, a, writeErr := writeATCCredentials(material)
					if writeErr != nil {
						return MTLSFetch{}, writeErr
					}
					defer os.RemoveAll(dir)
					cert, keyPath, ca = c, k, a
				case in.ATCCertsMissing:
					// Configured, and pointing at nothing — a Secret that did
					// not project, a key rotated out from under a running web.
					cert = filepath.Join(os.TempDir(), "brine-absent", "client.crt")
					keyPath = filepath.Join(os.TempDir(), "brine-absent", "client.key")
					ca = filepath.Join(os.TempDir(), "brine-absent", "ca.crt")
				default:
					return MTLSFetch{}, fmt.Errorf("the scenario did not say how the ATC is configured for mTLS")
				}

				vol := jetbridge.NewDaemonSetVolumeFromIP(
					key, key, "k8s-worker-1", host, in.config(port, cert, keyPath, ca))

				stream, streamErr := vol.StreamOut(in.Ctx, ".", nil)
				if streamErr != nil {
					return MTLSFetch{Err: streamErr, Message: streamErr.Error()}, nil
				}
				raw, readErr := io.ReadAll(stream)
				_ = stream.Close()
				if readErr != nil {
					return MTLSFetch{Err: readErr, Message: readErr.Error()}, nil
				}
				return MTLSFetch{Raw: raw}, nil
			},
		),

		// --- mTLS: checks ---

		CheckString[MTLSFetch]("the artifact arrives over mTLS as {string}",
			"the artifact",
			func(in MTLSFetch) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the read failed: %s", in.Message)
				}
				return string(in.Raw), nil
			}),

		// Asserts both halves of "loudly": that nothing came back, and that
		// what the operator is told names the cause. A client that quietly
		// dropped to an unauthenticated path would fail the first; one that
		// failed for some unrelated reason would fail the second.
		CheckContains[MTLSFetch]("the read is refused, naming {string}",
			"the refusal",
			func(in MTLSFetch) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf(
						"expected the read to be refused, but %d bytes arrived: %q",
						len(in.Raw), string(in.Raw))
				}
				return in.Message, nil
			}),

		// --- warm ownership ---

		brine.DefineMap[brine.Empty, WarmRollPlan](
			"artifact daemons on two nodes with one durable store behind them",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (WarmRollPlan, error) {
				daemons := newRollingDaemons()
				server := httptest.NewServer(daemons.handler())

				_, port, err := hostAndPort(server)
				if err != nil {
					server.Close()
					return WarmRollPlan{}, err
				}

				plan := WarmRollPlan{
					Ctx:     context.Background(),
					Daemons: daemons,
					Server:  server,
				}
				daemons.nodeFor[warmAddrOne] = plan.nodeOn(warmAddrOne)
				daemons.nodeFor[warmAddrTwo] = plan.nodeOn(warmAddrTwo)

				// Both pods have to be genuinely reachable or the ranking is
				// unobservable and the scenario would pass for the wrong
				// reason. Say so here rather than leaving a mystery timeout.
				if err := warmAddressReachable(port, warmAddrTwo); err != nil {
					server.Close()
					return WarmRollPlan{}, err
				}

				plan.Cluster = fake.NewSimpleClientset(plan.publish())
				plan.Client = jetbridge.NewDaemonClient(
					lagertest.NewTestLogger("brine-warm-roll"),
					plan.Cluster, "cicd", "artifact-daemon", port, nil,
				)
				plan.Backend = jetbridge.NewDaemonSetBackend(
					warmRollConfig(port), jetbridge.NewArtifactLocator(), nil)
				plan.Backend.SetDaemonClient(plan.Client)

				return plan, nil
			},
		),

		// The object is named by its CONTENT key, which is the name it has in
		// the bucket — the node-local alias it will be registered under is the
		// get step's business, and is what the warm asks for.
		Refine[WarmRollPlan]("only the durable store holds the object {string} containing {string}",
			func(in WarmRollPlan, a Args) WarmRollPlan {
				in.Daemons.mu.Lock()
				in.Daemons.store[a.String(0)] = a.String(1)
				in.Daemons.mu.Unlock()
				return in
			}),

		brine.DefineMap[WarmRollPlan, WarmRollPlan](
			"a get step warms the resource cache {string} under content key {string}",
			func(in WarmRollPlan, p brine.Params, _ *brine.Recorder) (WarmRollPlan, error) {
				cacheKey, _ := p.GetString(0)
				durableKey, ok := p.GetString(1)
				if !ok {
					return WarmRollPlan{}, fmt.Errorf("expected a cache key and a content key")
				}
				return in.warm(cacheKey, durableKey), nil
			},
		),

		// Age-based reclamation runs on every node, so the copy goes wherever
		// it landed. Without this the next lookup is a local hit and never
		// asks the ranking anything.
		brine.DefineMap[WarmRollPlan, WarmRollPlan](
			"the sweeper reclaims every node's copy of {string}",
			func(in WarmRollPlan, p brine.Params, _ *brine.Recorder) (WarmRollPlan, error) {
				key, ok := p.GetString(0)
				if !ok {
					return WarmRollPlan{}, fmt.Errorf("expected a cache key parameter")
				}
				in.Daemons.mu.Lock()
				for _, disk := range in.Daemons.disk {
					delete(disk, key)
				}
				in.Daemons.mu.Unlock()
				return in, nil
			},
		),

		// The roll. Every pod is replaced and the addresses come back attached
		// to different nodes, which is what an IP pool does — the nodes are
		// the only thing that did not move.
		brine.DefineMap[WarmRollPlan, WarmRollPlan](
			"the DaemonSet rolls and every pod comes back answering on a different address",
			func(in WarmRollPlan, _ brine.Params, _ *brine.Recorder) (WarmRollPlan, error) {
				in.Rolled = true

				in.Daemons.mu.Lock()
				in.Daemons.nodeFor[warmAddrOne] = in.nodeOn(warmAddrOne)
				in.Daemons.nodeFor[warmAddrTwo] = in.nodeOn(warmAddrTwo)
				in.Daemons.mu.Unlock()

				if _, err := in.Cluster.DiscoveryV1().EndpointSlices("cicd").
					Update(in.Ctx, in.publish(), metav1.UpdateOptions{}); err != nil {
					return WarmRollPlan{}, fmt.Errorf("republish daemon endpoints after the roll: %w", err)
				}
				return in, nil
			},
		),

		// The last action in the family, so it takes the cluster down: the
		// resource plane cannot own an httptest server a step created.
		brine.DefineMap[WarmRollPlan, WarmRollPlan](
			"a get step warms the resource cache {string} under content key {string} again",
			func(in WarmRollPlan, p brine.Params, _ *brine.Recorder) (WarmRollPlan, error) {
				defer in.Server.Close()

				cacheKey, _ := p.GetString(0)
				durableKey, ok := p.GetString(1)
				if !ok {
					return WarmRollPlan{}, fmt.Errorf("expected a cache key and a content key")
				}
				return in.warm(cacheKey, durableKey), nil
			},
		),

		CheckThat[WarmRollPlan]("every warm was served",
			func(in WarmRollPlan) error {
				if len(in.Observed) == 0 {
					return fmt.Errorf("no warm was attempted")
				}
				for i, obs := range in.Observed {
					if !obs.Served {
						return fmt.Errorf("warm %d was not served from the durable store", i+1)
					}
				}
				return nil
			}),

		CheckThat[WarmRollPlan]("both warms left the cache on the same node",
			func(in WarmRollPlan) error {
				if len(in.Observed) != 2 {
					return fmt.Errorf("expected two warms to compare, got %d", len(in.Observed))
				}
				for i, obs := range in.Observed {
					if obs.Node == "" {
						return fmt.Errorf(
							"warm %d left the cache on no node the cluster can name, so ownership is unobservable", i+1)
					}
				}
				if in.Observed[0].Node != in.Observed[1].Node {
					return fmt.Errorf(
						"the cache moved from %s to %s across a pod-address roll; every warmed copy in the cluster moves with it",
						in.Observed[0].Node, in.Observed[1].Node)
				}
				return nil
			}),
	}
}

// reachable confirms an address really does reach the listener, so a scenario
// that depends on two pods answering fails here with a reason rather than
// later with a ranking that could not be observed.
func warmAddressReachable(port int, addr string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s:%d/resource-caches/brine-reachability", addr, port)
	resp, err := client.Head(url)
	if err != nil {
		return fmt.Errorf("the second daemon pod is not reachable at %q: %w", addr, err)
	}
	resp.Body.Close()
	return nil
}
