package hangaroutput_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The activation walk, against real PostgreSQL as the activation role.
//
// The row's guards are the subject, as they are in activation_test.go: the walk
// only decides order, skips and refusals, and every write it makes is one of
// the CAS statements the schema checks. The Kubernetes side -- the cohort's
// pods, their handshakes and the DaemonSet's rollout counts -- is stood up in
// the test, because there is no cluster here and those three are interfaces
// for exactly that reason.

const walkDaemonSet = "concourse-hangar-output-daemon"

// walkCohort is a two-member cohort that answers for one epoch.
type walkCohort struct {
	epoch executioncontrol.ActivationEpoch
}

func (walkCohort) Members(context.Context) ([]activation.Member, error) {
	return []activation.Member{
		{Node: "node-a", Address: "10.0.0.1:7781"},
		{Node: "node-b", Address: "10.0.0.2:7781"},
	}, nil
}

func (cohort walkCohort) Base(context.Context, activation.Member) (executioncontrol.Handshake, error) {
	return executioncontrol.Handshake{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		LedgerVersion:   executioncontrol.LedgerVersion,
		ControlKeyID:    "control-key-1",
		ActivationEpoch: cohort.epoch,
	}, nil
}

func (cohort walkCohort) Extension(ctx context.Context, member activation.Member) (output.ExtensionHandshake, error) {
	base, _ := cohort.Base(ctx, member)

	return output.ExtensionHandshake{
		Base:                    base,
		CaptureExtensionVersion: output.ProtocolVersion,
		SourceLedgerVersion:     output.SourceLedgerVersion,
		ReceiptPublicKeyID:      "receipt-key-1",
		MaterializationKeyID:    "materialize-key-1",
		BucketFingerprint:       "gs://walk-output",
		DerivedNamespace:        "deployments/blue/walk",
	}, nil
}

// fixedReadiness answers the same DaemonSet state every time it is read.
type fixedReadiness struct{ state activation.DaemonSetReadiness }

func (readiness fixedReadiness) Readiness(context.Context) (activation.DaemonSetReadiness, error) {
	return readiness.state, nil
}

func settledDaemonSet() activation.DaemonSetReadiness {
	return activation.DaemonSetReadiness{
		Name: walkDaemonSet, Desired: 2, Updated: 2, Ready: 2,
		Members: []activation.MemberReadiness{{Pod: "daemon-a", Ready: true}, {Pod: "daemon-b", Ready: true}},
	}
}

func walkerFor(epochs activation.Epochs, epoch executioncontrol.ActivationEpoch,
	out *strings.Builder) activation.Walker {
	cohort := walkCohort{epoch: epoch}

	walker := activation.Walker{
		Epochs:             epochs,
		Cohort:             cohort,
		Handshakes:         cohort,
		Readiness:          fixedReadiness{state: settledDaemonSet()},
		ReceiptKeyLifetime: 90 * 24 * time.Hour,
		ReadinessTimeout:   5 * time.Second,
		Poll:               10 * time.Millisecond,
	}
	if out != nil {
		walker.Out = out
	}

	return walker
}

type rowSnapshot struct {
	base, output string
	revision     int64
	updated      time.Time
}

func snapshotRow(t *testing.T, conn *sql.DB, epoch executioncontrol.ActivationEpoch) (rowSnapshot, bool) {
	t.Helper()

	var row rowSnapshot
	err := conn.QueryRow(`SELECT base_state, output_state, revision, updated_at
		  FROM hangar_output_activation_epochs WHERE epoch_id = $1`, int64(epoch)).
		Scan(&row.base, &row.output, &row.revision, &row.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return rowSnapshot{}, false
	}
	if err != nil {
		t.Fatalf("reading epoch %d: %v", epoch, err)
	}

	return row, true
}

func mustSnapshotRow(t *testing.T, conn *sql.DB, epoch executioncontrol.ActivationEpoch) rowSnapshot {
	t.Helper()

	row, found := snapshotRow(t, conn, epoch)
	if !found {
		t.Fatalf("epoch %d has no row", epoch)
	}

	return row
}

