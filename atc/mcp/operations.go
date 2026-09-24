package mcp

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	"github.com/google/jsonschema-go/jsonschema"
)

// Operation describes one application action. Action is the canonical API
// authority, even when a future adapter uses an additive HTTP route. Discovery
// does not replace API role, policy or target checks.
type Operation struct {
	ID, Resource, Action, Scope, Description string
	Arguments, Result                        *jsonschema.Schema
	Aliases                                  []string
	ReadOnly, Idempotent                     bool
	handler                                  mcpserver.ToolHandler
}

// Implemented reports an actual executable MCP binding, never the existence of
// an API route, schema or consent label. The shipped compatibility alias is
// registered separately until the grouped adapters are installed.
func (o Operation) Implemented() bool { return o.handler != nil }

// Operations returns owned definitions: callers may prune/build schemas without
// changing another principal's catalog. Nil argument/result schemas are metadata
// for follow-on capabilities, not generic executable argument bags.
func Operations() []Operation {
	read := func(id, resource, action, description string, args, result *jsonschema.Schema) Operation {
		return Operation{ID: id, Resource: resource, Action: action, Scope: mcpauth.ScopeRead,
			Description: description, Arguments: args, Result: result, ReadOnly: true, Idempotent: true}
	}
	write := func(id, resource, action, scope, description string, args, result *jsonschema.Schema, idempotent bool) Operation {
		return Operation{ID: id, Resource: resource, Action: action, Scope: scope,
			Description: description, Arguments: args, Result: result, Idempotent: idempotent}
	}
	metadata := func(id, resource, action, scope, description string) Operation {
		return Operation{ID: id, Resource: resource, Action: action, Scope: scope, Description: description}
	}
	get := read("pipeline_get", "pipeline", atc.GetPipeline, "Read the exact pipeline instance and scheduling state.", pipelineArgs(nil), pipelineResult())
	get.Aliases = []string{"pipeline_status"}
	return []Operation{
		read("pipelines_list", "pipeline", atc.ListAllPipelines, "Find visible pipelines by optional team and name text. Filters precede live keyset pagination; default 20, maximum 100.",
			object(map[string]*jsonschema.Schema{"team": nameSchema(), "query": textSchema(256), "limit": integerSchema(1, 100), "cursor": cursorSchema()}), pageResult(pipelineResult())),
		get,
		read("pipeline_config_get", "pipeline", atc.GetConfig, "Read pipeline YAML and its version. Preserves credential references; requires team access even for public pipelines.",
			pipelineArgs(nil), object(map[string]*jsonschema.Schema{"config_yaml": configSchema(), "version": versionSchema()}, "config_yaml", "version")),
		write("pipeline_config_set", "pipeline", atc.SaveConfig, mcpauth.ScopePipelines, "Apply supplied YAML with an atomic version precondition. Version 0 creates only; a positive version updates only that version. Never retry a conflict automatically. No implicit read or credential expansion.",
			pipelineArgs(map[string]*jsonschema.Schema{"config_yaml": configSchema(), "version": versionSchema()}, "config_yaml", "version"),
			object(map[string]*jsonschema.Schema{"version": versionSchema(), "created": {Type: "boolean"}, "warnings": {Type: "array", Items: textSchema(configWarningMaxChars)}}, "version", "created", "warnings"), false),
		write("pipeline_pause", "pipeline", atc.PausePipeline, mcpauth.ScopePipelines, "Pause scheduling for the exact pipeline instance. Authorized independently of unpause.", pipelineArgs(nil), acceptedResult(), true),
		write("pipeline_unpause", "pipeline", atc.UnpausePipeline, mcpauth.ScopePipelines, "Resume scheduling for the exact pipeline instance. Authorized independently of pause.", pipelineArgs(nil), acceptedResult(), true),
		read("builds_list", "build", atc.ListPipelineBuilds, "List builds in an exact pipeline, newest build ID first. Optional job/status filters precede pagination. Live view; default 20, maximum 100.",
			pipelineArgs(map[string]*jsonschema.Schema{"job": nameSchema(), "status": buildStatusSchema(), "limit": integerSchema(1, 100), "cursor": cursorSchema()}), pageResult(buildResult())),
		read("build_get", "build", atc.GetBuild, "Read metadata for an exact numeric build ID. Output/log access is checked separately.", buildArgs(nil), buildResult()),
		read("build_logs_read", "build", atc.BuildEvents, "Read a resumable page of build events and output. Default 32768, maximum 65536 decoded UTF-8 log bytes; idle requests return within two seconds. Each page checks private-job output access. Stored events are limited to 8 MiB of JSON; indivisible metadata to 64 KiB. Completion is snapshot-scoped; keep the cursor to revalidate. STREAM_CHANGED means restart and replace accumulated output.",
			buildArgs(map[string]*jsonschema.Schema{"cursor": cursorSchema(), "max_bytes": integerSchema(1, 65536)}), logResult()),
		write("build_abort", "build", atc.AbortBuild, mcpauth.ScopeBuilds, "Request abort of an exact build. Acceptance does not mean termination has completed.", buildArgs(nil), acceptedResult(), true),
		write("job_trigger", "job", atc.CreateJobBuild, mcpauth.ScopeBuilds, "Trigger one already configured job. A lost response may follow creation: never retry automatically. Does not upload configuration or read output.",
			pipelineArgs(map[string]*jsonschema.Schema{"job": nameSchema()}, "job"), object(map[string]*jsonschema.Schema{"build_id": integerSchema(1, 0), "accepted": {Type: "boolean"}}, "build_id", "accepted"), false),
		metadata("resource_check", "resource", atc.CheckResource, mcpauth.ScopeBuilds, "Request a resource check. Not yet implemented through MCP."),
		metadata("resource_pin", "resource", atc.PinResourceVersion, mcpauth.ScopePipelines, "Pin a resource version in a pipeline. Not yet implemented through MCP."),
		metadata("resource_unpin", "resource", atc.UnpinResource, mcpauth.ScopePipelines, "Remove a resource version pin. Not yet implemented through MCP."),
		metadata("pipeline_run_create", "pipeline", atc.CreatePipelineRunV2, mcpauth.ScopePipelines, "Create a parameterized run that can interpolate configuration. Not yet implemented through MCP."),
		metadata("build_create", "build", atc.CreateBuild, mcpauth.ScopePipelines, "Create a one-off build with an executable plan. Not yet implemented through MCP."),
		metadata("container_exec", "container", atc.HijackContainer, mcpauth.ScopeHijack, "Execute in a build container. Container kind and target authorization still apply. Not yet implemented through MCP."),
		metadata("team_set", "team", atc.SetTeam, mcpauth.ScopeAdmin, "Set team authorization subject to the account's team authority. Not yet implemented through MCP."),
		metadata("team_delete", "team", atc.DestroyTeam, mcpauth.ScopeAdmin, "Delete a team; requires account-administrator authority. Not yet implemented through MCP."),
		metadata("server_log_level_set", "server", atc.SetLogLevel, mcpauth.ScopeAdmin, "Change server log level; requires account-administrator authority. Not yet implemented through MCP."),
	}
}

