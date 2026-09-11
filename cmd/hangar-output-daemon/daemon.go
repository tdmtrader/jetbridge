package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"fmt"
	"io"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

func activationEpoch(value uint64) executioncontrol.ActivationEpoch {
	return executioncontrol.ActivationEpoch(value)
}

// Daemon is the output plane's node-local publisher and receipt signer.
//
// What it holds is the whole statement of its role: one namespace, one
// publisher restricted to create and read, and one epoch's private key. What it
// does not hold is the point -- there is no cache client, no strict-input
// client, no list or delete capability, and no database handle. A daemon with a
// database handle would need a database credential on every node; the durable
// half of a capture is the control plane's transaction, and this process's
// contribution to it is a signed receipt it hands back.
type Daemon struct {
	namespace output.OutputNamespace
	publisher *publisher.Publisher
	signer    *output.ReceiptSigner

	// canonicalizer turns a sealed source directory into the one canonical form
	// this repository has. It is the foundation's, not a second implementation:
	// two answers to "what are these bytes" is two digests for one tree.
	canonicalizer hangar.Canonicalizer

	// controlKeyID names the Ed25519 key this node signs execution and source
	// ledger statements with. It is a DIFFERENT key from the receipt key: a
	// receipt says an object exists in a bucket, a control statement says a
	// process on this node did something, and an epoch pins both separately so
	// that rotating one does not rotate the other.
	controlKeyID  string
	controlSigner *executioncontrol.AcknowledgementSigner
	captureSigner *output.CaptureStatementSigner
}

// Build constructs the daemon from a validated configuration.
//
// It deliberately does not probe the bucket. The foundation's publisher startup
// calls Bucket.Attrs to fail fast on a misconfigured bucket, and copying that
// here would require storage.buckets.get on this principal -- a bucket-policy
// permission the output publisher must not have. The policy attestor owns
// bucket verification, and activation is gated on its attestation rather than
// on a probe from the principal whose honesty is being attested.
func Build(ctx context.Context, config Config) (*Daemon, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	namespace, err := config.Namespace()
	if err != nil {
		return nil, err
	}

	private, err := config.LoadReceiptKey()
	if err != nil {
		return nil, err
	}

	signer, err := output.NewReceiptSigner(config.ReceiptKeyID, namespace.ActivationEpoch(),
		private, output.ClockFunc(nowUTC))
	if err != nil {
		return nil, err
	}

	// The object seam, and no client above it. This root cannot name a
	// *storage.Client at all: hangar/gcs opens its own behind this constructor
	// and hands back an interface with no delete on it, which is what makes
	// `client.Bucket(b).Object(k).Delete(ctx)` here a compile error rather than
	// a line that built and passed every guard.
	objects, _, err := hangargcs.NewObjectClient(ctx, config.OutputEndpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: building the output object client: %v",
			output.ErrInfrastructure, err)
	}

	role, err := publisher.New(namespace, publisher.Restrict(objects), config.OperationTimeout)
	if err != nil {
		return nil, err
	}

	// The canonicalizer needs a trusted temporary parent that exists and is
	// owned by this process. The DaemonSet mounts the hostPath; the
	// subdirectory beneath it is the daemon's own, so it is created here rather
	// than assumed.
	if err := config.PrepareScratch(); err != nil {
		return nil, err
	}

	controlPrivate, err := config.LoadControlKey()
	if err != nil {
		return nil, err
	}
	controlSigner, err := executioncontrol.NewAcknowledgementSigner(controlPrivate)
	if err != nil {
		return nil, err
	}
	captureSigner, err := output.NewCaptureStatementSigner(controlPrivate)
	if err != nil {
		return nil, err
	}

	return &Daemon{
		namespace: namespace, publisher: role, signer: signer,
		canonicalizer: hangar.Canonicalizer{TempDir: config.ScratchDir},
		controlKeyID:  config.ControlKeyID,
		controlSigner: controlSigner,
		captureSigner: captureSigner,
	}, nil
}

