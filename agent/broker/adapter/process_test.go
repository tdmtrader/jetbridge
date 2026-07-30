package adapter_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestExecuteRunsWithoutShellAndDecodesTheNativeStream(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-codex")
	contents := `#!/bin/sh
read prompt
test "$prompt" = "fresh prompt" || exit 9
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"answer\":\"ok\"}"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}'
`
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Execute(context.Background(), adapter.Invocation{
		Binary: script, Env: map[string]string{"HOME": t.TempDir()}, WorkDir: t.TempDir(),
	}, "fresh prompt\n", broker.AdapterCodex, 4096)
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if string(result.Output) != `{"answer":"ok"}` {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestExecuteCancelsTheProcessGroup(t *testing.T) {
	script := filepath.Join(t.TempDir(), "slow")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := adapter.Execute(ctx, adapter.Invocation{
		Binary: script, Env: map[string]string{"HOME": t.TempDir()}, WorkDir: t.TempDir(),
	}, "", broker.AdapterClaude, 4096)
	if err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("Execute() error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancelled process was not reaped promptly")
	}
}
