package hangar

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
	"time"
)

const (
	warrantDomain   = "hangar-materialize-v1"
	warrantVersion  = 1
	warrantKeyBytes = sha256.Size
	warrantNonceLen = 16
	maxWarrantBytes = 4096
	MaxWarrantTTL   = 15 * time.Minute
)

type materializationWarrantClaims struct {
	Domain    string  `json:"domain"`
	Version   int     `json:"version"`
	Ref       TreeRef `json:"ref"`
	Handle    string  `json:"handle"`
	Volume    string  `json:"volume"`
	IssuedAt  int64   `json:"issued_at_nanos"`
	ExpiresAt int64   `json:"expires_at_nanos"`
	Nonce     string  `json:"nonce"`
}

type WarrantSigner struct {
	key    [warrantKeyBytes]byte
	ttl    time.Duration
	clock  func() time.Time
	random io.Reader
}

type WarrantVerifier struct {
	key    [warrantKeyBytes]byte
	maxTTL time.Duration
	clock  func() time.Time
}

func NewWarrantSigner(key []byte, ttl time.Duration, clock func() time.Time) (*WarrantSigner, error) {
	if len(key) != warrantKeyBytes {
		return nil, fmt.Errorf("hangar: materialization warrant key must contain exactly %d raw bytes", warrantKeyBytes)
	}
	if ttl <= 0 || ttl > MaxWarrantTTL {
		return nil, fmt.Errorf("hangar: materialization warrant TTL must be positive and no greater than %s", MaxWarrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	signer := &WarrantSigner{ttl: ttl, clock: clock, random: rand.Reader}
	copy(signer.key[:], key)
	return signer, nil
}

func NewWarrantVerifier(key []byte, maxTTL time.Duration, clock func() time.Time) (*WarrantVerifier, error) {
	if len(key) != warrantKeyBytes {
		return nil, fmt.Errorf("hangar: materialization warrant key must contain exactly %d raw bytes", warrantKeyBytes)
	}
	if maxTTL <= 0 || maxTTL > MaxWarrantTTL {
		return nil, fmt.Errorf("hangar: materialization warrant TTL must be positive and no greater than %s", MaxWarrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	verifier := &WarrantVerifier{maxTTL: maxTTL, clock: clock}
	copy(verifier.key[:], key)
	return verifier, nil
}

func (signer *WarrantSigner) Sign(ref TreeRef, handle, volume string) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", fmt.Errorf("hangar: sign materialization warrant: %w", err)
	}
	if !validMaterializationSegment(handle) || !validMaterializationSegment(volume) {
		return "", fmt.Errorf("hangar: sign materialization warrant: handle and volume must be canonical path segments")
	}
	now := signer.clock().UTC()
	issuedAt, ok := exactUnixNano(now)
	if now.IsZero() || !ok || issuedAt <= 0 {
		return "", fmt.Errorf("hangar: sign materialization warrant: clock is outside the supported range")
	}
	expires := now.Add(signer.ttl)
	expiresAt, ok := exactUnixNano(expires)
	if !ok || expiresAt <= issuedAt || expiresAt-issuedAt != signer.ttl.Nanoseconds() {
		return "", fmt.Errorf("hangar: sign materialization warrant: expiry is outside the supported range")
	}
	nonce := make([]byte, warrantNonceLen)
	if _, err := io.ReadFull(signer.random, nonce); err != nil {
		return "", fmt.Errorf("hangar: generate materialization warrant nonce: %w", err)
	}
	claims := materializationWarrantClaims{
		Domain: warrantDomain, Version: warrantVersion, Ref: ref, Handle: handle, Volume: volume,
		IssuedAt: issuedAt, ExpiresAt: expiresAt,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("hangar: marshal materialization warrant: %w", err)
	}
	mac := hmac.New(sha256.New, signer.key[:])
	_, _ = mac.Write([]byte(warrantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	raw := append(payload, mac.Sum(nil)...)
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(payload) > maxWarrantBytes || len(token) > maxWarrantBytes {
		return "", fmt.Errorf("hangar: materialization warrant exceeds maximum size")
	}
	return token, nil
}

func (verifier *WarrantVerifier) Verify(token string, ref TreeRef, handle, volume string) error {
	unauthorized := func() error { return ErrUnauthorized }
	if len(token) == 0 || len(token) > maxWarrantBytes || !validMaterializationSegment(handle) || !validMaterializationSegment(volume) || ref.Validate() != nil {
		return unauthorized()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token || len(raw) <= sha256.Size || len(raw)-sha256.Size > maxWarrantBytes {
		return unauthorized()
	}
	payload, providedMAC := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, verifier.key[:])
	_, _ = mac.Write([]byte(warrantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return unauthorized()
	}
	var claims materializationWarrantClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return unauthorized()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return unauthorized()
	}
	canonical, err := json.Marshal(claims)
	nonce, nonceErr := base64.RawURLEncoding.Strict().DecodeString(claims.Nonce)
	if err != nil || !bytes.Equal(canonical, payload) || claims.Domain != warrantDomain || claims.Version != warrantVersion ||
		claims.Ref.Validate() != nil || !validMaterializationSegment(claims.Handle) || !validMaterializationSegment(claims.Volume) ||
		claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > verifier.maxTTL.Nanoseconds() ||
		nonceErr != nil || len(nonce) != warrantNonceLen || base64.RawURLEncoding.EncodeToString(nonce) != claims.Nonce {
		return unauthorized()
	}
	now := verifier.clock().UTC()
	nowNanos, ok := exactUnixNano(now)
	if !ok || nowNanos < claims.IssuedAt || nowNanos >= claims.ExpiresAt {
		return unauthorized()
	}
	if !sameTreeRef(claims.Ref, ref) || !constantTimeStringEqual(claims.Handle, handle) || !constantTimeStringEqual(claims.Volume, volume) {
		return unauthorized()
	}
	return nil
}

func sameTreeRef(left, right TreeRef) bool {
	return constantTimeStringEqual(string(left.Scope), string(right.Scope)) &&
		constantTimeStringEqual(string(left.Digest), string(right.Digest)) && left.Generation == right.Generation
}

func constantTimeStringEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}

func validMaterializationSegment(segment string) bool {
	if len(segment) < 1 || len(segment) > 128 || !isASCIIAlphanumeric(segment[0]) || !isASCIIAlphanumeric(segment[len(segment)-1]) {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if !isASCIIAlphanumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlphanumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func exactUnixNano(value time.Time) (int64, bool) {
	value = value.UTC()
	nanos := value.UnixNano()
	return nanos, time.Unix(0, nanos).UTC().Equal(value)
}
