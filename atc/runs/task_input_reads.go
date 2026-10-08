package runs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

type taskInputSource interface {
	ManagedInputTimeout(int) time.Duration
	StatTaskInput(context.Context, string, string, hangar.TreeRef) (output.PublishedObject, error)
}

// SetInputReadMinter supplies the control plane's managed-read signing authority
// during startup. It is never copied into a task or retained Run definition.
func (s *ExecutionStarter) SetInputReadMinter(minter hangaroutput.WarrantMinter) {
	s.inputReadMinter = minter
}

// PrepareInputs runs after the worker has allocated the actual container
// handle. Each warrant is scoped to that handle's exact input volume, and its
// transaction repeats the Run/build/route checks before entering Hangar's locks.
func (s *ExecutionStarter) PrepareInputs(ctx context.Context, owner db.ContainerOwner, handle string, volumeNames []string, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	if spec.RunTaskID == "" {
		for _, input := range spec.Inputs {
			if input.RunInput != "" {
				return spec, atc.ErrInvalidRunInputs
			}
		}
		return spec, nil
	}
	buildID, planID, teamID, isBuild := db.BuildStepContainerIdentity(owner)
	if !isBuild || teamID != spec.TeamID || len(volumeNames) != len(spec.Inputs) {
		return spec, atc.ErrInvalidRunInputs
	}
	if err := s.CheckStart(ctx, owner, spec); err != nil {
		return spec, err
	}
	var selected db.RunTaskBinding
	var bindings map[int]atc.RunInputBinding
	err := s.transaction(ctx, func(tx db.Tx) error {
		var err error
		selected, err = db.LoadRunTask(ctx, tx, buildID, spec.RunTaskID)
		if err != nil {
			return err
		}
		if err = lockInputContainer(ctx, tx, handle, buildID, planID); err != nil {
			return err
		}
		bindings, err = matchRunTaskInputs(spec, selected, true)
		return err
	})
	if err != nil {
		return spec, err
	}
	if len(bindings) == 0 {
		return spec, nil
	}
	source, ok := s.Source.(taskInputSource)
	if !ok || s.inputReadMinter == nil || spec.ExecutionControl == nil || spec.ExecutionControl.Node == nil {
		return spec, atc.ErrRunResultsUnavailable
	}
	node := spec.ExecutionControl.Node
	prefix, err := db.HangarConsumerPrefixHeld("pipeline-run-input-read")
	if err != nil {
		return spec, err
	}
	prepared := spec
	prepared.Inputs = append([]runtime.Input(nil), spec.Inputs...)
	for i := range prepared.Inputs {
		binding, mapped := bindings[i]
		if !mapped {
			continue
		}
		admission := hangaroutput.ReadAdmission{
			Transactor: taskInputReadTransaction{ctx: ctx, conn: s.Conn, buildID: buildID, planID: planID, handle: handle, spec: spec, index: i, binding: binding},
			Claims:     db.NewHangarOutputRepository(prefix),
			Stat:       taskInputStat{source: source, name: node.Name, uid: string(node.UID)},
			Minter:     s.inputReadMinter, Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() }),
			Absences: &db.HangarAbsences{Conn: s.Conn},
		}
		destination := output.ReadDestination{Handle: handle, Volume: volumeNames[i]}
		warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{ClaimID: output.ClaimID(uuid.NewString()), Binding: inputReadBinding(destination), Ref: binding.Ref, Destination: destination, MaterializationTimeout: source.ManagedInputTimeout(len(bindings)), NodeUID: node.UID})
		if err != nil {
			return spec, err
		}
		prepared.Inputs[i].HangarRead = &output.ManagedReadRequest{Ref: binding.Ref, Destination: destination, Warrant: warrant.Token}
	}
	// Cancellation during the node stat or claim transaction closes start
	// admission. Any unused committed claim expires on its own.
	return prepared, s.CheckStart(ctx, owner, prepared)
}

// inputReadBinding is the opaque name a task input's reader's claim is taken
// under: the destination it was minted for, which is how releaseInputReads
// finds the claims of one task again.
func inputReadBinding(destination output.ReadDestination) output.OpaqueID {
	return output.OpaqueID("input-read:" + destination.Handle + "/" + destination.Volume)
}

