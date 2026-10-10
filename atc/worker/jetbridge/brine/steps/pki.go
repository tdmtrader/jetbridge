package steps

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// mtlsMaterial is one small PKI: a CA, a server certificate the daemon serves,
// and a client certificate the ATC presents. Real certificates verified by
// real Go TLS — there is no other way to make "the handshake succeeded" mean
// anything.
type mtlsMaterial struct {
	caPEM      []byte
	clientCert []byte
	clientKey  []byte

	// serverCert and serverKey are in the form a PROCESS needs: every fixture
	// starts the real daemon, which reads --tls-cert and --tls-key off disk.
	serverCert []byte
	serverKey  []byte
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
	clientPEM, clientKeyPEM, err := signLeaf(ca, caKey, 3, "atc-client",
		x509.ExtKeyUsageClientAuth, nil, nil)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("client certificate: %w", err)
	}

	return mtlsMaterial{
		caPEM:      caPEM,
		clientCert: clientPEM,
		clientKey:  clientKeyPEM,
		serverCert: serverPEM,
		serverKey:  serverKeyPEM,
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

// writeMTLSCredentials writes the real daemon and ATC credentials into one
// scenario-owned directory. Missing ATC paths are derived beneath this owned
// directory too, so another process cannot accidentally satisfy the setup.
func writeMTLSCredentials(m mtlsMaterial) (string, error) {
	dir, err := AttributedTempDir("brine-daemon-mtls-")
	if err != nil {
		return "", fmt.Errorf("make certificate directory: %w", err)
	}
	for name, body := range map[string][]byte{
		"client.crt": m.clientCert,
		"client.key": m.clientKey,
		"server.crt": m.serverCert,
		"server.key": m.serverKey,
		"ca.crt":     m.caPEM,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	return dir, nil
}
