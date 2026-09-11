package hangaroutput

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"crypto/ed25519"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// ReceiptKeyRing is the control plane's versioned receipt VERIFICATION material.
//
// Verification only. The private half lives in the output daemon's Pod and
// nowhere else, so nothing this type holds can sign a receipt -- which is what
// makes it safe to mount as a ConfigMap on the web node and to log the key ids
// it carries.
//
// It is a RING and not a key because rotation creates a new activation epoch
// rather than replacing a key in place: an old public key stays here while any
// durable state still references its epoch -- a reservation, a receipt, a
// recovery row -- and dropping it would make those receipts unverifiable
// forever rather than merely unusable.
type ReceiptKeyRing struct {
	ActiveKeyID     string                           `json:"active_key_id"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	Keys            []ReceiptKeyEntry                `json:"keys"`
}

// ReceiptKeyEntry is one epoch's verification key.
type ReceiptKeyEntry struct {
	ID        string                           `json:"id"`
	Epoch     executioncontrol.ActivationEpoch `json:"epoch"`
	Retired   bool                             `json:"retired"`
	PublicKey string                           `json:"public_key"`
}

// LoadReceiptKeyRing reads and validates a ring from disk.
//
// It validates rather than merely decodes, and the reason is the failure this
// plane cannot afford: a control plane that started with an unusable ring would
// verify nothing, and "verified nothing" and "verified successfully" are the
// same observable outcome on every path that only checks for an error late.
func LoadReceiptKeyRing(path string) (ReceiptKeyRing, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ReceiptKeyRing{}, fmt.Errorf("%w: reading the receipt key ring: %v",
			output.ErrIncomplete, err)
	}

	var ring ReceiptKeyRing
	if err := json.Unmarshal(body, &ring); err != nil {
		return ReceiptKeyRing{}, fmt.Errorf("%w: decoding the receipt key ring at %s: %v",
			output.ErrCorrupt, path, err)
	}
	if err := ring.Validate(); err != nil {
		return ReceiptKeyRing{}, err
	}

	return ring, nil
}

// Validate refuses a ring that cannot do its job.
func (ring ReceiptKeyRing) Validate() error {
	if ring.ActiveKeyID == "" {
		return fmt.Errorf("%w: the receipt key ring names no active key; a receipt names the "+
			"key that can check it", output.ErrIncomplete)
	}
	if ring.ActivationEpoch == 0 {
		return fmt.Errorf("%w: the receipt key ring names no activation epoch",
			output.ErrIncomplete)
	}
	if len(ring.Keys) == 0 {
		return fmt.Errorf("%w: the receipt key ring is empty", output.ErrIncomplete)
	}

	seen := map[string]string{}
	var active *ReceiptKeyEntry
	for index := range ring.Keys {
		entry := &ring.Keys[index]
		if entry.ID == "" {
			return fmt.Errorf("%w: receipt key ring entry %d has no id", output.ErrIncomplete, index)
		}
		if entry.Epoch == 0 {
			return fmt.Errorf("%w: receipt key %q has no activation epoch",
				output.ErrIncomplete, entry.ID)
		}
		material, err := base64.StdEncoding.DecodeString(entry.PublicKey)
		if err != nil {
			return fmt.Errorf("%w: receipt key %q is not base64: %v",
				output.ErrCorrupt, entry.ID, err)
		}
		if len(material) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: receipt key %q is %d bytes; an Ed25519 public key is %d",
				output.ErrCorrupt, entry.ID, len(material), ed25519.PublicKeySize)
		}
		if previous, repeated := seen[entry.ID]; repeated && previous != entry.PublicKey {
			return fmt.Errorf("%w: receipt key id %q appears twice with two different public "+
				"keys. A receipt key is never replaced in place: rotation creates a new "+
				"activation epoch with a new key id, and two keys under one id makes \"which "+
				"key checks this receipt\" unanswerable", output.ErrCorrupt, entry.ID)
		}
		seen[entry.ID] = entry.PublicKey
		if entry.ID == ring.ActiveKeyID {
			active = entry
		}
	}

	if active == nil {
		return fmt.Errorf("%w: the active receipt key %q has no entry in the ring",
			output.ErrIncomplete, ring.ActiveKeyID)
	}
	if active.Retired {
		return fmt.Errorf("%w: the active receipt key %q is retired. A retired key verifies "+
			"old receipts; it does not sign new ones", output.ErrIncomplete, ring.ActiveKeyID)
	}
	if active.Epoch != ring.ActivationEpoch {
		return fmt.Errorf("%w: the active receipt key %q is declared for epoch %d and the ring "+
			"is for epoch %d. Rotation creates a NEW epoch rather than reusing an id",
			output.ErrIncomplete, ring.ActiveKeyID, active.Epoch, ring.ActivationEpoch)
	}

	return nil
}

// VerifierFor returns the public key that checks a receipt signed under one key
// id, and says no rather than guessing.
//
// A retired key still verifies: retirement stops it signing, and the receipts it
// already signed must stay checkable for as long as anything references them.
func (ring ReceiptKeyRing) VerifierFor(keyID string) (ed25519.PublicKey, error) {
	for _, entry := range ring.Keys {
		if entry.ID != keyID {
			continue
		}
		material, err := base64.StdEncoding.DecodeString(entry.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("%w: receipt key %q is not base64: %v",
				output.ErrCorrupt, keyID, err)
		}

		return ed25519.PublicKey(material), nil
	}

	return nil, fmt.Errorf("%w: no verification key for receipt key id %q. The ring retains "+
		"public material while any durable state references its epoch, so this is either a "+
		"receipt from outside this deployment or a key that was dropped too early",
		output.ErrNotFound, keyID)
}
