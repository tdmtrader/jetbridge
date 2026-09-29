package steps

// Run cancellation over the GCS API emulator (spec amendment B5).
//
// Cancellation may reach a capture's bytes only through Hangar's generic
// cancel/settle seam and exact-ref lifecycle operations, and never while it
// holds a Run lock: a Run lock held across a node or store call is held for
// as long as an unreachable node takes to time out. Both halves are observed
// rather than inferred. The node source records every call the worker makes,
// and before each one a second connection asks PostgreSQL for the Run's row
// lock with NOWAIT. The bucket is read back through the emulator.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunCancellationGCS is a Run the production cancellation worker drove to its
// aborted publication, what its node source saw, and what the bucket held
// before the worker ran.
type RunCancellationGCS struct {
	Candidate RunOutputCandidate
	Source    recordingSource
	Before    []string
	Err       error
}

func RunCancellationGCSDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputRuntime, RunOutputCandidate]("its runtime producer stops capturing before the irreversible publish", func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder) (RunOutputCandidate, error) {
			// Resolved and not past the point: publish is the next transition.
			return stopRunCapture(in, rec, func(_ hangaroutput.Decision, r output.HandoffRecord) bool {
				return r.State == output.CaptureStateResolved && !r.PastIrreversiblePublishPoint
			})
		}),
		brine.DefineMap[RunOutputRuntime, RunOutputCandidate]("its runtime producer stops capturing after the irreversible publish", func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder) (RunOutputCandidate, error) {
			return stopRunCapture(in, rec, func(d hangaroutput.Decision, r output.HandoffRecord) bool {
				return d.Transition == hangaroutput.TransitionPublish && r.PastIrreversiblePublishPoint && r.Receipt == nil
			})
		}),
		brine.DefineMap[RunOutputCandidate, RunCancellationGCS]("the production cancellation worker settles its capture", func(in RunOutputCandidate, _ brine.Params, rec *brine.Recorder) (RunCancellationGCS, error) {
			out := RunCancellationGCS{Candidate: in}
			var err error
			if out.Before, err = in.Runtime.Start.Daemon.outputObjectKeys(); err != nil {
				return out, err
			}
			if out.Source, err = newLockProbedSource(in.Runtime, rec); err != nil {
				return out, err
			}
			// One coordinator, as atccmd builds one per component: its owner is
			// the worker's lease owner and the capture's owner after takeover.
			// Its drain's node and Pod calls are probed like the source's.
			coordinator, err := runOutputCoordinator(in.Runtime, freshUUID())
			if err != nil {
				return out, err
			}
			coordinator.Drain = recordingDrain{DrainConfirmer: coordinator.Drain, source: out.Source}
			out.Err = exerciseRunCancellationFinalityWith(in.Runtime, "gcs capture", func() runs.CancellationWorker {
				return cancellationWorkerOver(in.Runtime, out.Source, coordinator)
			})
			return out, nil
		}),
		CheckThat[RunCancellationGCS]("its aborted Run publishes an empty manifest", func(in RunCancellationGCS) error { return in.Err }),
		// A capture already registered and released decides nothing on
		// cancellation and dials no node, so there the probe covers only
		// execution control.
		CheckThat[RunCancellationGCS]("cancellation made no node call under a Run lock", func(in RunCancellationGCS) error {
			_, err := in.unlockedCalls()
			return err
		}),
		CheckThat[RunCancellationGCS]("cancellation settled the capture through Hangar's seam with no Run lock held", func(in RunCancellationGCS) error {
			calls, err := in.unlockedCalls()
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(calls, func(call string) bool {
				return call == "release" || call == "inspect-hold" || call == "publish" || call == "attest"
			}) {
				return fmt.Errorf("cancellation settled the capture without a capture-seam call: %v", calls)
			}
			return nil
		}),
		CheckThat[RunCancellationGCS]("the cancelled capture left no object, candidate or result", func(in RunCancellationGCS) error {
			r, claims, err := in.settled()
			if err != nil {
				return err
			}
			if r.State != output.CaptureStateCancelled || r.PastIrreversiblePublishPoint || r.Receipt != nil || !r.ReleaseAcknowledged || len(claims) != 0 {
				return fmt.Errorf("capture is %s, past publish=%t, publication receipt=%t, released=%t, %d claims", r.State, r.PastIrreversiblePublishPoint, r.Receipt != nil, r.ReleaseAcknowledged, len(claims))
			}
			if calls := in.Source.callsFor(r.Execution.ExecutionID); slices.Contains(calls, "publish") || slices.Contains(calls, "attest") {
				return fmt.Errorf("cancellation before the publish point published: %v", calls)
			}
			keys, err := in.Candidate.Runtime.Start.Daemon.outputObjectKeys()
			if err != nil {
				return err
			}
			if len(keys) != 0 {
				return fmt.Errorf("the bucket holds %v after cancellation before the publish point", keys)
			}
			var candidates, discards int
			if err := in.Candidate.Runtime.Start.DB.Conn.QueryRow(`SELECT
 (SELECT count(*) FROM pipeline_run_output_candidates WHERE handoff_id=$1),
 (SELECT count(*) FROM pipeline_run_output_discards WHERE handoff_id=$1)`, string(r.HandoffID)).Scan(&candidates, &discards); err != nil {
				return err
			}
			if candidates != 0 || discards != 0 {
				return fmt.Errorf("an unpublished capture has %d candidates and %d discards", candidates, discards)
			}
			return nil
		}),
		CheckThat[RunCancellationGCS]("cancellation registered its publication receipt through the repeated publish", func(in RunCancellationGCS) error {
			r, _, err := in.settled()
			if err != nil {
				return err
			}
			calls := in.Source.callsFor(r.Execution.ExecutionID)
			publish, attest, release := slices.Index(calls, "publish"), slices.Index(calls, "attest"), slices.Index(calls, "release")
			if publish < 0 || !(publish < attest && attest < release) {
				return fmt.Errorf("cancellation's calls, want publish < attest < release: %v", calls)
			}
			return nil
		}),
		CheckThat[RunCancellationGCS]("its publication receipt is settled as a discard without a claim", func(in RunCancellationGCS) error {
			r, claims, err := in.settled()
			if err != nil {
				return err
			}
			if r.State != output.CaptureStateRegistered || r.Receipt == nil || !r.ReleaseAcknowledged {
				return fmt.Errorf("capture is %s with publication receipt=%t and released=%t", r.State, r.Receipt != nil, r.ReleaseAcknowledged)
			}
			return assertCancelledDiscard(in.Candidate.Runtime.Start.DB.Conn, r, claims)
		}),
		CheckThat[RunCancellationGCS]("its marked object remains in the bucket unbound", func(in RunCancellationGCS) error {
			r, claims, err := in.settled()
			if err != nil {
				return err
			}
			daemon := in.Candidate.Runtime.Start.Daemon
			keys, err := daemon.outputObjectKeys()
			if err != nil {
				return err
			}
			if len(keys) != 1 || !slices.Equal(keys, in.Before) {
				return fmt.Errorf("the bucket held %v before cancellation and %v after", in.Before, keys)
			}
			attrs, err := daemon.Client.Bucket(daemon.OutputBucket).Object(keys[0]).Attrs(daemon.Ctx)
			if err != nil {
				return err
			}
			if attrs.Metadata[output.MarkerKeyVersion] == "" || attrs.Generation != r.Ref.Generation {
				return fmt.Errorf("object %q has marker %q at generation %d; the receipt names %d", keys[0], attrs.Metadata[output.MarkerKeyVersion], attrs.Generation, r.Ref.Generation)
			}
			if len(claims) != 0 {
				return fmt.Errorf("the cancelled Run's object is bound by %d claims", len(claims))
			}
			return nil
		}),
		CheckThat[RunCancellationGCS]("its hidden claim is released in the aborted publication", func(in RunCancellationGCS) error {
			r, claims, err := in.settled()
			if err != nil {
				return err
			}
			var claim string
			if err := in.Candidate.Runtime.Start.DB.Conn.QueryRow(`SELECT claim_id FROM pipeline_run_output_candidates WHERE handoff_id=$1`, string(r.HandoffID)).Scan(&claim); err != nil {
				return fmt.Errorf("the hidden candidate is gone: %w", err)
			}
			if len(claims) != 1 || string(claims[0].ClaimID) != claim || claims[0].Active() {
				return fmt.Errorf("aborted publication left %d claims on the candidate's generation, active=%t", len(claims), len(claims) == 1 && claims[0].Active())
			}
			// The release and the aborted status share one transaction exactly
			// when the last writer of both rows is the same transaction.
			var released, published string
			if err := in.Candidate.Runtime.Start.DB.Conn.QueryRow(`SELECT c.xmin::text,r.xmin::text FROM hangar_claims c,pipeline_runs r WHERE c.claim_id=$1 AND r.id=$2`, claim, in.Candidate.Runtime.Start.Creation.Run.ID()).Scan(&released, &published); err != nil {
				return err
			}
			if released != published {
				return fmt.Errorf("the claim was released by transaction %s and the Run published by %s", released, published)
			}
			return nil
		}),
	}
}

