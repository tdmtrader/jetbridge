package exec_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

type recordingOutputSealer struct {
	calls  []snapshot.SealRequest
	result map[string]snapshot.SealedOutput
	err    error
	stub   func(context.Context, snapshot.SealRequest) (map[string]snapshot.SealedOutput, error)
}

func (s *recordingOutputSealer) Seal(ctx context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	s.calls = append(s.calls, request.Clone())
	if s.stub != nil {
		return s.stub(ctx, request)
	}
	return s.result, s.err
}

func snapshotStoresForSealedOutputs(
	outputs map[string]snapshot.SealedOutput,
) (*snapshotfakes.FakeMetadataStore, *snapshotfakes.FakeContentStore) {
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedStub = func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		for _, output := range outputs {
			if output.Snapshot.ID != id {
				continue
			}
			return snapshot.Snapshot{
				ID: output.Snapshot.ID, Type: output.Snapshot.Type, Digest: output.Snapshot.Digest,
				Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
				CreatedAt: time.Now().UTC(),
			}, true, nil
		}
		return snapshot.Snapshot{}, false, nil
	}
	return metadata, new(snapshotfakes.FakeContentStore)
}
