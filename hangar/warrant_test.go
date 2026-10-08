package hangar

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustWarrantRef(t *testing.T, scope, digest string, generation int64) TreeRef {
	t.Helper()
	ref, err := NewTreeRef(Scope(scope), Digest("sha256:"+digest), generation)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

var warrantTestKey = []byte("0123456789abcdef0123456789abcdef")

func warrantClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func newWarrantPair(t *testing.T, now time.Time) (*Signer, *Verifier) {
	t.Helper()
	signer, err := NewSigner(warrantTestKey, 15*time.Minute, warrantClock(now))
	if err != nil {
		t.Fatal(err)
	}
	signer.random = bytes.NewReader(bytes.Repeat([]byte{0x11}, 64))
	verifier, err := NewVerifier(warrantTestKey, 15*time.Minute, warrantClock(now))
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier
}

func materializeWarrant(t *testing.T) Warrant {
	t.Helper()
	return Warrant{Purpose: PurposeMaterializeInput,
		Ref:    mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7),
		Handle: "handle-1", Volume: "volume-1"}
}

func controlWarrant(purpose Purpose) Warrant {
	operation := "stop"
	if purpose == PurposeControlCapture {
		operation = "seal"
	}
	return Warrant{Purpose: purpose, Operation: operation, ExecutionID: "0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b", Fence: 7}
}

func readWarrant(t *testing.T, now time.Time) Warrant {
	t.Helper()
	return Warrant{Purpose: PurposeReadResult,
		Ref:    mustWarrantRef(t, "builds", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7),
		Handle: "handle-1", Volume: "volume-1", ClaimID: "5f3d2a19-8c47-4e60-b1a2-0d9e8f7c6b5a", NodeUID: "node-9f2b1d4c",
		IssuedAt: now.Add(-time.Minute).UnixNano(), ExpiresAt: now.Add(5 * time.Minute).UnixNano()}
}

// The canonical form is the contract: length-prefixed, purpose first, every
// field present, then the window and the nonce.
func TestTheCanonicalWarrantBytesAreFrozen(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	warrant := readWarrant(t, now)
	warrant.Version = 1
	canonical, err := CanonicalWarrantBytes(warrant)
	if err != nil {
		t.Fatal(err)
	}
	want := "11:read-result|1:1|6:builds|71:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|1:7|" +
		"8:handle-1|8:volume-1|36:5f3d2a19-8c47-4e60-b1a2-0d9e8f7c6b5a|13:node-9f2b1d4c|0:|0:|1:0|" +
		"19:1799999940000000000|19:1800000300000000000|0:|"
	if string(canonical) != want {
		t.Fatalf("canonical bytes changed:\n got %s\nwant %s", canonical, want)
	}
	control := controlWarrant(PurposeControlCapture)
	control.Version, control.IssuedAt, control.ExpiresAt = 1, 1, 2
	control.Nonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 16))
	canonical, err = CanonicalWarrantBytes(control)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(canonical), "14:output-capture|1:1|0:|0:|1:0|0:|0:|0:|0:|4:seal|36:0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b|1:7|1:1|1:2|22:EREREREREREREREREREREQ|") {
		t.Fatalf("control canonical bytes changed: %s", canonical)
	}
}

func TestEveryPurposeRoundTripsAndNoPurposeVerifiesAsAnother(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	signer, verifier := newWarrantPair(t, now)
	expected := map[Purpose]Warrant{
		PurposeMaterializeInput: materializeWarrant(t),
		PurposeReadResult:       readWarrant(t, now),
		PurposeControlBase:      controlWarrant(PurposeControlBase),
		PurposeControlCapture:   controlWarrant(PurposeControlCapture),
	}
	tokens := map[Purpose]string{}
	for purpose, warrant := range expected {
		token, err := signer.Sign(warrant)
		if err != nil {
			t.Fatalf("sign %s: %v", purpose, err)
		}
		if strings.ContainsAny(token, "=+/|") {
			t.Fatalf("%s token is not raw base64url: %q", purpose, token)
		}
		tokens[purpose] = token
	}
	for purpose, token := range tokens {
		route := expected[purpose]
		route.IssuedAt, route.ExpiresAt, route.Nonce = 0, 0, ""
		bound, err := verifier.Verify(token, route)
		if err != nil {
			t.Fatalf("verify %s at its own route: %v", purpose, err)
		}
		if bound.Purpose != purpose || bound.ExpiresAt <= bound.IssuedAt {
			t.Fatalf("verify %s returned %+v", purpose, bound)
		}
		if purpose.singleUse() == (bound.Nonce == "") {
			t.Fatalf("%s nonce = %q", purpose, bound.Nonce)
		}
		// The daemon reads the claim id out of a read warrant; it does not
		// know it in advance.
		if purpose == PurposeReadResult {
			if bound.ClaimID != expected[purpose].ClaimID {
				t.Fatalf("read warrant carried claim %q", bound.ClaimID)
			}
			withClaim := route
			withClaim.ClaimID = "another-claim"
			if _, err := verifier.Verify(token, withClaim); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("read warrant verified for another claim: %v", err)
			}
		}
		for other, otherRoute := range expected {
			if other == purpose {
				continue
			}
			otherRoute.IssuedAt, otherRoute.ExpiresAt, otherRoute.Nonce = 0, 0, ""
			if _, err := verifier.Verify(token, otherRoute); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a %s warrant verified at a %s route: %v", purpose, other, err)
			}
			crossed := otherRoute
			crossed.Purpose = purpose
			if _, err := verifier.Verify(token, crossed); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a %s warrant verified against %s's bound fields: %v", purpose, other, err)
			}
		}
	}
}

