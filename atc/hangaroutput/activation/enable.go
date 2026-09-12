package activation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// HangarOutputMigration is the schema version this plane is.
//
// It is named here because "the migration ran" is a precondition of putting the
// plane into service and there is nowhere else an operator's command could read
// it from. If the migration is ever renumbered, this is the one other place
// that has to move, and the enable step refuses loudly rather than quietly
// admitting a plane whose schema it cannot find.
const HangarOutputMigration int64 = 1788936403

// PolicyDetectionBound is the freshness bound the schema's own deferred
// admission gate applies to a lifetime-policy attestation.
//
// Stated here as an interval literal because the check below has to be the SAME
// bound: a precondition that admitted a staler attestation than the gate does
// would let an operator enable a facet that refuses its first admission, which
// is the failure mode this whole step exists to move earlier.
const PolicyDetectionBound = "15 minutes"

// Precondition is one thing that has to be true before a facet goes into
// service, together with what was actually found.
//
// It carries WHY, in the same spirit as Residue: a refusal an operator cannot
// act on is a refusal they will work around. "policy attestation: not met" is a
// shrug; "the most recent attestation for this epoch is 41 minutes old, past the
// 15-minute detection bound -- the attestor is not running" is an instruction.
type Precondition struct {
	Name   string
	Met    bool
	Detail string
	Why    string
}

func (precondition Precondition) String() string {
	return fmt.Sprintf("%s: %s (%s)", precondition.Name, precondition.Detail, precondition.Why)
}

// ErrEnableRefused is what an epoch whose preconditions are not met answers.
var ErrEnableRefused = fmt.Errorf("%w: the facet's activation preconditions are not met",
	output.ErrIncomplete)

