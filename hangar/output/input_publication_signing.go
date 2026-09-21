package output

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"
)

// The fixed field order and length prefixes are independent of JSON encoding.
// Input receipts use a separate domain from task capture receipts and grants.
func canonicalInputPublication(p InputPublication) ([]byte, error) {
	if err := p.validateClaims(); err != nil {
		return nil, err
	}
	marker, _ := ParseObjectMarker(p.Marker)
	var b []byte
	field := func(s string) { b = append(b, fmt.Sprintf("%d:%s|", len(s), s)...) }
	number := func(n int64) { field(fmt.Sprint(n)) }
	stamp := func(t Timestamp) { field(t.UTC().Format(time.RFC3339Nano)) }
	field("hangar-input-publication/v1")
	field(p.Stage.Version)
	field(p.KeyID)
	field(string(p.Stage.ReservationID))
	field(string(p.Stage.NodeUID))
	number(int64(p.Stage.ActivationEpoch))
	field(string(p.Stage.Scope))
	field(string(p.Stage.Digest))
	number(p.Stage.Bytes)
	stamp(p.Stage.CreatedAt)
	stamp(p.Stage.ExpiresAt)
	field(p.Nonce)
	field(string(p.Attributes.Ref.Scope))
	field(string(p.Attributes.Ref.Digest))
	number(p.Attributes.Ref.Generation)
	number(p.Attributes.StoredBytes)
	number(p.Attributes.LogicalBytes)
	stamp(p.Attributes.CreatedAt)
	number(p.Metageneration)
	field(marker.Version)
	field(string(marker.ReservationID))
	number(int64(marker.ActivationEpoch))
	stamp(marker.CreatedAt)
	stamp(p.SignedAt)
	if len(b) > MaxCanonicalReceiptBytes {
		return nil, ErrLimitExceeded
	}
	return b, nil
}

// SignInputPublication records only an exact object observed by the publishing
// node. It provides no task start/stop evidence and cannot sign a capture receipt.
func (s *ReceiptSigner) SignInputPublication(p InputPublication) (InputPublication, error) {
	if s == nil || p.Stage.ActivationEpoch != s.epoch {
		return InputPublication{}, ErrUnauthorized
	}
	p.SignedAt, p.KeyID = NewTimestamp(s.clock.Now().UTC()), s.keyID
	body, err := canonicalInputPublication(p)
	if err != nil {
		return InputPublication{}, err
	}
	p.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(s.key, body))
	return p, nil
}

// VerifyInputPublication checks fresh evidence against the consumer's retained
// stage and nonce. The consumer must also consume that nonce and register its
// claim in one database transaction; this verifier alone grants no ownership.
func (v *ReceiptSignatureVerifier) VerifyInputPublication(p InputPublication, stage InputStage, nonce string) error {
	if v == nil || p.Stage != stage || p.Nonce != nonce {
		return ErrUnauthorized
	}
	body, err := canonicalInputPublication(p)
	if err != nil {
		return err
	}
	now := v.clock.Now().UTC()
	key, found := v.ring.byKeyID[p.KeyID]
	if !found || key.Epoch != stage.ActivationEpoch || now.Before(key.ValidFrom.Time) || !now.Before(key.ValidUntil.Time) || now.Before(p.SignedAt.Time) || !now.Before(stage.ExpiresAt.Time) || p.SignedAt.Before(key.ValidFrom.Time) || !p.SignedAt.Before(key.ValidUntil.Time) {
		return ErrUnauthorized
	}
	signature, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key.PublicKey, body, signature) {
		return ErrUnauthorized
	}
	return nil
}
