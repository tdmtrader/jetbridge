// Package runinput authenticates short-lived, sealed input source descriptors.
// The bearer is transient; only SourceID and the resolved ref may be retained.
package runinput

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

var ErrUnavailable = errors.New("sealed Run input is unavailable or unauthorized")

const MaxGrantTTL = 15 * time.Minute

// Audience is derived from authenticated admission, never from caller claims.
type Audience struct {
	TeamID          int    `json:"team_id"`
	TemplateID      int    `json:"template_id"`
	PrincipalDigest string `json:"principal_digest"`
	Input           string `json:"input"`
	Epoch           int64  `json:"activation_epoch"`
}

type sourceFacts struct {
	Audience Audience       `json:"audience"`
	Ref      hangar.TreeRef `json:"ref"`
}

type grantClaims struct {
	Source    sourceFacts `json:"source"`
	IssuedAt  int64       `json:"issued_at"`
	ExpiresAt int64       `json:"expires_at"`
}

type Authority struct {
	key []byte
	now func() time.Time
}

func NewAuthority(key []byte, now func() time.Time) (*Authority, error) {
	if len(key) < 32 || now == nil {
		return nil, ErrUnavailable
	}
	return &Authority{key: append([]byte(nil), key...), now: now}, nil
}

// PrincipalDigest matches the durable invocation principal identity. This
// function does not authenticate a subject; the caller must do that first.
func PrincipalDigest(subject string) string {
	sum := sha256.Sum256([]byte("run-invocation-principal/v1\x00" + subject))
	return hex.EncodeToString(sum[:])
}

func validAudience(a Audience) bool {
	digest, err := hex.DecodeString(a.PrincipalDigest)
	return a.TeamID > 0 && a.TemplateID > 0 && err == nil && len(digest) == 32 && strings.ToLower(a.PrincipalDigest) == a.PrincipalDigest && output.OutputName(a.Input).Validate() == nil && a.Epoch > 0
}

func SourceIDValid(id string) bool {
	const prefix = "input-v1-"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	digest := strings.TrimPrefix(id, prefix)
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && strings.ToLower(digest) == digest
}

func sourceID(source sourceFacts) string {
	body, _ := json.Marshal(source)
	sum := sha256.Sum256(append([]byte("run-input-source/v1\x00"), body...))
	return "input-v1-" + hex.EncodeToString(sum[:])
}

// Mint is an internal publication operation. Its caller must already have
// authorized the upload and verified this exact publication; a public endpoint
// must never mint from a caller-supplied ref alone.
func (a *Authority) Mint(audience Audience, ref hangar.TreeRef, ttl time.Duration) (string, string, error) {
	if a == nil || ttl < time.Second || ttl > MaxGrantTTL {
		return "", "", ErrUnavailable
	}
	now := a.now().UTC()
	return a.mintUntil(audience, ref, now, now.Add(ttl))
}

// MintUntil never outlives the committed upload claim. The absolute database
// deadline is rounded down in the token, rather than recomputed from a TTL
// after the claim's transaction has committed.
func (a *Authority) MintUntil(audience Audience, ref hangar.TreeRef, expires time.Time) (string, string, error) {
	if a == nil {
		return "", "", ErrUnavailable
	}
	return a.mintUntil(audience, ref, a.now().UTC(), expires)
}

func (a *Authority) mintUntil(audience Audience, ref hangar.TreeRef, now, expires time.Time) (string, string, error) {
	if !validAudience(audience) || ref.Validate() != nil || expires.Unix() <= now.Unix() || expires.Sub(now) > MaxGrantTTL {
		return "", "", ErrUnavailable
	}
	source := sourceFacts{Audience: audience, Ref: ref}
	claims := grantClaims{Source: source, IssuedAt: now.Unix(), ExpiresAt: expires.Unix()}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", "", ErrUnavailable
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return sourceID(source), payload + "." + base64.RawURLEncoding.EncodeToString(a.sign(payload)), nil
}

// Verify compares the complete audience and stable source identity. It should
// be called only for first admission; a committed replay needs no live bearer.
func (a *Authority) Verify(id, token string, audience Audience) (hangar.TreeRef, error) {
	var zero hangar.TreeRef
	if a == nil || !validAudience(audience) || !SourceIDValid(id) || len(token) > 8192 {
		return zero, ErrUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return zero, ErrUnavailable
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, a.sign(parts[0])) {
		return zero, ErrUnavailable
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return zero, ErrUnavailable
	}
	var claims grantClaims
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&claims) != nil || decoder.Decode(new(any)) != io.EOF {
		return zero, ErrUnavailable
	}
	now := a.now().UTC().Unix()
	if claims.Source.Audience != audience || claims.Source.Ref.Validate() != nil || sourceID(claims.Source) != id || claims.IssuedAt > now || claims.ExpiresAt <= now || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > int64(MaxGrantTTL/time.Second) {
		return zero, ErrUnavailable
	}
	return claims.Source.Ref, nil
}

func (a *Authority) sign(payload string) []byte {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte("run-input-grant/v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
