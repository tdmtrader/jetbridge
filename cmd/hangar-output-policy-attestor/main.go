// Command hangar-output-policy-attestor reads the output bucket's lifetime
// policy and IAM, and touches no object at all.
//
// That absence is the design. This is the workload whose word the plane trusts
// about whether the bucket is safe to publish into, and a workload that could
// also read, create or delete an object would be a workload whose compromise
// costs the data rather than the assessment. Its cloud principal holds bucket
// metadata reads and nothing else, its store type has no object method
// anywhere, and it links hangar/output/policy and no other role package.
//
// What it produces is EVIDENCE, not a verdict that outlives it. Every
// attestation records what was observed and when; every admission gate compares
// that against its own bound on the database clock. It is explicitly a
// bounded-staleness trust check (Req 51): a functioning monitor detects a policy
// change within the refresh bound, and cannot prevent a deletion inside it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/policy"
)

type attestorConfig struct {
	DSN             string
	Endpoint        string
	Bucket          string
	ActivationEpoch int64
	Interval        time.Duration

	// The four configured cloud identities. They are configuration and never
	// discovery: activation verifies the KSA-to-cloud identities and the exact
	// IAM grants rather than assuming the chart created them (Req 54), and a
	// deployment that learned its own principals from the bucket would be
	// verifying the bucket against itself.
	PublisherIdentity string
	InventoryIdentity string
	ReclaimerIdentity string
	AttestorIdentity  string
}

func (config *attestorConfig) bind(flags *flag.FlagSet) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. The attestor writes snapshots and findings; it never migrates.")
	flags.StringVar(&config.Endpoint, "output-endpoint", "",
		"Object-store endpoint. Empty means real GCS with ambient credentials.")
	flags.StringVar(&config.Bucket, "output-bucket", "",
		"The dedicated output bucket whose whole lifetime policy is read.")
	flags.Int64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The activation epoch this attestation speaks for.")
	flags.DurationVar(&config.Interval, "interval", output.MaxPolicyEvidenceAge/3,
		"How often the policy is re-read. It must be well inside the 15-minute detection bound, because evidence older than that blocks admission exactly as an unsafe policy does.")
	flags.StringVar(&config.PublisherIdentity, "publisher-identity", "",
		"Cloud identity bound to the publisher/materializer daemon's service account.")
	flags.StringVar(&config.InventoryIdentity, "inventory-identity", "",
		"Cloud identity bound to the inventory controller's service account.")
	flags.StringVar(&config.ReclaimerIdentity, "reclaimer-identity", "",
		"Cloud identity bound to the reclaimer's service account.")
	flags.StringVar(&config.AttestorIdentity, "attestor-identity", "",
		"Cloud identity bound to this attestor's own service account.")
}

func (config attestorConfig) expectation() policy.Expectation {
	return policy.Expectation{
		BucketFingerprint: fingerprint(config.Bucket),
		ActivationEpoch:   executioncontrol.ActivationEpoch(config.ActivationEpoch),
		Principals: map[output.PrincipalRole]string{
			output.PrincipalPublisher:      config.PublisherIdentity,
			output.PrincipalInventory:      config.InventoryIdentity,
			output.PrincipalReclaimer:      config.ReclaimerIdentity,
			output.PrincipalPolicyAttestor: config.AttestorIdentity,
		},
	}
}

func fingerprint(bucket string) string {
	if bucket == "" || strings.HasPrefix(bucket, "gs://") {
		return bucket
	}

	return "gs://" + bucket
}

