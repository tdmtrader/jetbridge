package runs

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

const MaxResultArchiveBytes int64 = 256 << 20

// ResultSource reads through an authenticated node, without bucket credentials.
type ResultSource interface {
	hangaroutput.ExactStat
	ManagedReadTimeout() time.Duration
	OpenManagedOutput(context.Context, output.ManagedReadRequest, int64) (io.ReadCloser, hangar.TreeAttributes, error)
}
type ResultSourceFunc func(context.Context, executioncontrol.ActivationEpoch) (ResultSource, error)

type ResultReader struct {
	Conn    db.DbConn
	Source  ResultSourceFunc
	Minter  hangaroutput.WarrantMinter
	Scratch string
}

// Read resolves a named retained result, admits its lease after exact metadata
// validation, then verifies a private copy before returning any external bytes.
func (r *ResultReader) Read(ctx context.Context, runID int, name string) (*hangar.CapturedTree, error) {
	if r == nil || r.Source == nil || r.Minter == nil {
		return nil, atc.ErrRunResultsUnavailable
	}
	selected, err := db.LoadRunResultRead(ctx, r.Conn, runID, name)
	if err != nil {
		return nil, err
	}
	source, err := r.Source(ctx, selected.Epoch)
	if err != nil {
		return nil, err
	}
	// The selected output plane owns the operation budget. Bound the whole
	// transfer and verification without imposing an unrelated API deadline.
	ctx, cancel := context.WithTimeout(ctx, output.ReadTransferTimeout(source.ManagedReadTimeout()))
	defer cancel()
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	nonce, err := output.NewReadWarrantNonce(rand.Reader)
	if err != nil {
		return nil, err
	}
	prefix, err := db.HangarConsumerPrefixHeld("pipeline-run-result-read")
	if err != nil {
		return nil, err
	}
	admission := hangaroutput.ReadAdmission{
		Transactor: resultReadTransaction{ctx: ctx, conn: r.Conn, runID: runID, name: name, selected: selected},
		Leases:     db.NewHangarOutputRepository(prefix), Stat: source, Minter: r.Minter,
		Clock: output.ClockFunc(func() time.Time { return time.Now().UTC() }),
	}
	destination := output.ReadDestination{Handle: id.String(), Volume: "result"}
	warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{
		ReadLeaseID: output.ReadLeaseID(id.String()), WarrantNonce: nonce, ClaimID: selected.Binding.ClaimID,
		Ref: selected.Binding.Ref, Destination: destination, ActivationEpoch: selected.Epoch, MaterializationTimeout: source.ManagedReadTimeout(),
	})
	if err != nil {
		return nil, err
	}
	archive, attributes, err := source.OpenManagedOutput(ctx, output.ManagedReadRequest{Ref: selected.Binding.Ref, Destination: destination, Warrant: warrant.Token}, MaxResultArchiveBytes)
	// If the transport failed, the node may still be staging. Its release or the
	// existing abandoned-lease cleaner owns closure; guessing would end protection.
	if err != nil {
		return nil, err
	}
	tree, err := (hangar.Canonicalizer{TempDir: r.Scratch, MaxContentBytes: MaxResultArchiveBytes}).Capture(ctx, io.LimitReader(archive, MaxResultArchiveBytes+1))
	err = errors.Join(err, archive.Close())
	if err == nil && (tree.Digest != selected.Binding.Ref.Digest || tree.ByteSize != attributes.LogicalBytes) {
		err = output.ErrCorrupt
	}
	if err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return nil, err
	}
	return tree, nil
}

type resultReadTransaction struct {
	ctx      context.Context
	conn     db.DbConn
	runID    int
	name     string
	selected db.RunResultRead
}

func (t resultReadTransaction) Begin() (hangaroutput.Transaction, error) {
	tx, err := t.conn.BeginTx(t.ctx, nil)
	if err != nil {
		return nil, err
	}
	if err = db.LockRunResultRead(t.ctx, tx, t.runID, t.name, t.selected); err != nil {
		db.Rollback(tx)
		return nil, err
	}
	return db.HangarOutputTx{Tx: tx}, nil
}
