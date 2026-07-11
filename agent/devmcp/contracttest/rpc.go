// Package contracttest is the dev-mcp contract-test kit
// (00-shared-contracts.md §3.1): any repo's dev-mcp implementation runs
// Run/RunWithOptions against its live endpoint in its own CI.
package contracttest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// rawCall POSTs a single JSON-RPC request with Accept: application/json
// (the buffered, non-SSE path) and decodes the response envelope.
func rawCall(ctx context.Context, endpoint, method string, params any) (rpcEnvelope, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return rpcEnvelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return rpcEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return rpcEnvelope{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rpcEnvelope{}, fmt.Errorf("%s: unexpected HTTP status %d", method, resp.StatusCode)
	}
	var env rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return rpcEnvelope{}, fmt.Errorf("decode %s response: %w", method, err)
	}
	return env, nil
}
