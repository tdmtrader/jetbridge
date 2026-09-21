package db

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// ReserveInputPublication records the node-derived logical identity BEFORE the
// consumer asks the node to create an object. The consumer owns authorization,
// the activation/domain prefix and this transaction. No network work occurs
// here. A reservation provides correlation, not a claim or a Run binding.
func (repository *HangarOutputRepository) ReserveInputPublication(ctx context.Context, tx output.Tx, stage output.InputStage, nonce string) error {
	if err := stage.Validate(); err != nil {
		return err
	}
	if err := (output.InputPublishRequest{Version: stage.Version, ReservationID: stage.ReservationID, Nonce: nonce}).Validate(); err != nil {
		return err
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{Logical: []HangarLogicalKey{{Scope: stage.Scope, Digest: stage.Digest}}}); err != nil {
		return err
	}
	body, err := json.Marshal(stage)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_input_publications
		(reservation_id, scope, digest, activation_epoch, stage, nonce, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, ($5::jsonb->>'expires_at')::timestamptz)
		ON CONFLICT (reservation_id) DO NOTHING`, string(stage.ReservationID), string(stage.Scope), string(stage.Digest), int64(stage.ActivationEpoch), body, nonce); err != nil {
		return hangarConflict(err)
	}
	var same bool
	if err := hangarQueryRow(ctx, tx, `SELECT stage=$2::jsonb AND nonce=$3::uuid FROM hangar_input_publications WHERE reservation_id=$1`, []any{string(stage.ReservationID), body, nonce}, &same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%w: input publication identity was already reserved for different facts", output.ErrConflict)
	}
	return nil
}

// RegisterInputPublication consumes one retained nonce and registers its exact
// signed publication. A consumer acquires its claim and writes its own binding
// in this SAME transaction, committing through HangarCommitError. A retry may
// return only the original publication; it never changes an exact generation.
func (repository *HangarOutputRepository) RegisterInputPublication(ctx context.Context, tx output.Tx, publication output.InputPublication, verifier *output.ReceiptSignatureVerifier) error {
	if err := publication.Validate(); err != nil {
		return err
	}
	stage, nonce, _, err := readInputPublication(ctx, tx, publication.Stage.ReservationID)
	if err != nil {
		return err
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: stage.Scope, Digest: stage.Digest}},
		Exact:   []hangar.TreeRef{publication.Attributes.Ref},
	}); err != nil {
		return err
	}
	stage, nonce, retained, err := readInputPublication(ctx, tx, publication.Stage.ReservationID)
	if err != nil {
		return err
	}
	if retained != nil {
		if !reflect.DeepEqual(*retained, publication) {
			return fmt.Errorf("%w: input reservation already registered another publication", output.ErrConflict)
		}
		return nil
	}
	if err := verifier.VerifyInputPublication(publication, stage, nonce); err != nil {
		return err
	}
	var fresh bool
	if err := hangarQueryRow(ctx, tx, `SELECT expires_at > clock_timestamp() FROM hangar_input_publications WHERE reservation_id=$1`, []any{string(stage.ReservationID)}, &fresh); err != nil {
		return err
	}
	if !fresh {
		return fmt.Errorf("%w: input publication reservation expired", output.ErrConflict)
	}
	lifecycle, err := repository.upsertLifecycle(ctx, tx, publication.Attributes.Ref, publication.Metageneration, int64(stage.ActivationEpoch), "registered")
	if err != nil {
		return err
	}
	var readable bool
	if err := hangarQueryRow(ctx, tx, `SELECT state IN ('registered', 'adopted') AND activation_epoch=$2 FROM hangar_exact_lifecycles WHERE id=$1`, []any{lifecycle, int64(stage.ActivationEpoch)}, &readable); err != nil {
		return err
	}
	if !readable {
		return fmt.Errorf("%w: input publication is no longer available for registration", output.ErrConflict)
	}
	body, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hangar_input_publications SET lifecycle_id=$2, publication=$3, registered_at=clock_timestamp() WHERE reservation_id=$1`, string(stage.ReservationID), lifecycle, body)
	return hangarConflict(err)
}

func readInputPublication(ctx context.Context, tx output.Tx, id output.ReservationID) (output.InputStage, string, *output.InputPublication, error) {
	var stage output.InputStage
	var nonce string
	var stageBody, publicationBody []byte
	if err := hangarQueryRow(ctx, tx, `SELECT stage, nonce, publication FROM hangar_input_publications WHERE reservation_id=$1`, []any{string(id)}, &stageBody, &nonce, &publicationBody); err != nil {
		return stage, nonce, nil, err
	}
	if err := json.Unmarshal(stageBody, &stage); err != nil {
		return stage, nonce, nil, fmt.Errorf("%w: retained input stage", output.ErrCorrupt)
	}
	if len(publicationBody) == 0 {
		return stage, nonce, nil, nil
	}
	var publication output.InputPublication
	if err := json.Unmarshal(publicationBody, &publication); err != nil {
		return stage, nonce, nil, fmt.Errorf("%w: retained input publication", output.ErrCorrupt)
	}
	return stage, nonce, &publication, nil
}
