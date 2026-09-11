package hangaroutput_test

// The injectors.
//
// Every one of them wraps a REAL collaborator and interferes with the ANSWER,
// never with the work. That is what an ambiguous failure is: a commit that
// committed and a caller that never learned it, an upload that landed and a
// response that never arrived, a node that did the thing and then went away.
// An injector that made the real call not happen would be testing a
// coordinator against a system that did nothing, which is a different and much
// easier problem -- and it is the problem a fake collaborator always tests.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// lostAnswer is what every injector returns. It is deliberately NOT one of the
// leaf's typed refusals: a lost answer is not a refusal, and a coordinator that
// could tell them apart from the error alone would be a coordinator relying on
// a distinction the network does not make.
var lostAnswer = fmt.Errorf("the answer was lost in transit")

// injectingDialer hands out one control per locator, wrapping the real client.
type injectingDialer struct {
	control *jetbridge.OutputControlClient

	// LoseAfter names an operation whose real call happens and whose answer is
	// then dropped, once.
	LoseAfter string

	// Unreachable makes every call fail before it is made -- node loss, which
	// is a different injection from a lost answer and has a different correct
	// response.
	Unreachable bool

	// CollideOnPublish makes every publish answer the publisher's own typed
	// conflict: an object exists at the server-derived key and it is not this
	// capture's bytes.
	//
	// It is injected rather than arranged in the bucket because arranging it
	// would need an out-of-band writer putting a DIFFERENT tree at a key that
	// contains this tree's digest -- which is a thing the derivation makes
	// impossible for anyone playing by the rules, and the whole reason the
	// conflict is the honest answer for somebody who is not. What the
	// coordinator does with the answer is what is under test.
	CollideOnPublish bool

	calls map[string]int
	mu    sync.Mutex
}

func (dialer *injectingDialer) ForLocator(locator string) (hangaroutput.SourceControl, error) {
	if dialer.Unreachable {
		return nil, fmt.Errorf("%w: node %q is gone", output.ErrInfrastructure, locator)
	}

	return &injectingControl{dialer: dialer, control: dialer.control}, nil
}

func (dialer *injectingDialer) count(operation string) int {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()

	if dialer.calls == nil {
		dialer.calls = map[string]int{}
	}
	dialer.calls[operation]++

	return dialer.calls[operation]
}

// intercept reports whether THIS call's answer should be lost. It is consumed:
// a permanently lost answer is an unreachable node, which is injected
// separately, and a coordinator that never converged would look identical.
func (dialer *injectingDialer) intercept(operation string) bool {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()

	if dialer.LoseAfter != operation {
		return false
	}
	dialer.LoseAfter = ""

	return true
}

func (dialer *injectingDialer) Calls(operation string) int {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()

	return dialer.calls[operation]
}

type injectingControl struct {
	dialer  *injectingDialer
	control *jetbridge.OutputControlClient
}

func (wrapper *injectingControl) Observe(ctx context.Context, id executioncontrol.Identity,
	wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error) {
	return wrapper.control.Observe(ctx, id, wait)
}

func (wrapper *injectingControl) InspectSeal(ctx context.Context, handoff output.HandoffID,
	id executioncontrol.Identity) (output.SealStarted, error) {
	return wrapper.control.InspectSeal(ctx, handoff, id)
}

func (wrapper *injectingControl) BeginSeal(ctx context.Context,
	request output.SealRequest) (output.SealStarted, error) {
	wrapper.dialer.count("begin-seal")
	started, err := wrapper.control.BeginSeal(ctx, request)
	if err == nil && wrapper.dialer.intercept("begin-seal") {
		return output.SealStarted{}, lostAnswer
	}

	return started, err
}

func (wrapper *injectingControl) ConfirmSeal(ctx context.Context,
	confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error) {
	wrapper.dialer.count("confirm-seal")
	ack, err := wrapper.control.ConfirmSeal(ctx, confirmation)
	if err == nil && wrapper.dialer.intercept("confirm-seal") {
		return output.CaptureAcknowledgement{}, lostAnswer
	}

	return ack, err
}

func (wrapper *injectingControl) Canonicalize(ctx context.Context,
	request output.PublicationRequest) (output.CanonicalizationResult, error) {
	wrapper.dialer.count("canonicalize")
	result, err := wrapper.control.Canonicalize(ctx, request)
	if err == nil && wrapper.dialer.intercept("canonicalize") {
		return output.CanonicalizationResult{}, lostAnswer
	}

	return result, err
}

func (wrapper *injectingControl) Publish(ctx context.Context,
	request output.PublicationRequest) (output.PublicationResult, error) {
	wrapper.dialer.count("publish")
	if wrapper.dialer.CollideOnPublish {
		return output.PublicationResult{}, fmt.Errorf("%w: an object exists at the "+
			"server-derived key and it is not this capture's bytes", output.ErrConflict)
	}
	result, err := wrapper.control.Publish(ctx, request)
	if err == nil && wrapper.dialer.intercept("publish") {
		// The object IS created. This is the lost upload response: the store
		// has the bytes and the caller does not know it.
		return output.PublicationResult{}, lostAnswer
	}

	return result, err
}

func (wrapper *injectingControl) Attest(ctx context.Context, challenge output.StatChallenge,
	claims output.ReceiptClaims) (output.Receipt, error) {
	wrapper.dialer.count("attest")
	receipt, err := wrapper.control.Attest(ctx, challenge, claims)
	if err == nil && wrapper.dialer.intercept("attest") {
		return output.Receipt{}, lostAnswer
	}

	return receipt, err
}

