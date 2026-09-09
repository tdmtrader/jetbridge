package executioncontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func signedFixture() Acknowledgement {
	return Acknowledgement{
		ProtocolVersion: ProtocolVersion,
		Kind:            AcknowledgementFinish,
		Identity: Identity{
			ExecutionID: "33333333-3333-4333-8333-333333333333",
			Fence:       4,
		},
		ActivationEpoch: 7,
		LedgerSequence:  12,
		NodeUID:         "node-1",
		PodUID:          "pod-1",
		ProcessIdentity: "proc-1",
		ObservedAt:      NewTimestamp(time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)),
		Outcome:         &ExitOutcome{ExitCode: 0},
	}
}

func TestASignedAcknowledgementVerifiesUnderThePinnedKeyAndNoOther(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	signer, err := NewAcknowledgementSigner(private)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}

	signed, err := signer.Sign(signedFixture())
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if signed.Signature == "" {
		t.Fatal("the signer returned an unsigned acknowledgement")
	}
	if err := VerifyAcknowledgement(signed, signer.PublicKey()); err != nil {
		t.Errorf("the signer's own statement does not verify under its own key: %v", err)
	}

	// And under nobody else's.
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	if err := VerifyAcknowledgement(signed, other); !errors.Is(err, ErrUnsigned) {
		t.Errorf("the statement verified under a key that did not sign it: %v", err)
	}

	// A statement that contradicts itself must not become a signed statement
	// that contradicts itself.
	broken := signedFixture()
	broken.Outcome = nil
	if _, err := signer.Sign(broken); !errors.Is(err, ErrIncomplete) {
		t.Errorf("the signer signed a finish acknowledgement with no outcome: %v", err)
	}
}

// Every field is signed, and this is how a field that stops being signed is
// found.
//
// The list in CanonicalAcknowledgementBytes is explicit on purpose -- a
// reflective canonicalizer signs whatever it is handed and cannot be reviewed --
// so the reviewable half is this walk: mutate one leaf, and the bytes must
// change. Signature itself is the one exemption, because it is what the bytes
// produce.
func TestEveryAcknowledgementFieldEntersTheSignedBytes(t *testing.T) {
	base := signedFixture()
	baseline := string(CanonicalAcknowledgementBytes(base))

	mutations := map[string]func(*Acknowledgement){
		"ProtocolVersion":  func(a *Acknowledgement) { a.ProtocolVersion = "hangar-execution-control-v2" },
		"Kind":             func(a *Acknowledgement) { a.Kind = AcknowledgementStop },
		"ExecutionID":      func(a *Acknowledgement) { a.ExecutionID = "44444444-4444-4444-8444-444444444444" },
		"Fence":            func(a *Acknowledgement) { a.Fence++ },
		"ActivationEpoch":  func(a *Acknowledgement) { a.ActivationEpoch++ },
		"LedgerSequence":   func(a *Acknowledgement) { a.LedgerSequence++ },
		"NodeUID":          func(a *Acknowledgement) { a.NodeUID = "node-2" },
		"PodUID":           func(a *Acknowledgement) { a.PodUID = "pod-2" },
		"ProcessIdentity":  func(a *Acknowledgement) { a.ProcessIdentity = "proc-2" },
		"ObservedAt":       func(a *Acknowledgement) { a.ObservedAt = NewTimestamp(base.ObservedAt.Add(time.Second)) },
		"Outcome.ExitCode": func(a *Acknowledgement) { a.Outcome = &ExitOutcome{ExitCode: 1} },
		"Outcome.Signalled": func(a *Acknowledgement) {
			a.Outcome = &ExitOutcome{ExitCode: 0, Signalled: true, Signal: "TERM"}
		},
		"Outcome.Signal": func(a *Acknowledgement) {
			a.Outcome = &ExitOutcome{ExitCode: 0, Signalled: true, Signal: "KILL"}
		},
		"Outcome (absent)": func(a *Acknowledgement) { a.Outcome = nil },
	}

	for name, mutate := range mutations {
		mutated := base
		mutate(&mutated)
		if string(CanonicalAcknowledgementBytes(mutated)) == baseline {
			t.Errorf("changing %s did not change the signed bytes. A field outside the "+
				"signature is a field an intermediary may rewrite", name)
		}
	}

	// The walk half: every leaf of the struct is either mutated above or is the
	// signature itself. Without this, adding a field and forgetting to sign it
	// is invisible.
	covered := map[string]bool{"Signature": true}
	for name := range mutations {
		covered[name] = true
		if _, leaf, found := strings.Cut(name, "."); found {
			covered[leaf] = true
		}
	}
	// Identity is embedded; its own leaves are what carry meaning.
	covered["Identity"] = true

	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, prefix string) {
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if field.Anonymous {
				walk(field.Type, prefix)

				continue
			}
			fieldType := field.Type
			for fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			if fieldType.Kind() == reflect.Struct && fieldType != reflect.TypeOf(Timestamp{}) {
				walk(fieldType, field.Name+".")

				continue
			}
			if !covered[prefix+field.Name] && !covered[field.Name] {
				t.Errorf("Acknowledgement.%s%s is not shown to enter the signed bytes. Add a "+
					"mutation for it, or the field can be edited in flight", prefix, field.Name)
			}
		}
	}
	walk(reflect.TypeOf(Acknowledgement{}), "")
}