// stopRunCapture runs the producer's capture until until, then lets its
// coordinator go the way a restarted web node's goes. The capture lease that
// coordinator held is expired rather than waited out, so the coordinator
// cancellation brings is the one left to take the capture over.
func stopRunCapture(in RunOutputRuntime, rec *brine.Recorder, until func(hangaroutput.Decision, output.HandoffRecord) bool) (RunOutputCandidate, error) {
	out, err := advanceRunCapture(in, rec, func(directory string, _ executioncontrol.Acknowledgement) error { return writeRunFindings(directory) }, until)
	if err != nil {
		return out, err
	}
	result, err := in.Start.DB.Conn.Exec(`UPDATE hangar_capture_attempt_leases SET renewed_at=clock_timestamp()-interval '16 minutes',expires_at=clock_timestamp()-interval '1 second' WHERE reservation_id=$1`, string(out.Record.ReservationID))
	if err != nil {
		return out, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return out, fmt.Errorf("the stopped capture has no lease to expire (%d rows): %v", n, err)
	}
	return out, nil
}

// settled reads the handoff and the claims on its generation as they are now.
func (in RunCancellationGCS) settled() (output.HandoffRecord, []output.ClaimRecord, error) {
	tx, err := in.Candidate.Runtime.Start.DB.Conn.Begin()
	if err != nil {
		return output.HandoffRecord{}, nil, err
	}
	defer db.Rollback(tx)
	repository := (RunOutputFinish{Start: in.Candidate.Runtime.Start}).repository()
	r, err := repository.LoadHandoffRecord(context.Background(), tx, in.Candidate.Record.HandoffID)
	if err != nil || r.Ref.Generation == 0 {
		return r, nil, err
	}
	claims, err := repository.ReadClaims(context.Background(), tx, r.Ref)
	return r, claims, err
}