func mustWalk(t *testing.T, walker activation.Walker, epoch executioncontrol.ActivationEpoch,
	target activation.Target, finalize bool) {
	t.Helper()

	if err := walker.Walk(context.Background(), epoch, target, finalize); err != nil {
		t.Fatalf("walking epoch %d to %s (finalize=%v): %v", epoch, target, finalize, err)
	}
}

// A1. One walk from no row reaches output, and a repeated walk is a no-op.
//
// The repeat is what lets the chart recreate the walk on every sync: a hook
// that wrote the row each time would advance the revision forever, and one
// that failed at its target would fail every sync after the first.
func TestAWalkFromNoRowReachesOutputAndARepeatWritesNothing(t *testing.T) {
	epochs, conn := activationFixture(t)
	const epoch = executioncontrol.ActivationEpoch(101)

	var out strings.Builder
	walker := walkerFor(epochs, epoch, &out)
	mustWalk(t, walker, epoch, activation.TargetOutput, false)

	first := mustSnapshotRow(t, conn, epoch)
	if first.base != "enabled" || first.output != "enabled" {
		t.Fatalf("the walk ended at base=%s output=%s; it should reach both enabled",
			first.base, first.output)
	}
	for _, transition := range []string{"began activation epoch 101", "attested epoch 101's base",
		"enabled epoch 101's base", "attested epoch 101's output", "enabled epoch 101's output",
		"epoch 101: base=enabled output=enabled"} {
		if !strings.Contains(out.String(), transition) {
			t.Errorf("the walk did not print %q:\n%s", transition, out.String())
		}
	}

	out.Reset()
	mustWalk(t, walker, epoch, activation.TargetOutput, false)
	second := mustSnapshotRow(t, conn, epoch)
	if second.revision != first.revision || !second.updated.Equal(first.updated) {
		t.Errorf("the repeated walk wrote the row: revision %d -> %d, updated_at %s -> %s",
			first.revision, second.revision, first.updated, second.updated)
	}
	if strings.Contains(out.String(), "attested") {
		t.Errorf("the repeated walk attested again:\n%s", out.String())
	}
}

// A5. An attestation waits for the DaemonSet to settle, and a cohort that does
// not settle is never attested.
func TestAWalkAttestsNothingUntilTheDaemonSetSettles(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(102)

	for name, probe := range map[string]struct {
		spoil func(*activation.DaemonSetReadiness)
		says  string
	}{
		"updated below desired": {
			spoil: func(state *activation.DaemonSetReadiness) { state.Updated = 1 },
			says:  "desired=2 updated=1 ready=2",
		},
		"ready below desired": {
			spoil: func(state *activation.DaemonSetReadiness) { state.Ready = 1 },
			says:  "desired=2 updated=2 ready=1",
		},
		"desired above the others": {
			spoil: func(state *activation.DaemonSetReadiness) { state.Desired = 3 },
			says:  "desired=3 updated=2 ready=2",
		},
		"a member pod not Ready": {
			spoil: func(state *activation.DaemonSetReadiness) { state.Members[1].Ready = false },
			says:  "pods not Ready: daemon-b",
		},
	} {
		t.Run(name, func(t *testing.T) {
			epochs, conn := activationFixture(t)

			state := settledDaemonSet()
			probe.spoil(&state)
			walker := walkerFor(epochs, epoch, nil)
			walker.Readiness = fixedReadiness{state: state}
			walker.ReadinessTimeout = 150 * time.Millisecond

			started := time.Now()
			err := walker.Walk(context.Background(), epoch, activation.TargetOutput, false)
			if err == nil {
				t.Fatal("the walk attested over a DaemonSet that had not settled")
			}
			if time.Since(started) < walker.ReadinessTimeout {
				t.Errorf("the walk failed after %s, before its %s readiness timeout",
					time.Since(started), walker.ReadinessTimeout)
			}
			for _, named := range []string{walkDaemonSet, probe.says, "150ms"} {
				if !strings.Contains(err.Error(), named) {
					t.Errorf("the failure does not name %q: %v", named, err)
				}
			}

			row := mustSnapshotRow(t, conn, epoch)
			if row.base != "initial" || row.output != "initial" {
				t.Errorf("the row is base=%s output=%s; nothing may be attested over an "+
					"unsettled cohort", row.base, row.output)
			}
		})
	}
}

