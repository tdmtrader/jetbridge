package db

import (
	"context"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// OutputNodeKeys authenticates membership using the operator's persisted
// cohort attestation, never the public key supplied by a node's message.
type OutputNodeKeys struct{ Conn DbConn }

func (keys OutputNodeKeys) Admitted(ctx context.Context, epoch executioncontrol.ActivationEpoch, nodeName, keyID string) (bool, error) {
	var admitted bool
	err := keys.Conn.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM hangar_output_activation_epochs e,
 jsonb_array_elements(e.base_attestation->'members') member
 WHERE e.epoch_id=$1 AND e.base_state IN ('enabled','draining')
 AND e.output_state IN ('enabled','draining') AND member->>'node'=$2
 AND member->>'control_key_id'=$3 AND member->>'activation_epoch'=$1::text)`, int64(epoch), nodeName, keyID).Scan(&admitted)
	return admitted, err
}
