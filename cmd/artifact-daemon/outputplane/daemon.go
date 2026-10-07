package outputplane

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar/disk"

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
	// namespace, publisher and signer are the OUTPUT facet, and all three are
	// zero on a base-control-only daemon. That is a real deployment and not a
	// degraded one: the sibling `exact_execution_control` track schedules onto
	// it, and Req 58's output-only downgrade has to reach it from a running
	// plane. What makes it safe is that the route table already names a facet
	// per route, so "there is no publisher" is a typed refusal at the boundary
	// rather than a nil dereference three calls in.
	namespace output.OutputNamespace
	publisher *publisher.Publisher
	signer    *output.ReceiptSigner

	// epoch is the base facet's activation epoch, which exists whether or not
	// the output facet does. It is read from configuration rather than from the
	// namespace for exactly that reason.
	epoch executioncontrol.ActivationEpoch

	// materializationKeyID is what the extension handshake reports so a control
	// plane knows which pinned key checks this node's read warrants.
	materializationKeyID string

	// canonicalizer turns a sealed source directory into the one canonical form
	// this repository has. It is the foundation's, not a second implementation:
	// two answers to "what are these bytes" is two digests for one tree.
	canonicalizer    hangar.Canonicalizer
	nodeUID          executioncontrol.NodeUID
	operationTimeout time.Duration

	// controlKeyID names the Ed25519 key this node signs execution and source
	// ledger statements with. It is a DIFFERENT key from the receipt key: a
	// receipt says an object exists in a bucket, a control statement says a
	// process on this node did something, and an epoch pins both separately so
	// that rotating one does not rotate the other.
	controlKeyID  string
	controlSigner *executioncontrol.AcknowledgementSigner
}

// Build constructs the daemon from a validated configuration.
//
// GCS construction does not inspect bucket policy: the publisher needs object
// operations, and storage configuration is the operator's responsibility.
// Actual storage failures remain typed errors rather than successful admission.
func Build(ctx context.Context, config Config) (*Daemon, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// Validate's key separation is over PATHS. This is the same rule over the
	// bytes, which is what authority actually follows.
	if err := config.RefuseCollidingKeyMaterial(); err != nil {
		return nil, err
	}
	var err error

	// The output facet, or nothing. Everything between here and the control key
	// is the capture extension, and a base-only daemon builds none of it -- no
	// namespace, no object client, no publisher, and above all no receipt
	// signing key. A key mounted into a process that cannot need it is a key an
	// exploit of that process gets for free.
	var (
		namespace     output.OutputNamespace
		signer        *output.ReceiptSigner
		role          *publisher.Publisher
		canonicalizer hangar.Canonicalizer
	)
	if config.OutputFacetEnabled() {
		namespace, err = config.Namespace()
		if err != nil {
			return nil, err
		}

		private, err := config.LoadReceiptKey()
		if err != nil {
			return nil, err
		}

		signer, err = output.NewReceiptSigner(config.ReceiptKeyID, namespace.ActivationEpoch(),
			private, output.ClockFunc(nowUTC))
		if err != nil {
			return nil, err
		}

		// The object seam, and no client above it. This root cannot name a
		// *storage.Client at all: hangar/gcs opens its own behind this
		// constructor and hands back an interface with no delete on it, which
		// is what makes `client.Bucket(b).Object(k).Delete(ctx)` here a compile
		// error rather than a line that built and passed every guard.
		var objects objectstore.Client
		if config.OutputStore == output.StoreDisk {
			objects, err = disk.NewClient(disk.ClientConfig{Endpoint: config.OutputEndpoint, StoreID: config.OutputStoreID, TokenFile: config.OutputTokenFile, CACert: config.OutputCACert, Timeout: config.OperationTimeout})
		} else {
			objects, _, err = hangargcs.NewClient(ctx, config.OutputEndpoint)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: building the output object client: %v",
				output.ErrInfrastructure, err)
		}

		role, err = publisher.New(namespace, publisher.Restrict(objects), config.OperationTimeout)
		if err != nil {
			return nil, err
		}

		// The canonicalizer needs a trusted temporary parent that exists and is
		// owned by this process. The DaemonSet mounts the hostPath; the
		// subdirectory beneath it is the daemon's own, so it is created here
		// rather than assumed.
		if err := config.PrepareScratch(); err != nil {
			return nil, err
		}
		canonicalizer = hangar.Canonicalizer{TempDir: config.ScratchDir}
	}

	controlPrivate, err := config.LoadControlKey()
	if err != nil {
		return nil, err
	}
	controlSigner, err := executioncontrol.NewAcknowledgementSigner(controlPrivate)
	if err != nil {
		return nil, err
	}

	return &Daemon{
		namespace: namespace, publisher: role, signer: signer,
		epoch:                activationEpoch(config.ActivationEpoch),
		materializationKeyID: config.MaterializationKeyID,
		canonicalizer:        canonicalizer,
		nodeUID:              executioncontrol.NodeUID(config.NodeUID),
		operationTimeout:     config.OperationTimeout,
		controlKeyID:         config.ControlKeyID,
		controlSigner:        controlSigner,
	}, nil
}