// releaseInputReads gives back the readers' claims of a task's managed inputs
// once the node has recorded the task's exact start. The inputs are
// materialized by init containers, and the exact command starts only in the
// main container, after every init container -- each of which checks its
// input's materialization receipt -- has exited 0. So by the start witness
// every read those claims protected is over, and the web that holds them
// releases them: the node daemon has no client for the web and never releases
// a claim.
//
// Best effort, one transaction per claim: a failed release is not the start's
// failure, and an unreleased claim still expires.
func (s *ExecutionStarter) releaseInputReads(ctx context.Context, buildID int, planID atc.PlanID) {
	logger := lagerctx.FromContext(ctx).Session("release-input-reads", lager.Data{
		"build": buildID, "plan": string(planID),
	})
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if s.Conn == nil {
		return
	}
	rows, err := s.Conn.QueryContext(release, `
		SELECT k.claim_id, l.scope, l.digest, l.generation
		  FROM hangar_claims k
		  JOIN hangar_exact_lifecycles l ON l.id = k.lifecycle_id
		  JOIN containers c ON starts_with(k.consumer_binding_id, 'input-read:' || c.handle || '/')
		 WHERE c.build_id = $1 AND c.plan_id = $2
		   AND k.released_at IS NULL AND k.expires_at IS NOT NULL`, buildID, string(planID))
	if err != nil {
		logger.Error("query-claims", err)
		return
	}
	var releases []output.ClaimRelease
	for rows.Next() {
		var id, scope, digest string
		var generation int64
		if err := rows.Scan(&id, &scope, &digest, &generation); err != nil {
			logger.Error("scan-claim", err)
			continue
		}
		releases = append(releases, output.ClaimRelease{
			ProtocolVersion: output.ProtocolVersion, ClaimID: output.ClaimID(id),
			Ref: hangar.TreeRef{Scope: hangar.Scope(scope), Digest: hangar.Digest(digest), Generation: generation},
		})
	}
	if err := rows.Err(); err != nil {
		logger.Error("iterate-claims", err)
	}
	_ = rows.Close()
	if len(releases) == 0 {
		return
	}
	// The token is minted without a consumer prefix lock: the release
	// transaction takes only suffix locks (the logical, exact and claim rows
	// ReleaseClaim names), so there is no prefix for it to hold.
	prefix, err := db.HangarConsumerPrefixHeld("pipeline-run-input-read")
	if err != nil {
		logger.Error("prefix", err)
		return
	}
	claims := db.NewHangarOutputRepository(prefix)
	for _, claim := range releases {
		claim.RequestedAt = output.NewTimestamp(time.Now().UTC())
		if err := releaseInputRead(release, s.Conn, claims, claim); err != nil {
			logger.Error("release-claim", err, lager.Data{"claim": string(claim.ClaimID)})
		}
	}
}

func releaseInputRead(ctx context.Context, conn db.DbConn, claims *db.HangarOutputRepository, release output.ClaimRelease) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer db.Rollback(tx)
	if err := claims.ReleaseClaim(ctx, db.HangarOutputTx{Tx: tx}, release); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

type taskInputStat struct {
	source    taskInputSource
	name, uid string
}

func (s taskInputStat) StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	return s.source.StatTaskInput(ctx, s.name, s.uid, ref)
}

type taskInputReadTransaction struct {
	ctx     context.Context
	conn    db.DbConn
	buildID int
	planID  atc.PlanID
	handle  string
	spec    runtime.ContainerSpec
	index   int
	binding atc.RunInputBinding
}

func (t taskInputReadTransaction) Begin() (hangaroutput.Transaction, error) {
	tx, err := t.conn.BeginTx(t.ctx, nil)
	if err != nil {
		return nil, err
	}
	selected, err := db.LoadRunTask(t.ctx, tx, t.buildID, t.spec.RunTaskID)
	if err == nil {
		err = lockInputContainer(t.ctx, tx, t.handle, t.buildID, t.planID)
	}
	if err == nil {
		var bindings map[int]atc.RunInputBinding
		bindings, err = matchRunTaskInputs(t.spec, selected, true)
		if err == nil && bindings[t.index] != t.binding {
			err = atc.ErrRunInputUnavailable
		}
	}
	if err != nil {
		db.Rollback(tx)
		return nil, err
	}
	return db.HangarOutputTx{Tx: tx}, nil
}

func lockInputContainer(ctx context.Context, tx db.Tx, handle string, buildID int, planID atc.PlanID) error {
	var id int
	err := tx.QueryRowContext(ctx, `SELECT id FROM containers WHERE handle=$1 AND build_id=$2 AND plan_id=$3 FOR KEY SHARE`, handle, buildID, planID).Scan(&id)
	if err == sql.ErrNoRows {
		return atc.ErrRunInputUnavailable
	}
	return err
}
