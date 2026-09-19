package hangar

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestMaterializationWarrantBindsExactRequestAndTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	ref := mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7)
	signer, err := NewWarrantSigner(key, 15*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWarrantVerifier(key, 15*time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	token, err := signer.Sign(ref, "handle-1", "volume-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "=") {
		t.Fatalf("warrant is padded rather than raw base64url: %q", token)
	}
	if err := verifier.Verify(token, ref, "handle-1", "volume-1"); err != nil {
		t.Fatalf("verify exact request: %v", err)
	}

	otherRef := ref
	otherRef.Generation++
	for name, check := range map[string]func() error{
		"scope": func() error {
			changed := ref
			changed.Scope = "other"
			return verifier.Verify(token, changed, "handle-1", "volume-1")
		},
		"digest": func() error {
			changed := mustWarrantRef(t, "builds", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 7)
			return verifier.Verify(token, changed, "handle-1", "volume-1")
		},
		"generation": func() error { return verifier.Verify(token, otherRef, "handle-1", "volume-1") },
		"handle":     func() error { return verifier.Verify(token, ref, "handle-2", "volume-1") },
		"volume":     func() error { return verifier.Verify(token, ref, "handle-1", "volume-2") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
		})
	}

	verifier.clock = func() time.Time { return now.Add(15 * time.Minute) }
	if err := verifier.Verify(token, ref, "handle-1", "volume-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("warrant at exact expiry: %v", err)
	}
}

func TestWarrantSignerRequiresExactRawKeyAndBoundedTTL(t *testing.T) {
	now := func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	for name, key := range map[string][]byte{
		"short":  make([]byte, 31),
		"long":   make([]byte, 33),
		"base64": []byte("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewWarrantSigner(key, time.Minute, now); err == nil {
				t.Fatal("accepted a key other than 32 raw bytes")
			}
		})
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, ttl := range []time.Duration{0, -time.Second, 15*time.Minute + time.Nanosecond} {
		if _, err := NewWarrantSigner(key, ttl, now); err == nil {
			t.Fatalf("accepted invalid TTL %v", ttl)
		}
	}
}

func TestWarrantSignerRejectsUnixNanoAliasesAndOverflow(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for name, now := range map[string]time.Time{
		"clock outside UnixNano range":  time.Date(2300, time.January, 1, 0, 0, 0, 0, time.UTC),
		"expiry outside UnixNano range": time.Unix(0, math.MaxInt64).Add(-30 * time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			signer, err := NewWarrantSigner(key, time.Minute, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			ref := mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
			if _, err := signer.Sign(ref, "handle-1", "volume-1"); err == nil {
				t.Fatal("UnixNano alias/overflow was signed")
			}
		})
	}
}

func TestMaterializationSegmentGrammar(t *testing.T) {
	for _, segment := range []string{"a", "A1", "handle-01234567-89ab-cdef", "volume.input_2"} {
		if !validMaterializationSegment(segment) {
			t.Fatalf("valid segment rejected: %q", segment)
		}
	}
	for _, segment := range []string{
		"", ".", "..", "-leading", "trailing-", ".hidden", "hidden.", "a/b", `a\b`,
		"has space", "colon:value", "control\x1f", "é", "e\u0301", string([]byte{0xff}), strings.Repeat("a", 129),
	} {
		if validMaterializationSegment(segment) {
			t.Fatalf("invalid or alias-prone segment accepted: %q", segment)
		}
	}
}

func TestMaterializationWarrantStrictParsingAndSafeErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	ref := mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
	signer, _ := NewWarrantSigner(key, time.Minute, func() time.Time { return now })
	verifier, _ := NewWarrantVerifier(key, time.Minute, func() time.Time { return now })
	token, err := signer.Sign(ref, "handle", "volume")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	badMAC := append([]byte(nil), raw...)
	badMAC[len(badMAC)-1] ^= 1
	variants := []string{
		token + "=",
		base64.URLEncoding.EncodeToString(raw),
		"!" + token,
		strings.Repeat("a", 4097),
		base64.RawURLEncoding.EncodeToString(badMAC),
		base64.RawURLEncoding.EncodeToString(append([]byte(`{"v":2}`), make([]byte, 32)...)),
	}
	for _, malformed := range variants {
		err := verifier.Verify(malformed, ref, "handle", "volume")
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("malformed warrant got %v", err)
		}
		if strings.Contains(err.Error(), malformed) {
			t.Fatalf("error leaked warrant: %v", err)
		}
	}
}

