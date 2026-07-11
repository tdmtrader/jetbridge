package principals_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func TestMintTokenShape(t *testing.T) {
	token, prefix, hash, err := principals.MintToken(42)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if !strings.HasPrefix(token, "cap1.42.") {
		t.Errorf("token = %q, want cap1.42. prefix", token)
	}
	secret := strings.TrimPrefix(token, "cap1.42.")
	if len(secret) != 43 {
		t.Errorf("secret length = %d, want 43 (32 bytes base64url raw)", len(secret))
	}
	if prefix != token[:12] {
		t.Errorf("prefix = %q, want first 12 chars %q", prefix, token[:12])
	}
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(hash))
	}
	if hash != principals.HashToken(token) {
		t.Errorf("hash mismatch with HashToken")
	}
}

func TestMintTokenUnique(t *testing.T) {
	a, _, _, _ := principals.MintToken(1)
	b, _, _, _ := principals.MintToken(1)
	if a == b {
		t.Fatal("two mints produced the same token")
	}
}

func TestParseTokenID(t *testing.T) {
	token, _, _, _ := principals.MintToken(7)
	id, ok := principals.ParseTokenID(token)
	if !ok || id != 7 {
		t.Errorf("ParseTokenID(%q) = %d,%v want 7,true", token, id, ok)
	}

	for _, bad := range []string{
		"", "cap1.", "cap1.7", "cap1..secret", "cap1.abc.secret",
		"cap2.7.secret", "cap1.-1.secret", "cap1.0.secret",
		"Bearer cap1.7.secret", "eyJhbGciOi.something.jwt",
	} {
		if _, ok := principals.ParseTokenID(bad); ok {
			t.Errorf("ParseTokenID(%q) = ok, want reject", bad)
		}
	}
}
