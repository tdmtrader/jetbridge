// mcp-schema-probe is an isolated compatibility fixture, never a production adapter.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/api/mcpserver"
	product "github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/internal/mcpclient"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// Public fixture labels: these have no authority outside this loopback process.
const grantPrefix = "fixture-only-"

type recorder struct {
	mu sync.Mutex
	f  *os.File
}

func (r *recorder) record(v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := json.NewEncoder(r.f).Encode(v); err != nil {
		log.Fatal(err)
	}
}

func operations(mode string) []product.Operation {
	var selected []product.Operation
	for _, op := range product.Operations() {
		if op.ID == "pipelines_list" || mode != "pruned" && op.ID == "pipeline_get" || mode == "mixed" && op.ID == "pipeline_pause" {
			selected = append(selected, op)
		}
	}
	if len(selected) == 0 {
		panic("production operation inventory is empty")
	}
	return selected
}

func schema(mode string) *jsonschema.Schema {
	return product.GroupInputSchema(operations(mode))
}

type input struct {
	Request struct {
		Operation string         `json:"operation"`
		Arguments map[string]any `json:"arguments"`
	} `json:"request"`
}

type output struct {
	Operation string `json:"operation"`
	Result    any    `json:"result"`
}

func server(mode string, rec *recorder) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "jetbridge-schema-probe", Version: "1"}, &sdk.ServerOptions{
		Instructions: "Synthetic JetBridge schema fixture. pipeline has exact request.operation/request.arguments branches. All returned facts are fixture data. No production access exists.",
	})
	closed, destructive := false, mode == "mixed"
	description := "Synthetic fixture pipeline operations: pipelines_list and pipeline_get. Exact instance_vars={} selects the non-instanced pipeline."
	if mode == "pruned" {
		description = "Fixture pipelines_list only. pipeline_get is not available to this synthetic grant."
	}
	if mode == "mixed" {
		description += " Also pipeline_pause, a synthetic mutation; the entire group is conservatively annotated as write capable."
	}
	sdk.AddTool(s, &sdk.Tool{Name: "pipeline", Description: description, InputSchema: schema(mode), OutputSchema: product.GroupOutputSchema(operations(mode)),
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: mode != "mixed", DestructiveHint: &destructive, IdempotentHint: mode != "mixed", OpenWorldHint: &closed},
	}, func(_ context.Context, _ *sdk.CallToolRequest, in input) (*sdk.CallToolResult, output, error) {
		rec.record(map[string]any{"event": "dispatch", "mode": mode, "arguments": in})
		pipeline := map[string]any{"id": 1, "team": "main", "pipeline": "payments", "instance_vars": map[string]any{}, "paused": false, "archived": false, "public": false}
		var result any = map[string]any{"items": []any{pipeline}, "next_cursor": nil}
		if in.Request.Operation == "pipeline_get" {
			result = pipeline
		}
		if in.Request.Operation == "pipeline_pause" {
			result = map[string]any{"accepted": true}
		}
		return nil, output{Operation: in.Request.Operation, Result: result}, nil
	})
	return s
}

func fixture(listener net.Listener, rec *recorder) http.Handler {
	base := "http://" + listener.Addr().String()
	servers := map[string]*sdk.Server{}
	for _, mode := range []string{"read", "pruned", "mixed"} {
		servers[mode] = server(mode, rec)
	}
	transport := mcpserver.NewHTTPHandler(func(r *http.Request) *sdk.Server {
		return servers[r.Header.Get("X-Fixture-Mode")]
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base + "/oauth"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/oauth", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": base + "/oauth", "authorization_endpoint": base + "/oauth/authorize", "token_endpoint": base + "/oauth/token", "revocation_endpoint": base + "/oauth/revoke", "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		mode := ""
		for candidate := range servers {
			if r.Header.Get("Authorization") == "Bearer "+grantPrefix+candidate {
				mode = candidate
			}
		}
		if mode == "" {
			rec.record(map[string]any{"event": "unauthenticated", "method": r.Method})
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "synthetic grant required", http.StatusUnauthorized)
			return
		}
		event := map[string]any{"event": "authenticated_http", "mode": mode, "method": r.Method, "protocol": r.Header.Get("MCP-Protocol-Version")}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024+1))
			if err != nil || len(body) > 1024*1024 {
				http.Error(w, "invalid fixture request", 400)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var message struct {
				Method string `json:"method"`
				Params struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &message) == nil {
				event["rpc_method"] = message.Method
				if message.Method == "initialize" {
					event["requested_protocol"] = message.Params.ProtocolVersion
				}
			}
		}
		rec.record(event)
		r.Header.Set("X-Fixture-Mode", mode)
		transport.ServeHTTP(w, r)
	})
	return mux
}