func TestMaterializationWarrantUsesFreshNonce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ref := mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
	signer, _ := NewWarrantSigner([]byte("0123456789abcdef0123456789abcdef"), time.Minute, func() time.Time { return now })
	first, err := signer.Sign(ref, "handle", "volume")
	if err != nil {
		t.Fatal(err)
	}
	second, err := signer.Sign(ref, "handle", "volume")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two warrants shared a nonce")
	}
}

func TestMaterializationWarrantRejectsAuthenticatedNoncanonicalAndInvalidClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	ref := mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
	verifier, _ := NewWarrantVerifier(key, time.Minute, func() time.Time { return now })
	valid := materializationWarrantClaims{
		Domain: warrantDomain, Version: warrantVersion, Ref: ref, Handle: "handle", Volume: "volume",
		IssuedAt: now.UnixNano(), ExpiresAt: now.Add(time.Minute).UnixNano(),
		Nonce: base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
	}
	mutations := map[string]func(*materializationWarrantClaims){
		"domain":     func(claims *materializationWarrantClaims) { claims.Domain = "other" },
		"version":    func(claims *materializationWarrantClaims) { claims.Version++ },
		"scope":      func(claims *materializationWarrantClaims) { claims.Ref.Scope = "other" },
		"digest":     func(claims *materializationWarrantClaims) { claims.Ref.Digest = "sha256:invalid" },
		"generation": func(claims *materializationWarrantClaims) { claims.Ref.Generation = 0 },
		"handle":     func(claims *materializationWarrantClaims) { claims.Handle = "../escape" },
		"volume":     func(claims *materializationWarrantClaims) { claims.Volume = "a/b" },
		"future issue": func(claims *materializationWarrantClaims) {
			claims.IssuedAt = now.Add(time.Nanosecond).UnixNano()
			claims.ExpiresAt = now.Add(time.Minute).UnixNano()
		},
		"expired": func(claims *materializationWarrantClaims) { claims.ExpiresAt = now.UnixNano() },
		"excess TTL": func(claims *materializationWarrantClaims) {
			claims.ExpiresAt = now.Add(time.Minute + time.Nanosecond).UnixNano()
		},
		"nonce": func(claims *materializationWarrantClaims) {
			claims.Nonce = base64.RawURLEncoding.EncodeToString([]byte("short"))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			claims := valid
			mutate(&claims)
			token := authenticatedWarrantToken(t, key, claims)
			if err := verifier.Verify(token, ref, "handle", "volume"); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v", err)
			}
		})
	}

	canonical, _ := json.Marshal(valid)
	for name, payload := range map[string][]byte{
		"whitespace": append([]byte(" "), canonical...),
		"unknown":    append(canonical[:len(canonical)-1], []byte(`,"unknown":true}`)...),
		"trailing":   append(append([]byte(nil), canonical...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			token := authenticatedWarrantPayload(key, payload)
			if err := verifier.Verify(token, ref, "handle", "volume"); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func authenticatedWarrantToken(t *testing.T, key []byte, claims materializationWarrantClaims) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return authenticatedWarrantPayload(key, payload)
}

func authenticatedWarrantPayload(key, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(warrantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(append([]byte(nil), payload...), mac.Sum(nil)...))
}

func mustWarrantRef(t *testing.T, scope, hexDigest string, generation int64) TreeRef {
	t.Helper()
	ref, err := NewTreeRef(Scope(scope), Digest("sha256:"+hexDigest), generation)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
