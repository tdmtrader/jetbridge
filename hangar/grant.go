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
	grantDomain   = "hangar-materialize-v1"
	grantVersion  = 1
	grantKeyBytes = sha256.Size
	grantNonceLen = 16
	maxGrantBytes = 4096
	MaxGrantTTL   = 15 * time.Minute
)

type materializationGrantClaims struct {
	Domain    string  `json:"domain"`
	Version   int     `json:"version"`
	Ref       TreeRef `json:"ref"`
	Handle    string  `json:"handle"`
	Volume    string  `json:"volume"`
	IssuedAt  int64   `json:"issued_at_nanos"`
	ExpiresAt int64   `json:"expires_at_nanos"`
	Nonce     string  `json:"nonce"`
}

type GrantSigner struct {
	key    [grantKeyBytes]byte
	ttl    time.Duration
	clock  func() time.Time
	random io.Reader
}

type GrantVerifier struct {
	key    [grantKeyBytes]byte
	maxTTL time.Duration
	clock  func() time.Time
}

func NewGrantSigner(key []byte, ttl time.Duration, clock func() time.Time) (*GrantSigner, error) {
	if len(key) != grantKeyBytes {
		return nil, fmt.Errorf("hangar: materialization grant key must contain exactly %d raw bytes", grantKeyBytes)
	}
	if ttl <= 0 || ttl > MaxGrantTTL {
		return nil, fmt.Errorf("hangar: materialization grant TTL must be positive and no greater than %s", MaxGrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	signer := &GrantSigner{ttl: ttl, clock: clock, random: rand.Reader}
	copy(signer.key[:], key)
	return signer, nil
}

func NewGrantVerifier(key []byte, maxTTL time.Duration, clock func() time.Time) (*GrantVerifier, error) {
	if len(key) != grantKeyBytes {
		return nil, fmt.Errorf("hangar: materialization grant key must contain exactly %d raw bytes", grantKeyBytes)
	}
	if maxTTL <= 0 || maxTTL > MaxGrantTTL {
		return nil, fmt.Errorf("hangar: materialization grant TTL must be positive and no greater than %s", MaxGrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	verifier := &GrantVerifier{maxTTL: maxTTL, clock: clock}
	copy(verifier.key[:], key)
	return verifier, nil
}

func (signer *GrantSigner) Sign(ref TreeRef, handle, volume string) (string, error) {
	if err := ref.Validate(); err != nil {
		return "", fmt.Errorf("hangar: sign materialization grant: %w", err)
	}
	if !validMaterializationSegment(handle) || !validMaterializationSegment(volume) {
		return "", fmt.Errorf("hangar: sign materialization grant: handle and volume must be canonical path segments")
	}
	now := signer.clock().UTC()
	if now.IsZero() || now.UnixNano() <= 0 {
		return "", fmt.Errorf("hangar: sign materialization grant: clock is outside the supported range")
	}
	nonce := make([]byte, grantNonceLen)
	if _, err := io.ReadFull(signer.random, nonce); err != nil {
		return "", fmt.Errorf("hangar: generate materialization grant nonce: %w", err)
	}
	claims := materializationGrantClaims{
		Domain: grantDomain, Version: grantVersion, Ref: ref, Handle: handle, Volume: volume,
		IssuedAt: now.UnixNano(), ExpiresAt: now.Add(signer.ttl).UnixNano(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("hangar: marshal materialization grant: %w", err)
	}
	mac := hmac.New(sha256.New, signer.key[:])
	_, _ = mac.Write([]byte(grantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	raw := append(payload, mac.Sum(nil)...)
	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(payload) > maxGrantBytes || len(token) > maxGrantBytes {
		return "", fmt.Errorf("hangar: materialization grant exceeds maximum size")
	}
	return token, nil
}

func (verifier *GrantVerifier) Verify(token string, ref TreeRef, handle, volume string) error {
	unauthorized := func() error { return ErrUnauthorized }
	if len(token) == 0 || len(token) > maxGrantBytes || !validMaterializationSegment(handle) || !validMaterializationSegment(volume) || ref.Validate() != nil {
		return unauthorized()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token || len(raw) <= sha256.Size || len(raw)-sha256.Size > maxGrantBytes {
		return unauthorized()
	}
	payload, providedMAC := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, verifier.key[:])
	_, _ = mac.Write([]byte(grantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return unauthorized()
	}
	var claims materializationGrantClaims
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
	if err != nil || !bytes.Equal(canonical, payload) || claims.Domain != grantDomain || claims.Version != grantVersion ||
		claims.Ref.Validate() != nil || !validMaterializationSegment(claims.Handle) || !validMaterializationSegment(claims.Volume) ||
		claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > verifier.maxTTL.Nanoseconds() ||
		nonceErr != nil || len(nonce) != grantNonceLen || base64.RawURLEncoding.EncodeToString(nonce) != claims.Nonce {
		return unauthorized()
	}
	now := verifier.clock().UTC()
	if now.UnixNano() < claims.IssuedAt || now.UnixNano() >= claims.ExpiresAt {
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
	if len(segment) < 1 || len(segment) > 255 || segment == "." || segment == ".." {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if character == '/' || character == '\\' || character == 0 {
			return false
		}
	}
	return true
}
