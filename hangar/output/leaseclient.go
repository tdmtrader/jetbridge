package output

// The daemon's side of lease control.
//
// It lives in this leaf package rather than beside the daemon because it is the
// protocol, not the deployment: the same three questions, the same signing, the
// same closed answer vocabulary, and no knowledge of who serves them. What the
// daemon supplies is a base URL, an HTTP client and its own node identity.
//
// It never decides anything. Every method here asks and reports; the decision
// is the control plane's, out of the database, and a client that answered from
// a cache or from a previous reply would be exactly the second opinion this
// exchange exists to avoid.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// MaxLeaseAnswerBytes bounds a reply. An answer is a lease and a word.
const MaxLeaseAnswerBytes = 1 << 16

// LeaseControlClient asks the control plane about one read lease.
type LeaseControlClient struct {
	// BaseURL is the control plane's lease-control root.
	BaseURL string

	// HTTP is the transport. It is a parameter because the deployment's is
	// mutually authenticated and this package must not decide that.
	HTTP *http.Client

	// NodeUID and KeyID identify the asker, and Signer holds the node's control
	// key -- the same one its source-ledger and release statements are signed
	// with, under a different domain.
	NodeUID executioncontrol.NodeUID
	KeyID   string
	Signer  *CaptureStatementSigner

	// Clock dates the question. A question with no instant in it is replayable
	// forever.
	Clock Clock
}

// ValidateLease asks whether this grant's lease may authorize work of the given
// length. It is called BEFORE the object is opened.
func (client *LeaseControlClient) ValidateLease(ctx context.Context, grant string, work time.Duration) (LeaseAnswer, error) {
	return client.ask(ctx, LeaseValidate, grant, work)
}

// RenewLease extends the lease while work proceeds.
func (client *LeaseControlClient) RenewLease(ctx context.Context, grant string, work time.Duration) (LeaseAnswer, error) {
	return client.ask(ctx, LeaseRenew, grant, work)
}

// ReleaseLease closes the reader's protection after verified staging. It is
// idempotent by the control plane's own rule, so a retry after a lost answer is
// safe.
func (client *LeaseControlClient) ReleaseLease(ctx context.Context, grant string) (LeaseAnswer, error) {
	return client.ask(ctx, LeaseRelease, grant, 0)
}

func (client *LeaseControlClient) ask(ctx context.Context, operation LeaseOperation, grant string, work time.Duration) (LeaseAnswer, error) {
	if err := client.wired(); err != nil {
		return LeaseAnswer{}, err
	}

	question, err := client.Signer.SignLeaseQuestion(LeaseQuestion{
		ProtocolVersion:          ProtocolVersion,
		Operation:                operation,
		KeyID:                    client.KeyID,
		NodeUID:                  client.NodeUID,
		Grant:                    grant,
		RequiredRemainingSeconds: int64(work.Round(time.Second).Seconds()),
		IssuedAt:                 NewTimestamp(client.Clock.Now().UTC()),
		Signature:                "unsigned",
	})
	if err != nil {
		return LeaseAnswer{}, err
	}

	body, err := json.Marshal(question)
	if err != nil {
		return LeaseAnswer{}, err
	}

	path := map[LeaseOperation]string{
		LeaseValidate: "/read-lease/v1/validate",
		LeaseRenew:    "/read-lease/v1/renew",
		LeaseRelease:  "/read-lease/v1/release",
	}[operation]

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(client.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return LeaseAnswer{}, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.HTTP.Do(request)
	if err != nil {
		return LeaseAnswer{}, fmt.Errorf("%w: asking the control plane to %s a read lease: %v",
			ErrInfrastructure, operation, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	var answer LeaseAnswer
	if err := json.NewDecoder(io.LimitReader(response.Body, MaxLeaseAnswerBytes)).
		Decode(&answer); err != nil {
		return LeaseAnswer{}, fmt.Errorf("%w: the control plane's %s answer does not decode: %v",
			ErrCorrupt, operation, err)
	}
	if answer.Operation != operation {
		return LeaseAnswer{}, fmt.Errorf("%w: asked to %s and was answered about %s",
			ErrCorrupt, operation, answer.Operation)
	}
	if !answer.Admitted {
		// A refusal is a VALUE, not a transport error: the daemon has to tell
		// "the lease is gone" from "the control plane is unreachable", and
		// collapsing them would make an outage look like a revocation.
		if err := answer.Refusal.Validate(); err != nil {
			return LeaseAnswer{}, err
		}
	}

	return answer, nil
}

func (client *LeaseControlClient) wired() error {
	switch {
	case client == nil || strings.TrimSpace(client.BaseURL) == "":
		return fmt.Errorf("%w: the lease-control client has no control-plane URL", ErrIncomplete)
	case client.HTTP == nil:
		return fmt.Errorf("%w: the lease-control client has no transport", ErrIncomplete)
	case client.NodeUID == "":
		return fmt.Errorf("%w: the lease-control client names no node", ErrInvalidIdentity)
	case client.KeyID == "":
		return fmt.Errorf("%w: the lease-control client names no signing key", ErrIncomplete)
	case client.Signer == nil:
		return fmt.Errorf("%w: the lease-control client cannot sign its questions", ErrIncomplete)
	case client.Clock == nil:
		return fmt.Errorf("%w: the lease-control client has no clock", ErrIncomplete)
	}

	return nil
}