// A2. A draining facet at the target is never advanced.
func TestAWalkNeverAdvancesADrainingFacet(t *testing.T) {
	epochs, conn := activationFixture(t)
	const epoch = executioncontrol.ActivationEpoch(103)

	walker := walkerFor(epochs, epoch, nil)
	mustWalk(t, walker, epoch, activation.TargetOutput, false)
	mustWalk(t, walker, epoch, activation.TargetBase, false)
	before := mustSnapshotRow(t, conn, epoch)
	if before.output != "draining" {
		t.Fatalf("the setup left output %q, not draining", before.output)
	}

	err := walker.Walk(context.Background(), epoch, activation.TargetOutput, false)
	if err == nil {
		t.Fatal("a walk to output over a draining output facet succeeded")
	}
	if !strings.Contains(err.Error(), "output facet is draining") {
		t.Errorf("the refusal does not name the draining output facet: %v", err)
	}

	after := mustSnapshotRow(t, conn, epoch)
	if after.output != "draining" || after.revision != before.revision {
		t.Errorf("the refused walk moved the row: output %s -> %s, revision %d -> %d",
			before.output, after.output, before.revision, after.revision)
	}
}

// A3. A lower target drains, finalizes only when asked, and stops where the
// schema or the residue stops it -- exiting 0, because the target is reached.
func TestALowerTargetDrainsAndFinalizesOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()

	t.Run("base from both enabled leaves output draining", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		const epoch = executioncontrol.ActivationEpoch(104)
		walker := walkerFor(epochs, epoch, nil)
		mustWalk(t, walker, epoch, activation.TargetOutput, false)

		mustWalk(t, walker, epoch, activation.TargetBase, false)
		row := mustSnapshotRow(t, conn, epoch)
		if row.base != "enabled" || row.output != "draining" {
			t.Fatalf("a walk to base left base=%s output=%s", row.base, row.output)
		}

		// --finalize with nothing left under the facet takes it to disabled.
		mustWalk(t, walker, epoch, activation.TargetBase, true)
		row = mustSnapshotRow(t, conn, epoch)
		if row.base != "enabled" || row.output != "disabled" {
			t.Errorf("a finalized walk to base left base=%s output=%s", row.base, row.output)
		}
	})

	t.Run("finalize with residue exits 0 and leaves output draining", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		const epoch = executioncontrol.ActivationEpoch(105)
		var out strings.Builder
		walker := walkerFor(epochs, epoch, &out)
		mustWalk(t, walker, epoch, activation.TargetOutput, false)
		mustExec(t, conn, `INSERT INTO hangar_policy_violations (activation_epoch, violation, subject, detail)
			VALUES ($1, 'out_of_band_absence', 'missing-object', 'observed loss')`, int64(epoch))

		out.Reset()
		// Unlike --mode=drain --finalize, which exits non-zero on the same
		// residue: the walk's target is reached once emission stops, and a
		// recreated hook that failed until the residue cleared would fail
		// every sync.
		mustWalk(t, walker, epoch, activation.TargetBase, true)
		row := mustSnapshotRow(t, conn, epoch)
		if row.output != "draining" {
			t.Errorf("a finalize refused for residue left output %q", row.output)
		}
		if !strings.Contains(out.String(), "open policy violations: 1") {
			t.Errorf("the walk did not print the residue:\n%s", out.String())
		}
	})

	t.Run("off reports the base facet the schema keeps enabled", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		const epoch = executioncontrol.ActivationEpoch(106)
		var out strings.Builder
		walker := walkerFor(epochs, epoch, &out)
		mustWalk(t, walker, epoch, activation.TargetOutput, false)

		out.Reset()
		mustWalk(t, walker, epoch, activation.TargetOff, false)
		row := mustSnapshotRow(t, conn, epoch)
		if row.base != "enabled" || row.output != "draining" {
			t.Errorf("off left base=%s output=%s", row.base, row.output)
		}
		for _, named := range []string{"base facet stays enabled", "hangar_output_epoch_needs_base"} {
			if !strings.Contains(out.String(), named) {
				t.Errorf("off did not report %q:\n%s", named, out.String())
			}
		}
	})

	t.Run("off with no row writes nothing", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		const epoch = executioncontrol.ActivationEpoch(107)
		mustWalk(t, walkerFor(epochs, epoch, nil), epoch, activation.TargetOff, true)

		var rows int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM hangar_output_activation_epochs`).
			Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("off with no row wrote %d row(s)", rows)
		}
	})
}

// A4. Raising the epoch is the re-enable, and only once the earlier epoch
// holds no enabled facet.
func TestRaisingTheEpochReEnablesOnlyOnceTheEarlierOneIsFinalized(t *testing.T) {
	ctx := context.Background()
	const first = executioncontrol.ActivationEpoch(1)
	const second = executioncontrol.ActivationEpoch(2)

	refusal := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("the walk raised the epoch while epoch 1's base facet is enabled")
		}
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("the refusal is not the typed conflict: %v", err)
		}
		for _, named := range []string{"epoch 1's base facet", "--epoch=1 --target=off --finalize"} {
			if !strings.Contains(err.Error(), named) {
				t.Errorf("the refusal does not name %q: %v", named, err)
			}
		}
	}

	t.Run("refused while epoch 1's base is enabled", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		mustWalk(t, walkerFor(epochs, first, nil), first, activation.TargetBase, false)
		before := mustSnapshotRow(t, conn, first)

		refusal(t, walkerFor(epochs, second, nil).Walk(ctx, second, activation.TargetOutput, false))

		if _, found := snapshotRow(t, conn, second); found {
			t.Error("the refused walk began epoch 2")
		}
		after := mustSnapshotRow(t, conn, first)
		if after.revision != before.revision || !after.updated.Equal(before.updated) {
			t.Errorf("the refused walk wrote epoch 1: revision %d -> %d", before.revision, after.revision)
		}
	})

	// A row that --mode=begin made gets the same named refusal, not the
	// partial unique index's infrastructure error.
	t.Run("refused the same way when epoch 2 was already begun", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		mustWalk(t, walkerFor(epochs, first, nil), first, activation.TargetBase, false)
		mustBegin(t, epochs, second)

		refusal(t, walkerFor(epochs, second, nil).Walk(ctx, second, activation.TargetOutput, false))

		row := mustSnapshotRow(t, conn, second)
		if row.base != "initial" || row.output != "initial" {
			t.Errorf("the refused walk moved epoch 2 to base=%s output=%s", row.base, row.output)
		}
	})

	t.Run("reaches the target once epoch 1 is finalized", func(t *testing.T) {
		epochs, conn := activationFixture(t)
		firstWalker := walkerFor(epochs, first, nil)
		mustWalk(t, firstWalker, first, activation.TargetOutput, false)
		// One walk takes both facets to disabled: output first, then base,
		// which the schema lets leave only once output is disabled.
		mustWalk(t, firstWalker, first, activation.TargetOff, true)
		row := mustSnapshotRow(t, conn, first)
		if row.base != "disabled" || row.output != "disabled" {
			t.Fatalf("off --finalize left epoch 1 at base=%s output=%s", row.base, row.output)
		}

		// The cohort double answers for epoch 2, as a DaemonSet rolled to
		// the raised epoch does; AttestBase refuses a member that speaks for 1.
		mustWalk(t, walkerFor(epochs, second, nil), second, activation.TargetOutput, false)
		row = mustSnapshotRow(t, conn, second)
		if row.base != "enabled" || row.output != "enabled" {
			t.Errorf("the raised epoch ended at base=%s output=%s", row.base, row.output)
		}
	})
}
