package output

import "testing"

// Capture-derived identities are stable and distinct.
func TestCaptureDerivedIdentitiesAreStable(t *testing.T) {
	a := CaptureKey{ExecutionID: "0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b", Output: "a"}
	b := CaptureKey{ExecutionID: "0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b", Output: "b"}
	if a.ClaimID() != a.ClaimID() || a.ClaimID() == b.ClaimID() || a.ClaimID() == ClaimID(a.MarkerID()) {
		t.Fatal("derived identities are not stable and distinct")
	}
	if err := a.ClaimID().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkerID().Validate(); err != nil {
		t.Fatal(err)
	}
}
