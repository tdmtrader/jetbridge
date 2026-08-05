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

// snapshotStoresForSealedOutputs fabricates authorized manifests for each
// sealed output, plus - via the variadic knownInputs - for any already-bound
// typed input ref a test also needs GetAuthorized to recognize (e.g. the
// managed output builder's forwarded intrinsic-metadata lookup).
func snapshotStoresForSealedOutputs(
	outputs map[string]snapshot.SealedOutput,
	knownInputs ...snapshot.SnapshotRef,
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
		for _, ref := range knownInputs {
			if ref.ID != id {
				continue
			}
			return snapshot.Snapshot{
				ID: ref.ID, Type: ref.Type, Digest: ref.Digest,
				Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
				CreatedAt: time.Now().UTC(),
			}, true, nil
		}
		return snapshot.Snapshot{}, false, nil
	}
	return metadata, new(snapshotfakes.FakeContentStore)
}
