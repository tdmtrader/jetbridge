package hangaroutput

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type ReadNodeNames interface {
	ReadNodeName(context.Context, executioncontrol.NodeUID) (string, error)
}
type ReadNodeMembership interface {
	Admitted(context.Context, executioncontrol.ActivationEpoch, string, string) (bool, error)
}

// ReadNodeKeys binds current Kubernetes node identity to the attested cohort
// and the configured epoch key. The existing control ring trusts that cohort
// as one signing principal; it does not promise isolation among its members.
type ReadNodeKeys struct {
	Nodes      ReadNodeNames
	Membership ReadNodeMembership
	Ring       ControlKeyRing
}

func (keys *ReadNodeKeys) PublicKeyFor(uid executioncontrol.NodeUID, keyID string) (ed25519.PublicKey, error) {
	if keys.Nodes == nil || keys.Membership == nil {
		return nil, output.ErrUnauthorized
	}
	if err := keys.Ring.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	name, err := keys.Nodes.ReadNodeName(ctx, uid)
	if err != nil {
		return nil, err
	}
	admitted, err := keys.Membership.Admitted(ctx, keys.Ring.ActivationEpoch, name, keyID)
	if err != nil {
		return nil, err
	}
	if !admitted {
		return nil, output.ErrUnauthorized
	}
	for _, entry := range keys.Ring.Keys {
		if entry.Epoch == keys.Ring.ActivationEpoch {
			key, _ := base64.StdEncoding.DecodeString(entry.PublicKey)
			return ed25519.PublicKey(key), nil
		}
	}
	return nil, output.ErrUnauthorized
}
