package activation

import (
	"context"
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
const HangarOutputMigration int64 = 1789793149

// Precondition is one thing that has to be true before a facet goes into
// service, together with what was actually found.
//
// Each result names the unmet condition and explains what the operator needs
// to repair before enabling the facet.
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

// EnablePreconditions reports schema, cohort, identity and runtime integrity
// preconditions. Bucket IAM and lifecycle configuration are operator-managed.
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
			"storage integrity. The schema's hangar_output_epoch_needs_base says the same thing "+
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
	add("storage identity attested", identity,
		fmt.Sprintf("receipt key %s, materialization key %s, bucket %s, namespace %s",
			quoted(receiptKey), quoted(materializeKey), quoted(bucket), quoted(namespace)),
		"these four facts are what every later refusal is measured against: the receipt "+
			"trigger compares a receipt's key id to this row, and the marker, the warrant and the "+
			"inventory cursor are all scoped by the bucket and namespace")

	add("receipt key is currently valid", keyWindowCovers != nil && *keyWindowCovers,
		describeKeyWindow(keyWindowCovers),
		"a facet enabled outside its receipt key's validity window can sign nothing, so every "+
			"capture under it reaches the publish point and then fails to register")

	var unresolved int
	if err := epochs.DB.QueryRowContext(ctx, `SELECT count(*) FROM hangar_policy_violations
        WHERE activation_epoch = $1 AND resolved_at IS NULL
          AND violation IN ('out_of_band_absence', 'runtime_principal_denied')`, int64(epoch)).Scan(&unresolved); err != nil {
		return nil, fmt.Errorf("%w: reading storage integrity findings: %v", output.ErrInfrastructure, err)
	}
	add("storage integrity", unresolved == 0, fmt.Sprintf("%d unresolved runtime findings", unresolved),
		"observed object loss or denied storage operations require repair and explicit reconciliation")

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
