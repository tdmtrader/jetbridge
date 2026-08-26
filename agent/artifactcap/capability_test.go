package artifactcap_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/artifactcap"
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