// OutputEnabled reports whether this daemon carries the capture extension.
func (daemon *Daemon) OutputEnabled() bool { return !daemon.namespace.IsZero() }

// ActivationEpoch is the BASE facet's epoch, which exists on every daemon.
//
// The capability verifier and the base handshake read it from here rather than
// from the namespace, because a base-only daemon has an epoch and no namespace,
// and reading it off the namespace is how that daemon would start answering
// every capability check against epoch zero.
func (daemon *Daemon) ActivationEpoch() executioncontrol.ActivationEpoch { return daemon.epoch }

// ReceiptSigner and Publisher are the output facet, and are nil without it.
func (daemon *Daemon) ReceiptSigner() *output.ReceiptSigner { return daemon.signer }

func (daemon *Daemon) Publisher() *publisher.Publisher { return daemon.publisher }

// ExtensionHandshake is the authenticated evidence Req 56 requires before a
// checkpoint or a capture: what this cohort speaks, which keys check its
// statements, and which bucket and namespace it publishes into.
//
// It embeds the base handshake rather than restating it, so a base-only cohort
// is attestable for exact control while output_state is still initial. None of
// it is authority; the activation epoch row is.
func (daemon *Daemon) ExtensionHandshake() output.ExtensionHandshake {
	return output.ExtensionHandshake{
		Base:                    daemon.BaseHandshake(),
		CaptureExtensionVersion: output.ProtocolVersion,
		SourceLedgerVersion:     output.SourceLedgerVersion,
		ReceiptPublicKeyID:      daemon.receiptKeyID(),
		MaterializationKeyID:    daemon.materializationKeyID,
		BucketFingerprint:       daemon.namespace.BucketFingerprint(),
		DerivedNamespace:        daemon.namespace.ListPrefix(),
	}
}

// BaseHandshake is what this daemon speaks for the base protocol.
func (daemon *Daemon) BaseHandshake() executioncontrol.Handshake {
	return executioncontrol.Handshake{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		LedgerVersion:   executioncontrol.LedgerVersion,
		ControlKeyID:    daemon.controlKeyID,
		ActivationEpoch: daemon.epoch,
	}
}

func (daemon *Daemon) receiptKeyID() string {
	if daemon.signer == nil {
		return ""
	}

	return daemon.signer.KeyID()
}

// ControlKeyID is what the handshake reports, so a control plane knows which
// pinned public key checks this node's statements.
func (daemon *Daemon) ControlKeyID() string { return daemon.controlKeyID }

// ControlSigner and CaptureSigner are the node's two statement signers over one
// key. They are handed to the ledgers at construction and to nothing else.
func (daemon *Daemon) ControlSigner() *executioncontrol.AcknowledgementSigner {
	return daemon.controlSigner
}

// ControlPublicKey is the half an activation epoch pins for this node's
// execution and source statements.
func (daemon *Daemon) ControlPublicKey() ed25519.PublicKey { return daemon.controlSigner.PublicKey() }

// Namespace is what this daemon publishes into.
func (daemon *Daemon) Namespace() output.OutputNamespace { return daemon.namespace }

// ReceiptPublicKey is the half the activation epoch pins. It is the only key
// material this process will hand out.
func (daemon *Daemon) ReceiptPublicKey() ed25519.PublicKey { return daemon.signer.PublicKey() }

// OpenRead opens one published object's bytes under a verified read warrant.
func (daemon *Daemon) OpenRead(ctx context.Context, warrant output.ReadWarrantClaims) (io.ReadCloser, output.PublishedObject, error) {
	return daemon.publisher.OpenExactObject(ctx, warrant.Ref, warrant)
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
