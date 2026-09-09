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

	storageClient, err := hangargcs.NewStorageClient(ctx, config.OutputEndpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: building the output storage client: %v",
			output.ErrInfrastructure, err)
	}
	objects, err := hangargcs.NewObjectClient(storageClient)
	if err != nil {
		return nil, err
	}

	role, err := publisher.New(namespace, publisher.Restrict(objects), config.OperationTimeout)
	if err != nil {
		return nil, err
	}

	return &Daemon{namespace: namespace, publisher: role, signer: signer}, nil
}

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
	Claims      output.ReceiptClaims          `json:"-"`
}

// Publish creates the object and signs the receipt for one capture.
//
// The order matters and is not negotiable. The object is created first, because
// a receipt is a statement about bytes that exist; then the exact generation is
// read back off the store rather than taken from the writer, because Req 26
// says the signed attributes come from a stat and not from what the writer
// believed; and only then is anything signed.
//
// What this method deliberately does not do is register anything. Registration
// is the caller's transaction -- it consumes the one-use stat challenge while
// revalidating the reservation, the fences, the epoch and the exact generation
// -- and a daemon that could register would be a daemon holding a database
// credential on every node.
func (daemon *Daemon) Publish(ctx context.Context, request PublishRequest, canonical io.Reader, size int64) (output.Receipt, output.PublishedObject, error) {
	if err := request.Namespace.Validate(); err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}
	if err := request.Reservation.Validate(); err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}

	object, err := daemon.publisher.EnsureObject(ctx, request.Reservation, canonical, size)
	if err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}

	// The exact stat, outside any lock and after the create, is what the
	// receipt's attributes are signed over.
	fresh, err := daemon.publisher.StatExactObject(ctx, object.Attributes.Ref)
	if err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}
	fresh.Deduplicated = object.Deduplicated

	claims := request.Claims
	claims.ProtocolVersion = output.ProtocolVersion
	claims.ReceiptVersion = output.ReceiptDomain
	claims.ActivationEpoch = daemon.namespace.ActivationEpoch()
	claims.ReservationID = request.Reservation.ReservationID
	claims.HandoffID = request.Reservation.HandoffID
	claims.Execution = request.Reservation.Execution
	claims.CaptureFence = request.Reservation.CaptureFence
	claims.Ref = fresh.Attributes.Ref
	claims.Attributes = output.AttributesFromFoundation(fresh.Attributes)
	claims.MarkerVersion = fresh.Marker.Version

	receipt, err := daemon.signer.Sign(claims)
	if err != nil {
		return output.Receipt{}, output.PublishedObject{}, err
	}

	return receipt, fresh, nil
}

// StatExact is the fresh exact-generation observation the control plane's
// challenge demands.
func (daemon *Daemon) StatExact(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	return daemon.publisher.StatExactObject(ctx, ref)
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