// EnablePreconditions reports every precondition for putting one facet into
// service, met or not.
//
// IT EXISTS BECAUSE THERE WAS NOTHING. `--mode=enable --facet=output` was held
// by a written instruction and by nothing in the code: the command never read a
// policy snapshot, never checked which migration the database is at, never
// asked whether a policy attestor is deployed and holding its lease, and never
// looked at the identity facts the epoch row itself carries. An operator could
// attest and enable with no conformance run and no attestor anywhere in the
// cluster.
//
// The plane still failed CLOSED -- the schema's deferred
// hangar_policy_admits_new_protection refuses the first admission, and the
// receipt trigger refuses a receipt whose key the epoch does not attest -- so
// this is not a hole through which anything unsafe passed. What it was is a
// refusal arriving at the wrong moment, in the wrong vocabulary, to the wrong
// person: a JB002 on some build's commit, hours later, rather than a sentence
// at the operator's terminal naming the thing that is missing.
//
// It is a READ, and like DrainResidue it returns EVERYTHING rather than
// stopping at the first unmet one. An operator enabling a plane wants the whole
// list in one pass.
func (epochs Epochs) EnablePreconditions(ctx context.Context,
	epoch executioncontrol.ActivationEpoch, facet Facet) ([]Precondition, error) {
	if _, err := facet.Column(); err != nil {
		return nil, err
	}

	var preconditions []Precondition
	add := func(name string, met bool, detail, why string) {
		preconditions = append(preconditions,
			Precondition{Name: name, Met: met, Detail: detail, Why: why})
	}

	// 1. THE SCHEMA. Everything below reads tables this migration creates, so
	// this one is asked first and the rest are only meaningful after it.
	var migrated bool
	if err := epochs.DB.QueryRowContext(ctx, `
		SELECT coalesce((
			SELECT direction = 'up' AND status <> 'failed'
			  FROM migrations_history WHERE version = $1
			 ORDER BY tstamp DESC LIMIT 1), false)`,
		HangarOutputMigration).Scan(&migrated); err != nil {
		return nil, fmt.Errorf("%w: reading the migration history: %v",
			output.ErrInfrastructure, err)
	}
	add("schema migration", migrated,
		fmt.Sprintf("migration %d is %s", HangarOutputMigration,
			metOrNot(migrated, "applied", "not the database's current state")),
		"the plane's tables, triggers and typed SQLSTATEs are this migration. A facet enabled "+
			"against a database that has been rolled back would admit work the schema cannot "+
			"refuse")

	// 2. THE FACET'S OWN STATE, read through the same row the CAS writes.
	state, err := epochs.Read(ctx, epoch)
	if err != nil {
		return nil, err
	}
	current := state.Base
	if facet == FacetOutput {
		current = state.Output
	}
	add("facet attested", current == "attested",
		fmt.Sprintf("the %s facet is %q", facet, current),
		"Enable moves an ATTESTED facet into service; the evidence that justified it is "+
			"written in the row by the same statement that moved the state, and a facet in any "+
			"other state has none")

	if facet == FacetBase {
		var protocol, ledger *string
		if err := epochs.DB.QueryRowContext(ctx, `
			SELECT protocol_version, ledger_version
			  FROM hangar_output_activation_epochs WHERE epoch_id = $1`,
			int64(epoch)).Scan(&protocol, &ledger); err != nil {
			return nil, fmt.Errorf("%w: reading epoch %d: %v", output.ErrInfrastructure, epoch, err)
		}
		add("cohort versions attested", present(protocol) && present(ledger),
			describeVersions(protocol, ledger),
			"the base attestation records the one protocol and ledger version the whole cohort "+
				"speaks; without them the row claims a homogeneous cohort and names nothing")

		return preconditions, nil
	}

	// A READOUT rather than a gate, and the difference is written down because
	// it cannot be reddened. hangar_output_epoch_needs_base pins base_state in
	// ('attested','enabled') for as long as output_state is anything but
	// `initial` or `disabled`, and output_state never moves backwards -- so an
	// output facet that is `attested` at all is already on a row whose base is
	// ready, and this line can only ever report `met`. It stays because the
	// pre-flight list is what an operator reads to understand the conjunction
	// they are enabling, and "base facet ready: the base facet is enabled" is
	// part of that sentence. TestTheSchemaIsWhatMakesTheBaseReadinessLineTrue
	// pins the constraint that makes it so.
	add("base facet ready", state.Base == "attested" || state.Base == "enabled",
		fmt.Sprintf("the base facet is %q", state.Base),
		"output admission is the conjunction of base readiness, the output facet and the "+
			"current policy. The schema's hangar_output_epoch_needs_base says the same thing "+
			"and would refuse the write")

	// 3. THE IDENTITY FACTS THE EPOCH ATTESTS. These are what every later
	// refusal is measured against -- the receipt trigger compares a receipt's
	// key id to this row -- so an epoch missing one of them is an epoch whose
	// first receipt is refused.
	var (
		receiptKey, materializeKey, bucket, namespace *string
		keyWindowCovers                               *bool
	)
	if err := epochs.DB.QueryRowContext(ctx, `
		SELECT receipt_public_key_id, materialization_key_id, bucket_fingerprint,
		       derived_namespace,
		       CASE WHEN receipt_key_valid_from IS NULL OR receipt_key_valid_until IS NULL
		            THEN NULL
		            ELSE now() >= receipt_key_valid_from AND now() < receipt_key_valid_until
		       END
		  FROM hangar_output_activation_epochs WHERE epoch_id = $1`,
		int64(epoch)).Scan(&receiptKey, &materializeKey, &bucket, &namespace,
		&keyWindowCovers); err != nil {
		return nil, fmt.Errorf("%w: reading epoch %d: %v", output.ErrInfrastructure, epoch, err)
	}

	identity := present(receiptKey) && present(materializeKey) && present(bucket) &&
		present(namespace)
	add("cloud identity attested", identity,
		fmt.Sprintf("receipt key %s, materialization key %s, bucket %s, namespace %s",
			quoted(receiptKey), quoted(materializeKey), quoted(bucket), quoted(namespace)),
		"these four facts are what every later refusal is measured against: the receipt "+
			"trigger compares a receipt's key id to this row, and the marker, the grant and the "+
			"inventory cursor are all scoped by the bucket and namespace")

	add("receipt key is currently valid", keyWindowCovers != nil && *keyWindowCovers,
		describeKeyWindow(keyWindowCovers),
		"a facet enabled outside its receipt key's validity window can sign nothing, so every "+
			"capture under it reaches the publish point and then fails to register")

	// 4. THE POLICY EVIDENCE, against the same bound the schema's deferred gate
	// applies. "We have not checked" and "the check failed" are the same amount
	// of evidence, and both of them arrive at the operator here rather than at
	// a build's commit.
	var (
		snapshotState  *string
		snapshotAge    *string
		snapshotBucket *string
		fresh          *bool
	)
	if err := epochs.DB.QueryRowContext(ctx, `
		SELECT snapshot.state, (now() - snapshot.observed_at)::text,
		       snapshot.bucket_fingerprint,
		       now() - snapshot.observed_at <= interval '`+PolicyDetectionBound+`'
		  FROM hangar_policy_snapshots snapshot
		 WHERE snapshot.activation_epoch = $1
		 ORDER BY snapshot.observed_at DESC, snapshot.id DESC LIMIT 1`,
		int64(epoch)).Scan(&snapshotState, &snapshotAge, &snapshotBucket, &fresh); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: reading the lifetime-policy attestation for epoch %d: %v",
			output.ErrInfrastructure, epoch, err)
	}

	safe := snapshotState != nil && *snapshotState == "safe"
	add("lifetime policy attested safe", safe,
		describeSnapshot(snapshotState),
		"the bucket's lifetime policy is what stands between this plane's objects and a "+
			"provider rule that deletes them out from under a claim. The schema refuses every "+
			"admission without a safe attestation, deferred, at the consumer's commit")

	add("lifetime policy attestation is fresh", fresh != nil && *fresh,
		describeFreshness(fresh, snapshotAge),
		"the same "+PolicyDetectionBound+" detection bound the schema's own admission gate "+
			"applies. A stale check is not a safe one, and an epoch enabled on one stops "+
			"admitting the moment the bound passes")

	add("the attestation is of the attested bucket",
		snapshotBucket != nil && bucket != nil && *snapshotBucket == *bucket,
		fmt.Sprintf("the attestation reads %s and the epoch attests %s",
			quoted(snapshotBucket), quoted(bucket)),
		"a policy reading of another bucket is no evidence about this one, and the two are "+
			"stored in different tables with nothing joining them")

	// 5. AND SOMEBODY TO KEEP IT FRESH. A snapshot is a moment; the attestor is
	// what makes the next one exist.
	var attestorHeld bool
	if err := epochs.DB.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM hangar_operation_leases
		                WHERE kind = $2 AND activation_epoch = $1 AND expires_at > now())`,
		int64(epoch), string(output.OperationPolicyAttestation)).Scan(&attestorHeld); err != nil {
		return nil, fmt.Errorf("%w: reading the policy attestation lease for epoch %d: %v",
			output.ErrInfrastructure, epoch, err)
	}
	add("a policy attestor is running", attestorHeld,
		metOrNot(attestorHeld, "a policy-attestation lease is held and unexpired",
			"no unexpired policy-attestation lease exists for this epoch"),
		"the attestation above is one moment. Without a controller renewing it the epoch stops "+
			"admitting anything "+PolicyDetectionBound+" after the last reading, and the "+
			"operator who enabled the facet will not be the one who finds out")

	return preconditions, nil
}

// RefuseEnable turns the unmet preconditions into the diagnostic refusal.
func RefuseEnable(epoch executioncontrol.ActivationEpoch, facet Facet,
	preconditions []Precondition) error {
	var lines []string
	for _, precondition := range preconditions {
		if !precondition.Met {
			lines = append(lines, "  - "+precondition.String())
		}
	}
	if len(lines) == 0 {
		return nil
	}

	return fmt.Errorf("%w: epoch %d's %s facet cannot be enabled:\n%s\n\n"+
		"Every one of these is checked again by the schema at the first admission, so nothing "+
		"unsafe passes either way. What this refusal buys is the moment: the alternative is a "+
		"deferred JB002 on some build's commit, hours later, naming a constraint rather than "+
		"the thing an operator has to go and do.",
		ErrEnableRefused, epoch, facet, strings.Join(lines, "\n"))
}

// EnableOutcome is what the enable step found and did.
type EnableOutcome struct {
	Facet         Facet
	Preconditions []Precondition
	Enabled       bool
}

// EnableStep checks first and enables second.
//
// The ORDER is the opposite of DrainStep's, and for the same reason DrainStep's
// is what it is. A drain stops emission first so the set it counts cannot grow
// while it counts; an enable must not start emission at all until the reasons
// not to have been read, because the state it would admit is the state it is
// checking for.
//
// With `enable` false it reports and changes nothing, which is the pre-flight an
// operator runs before a maintenance window. With `enable` true and anything
// unmet it returns ErrEnableRefused and enables nothing.
func (epochs Epochs) EnableStep(ctx context.Context, epoch executioncontrol.ActivationEpoch,
	facet Facet, enable bool) (EnableOutcome, error) {
	outcome := EnableOutcome{Facet: facet}

	var err error
	outcome.Preconditions, err = epochs.EnablePreconditions(ctx, epoch, facet)
	if err != nil {
		return outcome, err
	}
	if refusal := RefuseEnable(epoch, facet, outcome.Preconditions); refusal != nil {
		if !enable {
			return outcome, nil
		}

		return outcome, refusal
	}
	if !enable {
		return outcome, nil
	}

	if err := epochs.Enable(ctx, epoch, facet); err != nil {
		return outcome, err
	}
	outcome.Enabled = true

	return outcome, nil
}

func present(value *string) bool { return value != nil && *value != "" }

func quoted(value *string) string {
	if value == nil {
		return "(none)"
	}

	return fmt.Sprintf("%q", *value)
}

func metOrNot(met bool, yes, no string) string {
	if met {
		return yes
	}

	return no
}

func describeVersions(protocol, ledger *string) string {
	return fmt.Sprintf("protocol %s, ledger %s", quoted(protocol), quoted(ledger))
}

func describeKeyWindow(covers *bool) string {
	if covers == nil {
		return "the epoch attests no receipt key validity window"
	}
	if *covers {
		return "the current instant is inside the attested receipt key's validity window"
	}

	return "the current instant is OUTSIDE the attested receipt key's validity window"
}

func describeSnapshot(state *string) string {
	if state == nil {
		return "no lifetime-policy attestation exists for this epoch"
	}

	return fmt.Sprintf("the most recent lifetime-policy attestation is %q", *state)
}

func describeFreshness(fresh *bool, age *string) string {
	if fresh == nil || age == nil {
		return "there is no attestation to be stale"
	}
	if *fresh {
		return fmt.Sprintf("the most recent attestation is %s old", *age)
	}

	return fmt.Sprintf("the most recent attestation is %s old, past the %s detection bound",
		*age, PolicyDetectionBound)
}
