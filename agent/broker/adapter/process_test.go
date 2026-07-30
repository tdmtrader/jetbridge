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
	prepared, err := adapter.Prepare(context.Background(), profile(broker.AdapterCodex), adapter.Paths{
		WorkDir: t.TempDir(), ScratchDir: t.TempDir(), OutputSchema: "/schema/result.json",
	}, "secret", &fakeVersionProbe{path: script, version: "codex-cli 0.146.0\n"})
	if err != nil {
		t.Fatalf("Prepare(): %v", err)
	}
	result, err := adapter.Execute(context.Background(), prepared, "fresh prompt\n", 4096)
	if err != nil {
		t.Fatalf("Execute(): %v", err)
	}
	if string(result.Output) != `{"answer":"ok"}` {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestExecuteRejectsAnUnpreparedInvocation(t *testing.T) {
	_, err := adapter.Execute(context.Background(), adapter.PreparedInvocation{}, "", 4096)
	if err == nil || !strings.Contains(err.Error(), "prepared") {
		t.Fatalf("Execute() error = %v", err)
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
	schema := filepath.Join(t.TempDir(), "result.schema.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(context.Background(), profile(broker.AdapterClaude), adapter.Paths{
		WorkDir: t.TempDir(), ScratchDir: t.TempDir(), OutputSchema: schema,
	}, "secret", &fakeVersionProbe{path: script, version: "claude 2.1.212\n"})
	if err != nil {
		t.Fatalf("Prepare(): %v", err)
	}
	_, err = adapter.Execute(ctx, prepared, "", 4096)
	if err == nil || !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("Execute() error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancelled process was not reaped promptly")
	}
}
