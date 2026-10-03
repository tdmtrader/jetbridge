package session_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/session"
)

var errBinary = errors.New("binary file")

func textFiles(files map[string]string) session.TextFiles {
	return session.TextFiles{
		Paths: func() ([]string, error) {
			names := []string{}
			for _, name := range []string{"a.txt", "blob.bin", "dir/b.txt", "long.txt", "wide.txt"} {
				if _, ok := files[name]; ok {
					names = append(names, name)
				}
			}
			return names, nil
		},
		Read: func(name string) ([]byte, error) {
			if name == "blob.bin" {
				return nil, errBinary
			}
			if data, ok := files[name]; ok {
				return []byte(data), nil
			}
			return nil, errors.New("no such file")
		},
		Binary: errBinary,
	}
}

// serve runs one ServeTools conversation: each request is one line in, and
// the replies are decoded in order.
func serve(t *testing.T, requests ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	tools := session.TextTools(session.TextToolDescriptions{List: "l", Read: "r", Search: "s"})
	call := func(tool string, args json.RawMessage) (any, error) {
		if tool == "fail" {
			return nil, errors.New("tool failed")
		}
		return textFiles(map[string]string{"a.txt": "alpha\n"}).Call(tool, args)
	}
	if err := session.ServeTools("fixture", tools, call, strings.NewReader(strings.Join(requests, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		var reply map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, reply)
	}
	return replies
}

func errorCode(reply map[string]any) float64 {
	e, _ := reply["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

// The tool server speaks just enough JSON-RPC for an MCP client: it answers
// every request, never a notification, refuses tools before initialize, and
// reports a tool's failure as a result, never as a transport fault.
func TestServeToolsSpeaksMCP(t *testing.T) {
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
	replies := serve(t,
		`{"jsonrpc":"2.0","id":0,"method":"tools/list"}`,
		`not json`,
		`{"jsonrpc":"1.0","id":9,"method":"ping"}`,
		init,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"path":"a.txt"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":"bad"}`,
	)
	if len(replies) != 11 {
		t.Fatalf("%d replies to 11 requests and one notification: %v", len(replies), replies)
	}
	for i, want := range []float64{-32600, -32700, -32600} {
		if errorCode(replies[i]) != want {
			t.Errorf("reply %d: %v, want error %v", i, replies[i], want)
		}
	}
	if result := replies[3]["result"].(map[string]any); result["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize did not keep a supported version: %v", result)
	}
	if tools := replies[5]["result"].(map[string]any)["tools"].([]any); len(tools) != 3 {
		t.Errorf("tools/list: %v", tools)
	}
	text := func(reply map[string]any) (string, bool) {
		result := reply["result"].(map[string]any)
		return result["content"].([]any)[0].(map[string]any)["text"].(string), result["isError"].(bool)
	}
	if got, isError := text(replies[6]); isError || !strings.Contains(got, "1: alpha") {
		t.Errorf("read: %s %v", got, isError)
	}
	if got, isError := text(replies[7]); !isError || got != "tool failed" {
		t.Errorf("a failed tool: %s %v", got, isError)
	}
	if errorCode(replies[8]) != -32601 {
		t.Errorf("unknown method: %v", replies[8])
	}
	if result := replies[9]["result"].(map[string]any); result["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize did not fall back to the current version: %v", result)
	}
	if errorCode(replies[10]) != -32602 {
		t.Errorf("malformed call: %v", replies[10])
	}
}

func call(t *testing.T, files session.TextFiles, tool, args string) (map[string]any, error) {
	t.Helper()
	result, err := files.Call(tool, json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(result)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m, nil
}

// list, read and search page through a file set and never return more than
// their bounds: read is capped at 64 KiB of text, search returns locations.
func TestTextFilesPageWithinBounds(t *testing.T) {
	var long strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	files := textFiles(map[string]string{"a.txt": "alpha\nneedle\n", "blob.bin": "", "dir/b.txt": "needle\nneedle\n", "long.txt": long.String(), "wide.txt": strings.Repeat("w", 70<<10) + "\n"})

	listed, _ := call(t, files, "list", `{"prefix":"dir/"}`)
	if fmt.Sprint(listed["paths"]) != "[dir/b.txt]" || listed["total"] != 1.0 {
		t.Errorf("list by prefix: %v", listed)
	}
	paged, _ := call(t, files, "list", `{"offset":1,"limit":2}`)
	if fmt.Sprint(paged["paths"]) != "[blob.bin dir/b.txt]" || paged["next_offset"] != 3.0 {
		t.Errorf("list page: %v", paged)
	}
	if all, _ := call(t, files, "list", ""); all["total"] != 5.0 {
		t.Errorf("list without arguments: %v", all)
	}

	read, _ := call(t, files, "read", `{"path":"long.txt"}`)
	if read["next_line"] != 201.0 || read["total_lines"] != 250.0 || !strings.HasSuffix(read["text"].(string), "200: line 200\n") {
		t.Errorf("default read page: next %v of %v", read["next_line"], read["total_lines"])
	}
	tail, _ := call(t, files, "read", `{"path":"long.txt","start_line":249,"limit":5}`)
	if tail["text"] != "249: line 249\n250: line 250\n" {
		t.Errorf("read tail: %q", tail["text"])
	}
	if _, err := call(t, files, "read", `{"path":"wide.txt"}`); err == nil || !strings.Contains(err.Error(), "text-read limit") {
		t.Errorf("a line beyond the read limit: %v", err)
	}

	found, _ := call(t, files, "search", `{"text":"needle"}`)
	if fmt.Sprint(found["matches"]) != "[map[line:2 path:a.txt] map[line:1 path:dir/b.txt] map[line:2 path:dir/b.txt]]" || found["more"] != false {
		t.Errorf("search skips binaries and returns locations: %v", found)
	}
	more, _ := call(t, files, "search", `{"text":"needle","offset":1,"limit":1}`)
	if fmt.Sprint(more["matches"]) != "[map[line:1 path:dir/b.txt]]" || more["more"] != true || more["next_offset"] != 2.0 {
		t.Errorf("search page: %v", more)
	}

	for name, tc := range map[string][2]string{
		"unknown argument": {"list", `{"recursive":true}`},
		"list limit":       {"list", `{"limit":501}`},
		"negative offset":  {"list", `{"offset":-1}`},
		"read no page":     {"read", `{"path":"a.txt","start_line":-1}`},
		"read argument":    {"read", `{"file":"a.txt"}`},
		"read missing":     {"read", `{"path":"missing.txt"}`},
		"empty search":     {"search", `{"text":""}`},
		"search argument":  {"search", `{"pattern":"x"}`},
		"search limit":     {"search", `{"text":"x","limit":201}`},
		"unknown tool":     {"write", `{}`},
	} {
		if _, err := call(t, files, tc[0], tc[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