// ControlKeyID is what the handshake reports, so a control plane knows which
// pinned public key checks this node's statements.
func (daemon *Daemon) ControlKeyID() string { return daemon.controlKeyID }

// ControlSigner and CaptureSigner are the node's two statement signers over one
// key. They are handed to the ledgers at construction and to nothing else.
func (daemon *Daemon) ControlSigner() *executioncontrol.AcknowledgementSigner {
	return daemon.controlSigner
}

func (daemon *Daemon) CaptureSigner() *output.CaptureStatementSigner { return daemon.captureSigner }

// ControlPublicKey is the half an activation epoch pins for this node's
// execution and source statements.
func (daemon *Daemon) ControlPublicKey() ed25519.PublicKey { return daemon.controlSigner.PublicKey() }

// Namespace is what this daemon publishes into.
func (daemon *Daemon) Namespace() output.OutputNamespace { return daemon.namespace }

// ReceiptPublicKey is the half the activation epoch pins. It is the only key
// material this process will hand out.
func (daemon *Daemon) ReceiptPublicKey() ed25519.PublicKey { return daemon.signer.PublicKey() }

// PublishRequest is what the daemon is asked to publish.
//
// Namespace is embedded, and it is the only place a caller-chosen bucket, scope
// or key is representable at all. It is here rather than absent so that such a
// field is *refused with a message* instead of silently dropped: a hostile
// client really can put one in a request body, and the difference between
// ignoring it and refusing it is whether an operator ever finds out.
type PublishRequest struct {
	Namespace   output.CallerNamespaceRequest `json:"namespace"`
	Reservation output.ResolvedReservation    `json:"-"`
}

// Publish creates the object and reports the exact generation. It signs
// nothing.
//
// The order matters and is not negotiable. The object is created first, because
// a receipt is a statement about bytes that exist; then the exact generation is
// read back off the store rather than taken from the writer, because Req 26
// says the signed attributes come from a stat and not from what the writer
// believed.
//
// The receipt is not produced here, and the reason is the ordering the schema
// fixes: hangar_receipt_stat_challenges.generation is NOT NULL CHECK
// (generation > 0), so a challenge cannot exist until this call has returned
// the generation it names. A receipt signed at publish time is therefore a
// receipt bound to facts and to no challenge -- it answers every later
// challenge naming the same facts and cannot show its stat post-dates any of
// them. Signing lives in StatExact below, where a challenge is in hand.
//
// What this method deliberately does not do is register anything. Registration
// is the caller's transaction -- it consumes the one-use stat challenge while
// revalidating the reservation, the fences, the epoch and the exact generation
// -- and a daemon that could register would be a daemon holding a database
// credential on every node.
func (daemon *Daemon) Publish(ctx context.Context, request PublishRequest, canonical io.Reader, size int64) (output.PublishedObject, error) {
	if err := request.Namespace.Validate(); err != nil {
		return output.PublishedObject{}, err
	}
	if err := request.Reservation.Validate(); err != nil {
		return output.PublishedObject{}, err
	}

	object, err := daemon.publisher.EnsureObject(ctx, request.Reservation, canonical, size)
	if err != nil {
		return output.PublishedObject{}, err
	}

	fresh, err := daemon.publisher.StatExactObject(ctx, object.Attributes.Ref)
	if err != nil {
		return output.PublishedObject{}, err
	}
	fresh.Deduplicated = object.Deduplicated

	return fresh, nil
}

