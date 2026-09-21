package runs

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// SessionTransport addresses a previously admitted execution. It creates no
// worker and receives credentials only after the Run commits its one-use claim.
type SessionTransport interface {
	ExecBoundSession(context.Context, string, executioncontrol.Acknowledgement, time.Duration, []string, io.Reader, io.Writer) error
}

// CredentialHandoffConfig is trusted startup configuration, never request data.
type CredentialHandoffConfig struct {
	Source         SessionTransport
	Helper, Socket string
	Lifetime       time.Duration
	// WorkerImages are the only images a credential may be delivered into,
	// each a digest-qualified reference (repository@sha256:...). The Run's
	// snapshotted result producer must declare exactly docker:///<pin> as its
	// Pod's only image. Empty admits no delivery at all: the image comes from
	// a template any member able to set it controls, so an unpinned
	// deployment is one that hands the owner's credentials to that member.
	WorkerImages []string
}

// host[:port]/path components, an optional tag, then a mandatory digest.
var credentialWorkerImage = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?` +
	`(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$`)

// ValidateCredentialWorkerImage refuses a pin that is not a digest-qualified
// image reference. A tag can be moved after it is pinned.
func ValidateCredentialWorkerImage(image string) error {
	if !credentialWorkerImage.MatchString(image) {
		return fmt.Errorf("credential worker image %q must be a digest-qualified reference, repository@sha256:<64 hex>", image)
	}
	return nil
}

// AdmitWorkerImage refuses delivery, with ErrCredentialDelivery, unless
// producer -- the result producer's rootfs_uri as atc.RunTaskImage reports it
// -- is exactly docker:///<pin> for a valid configured pin.
func (config CredentialHandoffConfig) AdmitWorkerImage(producer string) error {
	for _, pin := range config.WorkerImages {
		if ValidateCredentialWorkerImage(pin) == nil && producer == "docker:///"+pin {
			return nil
		}
	}
	return ErrCredentialDelivery
}
