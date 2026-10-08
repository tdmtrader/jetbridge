package runs

import (
	"context"
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

const maxResultArchiveBytes int64 = 256 << 20

// ResultSource reads through an authenticated node, without bucket credentials.
type ResultSource interface {
	hangaroutput.ExactStat
	ManagedReadTimeout() time.Duration
	// NodeUID is the node the source reads from; the warrant is bound to it.
	NodeUID() executioncontrol.NodeUID
	OpenManagedOutput(context.Context, output.ManagedReadRequest, int64) (io.ReadCloser, hangar.TreeAttributes, error)
}
type ResultSourceFunc func(context.Context, executioncontrol.ActivationEpoch) (ResultSource, error)

type ResultReader struct {
	Conn    db.DbConn
	Source  ResultSourceFunc
	Minter  hangaroutput.WarrantMinter
	Scratch string
}

// Read resolves a named retained result, takes a reader's claim on it after
// exact metadata validation, then verifies a private copy before returning any
// external bytes.
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
	prefix, err := db.HangarConsumerPrefixHeld("pipeline-run-result-read")
	if err != nil {
		return nil, err
	}
	claims := db.NewHangarOutputRepository(prefix)
	admission := hangaroutput.ReadAdmission{
		Transactor: resultReadTransaction{ctx: ctx, conn: r.Conn, runID: runID, name: name, selected: selected},
		Claims:     claims, Stat: source, Minter: r.Minter,
		Clock:    output.ClockFunc(func() time.Time { return time.Now().UTC() }),
		Absences: &db.HangarAbsences{Conn: r.Conn},
	}
	destination := output.ReadDestination{Handle: id.String(), Volume: "result"}
	warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{
		ClaimID: output.ClaimID(id.String()), Binding: output.OpaqueID("result-read:" + id.String()),
		Ref: selected.Binding.Ref, Destination: destination, MaterializationTimeout: source.ManagedReadTimeout(),
		NodeUID: source.NodeUID(),
	})
	if err != nil {
		return nil, err
	}
	archive, attributes, err := source.OpenManagedOutput(ctx, output.ManagedReadRequest{Ref: selected.Binding.Ref, Destination: destination, Warrant: warrant.Token}, maxResultArchiveBytes)
	// If the transport failed, the node may still be staging under this
	// claim. It expires on its own; releasing it now would end protection a
	// read in progress may still need.
	if err != nil {
		return nil, err
	}
	tree, err := (hangar.Canonicalizer{TempDir: r.Scratch, MaxContentBytes: maxResultArchiveBytes}).Capture(ctx, io.LimitReader(archive, maxResultArchiveBytes+1))
	err = errors.Join(err, archive.Close())
	// The archive is consumed: the node's read of the object is over, so this
	// read's claim is given back here, by the web that holds it. The node
	// daemon has no client for the web and never releases a claim. A failed
	// release is not the read's failure -- the claim still expires.
	r.releaseClaim(ctx, claims, warrant.Claim)
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

// releaseClaim gives one reader's claim back in its own transaction. Best
// effort: an unreleased claim is bounded by its term.
func (r *ResultReader) releaseClaim(ctx context.Context, claims *db.HangarOutputRepository, claim output.ClaimRecord) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := r.Conn.BeginTx(release, nil)
	if err != nil {
		return
	}
	defer db.Rollback(tx)
	if err := claims.ReleaseClaim(release, db.HangarOutputTx{Tx: tx}, output.ClaimRelease{
		ProtocolVersion: output.ProtocolVersion, ClaimID: claim.ClaimID, Ref: claim.Ref,
		RequestedAt: output.NewTimestamp(time.Now().UTC()),
	}); err != nil {
		return
	}
	_ = tx.Commit()
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