// StatExact answers one stat challenge: a fresh exact-generation marked stat,
// performed outside every database lock, signed against the challenge that
// demanded it.
//
// The claims the caller passes are the control plane's own facts -- the
// producer checkpoint, the source incarnation, the writer fence and the
// selected output -- which this process cannot check and does not pretend to.
// What it fills in itself is everything it *can* observe or is authoritative
// for: the protocol and receipt versions, its own activation epoch, the
// challenge's identities and fences, the exact ref and strict attributes the
// stat returned, the marker version the store reported, and the nonce and
// issued-at of the challenge in hand. Req 26 is what revalidates the rest,
// against durable state, in the transaction that consumes the nonce.
func (daemon *Daemon) StatExact(ctx context.Context, challenge output.StatChallenge, claims output.ReceiptClaims) (output.Receipt, output.PublishedObject, error) {
	if err := challenge.Validate(); err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}
	if challenge.ActivationEpoch != daemon.namespace.ActivationEpoch() {
		return output.Receipt{}, output.PublishedObject{}, fmt.Errorf(
			"%w: the challenge names epoch %d and this daemon holds epoch %d's key",
			output.ErrConflict, challenge.ActivationEpoch, daemon.namespace.ActivationEpoch())
	}

	fresh, err := daemon.publisher.StatExactObject(ctx, challenge.Ref)
	if err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}
	if fresh.Attributes.Ref != challenge.Ref {
		return output.Receipt{}, output.PublishedObject{}, fmt.Errorf(
			"%w: the challenge names %s/%s/%d and the stat observed %s/%s/%d",
			output.ErrConflict,
			challenge.Ref.Scope, challenge.Ref.Digest, challenge.Ref.Generation,
			fresh.Attributes.Ref.Scope, fresh.Attributes.Ref.Digest, fresh.Attributes.Ref.Generation)
	}
	// The MARKED half of "a fresh exact-generation marked stat" is already
	// enforced, and enforced in one place: StatExactObject classifies what it
	// finds, and that classifier refuses an unmarked object, an object marked
	// for another scope, and an object marked with another digest, each with
	// its own message. Restating any of those here would be a second statement
	// of the rule that could drift from the first.
	//
	// What is deliberately NOT required anywhere is that the marker name THIS
	// challenge's reservation. It cannot be: two captures of identical
	// canonical bytes deduplicate to one object, and that object carries the
	// marker of whichever wrote it first -- so a reservation-equality rule
	// would make AC 8's "one object and two receipts" unreachable. The brine
	// dedup scenario is what found that. The reservation binding is in the
	// signed claims, taken from the challenge, and is revalidated against
	// durable state in the transaction that consumes the nonce (Req 26).

	claims.ProtocolVersion = output.ProtocolVersion
	claims.ReceiptVersion = output.ReceiptDomain
	claims.ActivationEpoch = daemon.namespace.ActivationEpoch()
	claims.HandoffID = challenge.HandoffID
	claims.ReservationID = challenge.ReservationID
	claims.CaptureFence = challenge.CaptureFence
	claims.ChallengeNonce = challenge.Nonce
	claims.ChallengeIssuedAt = challenge.IssuedAt
	claims.Ref = fresh.Attributes.Ref
	claims.Attributes = output.AttributesFromFoundation(fresh.Attributes)
	claims.MarkerVersion = fresh.Marker.Version

	receipt, err := daemon.signer.Sign(claims)
	if err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}

	return receipt, fresh, nil
}

// parsePKCS8Ed25519 refuses anything that is not an Ed25519 private key.
func parsePKCS8Ed25519(der []byte) (ed25519.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: the receipt signing key is not a PKCS#8 private key: %v",
			output.ErrCorrupt, err)
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: the receipt signing key is a %T; this cohort signs receipts "+
			"with %s and nothing else", output.ErrUnsupportedProtocol, parsed, output.ReceiptAlgorithm)
	}

	return private, nil
}

// The daemon holds the shared seam, never a cloud client type: hangargcs's
// adapter returns an objectstore.Client and publisher.Restrict narrows it. The
// direction is checked by the compiler on the Build path above -- this file
// names cloud.google.com/go/storage nowhere, which is what keeps hangar/gcs the
// only package in the repository that does.
var _ objectstore.Client = (objectstore.Client)(nil)
