package adapter_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestDecodeNativeStreamsNormalizesTerminalOutput(t *testing.T) {
	tests := []struct {
		name    string
		adapter broker.AdapterName
		stream  string
		want    string
		cost    bool
	}{
		{
			name: "codex", adapter: broker.AdapterCodex,
			stream: `{"type":"thread.started","thread_id":"thread-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"{\"answer\":\"codex\"}"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":4}}
`,
			want: `{"answer":"codex"}`,
		},
		{
			name: "claude", adapter: broker.AdapterClaude,
			stream: readFixture(t, "testdata/claude-2.1.212-success.jsonl"),
			want:   `{"answer":"claude"}`, cost: true,
		},
		{
			name: "cursor", adapter: broker.AdapterCursor,
			stream: `{"type":"system","subtype":"init","session_id":"session-2","model":"exact"}
{"type":"result","subtype":"success","result":"{\"answer\":\"cursor\"}","duration_ms":900}
`,
			want: `{"answer":"cursor"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := adapter.DecodeStream(tc.adapter, strings.NewReader(tc.stream), 32<<10)
			if err != nil {
				t.Fatalf("DecodeStream(): %v", err)
			}
			if string(result.Output) != tc.want {
				t.Fatalf("output = %q, want %q", result.Output, tc.want)
			}
			if tc.adapter != broker.AdapterCodex &&
				(len(result.Events) < 2 || result.Events[len(result.Events)-1].Kind != broker.EventCompleted) {
				t.Fatalf("terminal events = %#v", result.Events)
			}
			if tc.cost != (result.Usage.CostUSD != nil) {
				t.Fatalf("cost presence = %t, want %t", result.Usage.CostUSD != nil, tc.cost)
			}
		})
	}
}

func TestDecodeNativeStreamsDoesNotExposeNativePayloadsInBrokerEvents(t *testing.T) {
	result, err := adapter.DecodeStream(broker.AdapterClaude, strings.NewReader(
		`{"type":"system","session_id":"session-1","env":"TOKEN=super-secret"}
{"type":"result","subtype":"success","result":"non-authoritative prose","structured_output":{"answer":"ok"}}
`,
	), 4096)
	if err != nil {
		t.Fatalf("DecodeStream(): %v", err)
	}
	encoded, err := json.Marshal(result.Events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "session-1") {
		t.Fatalf("broker events expose native payload: %s", encoded)
	}
}

func TestDecodeNativeStreamsReturnsNormalizedPartialEventsOnFailure(t *testing.T) {
	result, err := adapter.DecodeStream(broker.AdapterClaude, strings.NewReader(
		`{"type":"system","session_id":"session-1"}
{"type":"result","subtype":"error","error":"provider body must not escape"}
`,
	), 4096)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("DecodeStream() error = %v, want failed native execution", err)
	}
	if got := result.Events; len(got) != 2 || got[0].Kind != broker.EventProgress || got[1].Kind != broker.EventFailed {
		t.Fatalf("partial events = %#v", got)
	}
}

func TestDecodeNativeStreamsFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
		want   string
		limit  int
	}{
		{"malformed", "{not-json}\n", "JSON", 4096},
		{"no result", `{"type":"system","subtype":"init"}` + "\n", "terminal", 4096},
		{"provider failure", `{"type":"result","subtype":"error","error":"denied"}` + "\n", "failed", 4096},
		{"claude missing structured output", `{"type":"result","subtype":"success","result":"{\"answer\":\"not-authoritative\"}"}` + "\n", "structured output", 4096},
		{"claude null structured output", `{"type":"result","subtype":"success","structured_output":null}` + "\n", "structured output", 4096},
		{"oversized", `{"type":"result","subtype":"success","result":"` + strings.Repeat("x", 100) + `"}` + "\n", "limit", 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.DecodeStream(broker.AdapterClaude, strings.NewReader(tc.stream), tc.limit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("DecodeStream() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
