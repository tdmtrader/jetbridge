package runs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

type taskInputSource interface {
	ManagedInputTimeout(int) time.Duration
	StatTaskInput(context.Context, string, string, executioncontrol.ActivationEpoch, hangar.TreeRef) (output.PublishedObject, error)
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
		selected, err = db.LoadRunTask(ctx, tx, buildID, spec.RunTaskID, int64(s.Epoch))
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
		nonce, err := output.NewReadWarrantNonce(rand.Reader)
		if err != nil {
			return spec, err
		}
		admission := hangaroutput.ReadAdmission{
			Transactor: taskInputReadTransaction{ctx: ctx, conn: s.Conn, buildID: buildID, planID: planID, handle: handle, spec: spec, index: i, binding: binding, epoch: int64(s.Epoch)},
			Leases:     db.NewHangarOutputRepository(prefix),
			Stat:       taskInputStat{source: source, name: node.Name, uid: string(node.UID), epoch: s.Epoch},
			Minter:     s.inputReadMinter, Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() }),
		}
		destination := output.ReadDestination{Handle: handle, Volume: volumeNames[i]}
		warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{ReadLeaseID: output.ReadLeaseID(uuid.NewString()), WarrantNonce: nonce, ClaimID: binding.ClaimID, Ref: binding.Ref, Destination: destination, ActivationEpoch: s.Epoch, MaterializationTimeout: source.ManagedInputTimeout(len(bindings))})
		if err != nil {
			return spec, err
		}
		prepared.Inputs[i].HangarRead = &output.ManagedReadRequest{Ref: binding.Ref, Destination: destination, Warrant: warrant.Token}
	}
	// Cancellation during the node stat or lease transaction closes start
	// admission. Any unused committed lease is bounded by the existing cleaner.
	return prepared, s.CheckStart(ctx, owner, prepared)
}

type taskInputStat struct {
	source    taskInputSource
	name, uid string
	epoch     executioncontrol.ActivationEpoch
}

func (s taskInputStat) StatExactObject(ctx context.Context, ref hangar.TreeRef) (output.PublishedObject, error) {
	return s.source.StatTaskInput(ctx, s.name, s.uid, s.epoch, ref)
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
	epoch   int64
}

func (t taskInputReadTransaction) Begin() (hangaroutput.Transaction, error) {
	tx, err := t.conn.BeginTx(t.ctx, nil)
	if err != nil {
		return nil, err
	}
	selected, err := db.LoadRunTask(t.ctx, tx, t.buildID, t.spec.RunTaskID, t.epoch)
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
