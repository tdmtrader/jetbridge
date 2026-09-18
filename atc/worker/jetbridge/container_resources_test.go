package jetbridge

import (
	"testing"

	"github.com/concourse/concourse/atc/runtime"
)

// API serialization erases the difference between nil and an empty resource map.
// Keep this in-memory contract here; Brine checks quantities and the absence of
// resource ceilings on the pod stored by the real Kubernetes API.
func TestRequestsOnlyResourceLimitsRemainNil(t *testing.T) {
	cpu, memory := uint64(256), uint64(536870912)
	got := buildResourceRequirements(runtime.ContainerLimits{CPURequest: &cpu, MemoryRequest: &memory})
	if got.Limits != nil {
		t.Fatalf("requests-only limits must remain nil, got %#v", got.Limits)
	}
}
