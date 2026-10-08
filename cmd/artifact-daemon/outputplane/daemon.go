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

// Daemon is the output plane's node-local publisher.
//
// What it holds is the whole statement of its role: one namespace and one
// publisher restricted to create and read. What it does not hold is the point
// -- there is no cache client, no strict-input client, no list or delete
// capability, and no database handle. The durable half of a capture is the
// control plane's row; this process answers a digest and a generation.
type Daemon struct {
	// namespace and publisher are the OUTPUT facet, and both are
	// zero on a base-control-only daemon. That is a real deployment and not a
	// degraded one: the sibling `exact_execution_control` track schedules onto
	// it, and withdrawing the output facet has to reach it from a running
	// plane. What makes it safe is that the route table already names a facet
	// per route, so "there is no publisher" is a typed refusal at the boundary
	// rather than a nil dereference three calls in.
	namespace output.OutputNamespace
	publisher *publisher.Publisher

	// epoch is the control-key generation, which exists whether or not the
	// output facet does. It is read from configuration rather than from the
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
	// ledger statements with. A control statement says a process on this node
	// did something; it is a separate key from the capability and read-warrant
	// keys so that rotating one does not rotate the others.
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
	// namespace, no object client, no publisher, and no read-warrant key. A key mounted into a process that cannot need it is a key an
	// exploit of that process gets for free.
	var (
		namespace     output.OutputNamespace
		role          *publisher.Publisher
		canonicalizer hangar.Canonicalizer
	)
	if config.OutputFacetEnabled() {
		namespace, err = config.Namespace()
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
		namespace: namespace, publisher: role,
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

// ActivationEpoch is the control-key generation, which exists on every daemon.
//
// The capability verifier and the base handshake read it from here rather than
// from the namespace, because a base-only daemon has an epoch and no namespace,
// and reading it off the namespace is how that daemon would start answering
// every capability check against epoch zero.
func (daemon *Daemon) ActivationEpoch() executioncontrol.ActivationEpoch { return daemon.epoch }

func (daemon *Daemon) Publisher() *publisher.Publisher { return daemon.publisher }

// ExtensionHandshake is what this daemon reports about its capture
// extension: which protocol it speaks, which keys check its statements, and
// which bucket and namespace it publishes into.
//
// It embeds the base handshake rather than restating it, so a base-only daemon
// answers for exact control alone. None of it is authority.
func (daemon *Daemon) ExtensionHandshake() output.ExtensionHandshake {
	return output.ExtensionHandshake{
		Base:                    daemon.BaseHandshake(),
		CaptureExtensionVersion: output.ProtocolVersion,
		SourceLedgerVersion:     output.SourceLedgerVersion,
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

// ControlKeyID is what the handshake reports, so a control plane knows which
// pinned public key checks this node's statements.
func (daemon *Daemon) ControlKeyID() string { return daemon.controlKeyID }

// ControlSigner is the node's statement signer. It is handed to the execution
// ledger at construction and to nothing else.
func (daemon *Daemon) ControlSigner() *executioncontrol.AcknowledgementSigner {
	return daemon.controlSigner
}

// ControlPublicKey is the half the web's control key ring pins, per
// control-key generation, for this node's execution statements.
func (daemon *Daemon) ControlPublicKey() ed25519.PublicKey { return daemon.controlSigner.PublicKey() }

// Namespace is what this daemon publishes into.
func (daemon *Daemon) Namespace() output.OutputNamespace { return daemon.namespace }

// OpenRead opens one published object's bytes under a verified read warrant.
func (daemon *Daemon) OpenRead(ctx context.Context, warrant output.ReadWarrantClaims) (io.ReadCloser, output.PublishedObject, error) {
	return daemon.publisher.OpenExactObject(ctx, warrant.Ref, warrant)
}

// The daemon holds the shared seam, never a cloud client type: hangargcs's
// adapter returns an objectstore.Client and publisher.Restrict narrows it. The
// direction is checked by the compiler on the Build path above -- this file
// names cloud.google.com/go/storage nowhere, which is what keeps hangar/gcs the
// only package in the repository that does.
var _ objectstore.Client = (objectstore.Client)(nil)

// parsePKCS8Ed25519 refuses anything that is not an Ed25519 private key.
func parsePKCS8Ed25519(der []byte) (ed25519.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: the signing key is not a PKCS#8 private key: %v",
			output.ErrCorrupt, err)
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: the signing key is a %T; this daemon signs with Ed25519 "+
			"and nothing else", output.ErrUnsupportedProtocol, parsed)
	}

	return private, nil
}
