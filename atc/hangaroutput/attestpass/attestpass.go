// Package attestpass is the policy attestor's bounded unit of work.
//
// It names no cloud SDK. The whole-bucket lifetime and IAM read arrives through
// the Source port declared here, which hangar/gcs.BucketPolicySource satisfies
// -- so the composition that decides what a reading MEANS can be driven without
// one, and the binary keeps the only reference to the provider adapter.
package attestpass

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/policy"
)

// Source is the authoritative whole-bucket read, declared where it is CONSUMED.
type Source interface {
	ReadLifetimePolicy(ctx context.Context) (output.BucketLifetimePolicy, error)
	ReadPrincipalBindings(ctx context.Context, identities map[output.PrincipalRole]string) (output.PrincipalBindings, error)
}

// Pass is one whole-bucket reading and everything derived from it.
//
// A read that FAILS is recorded, not skipped. "We have not checked" and "the
// check failed" are the same amount of evidence, and a monitor that went quiet
// on an outage would leave the last safe snapshot standing until it aged out --
// turning a detection into a delay.
type Pass struct {
	Expectation policy.Expectation
	Source      Source
	Repository  *db.HangarOutputRepository
	Transactor  controller.Transactor

	// Clock is what dates an unread snapshot. The read itself carries the
	// observation time the provider's adapter stamped.
	Clock output.Clock
}

var _ controller.Pass = (*Pass)(nil)

func (pass *Pass) now() time.Time {
	if pass.Clock == nil {
		return time.Now().UTC()
	}

	return pass.Clock.Now().UTC()
}

func (pass *Pass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	observation, readErr := pass.Source.ReadLifetimePolicy(ctx)
	snapshot, findings, err := policy.DeriveSnapshot(pass.Expectation, observation)
	if err != nil {
		return 0, err
	}
	if readErr != nil {
		snapshot = output.PolicySnapshot{
			ProtocolVersion:   output.ProtocolVersion,
			ActivationEpoch:   pass.Expectation.ActivationEpoch,
			BucketFingerprint: pass.Expectation.BucketFingerprint,
			Metageneration:    1,
			PolicyHash:        "sha256:unread",
			State:             output.PolicyUnknown,
			ObservedAt:        output.NewTimestamp(pass.now()),
		}
		findings = []output.PolicyFinding{{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   pass.Expectation.BucketFingerprint,
			Detail:    readErr.Error(),
		}}
	}

	if bindings, err := pass.Source.ReadPrincipalBindings(ctx,
		pass.Expectation.Principals); err == nil {
		findings = append(findings,
			policy.DeriveBindingFindings(pass.Expectation, bindings)...)
	} else {
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   pass.Expectation.BucketFingerprint,
			Detail:    err.Error(),
		})
	}

	// A finding anywhere is at_risk, even when the lifecycle half read clean:
	// an excessive IAM grant is a way for this bucket to lose objects, and the
	// state is the plane's answer to "may new work be admitted".
	if policy.AtRisk(findings) {
		snapshot.State = output.PolicyAtRisk
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.RecordPolicyAttestation(ctx, tx, snapshot, findings); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return len(findings), nil
}
