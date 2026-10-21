package atccmd

import (
	"strings"
	"testing"
)

// 0 jobs scheduling at once is not a limit but a scheduler that never
// schedules: it blocks on the first job while holding the scheduler lock. The
// web has to refuse it at startup instead.
func TestJobSchedulingMaxInFlightMustAllowOneJob(t *testing.T) {
	const flag = "--job-scheduling-max-in-flight"

	err := (&RunCommand{JobSchedulingMaxInFlight: 0}).validate()
	if err == nil || !strings.Contains(err.Error(), flag) {
		t.Fatalf("%s=0 accepted; the scheduler would block on its first job (validate: %v)", flag, err)
	}

	for _, n := range []uint64{1, 32} {
		err := (&RunCommand{JobSchedulingMaxInFlight: n}).validate()
		if err != nil && strings.Contains(err.Error(), flag) {
			t.Fatalf("%s=%d rejected: %v", flag, n, err)
		}
	}
}
