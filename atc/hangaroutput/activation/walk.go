package activation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Target is where an activation walk takes the configured epoch: off, base or
// output.
//
// It names how far into service the epoch should be, not a step to run. The
// step Jobs this replaces each made one transition and were each a values
// commit; a target is one value, and the walk works out which transitions it
// still needs from the row.
type Target string

const (
	// TargetOff takes every facet out of service that the schema lets leave.
	TargetOff Target = "off"
	// TargetBase is exact execution control alone.
	TargetBase Target = "base"
	// TargetOutput is base plus the durable-capture extension.
	TargetOutput Target = "output"
)

// ParseTarget reads a target from a flag.
func ParseTarget(value string) (Target, error) {
	target := Target(strings.TrimSpace(value))
	switch target {
	case TargetOff, TargetBase, TargetOutput:
		return target, nil
	}

	return "", fmt.Errorf("%w: %q is not an activation target; there are three: off, base "+
		"and output", output.ErrUnknownMember, value)
}

// covers reports whether a facet is at or below the target, so the walk takes
// it into service; a facet above the target is one the walk takes out.
func (target Target) covers(facet Facet) bool {
	switch target {
	case TargetOutput:
		return true
	case TargetBase:
		return facet == FacetBase
	}

	return false
}

// MemberReadiness is one listed pod of the output DaemonSet and whether its
// Ready condition is true.
type MemberReadiness struct {
	Pod   string
	Ready bool
}

// DaemonSetReadiness is what the Kubernetes API says about the output
// DaemonSet's rollout: the generation its status describes, its three counts
// and each member pod's Ready flag.
type DaemonSetReadiness struct {
	Name    string
	Desired int
	Updated int
	Ready   int
	Members []MemberReadiness

	// Generation is the DaemonSet's metadata.generation and
	// ObservedGeneration its status.observedGeneration. Until they are equal
	// the counts describe the previous template.
	Generation         int64
	ObservedGeneration int64
}

// settled is the condition an attestation waits for.
//
// Ready equal to desired is not enough on its own: mid-rollout the old pods are
// ready too, and they still speak the previous epoch, so an attestation then
// would fail as a mixed cohort rather than wait. Updated equal to desired is
// what says the rollout is over. A desired count of zero is a DaemonSet whose
// status the controller has not computed yet, or one scheduled nowhere; either
// way there is no cohort to attest, and AttestBase would refuse it as empty.
// And the counts mean any of that only once the controller has observed the
// current template: right after an upgrade the status still reads settled for
// the old one, and Argo's health check, which waits for that, is not in front
// of a plain Helm post-upgrade hook.
func (readiness DaemonSetReadiness) settled() bool {
	if readiness.ObservedGeneration != readiness.Generation {
		return false
	}
	if readiness.Desired == 0 || readiness.Updated != readiness.Desired ||
		readiness.Ready != readiness.Desired {
		return false
	}
	for _, member := range readiness.Members {
		if !member.Ready {
			return false
		}
	}

	return true
}

func (readiness DaemonSetReadiness) describe() string {
	var notReady []string
	for _, member := range readiness.Members {
		if !member.Ready {
			notReady = append(notReady, member.Pod)
		}
	}
	description := fmt.Sprintf("desired=%d updated=%d ready=%d",
		readiness.Desired, readiness.Updated, readiness.Ready)
	if readiness.ObservedGeneration != readiness.Generation {
		description += fmt.Sprintf(", observed generation %d of %d",
			readiness.ObservedGeneration, readiness.Generation)
	}
	if len(notReady) != 0 {
		description += fmt.Sprintf(", pods not Ready: %s", strings.Join(notReady, ", "))
	}

	return description
}

// CohortReadiness reads the output DaemonSet's rollout state.
//
// From the Kubernetes API, for the same reason the cohort is enumerated from
// it: a daemon's own report of its readiness is the thing being checked.
type CohortReadiness interface {
	Readiness(ctx context.Context) (DaemonSetReadiness, error)
}

