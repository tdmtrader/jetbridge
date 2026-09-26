package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/capture"
)

// ServeTools is a private stdio MCP server for a provider child. It has no
// credentials, remote endpoints, execution operations or Run records; call is
// the whole of what it can do. Tool errors are returned as isError results,
// never as transport faults, and notifications get no reply.
func ServeTools(name string, tools json.RawMessage, call func(tool string, args json.RawMessage) (any, error), in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	initialized := false
	for scanner.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		response := map[string]any{"jsonrpc": "2.0", "id": nil}
		fail := func(code int, message string) { response["error"] = map[string]any{"code": code, "message": message} }
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			fail(-32700, "Parse error")
		} else if req.JSONRPC != "2.0" || req.Method == "" {
			fail(-32600, "Invalid request")
		} else if len(req.ID) == 0 {
			continue
		} else {
			response["id"] = req.ID
			switch req.Method {
			case "initialize":
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(req.Params, &params) != nil {
					fail(-32602, "Invalid parameters")
					break
				}
				version := params.ProtocolVersion
				if version != "2024-11-05" && version != "2025-03-26" && version != "2025-06-18" {
					version = "2025-06-18"
				}
				response["result"] = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": name, "version": "1"}}
				initialized = true
			case "ping":
				response["result"] = map[string]any{}
			case "tools/list":
				if !initialized {
					fail(-32600, "Initialize first")
					break
				}
				response["result"] = map[string]any{"tools": tools}
			case "tools/call":
				if !initialized {
					fail(-32600, "Initialize first")
					break
				}
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				if json.Unmarshal(req.Params, &p) != nil {
					fail(-32602, "Invalid parameters")
					break
				}
				result, err := call(p.Name, p.Arguments)
				text := ""
				if err != nil {
					text = err.Error()
				} else {
					data, e := json.Marshal(result)
					if e != nil {
						return e
					}
					text = string(data)
				}
				response["result"] = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": err != nil}
			default:
				fail(-32601, "Method not found")
			}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// TextFiles is a read-only set of files served through list, read and
// search. Paths returns the sorted names; Read returns one file's bytes or an
// error. A Read error matching Binary is skipped by search and reported by
// read as a limitation.
type TextFiles struct {
	Paths  func() ([]string, error)
	Read   func(name string) ([]byte, error)
	Binary error
}

// TextToolDescriptions word the three tools for one file set.
type TextToolDescriptions struct{ List, Read, Search string }

// TextTools returns the MCP tool declarations for list, read and search.
func TextTools(d TextToolDescriptions) json.RawMessage {
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return json.RawMessage(`[
 {"name":"list","description":` + quote(d.List) + `,"inputSchema":{"type":"object","properties":{"prefix":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":500}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}},
 {"name":"read","description":` + quote(d.Read) + `,"inputSchema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":500}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}},
 {"name":"search","description":` + quote(d.Search) + `,"inputSchema":{"type":"object","required":["text"],"properties":{"text":{"type":"string","minLength":1,"maxLength":256},"prefix":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}}
]`)
}

// Call runs list, read or search. Reads are paged by line and capped at
// 64 KiB of text per call; search returns only locations.
func (f TextFiles) Call(tool string, args json.RawMessage) (any, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	switch tool {
	case "list":
		var p struct {
			Prefix string `json:"prefix"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := capture.DecodeStrict(args, &p); err != nil {
			return nil, errors.New("invalid list arguments")
		}
		if p.Limit == 0 {
			p.Limit = 200
		}
		if p.Offset < 0 || p.Limit < 1 || p.Limit > 500 {
			return nil, errors.New("invalid list page")
		}
		paths, err := f.Paths()
		if err != nil {
			return nil, err
		}
		names := []string{}
		for _, name := range paths {
			if strings.HasPrefix(name, p.Prefix) {
				names = append(names, name)
			}
		}
		start := min(p.Offset, len(names))
		end := min(start+p.Limit, len(names))
		return map[string]any{"paths": names[start:end], "next_offset": end, "total": len(names)}, nil
	case "read":
		var p struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			Limit     int    `json:"limit"`
		}
		if err := capture.DecodeStrict(args, &p); err != nil {
			return nil, errors.New("invalid read arguments")
		}
		if p.StartLine == 0 {
			p.StartLine = 1
		}
		if p.Limit == 0 {
			p.Limit = 200
		}
		if p.StartLine < 1 || p.Limit < 1 || p.Limit > 500 {
			return nil, errors.New("invalid read page")
		}
		data, err := f.Read(p.Path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(data) == 0 {
			lines = nil
		}
		start := min(p.StartLine-1, len(lines))
		end := min(start+p.Limit, len(lines))
		var text strings.Builder
		for i := start; i < end; i++ {
			line := fmt.Sprintf("%d: %s\n", i+1, lines[i])
			if text.Len()+len(line) > 64<<10 {
				if i == start {
					return nil, errors.New("line exceeds text-read limit; report this limitation")
				}
				end = i
				break
			}
			text.WriteString(line)
		}
		return map[string]any{"path": p.Path, "text": text.String(), "next_line": end + 1, "total_lines": len(lines)}, nil
	case "search":
		var p struct {
			Text   string `json:"text"`
			Prefix string `json:"prefix"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := capture.DecodeStrict(args, &p); err != nil {
			return nil, errors.New("invalid search arguments")
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Text == "" || len(p.Text) > 256 || p.Offset < 0 || p.Limit < 1 || p.Limit > 200 {
			return nil, errors.New("invalid search page")
		}
		paths, err := f.Paths()
		if err != nil {
			return nil, err
		}
		matches := []map[string]any{}
		index := 0
		for _, name := range paths {
			if !strings.HasPrefix(name, p.Prefix) {
				continue
			}
			data, err := f.Read(name)
			if f.Binary != nil && errors.Is(err, f.Binary) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for n, line := range strings.Split(string(data), "\n") {
				if !strings.Contains(line, p.Text) {
					continue
				}
				index++
				if index <= p.Offset {
					continue
				}
				if len(matches) == p.Limit {
					return map[string]any{"matches": matches, "next_offset": index - 1, "more": true}, nil
				}
				// Return locations only. read supplies bounded source text.
				matches = append(matches, map[string]any{"path": name, "line": n + 1})
			}
		}
		return map[string]any{"matches": matches, "next_offset": index, "more": false}, nil
	default:
		return nil, errors.New("unknown input tool")
	}
}