// GroupInputSchema keeps the MCP root an object and the precise union nested.
// The caller supplies only eligible operations. Empty unions reject all input.
func GroupInputSchema(operations []Operation) *jsonschema.Schema {
	branches := make([]*jsonschema.Schema, 0, len(operations))
	for _, op := range operations {
		if op.Arguments != nil {
			branches = append(branches, object(map[string]*jsonschema.Schema{
				"operation": {Const: constant(op.ID)}, "arguments": op.Arguments,
			}, "operation", "arguments"))
		}
	}
	union := &jsonschema.Schema{OneOf: branches}
	if len(branches) == 0 {
		union = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	}
	return object(map[string]*jsonschema.Schema{"request": union}, "request")
}

// GroupOutputSchema discriminates success results by canonical operation ID.
// Tool errors use isError/text rather than pretending to match this success union.
func GroupOutputSchema(operations []Operation) *jsonschema.Schema {
	result := &jsonschema.Schema{Type: "object"}
	for _, op := range operations {
		if op.Result != nil {
			result.OneOf = append(result.OneOf, object(map[string]*jsonschema.Schema{
				"operation": {Const: constant(op.ID)}, "result": op.Result,
			}, "operation", "result"))
		}
	}
	if len(result.OneOf) == 0 {
		result.Not = &jsonschema.Schema{}
	}
	return result
}

