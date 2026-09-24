package gcsdelete

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/hangar/objectstore"
)

func TestExactDeleteRefusesAnUnpinnedGenerationBeforeAccessingStorage(t *testing.T) {
	client := deleteClient{}
	for _, generation := range []int64{0, -1} {
		err := client.DeleteExact(context.Background(), "bucket", "key", generation)
		if !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("DeleteExact(%d): %v", generation, err)
		}
		if _, err := client.StatExact(context.Background(), "bucket", "key", generation); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("StatExact(%d): %v", generation, err)
		}
	}
}