// unlockedCalls is every node call cancellation made, each probed for the
// Run lock, none of which began capture work.
func (in RunCancellationGCS) unlockedCalls() ([]string, error) {
	calls, held := in.Source.probe.snapshot()
	if len(calls) == 0 {
		return nil, fmt.Errorf("cancellation made no node or store call, so the Run-lock probe proved nothing")
	}
	if len(held) != 0 {
		return nil, fmt.Errorf("cancellation made node calls under a Run lock: %v (all calls %v)", held, calls)
	}
	for _, call := range calls {
		// Sealing, drain confirmation and canonicalization begin a capture;
		// cancellation may only settle one that is already committed.
		if call == "begin-seal" || call == "confirm-seal" || call == "confirm-drain" || call == "canonicalize" {
			return nil, fmt.Errorf("cancellation began capture work with %q: %v", call, calls)
		}
	}
	return calls, nil
}

// recordingDrain is the capture coordinator's real drain, with its node and
// Pod calls noted, and so probed, by the source it records beside.
type recordingDrain struct {
	hangaroutput.DrainConfirmer
	source recordingSource
}

func (d recordingDrain) ConfirmDrain(ctx context.Context, locator string, started output.SealStarted) ([]output.DrainedWriter, error) {
	d.source.note(started.Acknowledgement.Execution.ExecutionID, "confirm-drain")
	return d.DrainConfirmer.ConfirmDrain(ctx, locator, started)
}

