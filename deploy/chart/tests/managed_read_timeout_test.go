package tests

import (
	"strings"
	"testing"
)

func TestManagedReadTimeoutReachesBothAdmissionAndExecution(t *testing.T) {
	out := renderOutput(t, "hangarOutput.operationTimeout=15m")
	for _, check := range []struct{ kind, suffix, flag string }{
		{"Deployment", "-web", "--kubernetes-hangar-output-operation-timeout=15m"},
		{"DaemonSet", "-" + outputDaemonComponent, "--output-timeout=15m"},
	} {
		workload := objectNamed(t, out, check.kind, check.suffix)
		if !strings.Contains(workload.body, check.flag) {
			t.Errorf("%s does not receive the shared read budget %q", workload.name, check.flag)
		}
	}
}