func TestAWarrantBindsEveryFieldAndItsWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	signer, verifier := newWarrantPair(t, now)
	warrant := materializeWarrant(t)
	token, err := signer.Sign(warrant)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Warrant){
		"scope": func(w *Warrant) { w.Ref.Scope = "other" },
		"digest": func(w *Warrant) {
			w.Ref.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"generation": func(w *Warrant) { w.Ref.Generation++ },
		"handle":     func(w *Warrant) { w.Handle = "handle-2" },
		"volume":     func(w *Warrant) { w.Volume = "volume-2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := warrant
			edit(&changed)
			if _, err := verifier.Verify(token, changed); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
		})
	}
	control := controlWarrant(PurposeControlBase)
	token, err = signer.Sign(control)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Warrant){
		"operation": func(w *Warrant) { w.Operation = "observe" },
		"execution": func(w *Warrant) { w.ExecutionID = "1f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b" },
		"fence":     func(w *Warrant) { w.Fence++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := control
			edit(&changed)
			if _, err := verifier.Verify(token, changed); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
		})
	}
	// The verifier's window: at the exact expiry and before issue.
	for name, at := range map[string]time.Time{"at expiry": now.Add(15 * time.Minute), "before issue": now.Add(-time.Nanosecond)} {
		verifier.clock = warrantClock(at)
		if _, err := verifier.Verify(token, control); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A tighter verifier refuses a longer-lived warrant.
	tight, err := NewVerifier(warrantTestKey, time.Minute, warrantClock(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tight.Verify(token, control); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a 15m warrant verified under a 1m bound: %v", err)
	}
}

func TestAnEditedOrReEncodedWarrantDoesNotVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	signer, verifier := newWarrantPair(t, now)
	warrant := controlWarrant(PurposeControlCapture)
	token, err := signer.Sign(warrant)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	payload, mac := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	var decoded Warrant
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	reencode := func(p []byte, m []byte) string {
		return base64.RawURLEncoding.EncodeToString(append(append([]byte{}, p...), m...))
	}
	cases := map[string]string{
		"padded":          token + "=",
		"flipped mac":     reencode(payload, append([]byte{mac[0] ^ 1}, mac[1:]...)),
		"extra field":     reencode([]byte(strings.TrimSuffix(string(payload), "}")+`,"x":1}`), mac),
		"reordered field": reencode([]byte(`{"version":1,`+strings.TrimPrefix(string(payload), `{`)), mac),
		"trailing bytes":  reencode(append(append([]byte{}, payload...), '{', '}'), mac),
		"empty":           "",
		"other key": func() string {
			other := hmac.New(sha256.New, bytes.Repeat([]byte{0x99}, 32))
			canonical, _ := CanonicalWarrantBytes(decoded)
			other.Write([]byte(WarrantDomain))
			other.Write([]byte{0})
			other.Write(canonical)
			return reencode(payload, other.Sum(nil))
		}(),
		"no domain": func() string {
			canonical, _ := CanonicalWarrantBytes(decoded)
			m := hmac.New(sha256.New, warrantTestKey)
			m.Write(canonical)
			return reencode(payload, m.Sum(nil))
		}(),
	}
	// A rewritten expiry with its MAC recomputed under the key is the forgery
	// the domain and the canonical form exist to make impossible without it.
	longer := decoded
	longer.ExpiresAt = decoded.IssuedAt + (16 * time.Minute).Nanoseconds()
	longerPayload, _ := json.Marshal(longer)
	cases["sixteen minute window"] = reencode(longerPayload, warrantMAC(warrantTestKey, func() []byte {
		b, _ := CanonicalWarrantBytes(longer)
		return b
	}()))
	for name, edited := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(edited, warrant); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
		})
	}
	if _, err := verifier.Verify(token, warrant); err != nil {
		t.Fatalf("the unedited token: %v", err)
	}
}