func pointer[T any](value T) *T  { return &value }
func constant(value string) *any { return pointer[any](value) }
func object(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}

// configWarningMaxChars bounds a single config warning in both the result
// schema and the adapter that fills it. A warning longer than the schema
// allows would fail outbound validation and report INVALID_RESULT for a
// config that is already committed, so the two must not drift apart.
const configWarningMaxChars = 4096

func textSchema(max int) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", MaxLength: pointer(max)}
}
func nameSchema() *jsonschema.Schema   { s := textSchema(256); s.MinLength = pointer(1); return s }
func cursorSchema() *jsonschema.Schema { s := textSchema(2048); s.MinLength = pointer(1); return s }
func integerSchema(min, max float64) *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "integer", Minimum: pointer(min)}
	if max > 0 {
		s.Maximum = pointer(max)
	}
	return s
}
func versionSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Pattern: `^(0|[1-9][0-9]{0,9})$`, MaxLength: pointer(10), Description: "Decimal config version in 0–2147483647. Zero means create only; a positive version means update only. The numeric bound is also validated before dispatch."}
}
func configSchema() *jsonschema.Schema {
	s := textSchema(128 * 1024)
	s.Description = "UTF-8 YAML, at most 131072 bytes (also checked before dispatch). Preserve credential/template references."
	return s
}
func pipelineArgs(extra map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	props := map[string]*jsonschema.Schema{"team": nameSchema(), "pipeline": nameSchema(), "instance_vars": {Type: "object", Description: "Exact instance variables; {} selects the non-instanced pipeline."}}
	for k, v := range extra {
		props[k] = v
	}
	return object(props, append([]string{"team", "pipeline", "instance_vars"}, required...)...)
}
func buildArgs(extra map[string]*jsonschema.Schema) *jsonschema.Schema {
	props := map[string]*jsonschema.Schema{"build_id": integerSchema(1, 0)}
	for k, v := range extra {
		props[k] = v
	}
	return object(props, "build_id")
}
func acceptedResult() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"accepted": {Type: "boolean"}}, "accepted")
}
func pipelineResult() *jsonschema.Schema {
	return pipelineArgs(map[string]*jsonschema.Schema{"id": integerSchema(1, 0), "paused": {Type: "boolean"}, "archived": {Type: "boolean"}, "public": {Type: "boolean"}}, "id", "paused", "archived", "public")
}
func buildStatusSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Enum: []any{"pending", "started", "succeeded", "failed", "errored", "aborted"}}
}
func buildResult() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"build_id": integerSchema(1, 0), "name": {Type: "string"}, "status": buildStatusSchema(), "team": nameSchema(), "pipeline": {Types: []string{"string", "null"}}, "instance_vars": {Type: "object"}, "job": {Types: []string{"string", "null"}}}, "build_id", "name", "status", "team", "pipeline", "instance_vars", "job")
}
func pageResult(item *jsonschema.Schema) *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{"items": {Type: "array", Items: item, MaxItems: pointer(100)}, "next_cursor": {Types: []string{"string", "null"}}}, "items", "next_cursor")
}
func logResult() *jsonschema.Schema {
	return object(map[string]*jsonschema.Schema{
		"events":      {Type: "array", Items: object(map[string]*jsonschema.Schema{"id": {Type: "string"}, "event": {Type: "string"}, "version": {Type: "string"}, "data": {Type: "object"}}, "id", "event", "version", "data")},
		"next_cursor": {Types: []string{"string", "null"}}, "finished": {Type: "boolean"}, "caught_up": {Type: "boolean"}, "truncated": {Type: "boolean"},
	}, "events", "next_cursor", "finished", "caught_up", "truncated")
}
