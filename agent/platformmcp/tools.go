package platformmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

var taskStatuses = map[string]bool{
	"pending": true, "in_progress": true, "done": true, "skipped": true, "blocked": true,
}

// registerTools registers the six ticket/task tools (§3.2 schemas, verbatim).
// ask_human is deliberately NOT registered here — it lands with the ask_human
// park/resume tasks (ticket #37 delta over plan 08 Task 10).
func (s *Server) registerTools() {
	s.mcp.AddTool("read_ticket",
		"Read this run's ticket: envelope and latest spec (call list_tasks / get_task for the plan).",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}),
		s.readTicket)

	s.mcp.AddTool("list_tasks",
		"List the active plan's tasks (ordering, title, status) — a cheap skeleton with no detail bodies.",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		}),
		s.listTasks)

	s.mcp.AddTool("get_task",
		"Get one active-plan task by its ordering, including its detail_md body.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"ordering"},
			"properties": map[string]any{
				"ordering": map[string]any{"type": "integer", "description": "task position in the active plan"},
			},
			"additionalProperties": false,
		}),
		s.getTask)

	s.mcp.AddTool("submit_spec",
		"Submit the spec for this ticket. Structure enters here — never as markdown files.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"title", "body"},
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
				"body":  map[string]any{"type": "string", "description": "markdown; rationale and tradeoffs belong here"},
				"acceptance_criteria": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1,
				},
				"links": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object", "required": []string{"title", "url"},
						"properties": map[string]any{
							"title": map[string]any{"type": "string"},
							"url":   map[string]any{"type": "string"},
						},
					},
				},
			},
			"additionalProperties": false,
		}),
		s.submitSpec)

	s.mcp.AddTool("submit_plan",
		"Replace the active plan with an ordered task list (orderings 1..N as given).",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"tasks"},
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array", "minItems": 1,
					"items": map[string]any{
						"type": "object", "required": []string{"title"},
						"properties": map[string]any{
							"title":  map[string]any{"type": "string"},
							"detail": map[string]any{"type": "string", "description": "optional markdown"},
						},
					},
				},
			},
			"additionalProperties": false,
		}),
		s.submitPlan)

	s.mcp.AddTool("update_task_status",
		"Update one active-plan task's status by its ordering.",
		mcpserver.MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"ordering", "status"},
			"properties": map[string]any{
				"ordering": map[string]any{"type": "integer", "minimum": 1, "description": "task position in the active plan"},
				"status":   map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "skipped", "blocked"}},
				"note":     map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		}),
		s.updateTaskStatus)
}

// readTicket returns envelope + spec ONLY — tasks are deliberately dropped from
// this result (§3.2 read model). Agents reach the plan through list_tasks /
// get_task so the whole plan is never dumped into context.
func (s *Server) readTicket(ctx context.Context, _ json.RawMessage, _ func(string)) (any, error) {
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading ticket: %w", err)
	}
	out := map[string]any{"ticket": payload.Ticket}
	if len(payload.Spec) > 0 {
		out["spec"] = payload.Spec
	} else {
		out["spec"] = nil
	}
	return out, nil
}

// listTasks returns the cheap task skeleton — {ordering, title, status} with no
// detail bodies (§3.2). It backs onto ticket-core's ActivePlan via
// GetAgentTicket. A ticket with no active plan yields {"tasks": []} — a
// successful result, not an error (E5 zero-rows rule).
func (s *Server) listTasks(ctx context.Context, _ json.RawMessage, _ func(string)) (any, error) {
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	type taskSkeleton struct {
		Ordering int    `json:"ordering"`
		Title    string `json:"title"`
		Status   string `json:"status"`
	}
	skeleton := make([]taskSkeleton, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		skeleton = append(skeleton, taskSkeleton{
			Ordering: task.Ordering, Title: task.Title, Status: task.Status,
		})
	}
	return map[string]any{"tasks": skeleton}, nil
}

// getTask returns one active-plan task including its detail_md. An unknown
// ordering returns a handler error, which the shared atc/api/mcpserver maps to
// an MCP tool error — a tools/call result with isError=true carrying the error
// text (§3.2). This is a tool-level error, NOT a JSON-RPC -32602 error object;
// the mcpserver only emits -32602 for a malformed tools/call envelope. The
// same mechanism covers a ticket with no active plan at all, whose error names
// the absent plan (E5).
func (s *Server) getTask(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Ordering int `json:"ordering"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	payload, err := s.client.GetTicket(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}
	if len(payload.Tasks) == 0 {
		return nil, fmt.Errorf("no active plan for ticket %d", s.cfg.TicketID)
	}
	for _, task := range payload.Tasks {
		if task.Ordering == in.Ordering {
			return map[string]any{
				"ordering":  task.Ordering,
				"title":     task.Title,
				"status":    task.Status,
				"detail_md": task.DetailMD,
			}, nil
		}
	}
	return nil, fmt.Errorf("no task with ordering %d in the active plan", in.Ordering)
}

func (s *Server) submitSpec(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Title              string              `json:"title"`
		Body               string              `json:"body"`
		AcceptanceCriteria []string            `json:"acceptance_criteria"`
		Links              []map[string]string `json:"links"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Title == "" || in.Body == "" {
		return nil, fmt.Errorf("title and body are required")
	}
	version, err := s.client.SubmitSpec(ctx, SpecSubmission{
		Title: in.Title, Body: in.Body,
		AcceptanceCriteria: in.AcceptanceCriteria, Links: in.Links,
	})
	if err != nil {
		return nil, fmt.Errorf("submitting spec: %w", err)
	}
	return map[string]int{"version": version}, nil
}

func (s *Server) submitPlan(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Tasks []TaskSubmission `json:"tasks"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if len(in.Tasks) == 0 {
		return nil, fmt.Errorf("tasks requires at least one entry")
	}
	for i, task := range in.Tasks {
		if task.Title == "" {
			return nil, fmt.Errorf("tasks[%d].title is required", i)
		}
	}
	planVersion, err := s.client.SubmitPlan(ctx, in.Tasks)
	if err != nil {
		return nil, fmt.Errorf("submitting plan: %w", err)
	}
	return map[string]int{"plan_version": planVersion}, nil
}

func (s *Server) updateTaskStatus(ctx context.Context, args json.RawMessage, _ func(string)) (any, error) {
	var in struct {
		Ordering int    `json:"ordering"`
		Status   string `json:"status"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Ordering < 1 {
		return nil, fmt.Errorf("ordering must be >= 1")
	}
	if !taskStatuses[in.Status] {
		return nil, fmt.Errorf("invalid status %q", in.Status)
	}
	if err := s.client.UpdateTaskStatus(ctx, in.Ordering, in.Status, in.Note); err != nil {
		return nil, fmt.Errorf("updating task: %w", err)
	}
	return map[string]bool{"ok": true}, nil
}
