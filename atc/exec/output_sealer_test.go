package exec_test

import (
	"context"

	"github.com/concourse/concourse/agent/snapshot"
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