// Walker moves one epoch's row toward a target, composing the guarded steps
// the other modes run one at a time.
//
// It decides only order, skips and refusals. Every write is still one of the
// four CAS statements, every enable still goes through EnableStep's
// preconditions and every drain through DrainStep's residue, so the walk adds
// no path to a state that the step modes could not reach.
type Walker struct {
	Epochs     Epochs
	Cohort     CohortSource
	Handshakes Handshaker
	Readiness  CohortReadiness

	// ReceiptKeyLifetime is how long the output attestation's receipt key is
	// valid for, from the moment of the attestation.
	ReceiptKeyLifetime time.Duration
	// ReadinessTimeout bounds the wait for the DaemonSet before each attest.
	ReadinessTimeout time.Duration
	// Poll is how often the DaemonSet is read while waiting. Zero is 2s.
	Poll time.Duration

	// Out receives each transition and the final row. Nil discards them.
	Out io.Writer
}

// Walk moves the epoch's row toward the target.
//
// It reads the row first and makes only the transitions the row shows are
// still needed, so a walk repeated at its target writes nothing: that is what
// lets the chart recreate it on every sync.
//
// Facets above the target are taken out first, output before base, because
// emission stops before anything else and base is what settles a capture.
// Facets at or below the target are then taken into service, base before
// output. A draining or disabled facet is never advanced: a facet does not
// move backwards, and the way back into service is a new epoch.
//
// It returns nil once the target is reached, including a lower target whose
// facets the schema or their residue keep from going further; it prints what
// stays and why. It returns an error naming the blocker when it cannot reach
// the target.
func (walker Walker) Walk(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	target Target, finalize bool) error {
	if _, err := ParseTarget(string(target)); err != nil {
		return err
	}

	_, err := walker.Epochs.Read(ctx, epoch)
	switch {
	case errors.Is(err, output.ErrNotFound):
		if target == TargetOff {
			walker.printf("epoch %d has no row, so there is nothing to take out of service; "+
				"nothing written\n", epoch)

			return nil
		}
		if err := walker.refuseWhileAnotherEpochIsEnabled(ctx, epoch); err != nil {
			return err
		}
		if err := walker.Epochs.Begin(ctx, epoch); err != nil && !errors.Is(err, ErrAlreadyAtTarget) {
			return err
		}
		walker.printf("began activation epoch %d (base=initial output=initial)\n", epoch)
	case err != nil:
		return err
	}
	defer walker.printRow(ctx, epoch)

	if err := walker.againIfStale(func() error {
		return walker.takeOut(ctx, epoch, target, finalize)
	}); err != nil {
		return err
	}

	for _, facet := range []Facet{FacetBase, FacetOutput} {
		if !target.covers(facet) {
			continue
		}
		if err := walker.againIfStale(func() error {
			return walker.takeIn(ctx, epoch, facet)
		}); err != nil {
			return err
		}
	}

	return nil
}

// againIfStale runs a step and, when its compare-and-set found the row moved,
// runs it once more. Every step reads the row first, so the second run goes on
// from wherever the row now is.
//
// Two walks overlap in the ordinary course: Argo recreates the hook while an
// older pod may still be running. When the other walk made the transition
// first, this one's CAS is ErrStaleEpoch, and failing on it would fail the sync
// over a row that is already where the walk was taking it. Once, because a row
// still moving under a second read is not an overlap but a contest, and the
// error says so.
func (walker Walker) againIfStale(step func() error) error {
	err := step()
	if !errors.Is(err, ErrStaleEpoch) {
		return err
	}
	walker.printf("the row moved under this walk (%v); reading it again\n", err)

	return step()
}

