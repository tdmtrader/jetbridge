package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/concourse/concourse/atc"
	"math/big"
	"unicode/utf8"

	"github.com/concourse/concourse/skymarshal/mcpauth"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMessageBytes = 1024 * 1024

type catalog struct {
	operations []Operation
	principal  mcpauth.Principal
	disabled   map[string]bool
	access     func(context.Context, string) (AccountEligibility, error)
}

type operationRequest struct {
	Request struct {
		Operation string          `json:"operation"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"request"`
}

// Validate the full declared branch, not the consent-pruned schema. This lets a
// well-formed missing-scope call receive an OAuth challenge without mistaking
// malformed/unknown input for a request for broader authority. No targets read.
func decodeOperation(resource string, input json.RawMessage, ops []Operation) (Operation, json.RawMessage, error) {
	var envelope operationRequest
	if json.Unmarshal(input, &envelope) != nil {
		return Operation{}, nil, errors.New("INVALID_ARGUMENTS: expected a precise request object")
	}
	for _, op := range ops {
		if op.ID != envelope.Request.Operation {
			continue
		}
		if op.Resource != resource {
			return Operation{}, nil, errors.New("INVALID_ARGUMENTS: operation belongs to a different resource")
		}
		if op.Arguments == nil {
			return Operation{}, nil, errors.New("NOT_IMPLEMENTED: no MCP adapter for this operation")
		}
		if err := validateJSON(GroupInputSchema([]Operation{op}), input); err != nil {
			return Operation{}, nil, errors.New("INVALID_ARGUMENTS: arguments do not match the operation schema")
		}
		var args map[string]any
		_ = json.Unmarshal(envelope.Request.Arguments, &args)
		if v, ok := args["version"].(string); ok {
			if _, err := atc.ParseConfigVersion(v); err != nil {
				return Operation{}, nil, errors.New("INVALID_ARGUMENTS: version exceeds 2147483647")
			}
		}
		if v, ok := args["config_yaml"].(string); ok && (len(v) > 128*1024 || !utf8.ValidString(v)) {
			return Operation{}, nil, errors.New("INVALID_ARGUMENTS: config_yaml exceeds 131072 UTF-8 bytes")
		}
		for _, key := range []string{"team", "pipeline", "job"} {
			if v, ok := args[key].(string); ok && !validName(v) {
				return Operation{}, nil, fmt.Errorf("INVALID_ARGUMENTS: invalid %s name", key)
			}
		}
		// JSON Schema integers include 1.0 and 1e3. Normalize these before
		// typed adapters decode; reject values outside the Go/API integer range.
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(envelope.Request.Arguments, &raw)
		for _, key := range []string{"build_id", "limit", "max_bytes"} {
			if value, ok := raw[key]; ok {
				n, valid := new(big.Rat).SetString(string(value))
				if !valid || !n.IsInt() || !n.Num().IsInt64() {
					return Operation{}, nil, errors.New("INVALID_ARGUMENTS: integer outside API range")
				}
				raw[key], _ = json.Marshal(n.Num().Int64())
			}
		}
		normalized, _ := json.Marshal(raw)
		return op, normalized, nil
	}
	return Operation{}, nil, errors.New("UNKNOWN_OPERATION: use capabilities_explain to inspect MCP support")
}

func validateJSON(schema *jsonschema.Schema, data []byte) error {
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	var value any
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8")
	}
	if err = json.Unmarshal(data, &value); err != nil {
		return err
	}
	return resolved.Validate(value)
}

func (c catalog) eligible(ctx context.Context) (map[string][]Operation, error) {
	groups := map[string][]Operation{}
	for _, op := range c.operations {
		if !op.Implemented() || c.disabled[op.ID] || !c.principal.HasScope(op.Scope) {
			continue
		}
		access, err := c.access(ctx, op.Action)
		if err != nil {
			return nil, err
		}
		if access != AccountIneligible {
			groups[op.Resource] = append(groups[op.Resource], op)
		}
	}
	return groups, nil
}

func (c catalog) dispatch(ctx context.Context, resource string, input json.RawMessage) (*sdk.CallToolResult, error) {
	op, args, err := decodeOperation(resource, input, c.operations)
	if err != nil {
		return toolError(err.Error()), nil
	}
	if !op.Implemented() {
		return toolError("NOT_IMPLEMENTED: no MCP adapter for this operation"), nil
	}
	if c.disabled[op.ID] {
		return toolError("DISABLED: this MCP operation is disabled by the operator"), nil
	}
	if !c.principal.HasScope(op.Scope) {
		return toolError("INSUFFICIENT_SCOPE: new consent is required for " + op.Scope), nil
	}
	access, err := c.access(ctx, op.Action)
	if err != nil {
		return toolError("TEMPORARY_ACCESS_FAILURE: retry discovery later"), nil
	}
	if access == AccountIneligible {
		return toolError("ACCOUNT_INELIGIBLE: contact an administrator; consent alone cannot grant account authority"), nil
	}
	value, err := op.handler(ctx, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	result := map[string]any{"operation": op.ID, "result": value}
	data, err := json.Marshal(result)
	if err != nil || validateJSON(GroupOutputSchema([]Operation{op}), data) != nil {
		return toolError("INVALID_RESULT: operation returned an invalid result; do not retry a mutation automatically"), nil
	}
	return toolSuccess(result), nil
}

func toolError(message string) *sdk.CallToolResult {
	if len(message) > 4096 {
		message = message[:4096]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += " [truncated]"
	}
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: message}}}
}
func toolSuccess(value any) *sdk.CallToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return toolError("INVALID_RESULT: response encoding failed")
	}
	result := &sdk.CallToolResult{StructuredContent: value, Content: []sdk.Content{&sdk.TextContent{Text: string(data)}}}
	encoded, err := json.Marshal(result)
	// Reserve room for the JSON-RPC envelope/id. Both structured and text copies
	// count; checking only the upstream API body would underestimate this cost.
	if err != nil || len(encoded) > maxMessageBytes-4096 {
		return toolError("RESULT_TOO_LARGE: request a smaller page; do not retry a mutation automatically")
	}
	return result
}