// ReleaseDrain inspects the node's hold and removes the Pod's evidence
// finalizer, after the source release.
func (d recordingDrain) ReleaseDrain(ctx context.Context, locator string, id executioncontrol.Identity, handoff output.HandoffID) error {
	d.source.note(id.ExecutionID, "release-drain")
	releaser, ok := d.DrainConfirmer.(hangaroutput.DrainReleaser)
	if !ok {
		return nil
	}
	return releaser.ReleaseDrain(ctx, locator, id, handoff)
}

// newLockProbedSource is the real node source, recording every call and
// probing the Run lock before each one.
func newLockProbedSource(in RunOutputRuntime, rec *brine.Recorder) (recordingSource, error) {
	probe, err := newRunLockProbe(in, rec)
	if err != nil {
		return recordingSource{}, err
	}
	source := newRecordingSource(in)
	source.probe = probe
	return source, nil
}

// runLockProbe asks its own PostgreSQL connection for the Run's row lock, the
// FOR NO KEY UPDATE every Run boundary takes, with NOWAIT: a refusal means
// some transaction holds it. The fixture's connection pool has one
// connection, so the probe needs a pool of its own.
type runLockProbe struct {
	conn  *sql.DB
	runID int
	mu    sync.Mutex
	calls []string
	held  []string
}

func newRunLockProbe(in RunOutputRuntime, rec *brine.Recorder) (*runLockProbe, error) {
	conn := in.Start.DB.runner.OpenSingleton()
	TrackDisposer(rec, "the Run lock probe connection", conn.Close)
	probe := &runLockProbe{conn: conn, runID: in.Start.Creation.Run.ID()}
	// A probe that cannot see a held lock would pass every scenario. It must
	// see one the fixture holds, and then see it gone.
	tx, err := in.Start.DB.Conn.Begin()
	if err != nil {
		return nil, err
	}
	var id int
	err = tx.QueryRow(`SELECT id FROM pipeline_runs WHERE id=$1 FOR NO KEY UPDATE`, probe.runID).Scan(&id)
	var locked bool
	if err == nil {
		locked, err = probe.locked()
	}
	db.Rollback(tx)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, fmt.Errorf("the Run-lock probe did not see a Run lock the fixture held")
	}
	if locked, err = probe.locked(); err != nil || locked {
		return nil, fmt.Errorf("the Run-lock probe still sees a released lock: %v", err)
	}
	return probe, nil
}

func (p *runLockProbe) locked() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := p.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id int
	err = tx.QueryRowContext(ctx, `SELECT id FROM pipeline_runs WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, p.runID).Scan(&id)
	if gcSQLState(err) == "55P03" {
		return true, nil
	}
	return false, err
}

func (p *runLockProbe) observe(call string) {
	locked, err := p.locked()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
	if err != nil {
		p.held = append(p.held, fmt.Sprintf("%s (probe failed: %v)", call, err))
	} else if locked {
		p.held = append(p.held, call)
	}
}

func (p *runLockProbe) snapshot() (calls, held []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls), slices.Clone(p.held)
}
