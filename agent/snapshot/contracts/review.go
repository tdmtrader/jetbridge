package contracts

import (
	"context"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
)

type reviewValidator struct{}

func (reviewValidator) Validate(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	var review schema.ReviewOutput
	if err := decodeStrictDocument(ctx, root, "review.json", &review); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := review.ValidateSnapshotV1(); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: review.json: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}
