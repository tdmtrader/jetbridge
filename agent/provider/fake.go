package provider

import (
	"context"
	"errors"
)

// FakeAdapter is a compact test adapter for runner/provider contract tests.
// It is intentionally inert unless StartFunc is supplied.
type FakeAdapter struct {
	IdentityValue     Identity
	CapabilitiesValue Capabilities
	StartFunc         func(context.Context, StartRequest, BoundaryControl) (RunningSession, error)
}

func (fake *FakeAdapter) Identity() Identity         { return fake.IdentityValue }
func (fake *FakeAdapter) Capabilities() Capabilities { return fake.CapabilitiesValue }
func (fake *FakeAdapter) Start(ctx context.Context, request StartRequest, control BoundaryControl) (RunningSession, error) {
	if fake == nil || fake.StartFunc == nil {
		return nil, errors.New("fake provider adapter has no StartFunc")
	}
	return fake.StartFunc(ctx, request, control)
}