func (wrapper *injectingControl) AcknowledgeRelease(ctx context.Context,
	intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error) {
	wrapper.dialer.count("release")
	ack, err := wrapper.control.AcknowledgeRelease(ctx, intent)
	if err == nil && wrapper.dialer.intercept("release") {
		// The source is GONE and the caller does not know it. The repeat has
		// to be idempotent at the node or the handoff never completes.
		return output.ReleaseAcknowledgement{}, lostAnswer
	}

	return ack, err
}

// stubDrain stands for the deployment's writer termination and container-status
// proof.
//
// It is a stub and not a real Kubernetes client on purpose, and the reason is
// the plan's own recut: what a real kubelet does with a Pod is a K3s flow under
// a build tag, and what this package owns is that an unprovable drain becomes
// seal_unconfirmed rather than a publication. The interesting input here is the
// REFUSAL, and a real cluster is not needed to say "I could not prove it".
type stubDrain struct {
	// Unprovable makes ConfirmDrain refuse, which is the Kubernetes timeout:
	// no complete final container status before the deadline.
	Unprovable bool

	// UnprovableOnce is the same typed refusal for ONE pass. It is what makes
	// the deadline assertable in both directions: the same evidence that is
	// terminal after the deadline has to be retried before it, and a stub that
	// could only refuse forever could only ever demonstrate the first.
	UnprovableOnce bool

	// TransientErrors is the arm this stub was missing, and its absence is why
	// a destructive guess at the seal boundary was invisible: a confirmer that
	// only ever refuses when told to, and refuses with the one typed sentinel
	// that MEANS terminal, cannot distinguish "could not prove it" from "could
	// not reach the apiserver this second". A kube-apiserver timeout is the
	// second, it is ordinary, and it is consumed here so the retry succeeds.
	TransientErrors int

	// WhileDraining runs INSIDE the drain, between the call and its answer, and
	// it is the arm that makes the seal deadline's real window assertable.
	//
	// The drain is the SLOW half of a seal: it terminates the producing Pod and
	// waits for a complete final container status, which is the half a
	// five-minute deadline actually elapses in. A stub that could only be slow
	// or refuse before the deadline could only ever demonstrate a deadline that
	// had already passed when the boundary was first considered -- which is the
	// easy half, and not the half production reaches.
	WhileDraining func()

	calls int
	mu    sync.Mutex
}

func (drain *stubDrain) ConfirmDrain(_ context.Context, _ string,
	started output.SealStarted) ([]output.DrainedWriter, error) {
	drain.mu.Lock()
	drain.calls++
	transient := drain.TransientErrors > 0
	if transient {
		drain.TransientErrors--
	}
	once := drain.UnprovableOnce
	drain.UnprovableOnce = false
	while := drain.WhileDraining
	drain.mu.Unlock()

	// Outside the lock and before the answer: what happens here happens while
	// the boundary is being proved, which is what it is for.
	if while != nil {
		while()
	}

	if transient {
		return nil, fmt.Errorf("%w: the apiserver did not answer in time",
			output.ErrInfrastructure)
	}
	if once {
		return nil, fmt.Errorf("%w: no complete final container status yet",
			output.ErrSealUnconfirmed)
	}
	if drain.Unprovable {
		return nil, fmt.Errorf("%w: no complete final container status before the deadline",
			output.ErrSealUnconfirmed)
	}
	if len(started.DrainSet) != 0 {
		return nil, fmt.Errorf("%w: the harness admitted no writer and the seal captured %d",
			output.ErrCorrupt, len(started.DrainSet))
	}

	return nil, nil
}

// Calls reports how many times the deployment's writer termination was driven.
//
// It is the one place a COUNT is the assertion rather than an outcome: "the
// drain was never asked" has no trace on the node or in a row, and the whole
// point of deciding admissibility on the database clock first is that a seal
// past its deadline does not terminate anybody's containers to find out.
func (drain *stubDrain) Calls() int {
	drain.mu.Lock()
	defer drain.mu.Unlock()

	return drain.calls
}

// ambiguousTransactor commits for real and then reports failure.
//
// This is the only honest shape for an ambiguous PostgreSQL commit: the rows
// are there and the caller does not know it. A transactor that rolled back
// instead would be testing a coordinator against a commit that did not happen,
// which the coordinator already handles by simply doing it again.
type ambiguousTransactor struct {
	inner hangaroutput.Transactor

	// LoseNext makes the next successful Commit report an error after it has
	// committed. Consumed, so the retry can succeed.
	LoseNext bool

	// Skip lets that many successful commits through first. One transition can
	// commit more than once -- taking the lease, issuing a challenge, admitting
	// a receipt -- and which of them loses its answer is a different crash
	// half, so a transactor that could only lose the first could only ever
	// exercise one of them.
	Skip int

	mu sync.Mutex
}

func (transactor *ambiguousTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.inner.Begin()
	if err != nil {
		return nil, err
	}

	return &ambiguousTransaction{Transaction: tx, owner: transactor}, nil
}

type ambiguousTransaction struct {
	hangaroutput.Transaction

	owner *ambiguousTransactor
}

func (tx *ambiguousTransaction) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}

	tx.owner.mu.Lock()
	defer tx.owner.mu.Unlock()
	if tx.owner.Skip > 0 {
		tx.owner.Skip--

		return nil
	}
	if tx.owner.LoseNext {
		tx.owner.LoseNext = false

		return lostAnswer
	}

	return nil
}