type explainArgs struct {
	Resource  string `json:"resource"`
	Operation string `json:"operation,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}
type capability struct {
	Operation string `json:"operation"`
	// The field carries the operation's description, so it says so: a client
	// rendering this as a short label would have rendered a paragraph.
	Description             string   `json:"description"`
	MCPSupport              string   `json:"mcp_support"`
	ExecutableBranchPresent bool     `json:"executable_branch_present"`
	MissingScopes           []string `json:"missing_scopes"`
	AccountAccess           string   `json:"account_access"`
	NextStep                string   `json:"next_step"`
	Explanation             string   `json:"explanation"`
}
type explanationPage struct {
	Items      []capability `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

func explainSchema() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"resource":  {Type: "string", Enum: []any{"pipeline", "build", "job", "resource", "container", "team", "server", "worker"}},
		"operation": nameSchema(), "cursor": cursorSchema(),
	}, "resource")
}

type explanationCursor struct {
	Resource, Operation string
	Offset              int
}

func (c catalog) explain(ctx context.Context, args explainArgs) (explanationPage, error) {
	page := explanationPage{Items: []capability{}}
	offset := 0
	if args.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(args.Cursor)
		var cursor explanationCursor
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Resource != args.Resource || cursor.Operation != args.Operation || cursor.Offset < 0 {
			return page, errors.New("INVALID_CURSOR: cursor does not match this capability query")
		}
		offset = cursor.Offset
	}
	var matches []Operation
	for _, op := range c.operations {
		if args.Operation != "" && op.ID == args.Operation && op.Resource != args.Resource {
			return page, errors.New("INVALID_ARGUMENTS: operation belongs to a different resource")
		}
		if op.Resource == args.Resource && (args.Operation == "" || op.ID == args.Operation) {
			matches = append(matches, op)
		}
	}
	if args.Operation != "" && len(matches) == 0 {
		if offset != 0 {
			return page, errors.New("INVALID_CURSOR")
		}
		page.Items = append(page.Items, capability{Operation: args.Operation, Description: "Unknown capability", MCPSupport: "unknown", MissingScopes: []string{}, AccountAccess: "not_evaluated", NextStep: "clarify", Explanation: "This ID is not in this deployment's capability metadata. Clarify the requested action; this does not establish product impossibility."})
		return page, nil
	}
	if offset > len(matches) {
		return page, errors.New("INVALID_CURSOR")
	}
	for i := offset; i < len(matches); i++ {
		if len(page.Items) == 20 {
			data, _ := json.Marshal(explanationCursor{args.Resource, args.Operation, i})
			cursor := base64.RawURLEncoding.EncodeToString(data)
			page.NextCursor = &cursor
			break
		}
		op := matches[i]
		e := capability{Operation: op.ID, Description: op.Description, MCPSupport: "not_implemented", MissingScopes: []string{}, AccountAccess: "not_evaluated", NextStep: "stop", Explanation: "No MCP adapter is installed. More consent cannot enable this capability. A separate CLI/API action may exist."}
		if op.Implemented() {
			if c.disabled[op.ID] {
				e.MCPSupport = "disabled"
				e.NextStep = "contact_operator"
				e.Explanation = "The operator disabled this MCP adapter. More consent does not enable it."
			} else {
				// missing_scopes is only listed where consent is the thing that
				// would change the answer. On an entry whose explanation says
				// consent cannot help, a scope list invites the client to open a
				// consent flow that resolves nothing.
				if !c.principal.HasScope(op.Scope) {
					e.MissingScopes = append(e.MissingScopes, op.Scope)
				}
				access, err := c.access(ctx, op.Action)
				if err != nil {
					return explanationPage{}, errors.New("TEMPORARY_ACCESS_FAILURE: capability access is temporarily unavailable")
				}
				e.MCPSupport = "implemented"
				e.AccountAccess = string(access)
				switch {
				case access == AccountIneligible:
					e.NextStep = "contact_admin"
					e.Explanation = "The account has no applicable authority. Additional consent alone cannot grant a role; any listed consent is also required."
				case len(e.MissingScopes) > 0:
					e.NextStep = "request_new_consent"
					e.Explanation = "Ask the user to authorize only the listed categories in a new consent flow. Refresh cannot broaden a grant. Target, role and policy checks still apply."
				default:
					e.ExecutableBranchPresent = true
					e.NextStep = "use_available_tool"
					e.Explanation = "Refresh a stale client catalog if necessary. Every target is still checked at execution; client approval policy may also require user approval."
				}
			}
		}
		page.Items = append(page.Items, e)
	}
	return page, nil
}
