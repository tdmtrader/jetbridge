package executioncontrol

// Signing an acknowledgement.
//
// An acknowledgement is the only thing in this protocol that constitutes proof,
// and a statement anybody can write is not proof. The node's daemon signs each
// one with the control key its activation epoch pins; a control plane verifies
// with the public half it already holds, and never with a key the message
// carried.
//
// The signature covers the canonical bytes of every field except the signature
// itself, so no field can be edited in flight and no field can be added without
// entering the signed set -- CanonicalAcknowledgementBytes is written as an
// explicit field list for that reason, and the coverage test walks the struct
// to prove the list is complete.
//
// This file is stdlib-only, like the rest of this leaf package.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// AcknowledgementDomain separates these bytes from every other signature in the
// system. A receipt and an acknowledgement are signed by different keys for
// different purposes; a domain string means neither can ever be presented as
// the other even if a key were reused by mistake.
const AcknowledgementDomain = "hangar-execution-acknowledgement-v1"

// ErrUnsigned is an acknowledgement whose signature is absent, malformed, or
// does not verify under the pinned key.
var ErrUnsigned = errors.New("hangar/executioncontrol: acknowledgement does not verify")

// CanonicalAcknowledgementBytes is the exact byte string an acknowledgement's
// signature covers.
//
// Length-prefixed fields, not a delimiter: a delimiter that can occur inside a
// value lets two different acknowledgements canonicalize the same way, and a
// node uid is an opaque string this package does not get to constrain.
func CanonicalAcknowledgementBytes(ack Acknowledgement) []byte {
	var out []byte

	field := func(value string) {
		out = binary.BigEndian.AppendUint64(out, uint64(len(value)))
		out = append(out, value...)
	}

	field(AcknowledgementDomain)
	field(ack.ProtocolVersion)
	field(string(ack.Kind))
	field(string(ack.ExecutionID))
	field(strconv.FormatUint(uint64(ack.Fence), 10))
	field(strconv.FormatUint(uint64(ack.ActivationEpoch), 10))
	field(strconv.FormatUint(uint64(ack.LedgerSequence), 10))
	field(string(ack.NodeUID))
	field(string(ack.PodUID))
	field(string(ack.ProcessIdentity))
	field(ack.ObservedAt.UTC().Format(timestampLayout))
	if ack.Outcome == nil {
		field("no-outcome")
	} else {
		field("outcome")
		field(strconv.Itoa(ack.Outcome.ExitCode))
		field(strconv.FormatBool(ack.Outcome.Signalled))
		field(ack.Outcome.Signal)
	}

	return out
}

// AcknowledgementSigner is the node's half. The private key exists only in the
// daemon process that holds the ledger.
type AcknowledgementSigner struct {
	private ed25519.PrivateKey
}

func NewAcknowledgementSigner(private ed25519.PrivateKey) (*AcknowledgementSigner, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: the control signing key is %d bytes, not %d",
			ErrIncomplete, len(private), ed25519.PrivateKeySize)
	}

	return &AcknowledgementSigner{private: private}, nil
}

// PublicKey is the half an activation epoch pins.
func (signer *AcknowledgementSigner) PublicKey() ed25519.PublicKey {
	return signer.private.Public().(ed25519.PublicKey)
}

// Sign returns the acknowledgement with its signature filled in. It validates
// first: an acknowledgement that contradicts itself must not become one that
// contradicts itself and is signed.
func (signer *AcknowledgementSigner) Sign(ack Acknowledgement) (Acknowledgement, error) {
	ack.Signature = ""
	// Validate refuses an unsigned acknowledgement, which is exactly what this
	// one is until the next line, so the structural half is checked against a
	// placeholder and the real signature replaces it.
	probe := ack
	probe.Signature = "unsigned"
	if err := probe.Validate(); err != nil {
		return Acknowledgement{}, err
	}

	ack.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(signer.private, CanonicalAcknowledgementBytes(ack)))

	return ack, nil
}

// VerifyAcknowledgement checks the statement against the pinned public key.
//
// The key is a parameter and never read out of the message: an acknowledgement
// that named the key that checks it would be a statement verifying itself.
func VerifyAcknowledgement(ack Acknowledgement, public ed25519.PublicKey) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: the pinned public key is %d bytes, not %d",
			ErrIncomplete, len(public), ed25519.PublicKeySize)
	}

	signature, err := base64.StdEncoding.DecodeString(ack.Signature)
	if err != nil {
		return fmt.Errorf("%w: the signature is not base64: %v", ErrUnsigned, err)
	}

	unsigned := ack
	unsigned.Signature = ""
	if !ed25519.Verify(public, CanonicalAcknowledgementBytes(unsigned), signature) {
		return fmt.Errorf("%w: the %s acknowledgement for %s does not verify under the pinned "+
			"key", ErrUnsigned, ack.Kind, ack.ExecutionID)
	}

	return nil
}
