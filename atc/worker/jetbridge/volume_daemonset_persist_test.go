package jetbridge

import (
	"strings"
	"testing"
)

// A step output wrapped without a volume repository cannot be given a row, and
// says so: the artifact output step turns that into a failed build naming the
// output, never a nil dereference. The path with a repository is pinned against
// a real database in atc/exec/artifact_output_step_jetbridge_test.go.
func TestDaemonSetVolume_PersistVolumeWithoutARepositoryRefuses(t *testing.T) {
	vol := NewDaemonSetVolume("h-output-o", "h-output-o", "w", nil, "", Config{}, nil)

	row, err := vol.PersistVolume(1)
	if err == nil || !strings.Contains(err.Error(), "no volume repository") {
		t.Fatalf("PersistVolume() = %v, %v; want a refusal naming the missing repository", row, err)
	}
	if vol.DBVolume() != nil {
		t.Fatalf("a refused PersistVolume left a row behind: %v", vol.DBVolume())
	}
}
