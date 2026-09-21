package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
)

var (
	ErrCredentialInput    = errors.New("invalid session credential input")
	ErrCredentialDelivery = errors.New("credential delivery was not acknowledged; inspect the existing Run")
)

func (a *admitter) SetCredentialHandoffConfig(config CredentialHandoffConfig) {
	a.credentialHandoff = config
}

func (a *admitter) credentialTarget(ctx context.Context, ref TemplateRef, principal Principal, number int, result string, epoch int64, action string, claim bool) (db.RunCredentialTarget, error) {
	var target db.RunCredentialTarget
	subject, ok := principal.Claims["sub"].(string)
	if !ok || subject == "" {
		return target, ErrUnauthorized
	}
	tx, err := a.conn.BeginTx(ctx, nil)
	if err != nil {
		return target, err
	}
	defer db.Rollback(tx)
	auth, err := a.authorizeActionLocked(ctx, tx, ref.Team, principal, atc.CreatePipelineRunV2)
	if err != nil {
		return target, err
	}
	// This action may be configured more strictly than creation, while a
	// stricter creation role must still apply to credential-bearing sessions.
	if _, err = a.authorizeAction(tx, ref.Team, principal, action); err != nil {
		return target, err
	}
	pipeline, err := a.resolveTemplate(tx, auth, ref)
	if err != nil {
		return target, err
	}
	target, err = db.LoadRunCredentialTarget(ctx, tx, pipeline.ID(), number, runinput.PrincipalDigest(subject), result, epoch, claim)
	if errors.Is(err, db.ErrRunCredentialOwner) {
		return target, ErrUnauthorized
	}
	if err != nil {
		return target, err
	}
	// Only a new delivery reaches the image; a claimed or ready session is a
	// recorded fact. Refusing here also rolls back a claim made just above.
	if target.Status == "available" || target.ClaimedNow {
		if err := a.credentialHandoff.AdmitWorkerImage(target.WorkerImage); err != nil {
			return target, err
		}
	}
	return target, db.HangarCommitError(tx.Commit())
}

func (a *admitter) InspectCredentialHandoff(ctx context.Context, ref TemplateRef, principal Principal, number int, result string, epoch int64) (atc.RunCredentialSession, error) {
	target, err := a.credentialTarget(ctx, ref, principal, number, result, epoch, atc.GetPipelineRunCredentialSession, false)
	return target.RunCredentialSession, err
}

// HandoffCredentials owns and closes input. No database transaction remains
// open while reading or forwarding it. The second authorization and the claim
// commit together, after reading, so a revoked owner cannot use an open upload.
func (a *admitter) HandoffCredentials(ctx context.Context, ref TemplateRef, principal Principal, number int, result string, epoch int64, input io.ReadCloser) (atc.RunCredentialSession, error) {
	if input == nil {
		return atc.RunCredentialSession{}, ErrCredentialInput
	}
	defer input.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stop()
	target, err := a.credentialTarget(ctx, ref, principal, number, result, epoch, atc.HandoffPipelineRunCredentials, false)
	if err != nil || target.Status != "available" {
		return target.RunCredentialSession, err
	}
	config := a.credentialHandoff
	if config.Source == nil || !filepath.IsAbs(config.Helper) || !filepath.IsAbs(config.Socket) || config.Lifetime <= 0 || config.Lifetime > 32*time.Minute {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	data, err := io.ReadAll(io.LimitReader(input, 65537))
	defer clear(data)
	if err != nil || len(data) > 65536 || !json.Valid(data) {
		return target.RunCredentialSession, ErrCredentialInput
	}
	target, err = a.credentialTarget(ctx, ref, principal, number, result, epoch, atc.HandoffPipelineRunCredentials, true)
	if err != nil || !target.ClaimedNow {
		return target.RunCredentialSession, err
	}
	var response credentialResponse
	command := []string{config.Helper, "auth-handoff", "--socket", config.Socket, "--run-id", strconv.Itoa(target.RunID)}
	if err = config.Source.ExecBoundSession(ctx, target.NodeName, *target.Start, config.Lifetime, command, bytes.NewReader(data), &response); err != nil {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	if string(response.data) != fmt.Sprintf("{\"run_id\":%d,\"status\":\"ready\"}\n", target.RunID) {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	// A disconnected caller does not erase an observed worker acknowledgement.
	// This bounded transaction records a fact, not renewed caller authority.
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer recordCancel()
	tx, err := a.conn.BeginTx(recordCtx, nil)
	if err != nil {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	defer db.Rollback(tx)
	if err = db.RecordRunCredentialReady(recordCtx, tx, target.RunID, target.HandoffID); err != nil {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	if err = tx.Commit(); err != nil {
		return target.RunCredentialSession, ErrCredentialDelivery
	}
	target.Status = "ready"
	return target.RunCredentialSession, nil
}

type credentialResponse struct{ data []byte }

func (w *credentialResponse) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > 128 {
		return 0, io.ErrShortWrite
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