// takeOut drains every facet above the target, output first.
func (walker Walker) takeOut(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	target Target, finalize bool) error {
	if !target.covers(FacetOutput) {
		if err := walker.drain(ctx, epoch, FacetOutput, finalize); err != nil {
			return err
		}
	}
	if target.covers(FacetBase) {
		return nil
	}

	state, err := walker.Epochs.Read(ctx, epoch)
	if err != nil {
		return err
	}
	if (state.Base == "enabled" || state.Base == "draining") &&
		state.Output != "initial" && state.Output != "disabled" {
		walker.printf("epoch %d's base facet stays %s: hangar_output_epoch_needs_base holds "+
			"base in service while the output facet is %q, and output reaches `disabled` only "+
			"through a drain finalized with nothing left under it\n",
			epoch, state.Base, state.Output)

		return nil
	}

	return walker.drain(ctx, epoch, FacetBase, finalize)
}

// drain runs one facet's DrainStep and reports it.
//
// A finalize refused for residue is reported and is NOT the walk's failure:
// the target is reached once emission has stopped, and a recreated hook that
// failed until every tombstone was purged would fail every sync, possibly
// forever. `--mode=drain --finalize` keeps its non-zero exit for the operator
// who asked for exactly that step.
func (walker Walker) drain(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet, finalize bool) error {
	outcome, err := walker.Epochs.DrainStep(ctx, epoch, facet, finalize)
	if outcome.Drained {
		walker.printf("epoch %d's %s facet is draining: no new admission, and every release, "+
			"settlement and already-admitted delete continues\n", epoch, facet)
	}
	if err != nil && !errors.Is(err, ErrDrainRefused) {
		return err
	}
	switch {
	case outcome.Skipped:
		walker.printf("epoch %d's %s facet is %q; nothing to drain\n", epoch, facet, outcome.State)
	case len(outcome.Residue) != 0:
		walker.printf("epoch %d's %s facet still holds state and stays draining:\n", epoch, facet)
		for _, one := range outcome.Residue {
			walker.printf("  - %s\n", one)
		}
	case outcome.Disabled:
		walker.printf("epoch %d's %s facet is disabled\n", epoch, facet)
	default:
		walker.printf("epoch %d's %s facet holds nothing and stays draining; walk again with "+
			"--finalize to disable it\n", epoch, facet)
	}

	return nil
}

// takeIn attests and enables one facet at or below the target, skipping
// whatever the row shows is done.
func (walker Walker) takeIn(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	state, err := walker.Epochs.Read(ctx, epoch)
	if err != nil {
		return err
	}
	current := state.Base
	if facet == FacetOutput {
		current = state.Output
	}

	switch current {
	case "enabled":
		walker.printf("epoch %d's %s facet is already enabled; no transition made\n", epoch, facet)

		return nil
	case "draining", "disabled":
		return fmt.Errorf("%w: epoch %d's %s facet is %s, and the walk never advances a "+
			"draining or disabled facet: a facet does not move backwards, so the way back into "+
			"service is a new epoch. Walk this one to --target=off --finalize, then raise the "+
			"activation epoch", output.ErrConflict, epoch, facet, current)
	}

	// Before the attest as well as the enable: an attestation the walk would
	// then refuse to enable is a write made for nothing.
	if err := walker.refuseWhileAnotherEpochIsEnabled(ctx, epoch); err != nil {
		return err
	}
	if current == "initial" || current == "attesting" {
		if err := walker.attest(ctx, epoch, facet); err != nil {
			return err
		}
	}
	if _, err := walker.Epochs.EnableStep(ctx, epoch, facet, true); err != nil {
		if errors.Is(err, ErrAlreadyAtTarget) {
			return nil
		}

		return err
	}
	walker.printf("enabled epoch %d's %s facet\n", epoch, facet)

	return nil
}

