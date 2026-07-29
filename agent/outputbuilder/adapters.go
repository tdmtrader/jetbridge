package outputbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

const (
	DefaultMCPAddress            = "127.0.0.1:7783"
	maxAdapterRequestBytes int64 = 1 << 20
)

// CLI is a transport adapter only. It has no authority flags and always acts
// through the Builder passed by the platform process.
type CLI struct {
	builder        *Builder
	stdin          io.Reader
	stdout, stderr io.Writer
}

func NewCLI(builder *Builder, stdin io.Reader, stdout, stderr io.Writer) *CLI {
	return &CLI{builder: builder, stdin: stdin, stdout: stdout, stderr: stderr}
}
func (cli *CLI) Run(ctx context.Context, args []string) int {
	if cli == nil || cli.builder == nil || ctx == nil {
		return 1
	}
	if len(args) == 0 {
		cli.errorf("a command is required")
		return 2
	}
	switch args[0] {
	case "describe", "validate":
		if len(args) != 3 || args[1] != "--output" {
			cli.errorf("%s requires --output", args[0])
			return 2
		}
		if args[0] == "describe" {
			value, err := cli.builder.DescribeOutput(ctx, args[2])
			if err != nil {
				cli.errorf("describe: %v", err)
				return 1
			}
			return cli.json(value)
		}
		value, err := cli.builder.ValidateOutput(ctx, args[2])
		if err != nil {
			cli.errorf("validate: %v", err)
			return 1
		}
		if exit := cli.json(value); exit != 0 {
			return exit
		}
		if !value.Valid {
			return 1
		}
		return 0
	case "write":
		if len(args) != 1 || cli.stdin == nil {
			cli.errorf("write accepts no flags and requires stdin")
			return 2
		}
		body, err := readBounded(cli.stdin)
		if err != nil {
			cli.errorf("write request: %v", err)
			return 2
		}
		var request WriteRequest
		if err := strictJSON(body, &request); err != nil {
			cli.errorf("write request: %v", err)
			return 2
		}
		value, err := cli.builder.WriteOutput(ctx, request)
		if err != nil {
			cli.errorf("write: %v", err)
			return 1
		}
		if exit := cli.json(value); exit != 0 {
			return exit
		}
		if !value.Valid {
			return 1
		}
		return 0
	default:
		cli.errorf("unknown command %q", args[0])
		return 2
	}
}
func (cli *CLI) json(value any) int {
	if cli.stdout == nil {
		return 1
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 1
	}
	_, err = fmt.Fprintf(cli.stdout, "%s\n", encoded)
	if err != nil {
		return 1
	}
	return 0
}
func (cli *CLI) errorf(format string, args ...any) {
	if cli.stderr != nil {
		fmt.Fprintf(cli.stderr, "agent-output: "+format+"\n", args...)
	}
}

func strictJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func readBounded(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxAdapterRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > maxAdapterRequestBytes {
		return nil, fmt.Errorf("request must be non-empty and at most %d bytes", maxAdapterRequestBytes)
	}
	return data, nil
}

func ValidateMCPListenAddress(address string) error {
	if address != DefaultMCPAddress {
		return fmt.Errorf("output builder MCP listen address must be exactly %q", DefaultMCPAddress)
	}
	return nil
}
func ListenMCP(address string) (net.Listener, error) {
	if err := ValidateMCPListenAddress(address); err != nil {
		return nil, err
	}
	return net.Listen("tcp", address)
}

type mcpServer struct{ builder *Builder }
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type mcpToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type mcpOutput struct {
	Output string `json:"output"`
}

func NewMCPServer(builder *Builder) http.Handler {
	server := &mcpServer{builder: builder}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/mcp", server.serve)
	return mux
}
func (server *mcpServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := readBounded(r.Body)
	if err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: err.Error()}})
		return
	}
	var request mcpRequest
	if err := strictJSON(raw, &request); err != nil || request.JSONRPC != "2.0" {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32600, Message: "invalid request"}})
		return
	}
	switch request.Method {
	case "tools/list":
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": []map[string]any{{"name": "describe_output"}, {"name": "write_output"}, {"name": "validate_output"}}}})
	case "ping":
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}})
	case "tools/call":
		server.call(w, r.Context(), request)
	default:
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32601, Message: "method not found"}})
	}
}

func (server *mcpServer) call(w http.ResponseWriter, ctx context.Context, request mcpRequest) {
	if server.builder == nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32603, Message: "output builder unavailable"}})
		return
	}
	var call mcpToolCall
	if err := strictJSON(request.Params, &call); err != nil || call.Name == "" {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32602, Message: "invalid tool parameters"}})
		return
	}
	var result any
	var err error
	switch call.Name {
	case "describe_output":
		var args mcpOutput
		err = strictJSON(call.Arguments, &args)
		if err == nil {
			result, err = server.builder.DescribeOutput(ctx, args.Output)
		}
	case "validate_output":
		var args mcpOutput
		err = strictJSON(call.Arguments, &args)
		if err == nil {
			result, err = server.builder.ValidateOutput(ctx, args.Output)
		}
	case "write_output":
		var args WriteRequest
		err = strictJSON(call.Arguments, &args)
		if err == nil {
			result, err = server.builder.WriteOutput(ctx, args)
		}
	default:
		err = fmt.Errorf("unknown tool %q", call.Name)
	}
	if err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32602, Message: err.Error()}})
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32603, Message: "encode tool result"}})
		return
	}
	writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"content": []map[string]string{{"type": "text", "text": string(encoded)}}}})
}
func writeMCP(w http.ResponseWriter, value mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
