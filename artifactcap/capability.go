// Package artifactcap signs and verifies the narrowly-scoped capabilities
// used by artifact init containers. It deliberately has no transport or
// filesystem dependencies so both the ATC and artifact daemon share the
// exact same wire contract.
package artifactcap

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	tokenPrefix           = "jb-resolve-v1"
	acknowledgementPrefix = "jb-resolve-ack-v1"
	receiptFilenamePrefix = ".jetbridge-resolve-receipt-"
	keySize               = sha256.Size
	nonceSize             = 16
)

var ErrInvalidCapability = errors.New("invalid artifact resolve capability")

// LoadKeyFile reads the raw 32-byte shared key. It intentionally rejects
// whitespace trimming and alternative encodings so operator mistakes fail
// closed and the web and daemon cannot silently derive different keys.
func LoadKeyFile(name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("artifact capability key file is required")
	}
	key, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read artifact capability key file: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("artifact capability key file must contain exactly %d bytes", keySize)
	}
	return append([]byte(nil), key...), nil
}

type resolveClaims struct {
	Domain      string `json:"domain"`
	Version     int    `json:"version"`
	SourceKey   string `json:"source_key"`
	Destination string `json:"destination"`
	ExpiresAt   int64  `json:"expires_at"`
	Nonce       string `json:"nonce"`
}

type Signer struct {
	key [keySize]byte
}

type Verifier struct {
	key [keySize]byte
}

func NewSigner(key []byte) (*Signer, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("artifact capability key must contain exactly %d bytes", keySize)
	}
	var copied [keySize]byte
	copy(copied[:], key)
	return &Signer{key: copied}, nil
}

func NewVerifier(key []byte) (*Verifier, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("artifact capability key must contain exactly %d bytes", keySize)
	}
	var copied [keySize]byte
	copy(copied[:], key)
	return &Verifier{key: copied}, nil
}

func (s *Signer) SignResolve(sourceKey, destination string, expiresAt time.Time) (string, error) {
	if sourceKey == "" || destination == "" || expiresAt.IsZero() {
		return "", fmt.Errorf("%w: source, destination, and expiry are required", ErrInvalidCapability)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate artifact capability nonce: %w", err)
	}
	claims := resolveClaims{
		Domain:      tokenPrefix,
		Version:     1,
		SourceKey:   sourceKey,
		Destination: destination,
		ExpiresAt:   expiresAt.Unix(),
		Nonce:       base64.RawURLEncoding.EncodeToString(nonce),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal artifact capability: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := tokenPrefix + "." + encoded
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Signer) ResolveAcknowledgement(token string) (string, error) {
	return resolveAcknowledgement(s.key[:], token)
}

func (v *Verifier) ResolveAcknowledgement(token string) (string, error) {
	return resolveAcknowledgement(v.key[:], token)
}

func resolveAcknowledgement(key []byte, token string) (string, error) {
	if !validTokenMAC(key, token) {
		return "", ErrInvalidCapability
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(acknowledgementPrefix + "\x00" + token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ResolveReceiptFilename maps an authenticated acknowledgement to the one
// opaque file name the resolving daemon must materialize in the destination.
// Only canonical raw-URL SHA-256 values are accepted, keeping the result a
// single safe path component shared by daemon and init-container code.
func ResolveReceiptFilename(acknowledgement string) (string, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(acknowledgement)
	if err != nil || len(decoded) != sha256.Size || base64.RawURLEncoding.EncodeToString(decoded) != acknowledgement {
		return "", fmt.Errorf("%w: malformed resolve acknowledgement", ErrInvalidCapability)
	}
	return receiptFilenamePrefix + acknowledgement, nil
}

func validTokenMAC(key []byte, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] == "" || parts[2] == "" {
		return false
	}
	providedMAC, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(providedMAC) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	return hmac.Equal(providedMAC, mac.Sum(nil))
}

func (v *Verifier) VerifyResolve(token, sourceKey, destination string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] == "" || parts[2] == "" {
		return ErrInvalidCapability
	}
	signed := parts[0] + "." + parts[1]
	providedMAC, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(providedMAC) != sha256.Size {
		return ErrInvalidCapability
	}
	mac := hmac.New(sha256.New, v.key[:])
	_, _ = mac.Write([]byte(signed))
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return ErrInvalidCapability
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return ErrInvalidCapability
	}
	var claims resolveClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return ErrInvalidCapability
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidCapability
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(payload, canonical) {
		return ErrInvalidCapability
	}
	nonce, nonceErr := base64.RawURLEncoding.Strict().DecodeString(claims.Nonce)
	if claims.Domain != tokenPrefix || claims.Version != 1 || claims.SourceKey == "" || claims.Destination == "" || claims.ExpiresAt <= 0 || nonceErr != nil || len(nonce) != nonceSize {
		return ErrInvalidCapability
	}
	if !hmac.Equal([]byte(claims.SourceKey), []byte(sourceKey)) || !hmac.Equal([]byte(claims.Destination), []byte(destination)) {
		return ErrInvalidCapability
	}
	if !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return ErrInvalidCapability
	}
	return nil
}