func TestSignRefusesWhatItsPurposeDoesNotBindAndAnOverlongToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer, _ := newWarrantPair(t, now)
	cases := map[string]Warrant{
		"no purpose":                {},
		"unknown purpose":           {Purpose: "stop"},
		"materialize with no ref":   {Purpose: PurposeMaterializeInput, Handle: "h", Volume: "v"},
		"materialize with a path":   {Purpose: PurposeMaterializeInput, Ref: materializeWarrant(t).Ref, Handle: "../h", Volume: "v"},
		"materialize with a claim":  func() Warrant { w := materializeWarrant(t); w.ClaimID = "c"; return w }(),
		"materialize with a window": func() Warrant { w := materializeWarrant(t); w.IssuedAt = 1; return w }(),
		"materialize with a nonce":  func() Warrant { w := materializeWarrant(t); w.Nonce = "x"; return w }(),
		"control with no operation": {Purpose: PurposeControlBase, ExecutionID: "e"},
		"control with no execution": {Purpose: PurposeControlBase, Operation: "stop"},
		"control with a ref":        func() Warrant { w := controlWarrant(PurposeControlBase); w.Ref = materializeWarrant(t).Ref; return w }(),
		"control with a separator":  func() Warrant { w := controlWarrant(PurposeControlBase); w.Operation = "st op"; return w }(),
		"read with no claim":        func() Warrant { w := readWarrant(t, now); w.ClaimID = ""; return w }(),
		"read with no node":         func() Warrant { w := readWarrant(t, now); w.NodeUID = ""; return w }(),
		"read with no window":       func() Warrant { w := readWarrant(t, now); w.IssuedAt, w.ExpiresAt = 0, 0; return w }(),
		"read with a nonce":         func() Warrant { w := readWarrant(t, now); w.Nonce = "x"; return w }(),
		"read with a 16m window": func() Warrant {
			w := readWarrant(t, now)
			w.ExpiresAt = w.IssuedAt + (16 * time.Minute).Nanoseconds()
			return w
		}(),
		"read expiring at issue": func() Warrant { w := readWarrant(t, now); w.ExpiresAt = w.IssuedAt; return w }(),
		"read with an operation": func() Warrant { w := readWarrant(t, now); w.Operation = "seal"; return w }(),
		"overlong execution": func() Warrant {
			w := controlWarrant(PurposeControlBase)
			w.ExecutionID = strings.Repeat("e", 129)
			return w
		}(),
	}
	for name, warrant := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := signer.Sign(warrant); err == nil {
				t.Fatal("signed")
			}
		})
	}
}

func TestReMintingOneReadWarrantIsByteIdenticalAndControlMintsAreNot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer, _ := newWarrantPair(t, now)
	first, err := signer.Sign(readWarrant(t, now))
	if err != nil {
		t.Fatal(err)
	}
	second, err := signer.Sign(readWarrant(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("two mints of one claim's read warrant differ")
	}
	signer.random = bytes.NewReader(append(bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16)...))
	one, _ := signer.Sign(controlWarrant(PurposeControlBase))
	two, _ := signer.Sign(controlWarrant(PurposeControlBase))
	if one == two {
		t.Fatal("two control mints share a nonce")
	}
}

func TestSignerAndVerifierRequireExactRawKeyAndBoundedTTL(t *testing.T) {
	now := warrantClock(time.Unix(1_800_000_000, 0).UTC())
	for name, key := range map[string][]byte{
		"short":  make([]byte, 31),
		"long":   make([]byte, 33),
		"base64": []byte("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSigner(key, time.Minute, now); err == nil {
				t.Fatal("signer accepted a key other than 32 raw bytes")
			}
			if _, err := NewVerifier(key, time.Minute, now); err == nil {
				t.Fatal("verifier accepted a key other than 32 raw bytes")
			}
		})
	}
	for _, ttl := range []time.Duration{0, -time.Second, MaxWarrantTTL + time.Nanosecond} {
		if _, err := NewSigner(warrantTestKey, ttl, now); err == nil {
			t.Fatalf("signer accepted TTL %s", ttl)
		}
		if _, err := NewVerifier(warrantTestKey, ttl, now); err == nil {
			t.Fatalf("verifier accepted TTL %s", ttl)
		}
	}
	if _, err := NewSigner(warrantTestKey, MaxWarrantTTL, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTheVerifierRefusesAnExpectationThatCarriesTheTokensOwnFields(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer, verifier := newWarrantPair(t, now)
	token, err := signer.Sign(controlWarrant(PurposeControlBase))
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]Warrant{
		"nonce":  func() Warrant { w := controlWarrant(PurposeControlBase); w.Nonce = "x"; return w }(),
		"window": func() Warrant { w := controlWarrant(PurposeControlBase); w.IssuedAt = 1; return w }(),
		"empty":  {},
	} {
		if _, err := verifier.Verify(token, expected); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