func main() {
	config := attestorConfig{}
	config.bind(flag.CommandLine)
	flag.Parse()

	if err := run(context.Background(), config); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-policy-attestor: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config attestorConfig) error {
	expectation := config.expectation()
	if err := expectation.Validate(); err != nil {
		return err
	}
	// The refresh has to be well inside the bound it is monitoring. An attestor
	// configured to run exactly at the bound is one whose evidence is stale for
	// an instant before every refresh, which blocks admission for that instant
	// on every cycle.
	if config.Interval <= 0 || config.Interval >= output.MaxPolicyEvidenceAge {
		return fmt.Errorf("%w: a refresh interval of %s against a %s detection bound; the "+
			"attestation would be stale before it was replaced", output.ErrIncomplete,
			config.Interval, output.MaxPolicyEvidenceAge)
	}

	client, err := hangargcs.NewStorageClient(ctx, config.Endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// The narrowing. A bucket handle, and no object handle anywhere below.
	source, err := hangargcs.NewBucketPolicySource(client, config.Bucket,
		func() time.Time { return time.Now().UTC() })
	if err != nil {
		return err
	}

	conn, err := controller.OpenDatabase(config.DSN, 2)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	repository := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
	transactor := controller.SQLTransactor{DB: conn, CommitError: db.HangarCommitError}

	runner := &controller.Runner{
		Kind:            output.OperationPolicyAttestation,
		ActivationEpoch: config.ActivationEpoch,
		OwnerID:         uuid.NewString(),
		Term:            output.MinLeaseTerm,
		Transactor:      transactor,
		Leases:          repository,
		Pass: &attestPass{
			expectation: expectation,
			source:      source,
			repository:  repository,
			transactor:  transactor,
		},
		Reporter: controller.ReporterFunc(func(ctx context.Context, kind output.OperationKind, processed int, class string) {
			lagerctx.FromContext(ctx).Info("hangar-output-pass", lager.Data{
				"kind": string(kind), "processed": processed, "class": class,
			})
		}),
	}

	logger := lager.NewLogger("hangar-output-policy-attestor")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))
	ctx = lagerctx.NewContext(ctx, logger)

	ticker := time.NewTicker(controller.Interval(config.Interval))
	defer ticker.Stop()

	for {
		if err := runner.Run(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// attestPass is one whole-bucket reading and everything derived from it.
//
// A read that FAILS is recorded, not skipped. "We have not checked" and "the
// check failed" are the same amount of evidence, and a monitor that went quiet
// on an outage would leave the last safe snapshot standing until it aged out --
// turning a detection into a delay.
type attestPass struct {
	expectation policy.Expectation
	source      *hangargcs.BucketPolicySource
	repository  *db.HangarOutputRepository
	transactor  controller.Transactor
}

func (pass *attestPass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	observation, readErr := pass.source.ReadLifetimePolicy(ctx)
	snapshot, findings, err := policy.DeriveSnapshot(pass.expectation, observation)
	if err != nil {
		return 0, err
	}
	if readErr != nil {
		snapshot = output.PolicySnapshot{
			ProtocolVersion:   output.ProtocolVersion,
			ActivationEpoch:   pass.expectation.ActivationEpoch,
			BucketFingerprint: pass.expectation.BucketFingerprint,
			Metageneration:    1,
			PolicyHash:        "sha256:unread",
			State:             output.PolicyUnknown,
			ObservedAt:        output.NewTimestamp(time.Now().UTC()),
		}
		findings = []output.PolicyFinding{{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   pass.expectation.BucketFingerprint,
			Detail:    readErr.Error(),
		}}
	}

	if bindings, err := pass.source.ReadPrincipalBindings(ctx,
		pass.expectation.Principals); err == nil {
		findings = append(findings,
			policy.DeriveBindingFindings(pass.expectation, bindings)...)
	} else {
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   pass.expectation.BucketFingerprint,
			Detail:    err.Error(),
		})
	}

	// A finding anywhere is at_risk, even when the lifecycle half read clean:
	// an excessive IAM grant is a way for this bucket to lose objects, and the
	// state is the plane's answer to "may new work be admitted".
	if policy.AtRisk(findings) {
		snapshot.State = output.PolicyAtRisk
	}

	tx, err := pass.transactor.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.repository.RecordPolicyAttestation(ctx, tx, snapshot, findings); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return len(findings), nil
}
