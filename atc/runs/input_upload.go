package runs

import (
	"context"
	"io"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// InputPublisher is the authenticated node transport. The web process never
// receives object-store credentials or accepts a caller-selected object ref.
type InputPublisher interface {
	StageInput(context.Context, executioncontrol.NodeUID, io.Reader) (output.InputStage, error)
	PublishInput(context.Context, output.InputStage, string, *output.ReceiptSignatureVerifier) (output.InputPublication, error)
}

type InputUploadNode struct {
	UID       executioncontrol.NodeUID
	Publisher InputPublisher
	Verifier  *output.ReceiptSignatureVerifier
}

// InputUploadConfig is startup wiring. ClaimTTL defaults to the maximum grant
// lifetime and may be shortened; an uploading caller cannot extend it.
type InputUploadConfig struct {
	Source   func(context.Context, int64) (InputUploadNode, error)
	ClaimTTL time.Duration
}

func (a *admitter) SetInputUploadConfig(config InputUploadConfig) { a.inputUploads = config }

// UploadInput authenticates an input audience before reading bytes and again
// around each publication boundary. No transaction remains open during node
// work; only a committed exact publication and temporary claim yield a grant.
func (a *admitter) UploadInput(ctx context.Context, ref TemplateRef, principal Principal, name string, epoch int64, archive io.Reader) (atc.RunInputSource, error) {
	var source atc.RunInputSource
	config := a.inputUploads
	if config.ClaimTTL == 0 {
		config.ClaimTTL = runinput.MaxGrantTTL
	}
	if a.sealedInputs == nil || config.Source == nil || archive == nil || config.ClaimTTL < time.Second || config.ClaimTTL > runinput.MaxGrantTTL {
		return source, atc.ErrRunInputUnavailable
	}
	var audience runinput.Audience
	if err := a.withInputUpload(ctx, ref, principal, name, epoch, func(_ db.Tx, current runinput.Audience) error { audience = current; return nil }); err != nil {
		return source, err
	}
	node, err := config.Source(ctx, epoch)
	if err != nil {
		return source, err
	}
	if node.Publisher == nil || node.Verifier == nil || node.UID == "" {
		return source, atc.ErrRunInputUnavailable
	}
	stage, err := node.Publisher.StageInput(ctx, node.UID, archive)
	if err != nil {
		return source, err
	}
	if stage.NodeUID != node.UID || int64(stage.ActivationEpoch) != epoch {
		return source, atc.ErrRunInputUnavailable
	}
	nonce := uuid.NewString()
	if err := a.withInputUpload(ctx, ref, principal, name, epoch, func(tx db.Tx, current runinput.Audience) error {
		if current != audience {
			return atc.ErrRunInputUnavailable
		}
		return a.runFactory.ReserveRunInputUpload(ctx, tx, current, stage, nonce, config.ClaimTTL)
	}); err != nil {
		return source, err
	}
	publication, err := node.Publisher.PublishInput(ctx, stage, nonce, node.Verifier)
	if err != nil {
		return source, err
	}
	var claim db.RunInputUploadClaim
	if err := a.withInputUpload(ctx, ref, principal, name, epoch, func(tx db.Tx, current runinput.Audience) error {
		if current != audience {
			return atc.ErrRunInputUnavailable
		}
		var err error
		claim, err = a.runFactory.RegisterRunInputUpload(ctx, tx, current, publication, node.Verifier)
		return err
	}); err != nil {
		return source, err
	}
	source.SourceID, source.Bearer, err = a.sealedInputs.MintUntil(audience, claim.Ref, claim.ExpiresAt)
	return source, err
}

func (a *admitter) withInputUpload(ctx context.Context, ref TemplateRef, principal Principal, name string, epoch int64, operation func(db.Tx, runinput.Audience) error) error {
	subject, ok := principal.Claims["sub"].(string)
	if !ok || subject == "" {
		return ErrUnauthorized
	}
	tx, err := a.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	auth, err := a.authorizeActionLocked(ctx, tx, ref.Team, principal, atc.UploadPipelineRunInput)
	if err != nil {
		return err
	}
	pipeline, err := a.resolveTemplate(tx, auth, ref)
	if err != nil {
		return err
	}
	audience, err := a.runFactory.InputUploadAudience(ctx, tx, pipeline, name, epoch, runinput.PrincipalDigest(subject))
	if err != nil {
		return refusal(err)
	}
	if err := operation(tx, audience); err != nil {
		return err
	}
	return db.HangarCommitError(tx.Commit())
}
