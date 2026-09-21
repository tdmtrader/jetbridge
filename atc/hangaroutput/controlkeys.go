package hangaroutput

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// ControlKeyRing pins one node-control signing identity per activation epoch.
// Old entries remain available while handoffs from their epoch need recovery.
// These are public keys; receipt signing uses a separate ring and key role.
type ControlKeyRing struct {
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	Keys            []ControlKeyEntry                `json:"keys"`
}

type ControlKeyEntry struct {
	Epoch     executioncontrol.ActivationEpoch `json:"epoch"`
	PublicKey string                           `json:"public_key"`
}

func LoadControlKeyRing(path string) (ControlKeyRing, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ControlKeyRing{}, err
	}
	var ring ControlKeyRing
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ring); err != nil {
		return ControlKeyRing{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ControlKeyRing{}, fmt.Errorf("%w: trailing data in control key ring", output.ErrCorrupt)
	}
	return ring, ring.Validate()
}

func (ring ControlKeyRing) Validate() error {
	seen := map[executioncontrol.ActivationEpoch]bool{}
	for _, key := range ring.Keys {
		public, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize || key.Epoch == 0 || seen[key.Epoch] {
			return fmt.Errorf("%w: each control epoch requires one Ed25519 public key", output.ErrCorrupt)
		}
		seen[key.Epoch] = true
	}
	if !seen[ring.ActivationEpoch] {
		return fmt.Errorf("%w: no control verification key for the active epoch", output.ErrIncomplete)
	}
	return nil
}

func (ring ControlKeyRing) VerifyCapture(ack output.CaptureAcknowledgement) error {
	if err := ring.Validate(); err != nil {
		return err
	}
	for _, key := range ring.Keys {
		if key.Epoch == ack.ActivationEpoch {
			public, _ := base64.StdEncoding.DecodeString(key.PublicKey)
			return output.VerifyCaptureAcknowledgement(ack, ed25519.PublicKey(public))
		}
	}
	return fmt.Errorf("%w: no control verification key for the hold's epoch", output.ErrUnsigned)
}

func (ring ControlKeyRing) VerifyExecution(ack executioncontrol.Acknowledgement) error {
	if err := ring.Validate(); err != nil {
		return err
	}
	for _, key := range ring.Keys {
		if key.Epoch == ack.ActivationEpoch {
			public, _ := base64.StdEncoding.DecodeString(key.PublicKey)
			return executioncontrol.VerifyAcknowledgement(ack, ed25519.PublicKey(public))
		}
	}
	return fmt.Errorf("%w: no control verification key for the execution's epoch", output.ErrUnsigned)
}

func (ring ControlKeyRing) VerifyRelease(ack output.ReleaseAcknowledgement) error {
	if err := ring.Validate(); err != nil {
		return err
	}
	for _, key := range ring.Keys {
		if key.Epoch == ack.ActivationEpoch {
			public, _ := base64.StdEncoding.DecodeString(key.PublicKey)
			return output.VerifyReleaseAcknowledgement(ack, ed25519.PublicKey(public))
		}
	}
	return fmt.Errorf("%w: no control verification key for the release's epoch", output.ErrUnsigned)
}
