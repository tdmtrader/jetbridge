package outputplane

import (
	"context"
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

// Daemon is the output plane's node-local publisher.
//
// What it holds is the whole statement of its role: one namespace and one
// publisher restricted to create and read. What it does not hold is the point
// -- there is no cache client, no strict-input client, no list or delete
// capability, and no database handle. The durable half of a capture is the
// control plane's row; this process answers a digest and a generation.
type Daemon struct {
	// namespace and publisher are the capture extension, and both are
	// zero on a base-control-only daemon. That is a real deployment and not a
	// degraded one: the sibling `exact_execution_control` track schedules onto
	// it, and withdrawing the extension has to reach it from a running
	// plane. What makes it safe is that the route table already names a
	// purpose per route, so "there is no publisher" is a typed refusal at the
	// boundary rather than a nil dereference three calls in.
	namespace output.OutputNamespace
	publisher *publisher.Publisher

	// canonicalizer turns a sealed source directory into the one canonical form
	// this repository has. It is the foundation's, not a second implementation:
	// two answers to "what are these bytes" is two digests for one tree.
	canonicalizer    hangar.Canonicalizer
	nodeUID          executioncontrol.NodeUID
	operationTimeout time.Duration
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
	var err error

	// The capture extension, or nothing. Everything below is the extension,
	// and a base-only daemon builds none of it -- no namespace, no object
	// client and no publisher.
	var (
		namespace     output.OutputNamespace
		role          *publisher.Publisher
		canonicalizer hangar.Canonicalizer
	)
	if config.CaptureEnabled() {
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

	return &Daemon{
		namespace: namespace, publisher: role,
		canonicalizer:    canonicalizer,
		nodeUID:          executioncontrol.NodeUID(config.NodeUID),
		operationTimeout: config.OperationTimeout,
	}, nil
}

// OutputEnabled reports whether this daemon carries the capture extension.
func (daemon *Daemon) OutputEnabled() bool { return !daemon.namespace.IsZero() }

func (daemon *Daemon) Publisher() *publisher.Publisher { return daemon.publisher }

// ExtensionHandshake is what this daemon reports about its capture
// extension: which protocol it speaks, which key domain checks its read
// warrants (there is one: the Hangar key's), and which bucket and namespace it
// publishes into.
//
// It embeds the base handshake rather than restating it, so a base-only daemon
// answers for exact control alone. None of it is authority.
func (daemon *Daemon) ExtensionHandshake() output.ExtensionHandshake {
	return output.ExtensionHandshake{
		Base:                    daemon.BaseHandshake(),
		CaptureExtensionVersion: output.ProtocolVersion,
		SourceLedgerVersion:     output.SourceLedgerVersion,
		BucketFingerprint:       daemon.namespace.BucketFingerprint(),
		DerivedNamespace:        daemon.namespace.ListPrefix(),
	}
}

// BaseHandshake is what this daemon speaks for the base protocol.
func (daemon *Daemon) BaseHandshake() executioncontrol.Handshake {
	return executioncontrol.Handshake{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		LedgerVersion:   executioncontrol.LedgerVersion,
	}
}

// Namespace is what this daemon publishes into.
func (daemon *Daemon) Namespace() output.OutputNamespace { return daemon.namespace }

// OpenRead opens one published object's bytes under a verified read warrant.
func (daemon *Daemon) OpenRead(ctx context.Context, warrant hangar.Warrant) (io.ReadCloser, output.PublishedObject, error) {
	return daemon.publisher.OpenExactObject(ctx, warrant.Ref, warrant)
}

// The daemon holds the shared seam, never a cloud client type: hangargcs's
// adapter returns an objectstore.Client and publisher.Restrict narrows it. The
// direction is checked by the compiler on the Build path above -- this file
// names cloud.google.com/go/storage nowhere, which is what keeps hangar/gcs the
// only package in the repository that does.
var _ objectstore.Client = (objectstore.Client)(nil)
