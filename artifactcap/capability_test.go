package artifactcap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/artifactcap"
)

func TestResolveCapabilityBindsEveryAuthorizedField(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_750_000_000, 0).UTC()
	signer, err := artifactcap.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.SignResolve("producer/output", "/var/concourse/artifacts/steps/consumer/input", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := artifactcap.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}

	if err := verifier.VerifyResolve(token, "producer/output", "/var/concourse/artifacts/steps/consumer/input", now); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		token string
		key   string
		dest  string
		now   time.Time
	}{
		"source":      {token, "another/output", "/var/concourse/artifacts/steps/consumer/input", now},
		"destination": {token, "producer/output", "/var/concourse/artifacts/steps/other/input", now},
		"expiry":      {token, "producer/output", "/var/concourse/artifacts/steps/consumer/input", now.Add(time.Minute)},
		"signature":   {token + "x", "producer/output", "/var/concourse/artifacts/steps/consumer/input", now},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifier.VerifyResolve(tc.token, tc.key, tc.dest, tc.now); err == nil {
				t.Fatal("changed or expired capability was accepted")
			}
		})
	}
}

// The MAC comparison itself. Every negative case in the binding test above is
// rejected BEFORE hmac.Equal runs — "signature" appends a byte, so the MAC
// fails the length guard; the others present a token this signer really signed
// and fail on the claims. These tokens are well-formed all the way down (three
// parts, valid base64, 32-byte MAC, parseable canonical payload), so the ONLY
// thing wrong with them is that the MAC does not authenticate the payload —
// delete or invert the hmac.Equal check and this test is what fails.
func TestVerifyResolve_RejectsAWellFormedForgedMAC(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	otherKey := []byte("fedcba9876543210fedcba9876543210")
	now := time.Unix(1_750_000_000, 0).UTC()
	source, dest := "producer/output", "/var/concourse/artifacts/steps/consumer/input"

	signer, err := artifactcap.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := artifactcap.NewSigner(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := artifactcap.NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("signed with a different key", func(t *testing.T) {
		token, err := otherSigner.SignResolve(source, dest, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := verifier.VerifyResolve(token, source, dest, now); err == nil {
			t.Fatal("a capability signed under a different key was accepted")
		}
	})

	t.Run("payload and MAC from different tokens", func(t *testing.T) {
		// Both halves are individually genuine under the RIGHT key — the
		// payload of one token, the (valid, 32-byte) MAC of another. Only the
		// binding between them is wrong.
		first, err := signer.SignResolve(source, dest, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		second, err := signer.SignResolve(source, dest, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		firstParts := strings.Split(first, ".")
		secondParts := strings.Split(second, ".")
		if firstParts[2] == secondParts[2] {
			t.Fatal("two signings produced one MAC; the nonce is not doing its job")
		}
		spliced := firstParts[0] + "." + firstParts[1] + "." + secondParts[2]
		if err := verifier.VerifyResolve(spliced, source, dest, now); err == nil {
			t.Fatal("a payload presented under another token's MAC was accepted")
		}
	})
}

func TestResolveCapabilityHasUniqueAuthenticatedAcknowledgement(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	otherKey := []byte("fedcba9876543210fedcba9876543210")
	signer, _ := artifactcap.NewSigner(key)
	verifier, _ := artifactcap.NewVerifier(key)
	otherVerifier, _ := artifactcap.NewVerifier(otherKey)
	expires := time.Now().Add(time.Hour)
	first, err := signer.SignResolve("producer/output", "/artifact-store/steps/consumer/input", expires)
	if err != nil {
		t.Fatal(err)
	}
	second, err := signer.SignResolve("producer/output", "/artifact-store/steps/consumer/input", expires)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("equivalent operations reused a replayable capability token")
	}
	want, err := signer.ResolveAcknowledgement(first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifier.ResolveAcknowledgement(first)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" || got != want {
		t.Fatalf("daemon acknowledgement = %q, want signer expectation %q", got, want)
	}
	wrong, err := otherVerifier.ResolveAcknowledgement(first)
	if err == nil && wrong == want {
		t.Fatal("different key forged the expected acknowledgement")
	}
	receiptName, err := artifactcap.ResolveReceiptFilename(want)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(receiptName) != receiptName || receiptName == want {
		t.Fatalf("receipt filename = %q, want one safe opaque path component", receiptName)
	}
	if _, err := artifactcap.ResolveReceiptFilename("../" + want); err == nil {
		t.Fatal("unsafe acknowledgement was accepted as a receipt filename")
	}
}

func TestLoadKeyFileIsStrictAndCopiesTheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolve.key")
	want := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := artifactcap.LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("key = %q, want exact file bytes", got)
	}
	got[0] = 'x'
	again, err := artifactcap.LoadKeyFile(path)
	if err != nil || string(again) != string(want) {
		t.Fatalf("load returned shared mutable bytes: %q, %v", again, err)
	}

	for _, contents := range [][]byte{nil, []byte("short"), append(append([]byte{}, want...), '\n')} {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := artifactcap.LoadKeyFile(path); err == nil {
			t.Fatalf("accepted malformed file length %d", len(contents))
		}
	}
}

func TestResolveCapabilityRejectsMalformedKeys(t *testing.T) {
	for _, key := range [][]byte{nil, []byte("short"), make([]byte, 33)} {
		if _, err := artifactcap.NewSigner(key); err == nil {
			t.Fatalf("accepted malformed key length %d", len(key))
		}
		if _, err := artifactcap.NewVerifier(key); err == nil {
			t.Fatalf("accepted malformed key length %d", len(key))
		}
	}
}