func check(endpoint, dest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var evidence []any
	for _, mode := range []string{"read", "pruned", "mixed"} {
		state, err := os.MkdirTemp("", "jb-schema-reference-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(state)
		client, err := mcpclient.New(mcpclient.Config{Endpoint: endpoint, ClientID: "fixture-reference", StatePath: filepath.Join(state, "grant.json")})
		if err != nil {
			return err
		}
		// SaveToken/Connect exercise the actual reference client using a new synthetic
		// fixture grant. No existing credential file is opened or copied.
		err = client.SaveToken(ctx, &oauth2.Token{AccessToken: grantPrefix + mode, RefreshToken: "fixture-unused-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			return err
		}
		session, err := client.Connect(ctx)
		if err != nil {
			return err
		}
		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			return err
		}
		if len(listed.Tools) != 1 || listed.Tools[0].Name != "pipeline" {
			return errors.New("unexpected tool list")
		}
		// Compare JSON-normalized schemas: the SDK client must receive every
		// const, required field and closed object exactly as the fixture declared.
		actual, _ := json.Marshal(listed.Tools[0].InputSchema)
		wanted, _ := json.Marshal(schema(mode))
		var actualJSON, wantedJSON any
		if json.Unmarshal(actual, &actualJSON) != nil || json.Unmarshal(wanted, &wantedJSON) != nil || !reflect.DeepEqual(actualJSON, wantedJSON) {
			return errors.New("listed schema changed across HTTP transport")
		}
		entry := map[string]any{"mode": mode, "list": listed}
		var calls []any
		for _, test := range []struct {
			op        string
			args      map[string]any
			wantError bool
		}{
			{"pipelines_list", map[string]any{"team": "main"}, false},
			{"pipeline_get", map[string]any{"team": "main", "pipeline": "payments", "instance_vars": map[string]any{}}, mode == "pruned"},
			{"pipeline_get", map[string]any{"team": "main", "instance_vars": map[string]any{}}, true},
			{"pipelines_list", map[string]any{"team": "main", "pipeline": "payments"}, true},
		} {
			args := map[string]any{"request": map[string]any{"operation": test.op, "arguments": test.args}}
			result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "pipeline", Arguments: args})
			if err != nil {
				return err
			}
			if result.IsError != test.wantError {
				return fmt.Errorf("%s/%s: expected error=%v", mode, test.op, test.wantError)
			}
			calls = append(calls, map[string]any{"arguments": args, "expected_error": test.wantError, "result": result})
		}
		entry["calls"] = calls
		evidence = append(evidence, entry)
		if err := session.Close(); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(map[string]any{"client": "internal/mcpclient.Client.Connect", "sdk": "v1.6.1", "profile": "2025-11-25", "checks": evidence}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dest, append(b, '\n'), 0644)
}

func main() {
	mode := flag.String("mode", "serve", "serve or reference-check")
	listen := flag.String("listen", "127.0.0.1:0", "loopback fixture listener")
	endpoint := flag.String("endpoint", "", "fixture MCP URL for reference-check")
	out := flag.String("out", "", "evidence JSON file")
	flag.Parse()
	if *mode == "reference-check" {
		if err := check(*endpoint, *out); err != nil {
			log.Fatal(err)
		}
		fmt.Println("reference schema checks passed")
		return
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("fixture must bind numeric loopback")
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	fmt.Println("http://" + l.Addr().String() + "/mcp")
	if err := http.Serve(l, fixture(l, &recorder{f: f})); err != nil {
		log.Fatal(err)
	}
}
