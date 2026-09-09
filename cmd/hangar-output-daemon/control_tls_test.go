package main

// The control API's transport, now that its caller is off-node.
//
// Phase 3 listened on 127.0.0.1 in plaintext and that was the right shape: every
// caller was a pod on this node and the capability is a signed, facet-scoped,
// single-use bearer token. Phase 4 wires the ATC, which is on the web pod, and a
// bearer token over plaintext off-node is interceptable inside its TTL.
//
// The pair is the whole test. Every control-plane route requires a verified
// client certificate, and the ONE route whose caller is a container in a Pod on
// this node stays reachable without one -- because the capture control init
// holds no client certificate and Req 24 will not give it one. A daemon that
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
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	server := NewServer(fixture.daemon, fixture.ledger, fixture.source, verifier, "")
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
	call := func(client *http.Client, path string, facet executioncontrol.Facet,
		operation string, body any) int {
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
		_, _ = io.Copy(io.Discard, response.Body)

		return response.StatusCode
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
	} {
		if code := call(withCert, row.path, row.facet, row.operation, row.body); code != http.StatusOK {
			t.Fatalf("%s answered %d to the control plane's own certificate", row.path, code)
		}
		if code := call(withoutCert, row.path, row.facet, row.operation, row.body); code != http.StatusUnauthorized {
			t.Errorf("%s answered %d with no client certificate; the ATC's capability would be "+
				"honoured from anything that could reach this port", row.path, code)
		}
	}

	// And the node-local hold, from a caller with no certificate at all: this
	// is the capture control init, and it must still work.
	code := call(withoutCert, "/capture/v1/hold", output.CaptureFacet, "hold", admission())
	if code != http.StatusOK {
		t.Errorf("the node-local capture hold answered %d without a client certificate; the "+
			"control init holds none and cannot be given one, so this refusal would stop every "+
			"capture-selected producer from starting", code)
	}

	// The unauthenticated surfaces stay unauthenticated: a readiness probe has
	// no certificate either.
	for _, path := range []string{"/healthz", "/readyz", "/handshake"} {
		response, err := withoutCert.Get(secured.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s answered %d to a probe with no client certificate", path, response.StatusCode)
		}
	}
}

// A partial TLS configuration has no honest reading, so it is refused at
// startup rather than silently serving plaintext to a control plane that
// believes it is dialling https.
func TestAPartiallyConfiguredControlTLSIsRefusedAtStartup(t *testing.T) {
	base := validConfig(t, "http://127.0.0.1:1", "bucket-that-is-never-reached")

	// The two coherent configurations first.
	if err := base.Validate(); err != nil {
		t.Fatalf("a daemon with no TLS at all was refused: %v", err)
	}
	full := base
	full.TLSCert, full.TLSKey, full.TLSCACert = "a.crt", "a.key", "ca.crt"
	if err := full.Validate(); err != nil {
		t.Fatalf("a fully configured daemon was refused: %v", err)
	}
	if !full.TLSEnabled() {
		t.Error("a daemon with all three files does not report TLS enabled")
	}

	for name, partial := range map[string]Config{
		"only a certificate":      {TLSCert: "a.crt"},
		"a certificate and a key": {TLSCert: "a.crt", TLSKey: "a.key"},
		"a key and a CA":          {TLSKey: "a.key", TLSCACert: "ca.crt"},
		"only a client CA":        {TLSCACert: "ca.crt"},
	} {
		config := base
		config.TLSCert, config.TLSKey, config.TLSCACert = partial.TLSCert, partial.TLSKey, partial.TLSCACert
		err := config.Validate()
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("%s was admitted: %v", name, err)

			continue
		}
		if !strings.Contains(err.Error(), "must also be set") {
			t.Errorf("%s was refused without naming what is missing: %v", name, err)
		}
		if config.TLSEnabled() {
			t.Errorf("%s reports TLS enabled", name)
		}
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
