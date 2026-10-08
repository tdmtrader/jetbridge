package outputplane

// The control API's transport, now that its caller is off-node.
//
// The control API first listened on 127.0.0.1 in plaintext and that was the
// right shape: every caller was a pod on this node and the capability is a
// signed, facet-scoped, single-use bearer token. Its caller is now the ATC,
// which is on the web pod, and a bearer token over plaintext off-node is
// interceptable inside its TTL.
//
// The pair is the whole test. Every control-plane route requires a verified
// client certificate, and the ONE route whose caller is a container in a Pod on
// this node stays reachable without one -- because the capture control init
// holds no client certificate and the task's Pod is never given one. A daemon that
// refused everything would be an outage; a daemon that refused nothing would be
// the exposure.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"reflect"
	"sort"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func TestTheControlAPIRequiresAClientCertificateExceptForTheNodeLocalHold(t *testing.T) {
	fixture := newRoutes(t, "")
	admitted(t, &fixture.ledgerFixture)

	pki := mintControlPKI(t)

	verifier, err := executioncontrol.NewCapabilityVerifier(capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	server := NewServer(fixture.daemon, fixture.ledger, fixture.capture, verifier, "")
	server.RequireClientCertificates()

	secured := httptest.NewUnstartedServer(server.Handler())
	secured.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pki.server},
		ClientCAs:    pki.pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	secured.StartTLS()
	defer secured.Close()

	// Two clients: one that presents the control plane's certificate, and one
	// that presents none. Both trust the daemon, so what differs between them
	// is only the client certificate.
	withCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pki.pool,
		Certificates: []tls.Certificate{pki.client},
	}}}
	withoutCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pki.pool,
	}}}

	nonce := 0
	answer := func(client *http.Client, path string, facet executioncontrol.Facet,
		operation string, body any) (int, []byte) {
		t.Helper()

		nonce++
		token, err := fixture.minter.Mint(executioncontrol.CapabilityClaims{
			Facet:           facet,
			Operation:       operation,
			Identity:        identity(1),
			ActivationEpoch: fixture.epoch,
		}, "tls-"+operation+"-"+strconv.Itoa(nonce))
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		request, err := http.NewRequest(http.MethodPost, secured.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		request.Header.Set(CapabilityHeader, string(token))
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer response.Body.Close()
		answered, _ := io.ReadAll(response.Body)

		return response.StatusCode, answered
	}
	call := func(client *http.Client, path string, facet executioncontrol.Facet,
		operation string, body any) int {
		t.Helper()
		code, _ := answer(client, path, facet, operation, body)

		return code
	}

	// The control plane's own operations, with its certificate. These are the
	// controls: a refusal below means nothing unless these succeed.
	for _, row := range []struct {
		path      string
		facet     executioncontrol.Facet
		operation string
		body      any
	}{
		{"/execution/v1/classify", executioncontrol.BaseFacet, "classify", identifiedBy(identity(1))},
		{"/execution/v1/cleanup-eligible", executioncontrol.BaseFacet, "cleanup-eligible",
			identifiedBy(identity(1))},
		// The release is a CAPTURE-facet route and it is still the control
		// plane's. Its presence here is what stops the node-local exemption
		// below from being read as "capture routes are exempt".
		{"/capture/v1/release", output.CaptureFacet, "release", output.CaptureReleaseRequest{
			ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}},
	} {
		if code := call(withCert, row.path, row.facet, row.operation, row.body); code != http.StatusOK {
			t.Fatalf("%s answered %d to the control plane's own certificate", row.path, code)
		}
		if code := call(withoutCert, row.path, row.facet, row.operation, row.body); code != http.StatusUnauthorized {
			t.Errorf("%s answered %d with no client certificate; the ATC's capability would be "+
				"honoured from anything that could reach this port", row.path, code)
		}
	}

	// The start inspection reads a signed node fact and is the control
	// plane's alone. This execution has no start, so the certificate is
	// answered with the ledger's 404 and its absence with 401.
	if code := call(withCert, "/execution/v1/start/inspect", executioncontrol.BaseFacet, "inspect-start",
		identifiedBy(identity(1))); code != http.StatusNotFound {
		t.Fatalf("the start inspection answered %d to the control plane's own certificate", code)
	}
	if code := call(withoutCert, "/execution/v1/start/inspect", executioncontrol.BaseFacet, "inspect-start",
		identifiedBy(identity(1))); code != http.StatusUnauthorized {
		t.Errorf("the start inspection answered %d with no client certificate", code)
	}

	// And the node-local hold, from a caller with no certificate at all: this
	// is the capture control init, and it must still work.
	// A step the table above did not release: a released step leaves a
	// tombstone that refuses every later hold.
	unreleased := holdRequest()
	unreleased.Output = "unreleased"
	code := call(withoutCert, "/capture/v1/hold", output.CaptureFacet, "hold", unreleased)
	if code != http.StatusOK {
		t.Errorf("the node-local capture hold answered %d without a client certificate; the "+
			"control init holds none and cannot be given one, so this refusal would stop every "+
			"capture-selected producer from starting", code)
	}

	// The probes stay unauthenticated: a readiness probe has no certificate.
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := withoutCert.Get(secured.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s answered %d to a probe with no client certificate", path, response.StatusCode)
		}
	}

	// The base handshake's callers are off-node, and every off-node route
	// requires a verified client certificate.
	response, err := withoutCert.Get(secured.URL + "/handshake")
	if err != nil {
		t.Fatalf("/handshake: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("/handshake answered %d with no client certificate; expected 401", response.StatusCode)
	}
	response, err = withCert.Get(secured.URL + "/handshake")
	if err != nil {
		t.Fatalf("/handshake with a certificate: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("/handshake answered %d with a client certificate", response.StatusCode)
	}
}

// EXACTLY ONE route is exempt from the client certificate, and it is named.
//
// The two tables above drive the routes a scenario names. This is the
// structural half: a route added later with `nodeLocal: true` -- or a
// misplaced `true` in a copied row -- would open the control API to anything
// that can reach the port, and no drive test names a route nobody wrote yet.
// `canonicalize` was added later, and this is what says it did not become exempt.
func TestExactlyOneControlRouteIsExemptFromTheClientCertificate(t *testing.T) {
	var exempt []string
	for pattern, declared := range (&Server{}).routes() {
		if declared.nodeLocal {
			exempt = append(exempt, pattern)
		}
	}
	sort.Strings(exempt)

	want := []string{"POST /capture/v1/hold"}
	if !reflect.DeepEqual(exempt, want) {
		t.Errorf("the routes exempt from the client certificate are %v; the exemption exists for "+
			"the capture control init, which is a container in a Pod on this node and holds no "+
			"certificate, and it is %v", exempt, want)
	}
}

type controlPKI struct {
	pool   *x509.CertPool
	server tls.Certificate
	client tls.Certificate
}

// mintControlPKI builds one small CA, a server certificate for 127.0.0.1 and a
// client certificate under the same CA.
func mintControlPKI(t *testing.T) controlPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hangar-output-control-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("signing the CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing the CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	leaf := func(serial int64, commonName string, usage []x509.ExtKeyUsage, ips []net.IP) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generating a leaf key: %v", err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: commonName},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  usage,
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("signing %s: %v", commonName, err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("encoding %s's key: %v", commonName, err)
		}
		certificate, err := tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		)
		if err != nil {
			t.Fatalf("assembling %s: %v", commonName, err)
		}

		return certificate
	}

	return controlPKI{
		pool: pool,
		server: leaf(2, "hangar-output-daemon",
			[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []net.IP{net.ParseIP("127.0.0.1")}),
		client: leaf(3, "concourse-web",
			[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil),
	}
}

// A peer artifact daemon presents the daemons' shared serving certificate as
// its client certificate (mirroring, cross-node fetches), and it verifies
// against the one CA. On the output plane's off-node routes it is refused:
// only the control plane drives a node's capture plane. The node-local hold is
// unaffected, and a different certificate from the same CA is admitted.
func TestTheDaemonsOwnCertificateCannotDriveTheOutputPlane(t *testing.T) {
	fixture := newRoutes(t, "")
	verifier, err := executioncontrol.NewCapabilityVerifier(capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	server := NewServer(fixture.daemon, fixture.ledger, fixture.capture, verifier, "")
	server.RequireClientCertificates()
	daemonDER := []byte("the daemons' serving certificate")
	server.RefuseDaemonCertificate(daemonDER)
	handler := server.Handler()

	call := func(method, path string, peer []byte) int {
		request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: peer}}}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		return recorder.Code
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/handshake"},
		{http.MethodPost, "/execution/v1/classify"},
		{http.MethodPost, "/capture/v1/seal"},
		{http.MethodPost, "/read/v1/stat"},
	} {
		code := call(route.method, route.path, daemonDER)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("%s %s answered %d to the daemons' own certificate", route.method, route.path, code)
		}
	}
	if code := call(http.MethodGet, "/handshake", []byte("the web's client certificate")); code != http.StatusOK {
		t.Errorf("the control plane's certificate was refused the handshake: %d", code)
	}
}

// The pod's readiness is the artifact daemon's, so a plane whose ledger is
// quarantined says so on its handshakes: a node that cannot answer for its
// ledger must not claim to speak the protocol.
func TestAnUnreadyPlaneRefusesItsHandshakes(t *testing.T) {
	fixture := newRoutes(t, "")
	verifier, err := executioncontrol.NewCapabilityVerifier(capabilitySecret(), time.Minute, fixture.clock)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(fixture.daemon, fixture.ledger, fixture.capture, verifier, "a record was quarantined").Handler()
	for _, path := range []string{"/handshake", "/capture/v1/handshake", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s answered %d with a quarantined ledger", path, recorder.Code)
		}
	}
}