// attest waits for the cohort to settle, gathers its evidence and writes it.
func (walker Walker) attest(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	if err := walker.waitForTheCohort(ctx, epoch, facet); err != nil {
		return err
	}

	var evidence Evidence
	var err error
	if facet == FacetBase {
		evidence, err = AttestBase(ctx, walker.Cohort, walker.Handshakes, epoch)
	} else {
		if walker.ReceiptKeyLifetime <= 0 {
			return fmt.Errorf("%w: the walk has no positive receipt key lifetime for the "+
				"output attestation", output.ErrIncomplete)
		}
		evidence, err = AttestOutput(ctx, walker.Cohort, walker.Handshakes, epoch)
		evidence.ReceiptKeyValidFrom = time.Now().UTC()
		evidence.ReceiptKeyValidUntil = evidence.ReceiptKeyValidFrom.Add(walker.ReceiptKeyLifetime)
	}
	if err != nil {
		return err
	}
	if err := walker.Epochs.Attest(ctx, epoch, facet, evidence); err != nil &&
		!errors.Is(err, ErrAlreadyAtTarget) {
		return err
	}
	walker.printf("attested epoch %d's %s facet over the cohort digest %s\n",
		epoch, facet, evidence.CohortDigest)

	return nil
}

// waitForTheCohort holds an attestation until the output DaemonSet's updated,
// ready and desired counts agree and every listed member pod is Ready.
//
// A read that fails is waited through rather than returned: the API server
// may be restarting, and the timeout bounds the wait either way. Its last
// answer is in the timeout's message.
func (walker Walker) waitForTheCohort(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet) error {
	if walker.Readiness == nil {
		return fmt.Errorf("%w: the walk has no reader for the output DaemonSet's readiness",
			output.ErrIncomplete)
	}
	if walker.ReadinessTimeout <= 0 {
		return fmt.Errorf("%w: the walk's readiness timeout must be positive",
			output.ErrIncomplete)
	}
	poll := walker.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}

	deadline := time.Now().Add(walker.ReadinessTimeout)
	for {
		readiness, err := walker.Readiness.Readiness(ctx)
		if err == nil && readiness.settled() {
			return nil
		}
		if !time.Now().Before(deadline) {
			last := readiness.describe()
			if err != nil {
				last = err.Error()
			}

			return fmt.Errorf("%w: epoch %d's %s facet was not attested: the output DaemonSet "+
				"%s did not settle within the %s readiness timeout (%s). An attestation waits "+
				"until its updated, ready and desired counts are equal and every member pod is "+
				"Ready, because a cohort mid-rollout still speaks the previous epoch",
				output.ErrIncomplete, epoch, facet, readiness.Name, walker.ReadinessTimeout, last)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// refuseWhileAnotherEpochIsEnabled names the other epoch that still holds an
// enabled facet.
//
// At most one base and one output facet may be enabled across all epochs, so
// raising the epoch re-enables only after the earlier one is finalized, and so
// does lowering it. Without this the enable would reach the partial unique
// index and surface as an infrastructure error that names a constraint rather
// than the epoch and the command. It runs before a begin and before every
// attest and enable, whoever began the row: a row that `--mode=begin` made is
// refused the same way.
func (walker Walker) refuseWhileAnotherEpochIsEnabled(ctx context.Context,
	epoch executioncontrol.ActivationEpoch) error {
	other, found, err := walker.Epochs.EnabledOther(ctx, epoch)
	if err != nil || !found {
		return err
	}

	var facets []string
	if other.Base == "enabled" {
		facets = append(facets, "base")
	}
	if other.Output == "enabled" {
		facets = append(facets, "output")
	}

	return fmt.Errorf("%w: epoch %d's %s facet is still enabled, and only one epoch's facet "+
		"may be in service at a time. Finalize it first, with the activation command's "+
		"--epoch=%d --target=off --finalize, and walk epoch %d again once it holds no enabled "+
		"facet", output.ErrConflict, other.Epoch, strings.Join(facets, " and "), other.Epoch, epoch)
}

func (walker Walker) printRow(ctx context.Context, epoch executioncontrol.ActivationEpoch) {
	state, err := walker.Epochs.Read(ctx, epoch)
	if err != nil {
		return
	}
	walker.printf("epoch %d: base=%s output=%s revision=%d\n",
		state.Epoch, state.Base, state.Output, state.Revision)
}

func (walker Walker) printf(format string, arguments ...any) {
	if walker.Out == nil {
		return
	}
	fmt.Fprintf(walker.Out, format, arguments...)
}
