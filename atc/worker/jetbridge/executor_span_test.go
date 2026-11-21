package jetbridge

import (
	"strings"
	"testing"
)

// A supervised task's exec is ["sh", "-c", <script>], and the script embeds
// the task's run.path and run.args. The span records the command's path and
// never its arguments, however short the command is.
func TestTheExecSpanRecordsTheCommandPathOnly(t *testing.T) {
	for _, command := range [][]string{
		{"sh", "-c", "exec /bin/deploy --token=s3cr3t"},
		{"/bin/deploy", "--token=s3cr3t"},
		{"sh", "-c", "a", "b", "c", "d", "--token=s3cr3t"},
	} {
		attrs := execSpanAttrs("ns", "pod", "main", command, false, ExecAttrs{Purpose: "task"})
		if got := attrs["exec.command"]; got != command[0] {
			t.Errorf("exec.command = %q, want %q", got, command[0])
		}
		for key, value := range attrs {
			if strings.Contains(value, "s3cr3t") {
				t.Errorf("span attribute %s carries an argument: %q", key, value)
			}
		}
	}
	if _, ok := execSpanAttrs("ns", "pod", "main", nil, false, ExecAttrs{})["exec.command"]; ok {
		t.Error("an empty command records exec.command")
	}
}
