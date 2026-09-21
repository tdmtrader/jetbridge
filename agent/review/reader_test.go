package review

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestInputToolsReadOnlyScope(t *testing.T) {
	b := captureTest(t)
	reader, err := NewInputReader(b)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, name := range []string{"head/parser.go", "base/deleted.txt", "manifest.json", "change.diff"} {
		result, err := reader.Call("read", json.RawMessage(`{"path":"`+name+`","start_line":1,"limit":10}`))
		if err != nil || result == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../auth.json", "/etc/passwd", "head/../../auth.json", "auth.json", "head/not-there"} {
		if _, err := reader.Call("read", json.RawMessage(`{"path":"`+name+`"}`)); err == nil {
			t.Fatal("allowed " + name)
		}
	}
	if _, err := reader.Call("exec", json.RawMessage(`{"command":"touch marker"}`)); err == nil {
		t.Fatal("allowed command execution")
	}
	if _, err := reader.Call("read", json.RawMessage(`{"path":"head/parser.go","limit":-1}`)); err == nil {
		t.Fatal("accepted bad paging")
	}
	result, err := reader.Call("search", json.RawMessage(`{"text":"s[1]"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "head/parser.go") {
		t.Fatal("missing match")
	}
}

func TestInputMCPFraming(t *testing.T) {
	b := captureTest(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":"head/parser.go"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"path":"../auth.json"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := ServeInputTools(b, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("notification produced a reply or lost a request: %s", output.String())
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("stdout contains non-JSON")
		}
	}
	if !strings.Contains(lines[2], "s[1]") || !strings.Contains(lines[3], `"isError":true`) {
		t.Fatal(output.String())
	}
}
