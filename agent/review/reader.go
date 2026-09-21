package review

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// InputReader exposes only the sealed bundle inventory. It never opens a path
// outside that inventory, even if the model knows the runtime-home location.
type InputReader struct {
	root  *os.Root
	files map[string]string
	paths []string
}

var errBinaryInput = errors.New("binary input: inspect its manifest metadata and report this limitation")

func NewInputReader(b *Bundle) (*InputReader, error) {
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return nil, err
	}
	r := &InputReader{root: root, files: map[string]string{"change.diff": b.Manifest.DiffDigest}}
	for _, f := range b.Manifest.Files {
		r.files[f.Side+"/"+f.Path] = f.Digest
	}
	if b.Manifest.PlanDigest != nil {
		r.files["plan.md"] = *b.Manifest.PlanDigest
	}
	m, err := readRootFile(root, "manifest.json", maxFileBytes)
	if err != nil {
		root.Close()
		return nil, err
	}
	r.files["manifest.json"] = digest(m)
	for p := range r.files {
		r.paths = append(r.paths, p)
	}
	sort.Strings(r.paths)
	return r, nil
}
func (r *InputReader) Close() error { return r.root.Close() }

func (r *InputReader) read(name string) ([]byte, error) {
	want, ok := r.files[name]
	if !ok || !safePath(name) {
		return nil, errors.New("path is outside the review input")
	}
	limit := int64(maxFileBytes)
	if name == "change.diff" {
		limit = maxBundleBytes
	}
	b, err := readRootFile(r.root, name, limit)
	if err != nil {
		return nil, errors.New("cannot read input file")
	}
	if digest(b) != want {
		return nil, errors.New("input changed after capture")
	}
	if !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return nil, errBinaryInput
	}
	return b, nil
}

func (r *InputReader) Call(tool string, args json.RawMessage) (any, error) {
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
		if err := decodeStrict(args, &p); err != nil {
			return nil, errors.New("invalid list arguments")
		}
		if p.Limit == 0 {
			p.Limit = 200
		}
		if p.Offset < 0 || p.Limit < 1 || p.Limit > 500 {
			return nil, errors.New("invalid list page")
		}
		names := []string{}
		for _, name := range r.paths {
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
		if err := decodeStrict(args, &p); err != nil {
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
		data, err := r.read(p.Path)
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
		if err := decodeStrict(args, &p); err != nil {
			return nil, errors.New("invalid search arguments")
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Text == "" || len(p.Text) > 256 || p.Offset < 0 || p.Limit < 1 || p.Limit > 200 {
			return nil, errors.New("invalid search page")
		}
		matches := []map[string]any{}
		index := 0
		for _, name := range r.paths {
			if !strings.HasPrefix(name, p.Prefix) {
				continue
			}
			data, err := r.read(name)
			if errors.Is(err, errBinaryInput) {
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

var inputTools = json.RawMessage(`[
 {"name":"list","description":"List captured input paths, including manifest.json, change.diff, optional plan.md, and base/head trees.","inputSchema":{"type":"object","properties":{"prefix":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":500}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}},
 {"name":"read","description":"Read numbered UTF-8 lines from a captured file. Symlink targets are inert text; binary files return a limitation. Use next_line to continue.","inputSchema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"start_line":{"type":"integer","minimum":1},"limit":{"type":"integer","minimum":1,"maximum":500}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}},
 {"name":"search","description":"Find literal text in captured files; returns file/line locations. Binary files are skipped. Use prefix to narrow and next_offset when more is true.","inputSchema":{"type":"object","required":["text"],"properties":{"text":{"type":"string","minLength":1,"maxLength":256},"prefix":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200}},"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":false}}
]`)

// ServeInputTools is a private stdio MCP server for the worker's Codex child.
// It has no credentials, remote endpoints, execution operations or Run records.
// It is distinct from the future public submit/status/result MCP surface.
func ServeInputTools(b *Bundle, in io.Reader, out io.Writer) error {
	r, err := NewInputReader(b)
	if err != nil {
		return err
	}
	defer r.Close()
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
				response["result"] = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "jetbridge-review-input", "version": "1"}}
				initialized = true
			case "ping":
				response["result"] = map[string]any{}
			case "tools/list":
				if !initialized {
					fail(-32600, "Initialize first")
					break
				}
				response["result"] = map[string]any{"tools": inputTools}
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
				result, err := r.Call(p.Name, p.Arguments)
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
