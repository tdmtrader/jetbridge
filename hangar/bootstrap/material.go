package bootstrap

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// RandomKeySize is the length of every symmetric key in the inventory.
const RandomKeySize = 32

// MinimumTokenLength is the shortest store token the disk store accepts.
const MinimumTokenLength = 32

// certificateLifetime is how long a generated certificate is valid. The
// bootstrap never renews (spec: rotation and renewal are out of scope), so it
// is long; the trusted readers, not expiry, bound who may use the keys.
const certificateLifetime = 20 * 365 * 24 * time.Hour

func randomKey() ([]byte, error) {
	key := make([]byte, RandomKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func validateRandomKey(key []byte) error {
	if len(key) != RandomKeySize {
		return fmt.Errorf("holds %d bytes, want exactly %d", len(key), RandomKeySize)
	}
	return nil
}

// newEd25519Key returns a PKCS#8 PEM private key and its raw public half.
func newEd25519Key() ([]byte, ed25519.PublicKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), public, nil
}

// ed25519PublicHalf validates a PKCS#8 PEM Ed25519 private key and returns its
// public half.
func ed25519PublicHalf(keyPEM []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("is not one PKCS#8 PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("is not a PKCS#8 key: %w", err)
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("is a %T, not an Ed25519 key", key)
	}
	return private.Public().(ed25519.PublicKey), nil
}

// storeTokenNames are the disk store's four principals.
var storeTokenNames = []string{"input", "publisher", "inventory", "reclaimer"}

// newStoreTokens returns the four distinct tokens and the server.json mapping
// them, keyed as the store Secret holds them.
func newStoreTokens() (map[string][]byte, error) {
	data := map[string][]byte{}
	server := map[string]string{}
	for _, name := range storeTokenNames {
		raw, err := randomKey()
		if err != nil {
			return nil, err
		}
		token := hex.EncodeToString(raw)
		data[name] = []byte(token)
		server[name] = token
	}
	body, err := json.Marshal(server)
	if err != nil {
		return nil, err
	}
	data["server.json"] = body
	return data, nil
}

func validateStoreTokens(data map[string][]byte) error {
	seen := map[string]bool{}
	for _, name := range storeTokenNames {
		token := string(data[name])
		if len(token) < MinimumTokenLength {
			return fmt.Errorf("token %q is %d characters, want at least %d", name, len(token), MinimumTokenLength)
		}
		if seen[token] {
			return fmt.Errorf("token %q repeats another principal's token", name)
		}
		seen[token] = true
	}
	var server map[string]string
	if err := json.Unmarshal(data["server.json"], &server); err != nil {
		return fmt.Errorf("server.json is not a JSON object: %w", err)
	}
	if len(server) != len(storeTokenNames) {
		return fmt.Errorf("server.json names %d principals, want %d", len(server), len(storeTokenNames))
	}
	for _, name := range storeTokenNames {
		if server[name] != string(data[name]) {
			return fmt.Errorf("server.json's %q token is not the Secret's", name)
		}
	}
	return nil
}

// certificateAuthority is a CA certificate and its key.
type certificateAuthority struct {
	cert    *x509.Certificate
	certPEM []byte
	key     crypto.Signer
	keyPEM  []byte
}

func newCertificateAuthority(commonName string) (certificateAuthority, error) {
	key, keyPEM, err := newECDSAKey()
	if err != nil {
		return certificateAuthority{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serialNumber(),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return certificateAuthority{}, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return certificateAuthority{}, err
	}
	return certificateAuthority{cert: cert, certPEM: pemCert(der), key: key, keyPEM: keyPEM}, nil
}

func loadCertificateAuthority(certPEM, keyPEM []byte) (certificateAuthority, error) {
	cert, err := parseCertificate(certPEM)
	if err != nil {
		return certificateAuthority{}, fmt.Errorf("ca.crt %w", err)
	}
	if !cert.IsCA {
		return certificateAuthority{}, errors.New("ca.crt is not a CA certificate")
	}
	key, err := parseSigner(keyPEM)
	if err != nil {
		return certificateAuthority{}, fmt.Errorf("ca.key %w", err)
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return certificateAuthority{}, errors.New("ca.key does not match ca.crt")
	}
	return certificateAuthority{cert: cert, certPEM: certPEM, key: key, keyPEM: keyPEM}, nil
}

// leafUsage is what a leaf certificate authenticates.
type leafUsage string

const (
	serverLeaf leafUsage = "server"
	clientLeaf leafUsage = "client"
)

func (ca certificateAuthority) issue(usage leafUsage, commonName string, dnsNames []string) (certPEM, keyPEM []byte, err error) {
	key, keyPEM, err := newECDSAKey()
	if err != nil {
		return nil, nil, err
	}
	extUsage := x509.ExtKeyUsageServerAuth
	if usage == clientLeaf {
		extUsage = x509.ExtKeyUsageClientAuth
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber(),
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certificateLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{extUsage},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, key.Public(), ca.key)
	if err != nil {
		return nil, nil, err
	}
	return pemCert(der), keyPEM, nil
}

// validateLeaf checks that a leaf pair matches, chains to the CA, carries the
// usage, and names every required DNS name.
func validateLeaf(ca *x509.Certificate, usage leafUsage, certPEM, keyPEM []byte, dnsNames []string) error {
	cert, err := parseCertificate(certPEM)
	if err != nil {
		return fmt.Errorf("tls.crt %w", err)
	}
	key, err := parseSigner(keyPEM)
	if err != nil {
		return fmt.Errorf("tls.key %w", err)
	}
	if !publicKeysEqual(cert.PublicKey, key.Public()) {
		return errors.New("tls.key does not match tls.crt")
	}
	extUsage := x509.ExtKeyUsageServerAuth
	if usage == clientLeaf {
		extUsage = x509.ExtKeyUsageClientAuth
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{extUsage}}); err != nil {
		return fmt.Errorf("tls.crt does not chain to its CA for %s use: %w", usage, err)
	}
	for _, name := range dnsNames {
		if err := cert.VerifyHostname(name); err != nil {
			return fmt.Errorf("tls.crt does not name %q", name)
		}
	}
	return nil
}

func newECDSAKey() (crypto.Signer, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("is not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("does not parse: %w", err)
	}
	return cert, nil
}

func parseSigner(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("is not a PEM key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("is not a PKCS#8 key: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("is a %T, which cannot sign", key)
	}
	return signer, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(equaler)
	return ok && ea.Equal(b)
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func serialNumber() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic(err)
	}
	return n
}

// Fingerprint is the loggable identity of public material: the first 16 hex
// digits of its SHA-256.
func Fingerprint(public []byte) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:8])
}
