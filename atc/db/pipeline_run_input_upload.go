package db

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

type RunInputUploadClaim struct {
	Ref       hangar.TreeRef
	ExpiresAt time.Time
}

func validateRunnableTemplate(locked *pipeline) error {
	if locked.InstanceVars() != nil {
		return ErrPipelineRunInstanced
	}
	if !locked.Template() {
		return ErrPipelineRunNotTemplate
	}
	if locked.Archived() {
		return ErrPipelineRunArchived
	}
	if locked.Paused() {
		return ErrPipelineRunPaused
	}
	return nil
}

// InputUploadAudience follows the shared authorization/team prefix. It retains
// activation and template locks through the caller's transaction and reads the
// effective declarations on that same connection.
func (f *pipelineRunFactory) InputUploadAudience(ctx context.Context, tx Tx, template Pipeline, input string, epoch int64, principal string) (runinput.Audience, error) {
	var audience runinput.Audience
	// An upload is published under the Hangar epoch (epoch), while Run
	// admission is open under its own marker; both are the activation prefix.
	if marker, err := lockRunActivationMarker(ctx, tx); err != nil {
		return audience, err
	} else if !marker.enabled {
		return audience, atc.ErrRunResultsUnavailable
	}
	if err := lockEnabledHangarEpoch(ctx, tx, epoch); err != nil {
		return audience, err
	}
	locked := newPipeline(f.conn, f.lockFactory)
	if err := scanPipeline(locked, pipelinesQuery.Where(sq.Eq{"p.id": template.ID()}).Suffix("FOR UPDATE OF p").RunWith(tx).QueryRow()); err != nil {
		return audience, err
	}
	if locked.TeamID() != template.TeamID() {
		return audience, atc.ErrRunInputUnavailable
	}
	if err := validateRunnableTemplate(locked); err != nil {
		return audience, err
	}
	config, err := f.effectiveConfig(tx, locked, nil)
	if err != nil {
		return audience, err
	}
	if err := configvalidate.ValidateTemplateConfig(config); err != nil {
		return audience, ErrPipelineTemplateInvalid{Err: err}
	}
	declarations, err := atc.RunTaskDeclarations(config)
	if err != nil {
		return audience, ErrPipelineTemplateInvalid{Err: err}
	}
	for _, task := range declarations {
		for _, declaration := range task.Inputs {
			if declaration.Name == input {
				return runinput.Audience{TeamID: locked.TeamID(), TemplateID: locked.ID(), PrincipalDigest: principal, Input: input, Epoch: epoch}, nil
			}
		}
	}
	return audience, atc.ErrInvalidRunInputs
}

func inputUploadRepository() *HangarOutputRepository {
	prefix, _ := HangarConsumerPrefixHeld("pipeline-run-input-upload")
	return NewHangarOutputRepository(prefix)
}

// ReserveRunInputUpload owns only the temporary upload claim. Its row precedes
// the Hangar suffix; its deferred FK and logical reservation commit together.
func (f *pipelineRunFactory) ReserveRunInputUpload(ctx context.Context, tx Tx, audience runinput.Audience, stage output.InputStage, nonce string, ttl time.Duration) error {
	if int64(stage.ActivationEpoch) != audience.Epoch || ttl < time.Second || ttl > runinput.MaxGrantTTL {
		return atc.ErrRunInputUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_input_uploads
		(reservation_id, template_pipeline_id, team_id, principal_digest, input_name, activation_epoch, claim_id, expires_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,date_trunc('second', clock_timestamp()+$8::interval)
		WHERE NOT EXISTS (SELECT 1 FROM hangar_input_publications WHERE reservation_id=$1)
		ON CONFLICT (reservation_id) DO NOTHING`, string(stage.ReservationID), audience.TemplateID, audience.TeamID, audience.PrincipalDigest, audience.Input, audience.Epoch, uuid.NewString(), hangarInterval(ttl.Truncate(time.Second))); err != nil {
		return HangarCommitError(err)
	}
	if _, _, err := lockRunInputUpload(ctx, tx, audience, stage.ReservationID); err != nil {
		return err
	}
	return inputUploadRepository().ReserveInputPublication(ctx, tx, stage, nonce)
}

func lockRunInputUpload(ctx context.Context, tx Tx, a runinput.Audience, id output.ReservationID) (output.ClaimID, time.Time, error) {
	var claim output.ClaimID
	var expires time.Time
	err := tx.QueryRowContext(ctx, `SELECT claim_id, expires_at FROM pipeline_run_input_uploads
		WHERE reservation_id=$1 AND template_pipeline_id=$2 AND team_id=$3 AND principal_digest=$4 AND input_name=$5 AND activation_epoch=$6 AND expires_at > clock_timestamp()
		FOR UPDATE`, string(id), a.TemplateID, a.TeamID, a.PrincipalDigest, a.Input, a.Epoch).Scan(&claim, &expires)
	if err == sql.ErrNoRows {
		err = atc.ErrRunInputUnavailable
	}
	return claim, expires, err
}

func (f *pipelineRunFactory) RegisterRunInputUpload(ctx context.Context, tx Tx, audience runinput.Audience, publication output.InputPublication, verifier *output.ReceiptSignatureVerifier) (RunInputUploadClaim, error) {
	var result RunInputUploadClaim
	claim, expires, err := lockRunInputUpload(ctx, tx, audience, publication.Stage.ReservationID)
	if err != nil {
		return result, err
	}
	repository := inputUploadRepository()
	if err := repository.RegisterInputPublication(ctx, tx, publication, verifier); err != nil {
		return result, err
	}
	if err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: publication.Attributes.Ref, ConsumerBindingID: output.OpaqueID(claim), RequestedAt: output.NewTimestamp(time.Now())}); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_input_uploads SET claim_acquired_at=coalesce(claim_acquired_at, clock_timestamp()) WHERE reservation_id=$1`, string(publication.Stage.ReservationID)); err != nil {
		return result, HangarCommitError(err)
	}
	return RunInputUploadClaim{Ref: publication.Attributes.Ref, ExpiresAt: expires}, nil
}

// ReleaseExpiredInputUploads runs even when new admission is held. It releases
// only the upload claim; Run claims and exact publication tombstones survive.
func (l *pipelineRunReclaimLifecycle) ReleaseExpiredInputUploads(ctx context.Context, limit int) error {
	if limit <= 0 {
		return nil
	}
	rows, err := l.conn.QueryContext(ctx, `SELECT reservation_id, team_id, template_pipeline_id FROM pipeline_run_input_uploads WHERE expires_at <= clock_timestamp() ORDER BY expires_at, reservation_id LIMIT $1`, limit)
	if err != nil {
		return err
	}
	type candidate struct {
		id             string
		team, template int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.team, &c.template); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if err := l.releaseInputUpload(ctx, c.id, c.team, c.template); err != nil {
			return err
		}
	}
	return nil
}

func (l *pipelineRunReclaimLifecycle) releaseInputUpload(ctx context.Context, id string, team, template int) error {
	tx, err := l.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if _, err := tx.ExecContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, team); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM pipelines WHERE id=$1 FOR SHARE`, template); err != nil {
		return err
	}
	var claim output.ClaimID
	var acquired sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT claim_id, claim_acquired_at FROM pipeline_run_input_uploads WHERE reservation_id=$1 AND expires_at <= clock_timestamp() FOR UPDATE`, id).Scan(&claim, &acquired)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if acquired.Valid {
		var ref hangar.TreeRef
		if err := tx.QueryRowContext(ctx, `SELECT l.scope, l.digest, l.generation FROM hangar_input_publications p JOIN hangar_exact_lifecycles l ON l.id=p.lifecycle_id WHERE p.reservation_id=$1`, id).Scan(&ref.Scope, &ref.Digest, &ref.Generation); err != nil {
			return err
		}
		if err := inputUploadRepository().ReleaseClaim(ctx, tx, output.ClaimRelease{ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: ref, RequestedAt: output.NewTimestamp(time.Now())}); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_run_input_uploads WHERE reservation_id=$1`, id); err != nil {
		return err
	}
	return HangarCommitError(tx.Commit())
}
