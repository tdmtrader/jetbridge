package platformmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StatusError is returned by do for EVERY non-2xx ATC response so callers can
// inspect the status via errors.As — AwaitAnswer's fatal-auth counting depends
// on it (D6, 2026-07-09 SSE seam delta / F31 leg 3).
type StatusError struct {
	Method string
	Path   string
	Code   int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// ErrPrincipalRejected is returned (wrapped) by AwaitAnswer after
// AuthFailureLimit CONSECUTIVE 401/403 responses: the per-run principal is
// expired or revoked, and the step must fail loudly rather than park forever.
var ErrPrincipalRejected = errors.New("agent principal rejected: consecutive auth failures exceeded limit")

// Question mirrors one agent_run_questions row on the wire (shared contracts
// §1.9 + the notified_at addendum). Timestamps are epoch seconds in JSON;
// zero = unset. The canonical domain package (`agent/api/questions`, plan 08
// Task 3 / remainder Slice B) has not landed yet — this local mirror is
// byte-compatible with its frozen JSON shape, and the ask_human tasks swap to
// the shared type once Slice B ships it.
type Question struct {
	ID             int      `json:"id"`
	TicketID       int      `json:"ticket_id"`
	PipelineRunID  *int     `json:"pipeline_run_id,omitempty"`
	BuildID        int      `json:"build_id"`
	StepName       string   `json:"step_name"`
	Kind           string   `json:"kind"`
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	TimeoutPolicy  string   `json:"timeout_policy"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	DefaultAnswer  string   `json:"default_answer,omitempty"`
	AskedAt        int64    `json:"asked_at"`
	AnsweredAt     int64    `json:"answered_at,omitempty"`
	Answer         string   `json:"answer,omitempty"`
	AnsweredBy     string   `json:"answered_by,omitempty"`
	NotifiedAt     int64    `json:"notified_at,omitempty"`
}

// ATCClient is the sidecar's principal-authed ATC API client. Its long-poll
// loop (AwaitAnswer) is the park half of the §3.2 park/resume protocol:
// transport errors and 5xx responses are retried forever — an ATC/web-node
// restart while parked just means a few failed polls until the new node
// answers — but AuthFailureLimit CONSECUTIVE 401/403 responses are fatal
// (ErrPrincipalRejected): a revoked or expired per-run principal must fail
// the step loudly, never silently park it forever (F31 leg 3).
type ATCClient struct {
	baseURL  string
	token    string
	ticketID int
	http     *http.Client

	// PollWait is the server-side wait per long-poll request (default 30s).
	PollWait time.Duration
	// RetryInterval is the sleep after a failed poll (default 5s).
	RetryInterval time.Duration
	// AuthFailureLimit is the number of CONSECUTIVE 401/403 responses after
	// which AwaitAnswer gives up with ErrPrincipalRejected. FROZEN default 12:
	// with RetryInterval 5s that is >= 60s of sustained auth failures, which
	// outlives the §1.2 60s principal-verification cache — a revoked principal
	// is confirmed while a cache-warm blip cannot trip it.
	AuthFailureLimit int
}

func NewATCClient(baseURL, principalToken string, ticketID int) *ATCClient {
	return &ATCClient{
		baseURL:  baseURL,
		token:    principalToken,
		ticketID: ticketID,
		// No global timeout: long-polls legitimately hold the connection.
		// Individual requests carry contexts.
		http:             &http.Client{},
		PollWait:         30 * time.Second,
		RetryInterval:    5 * time.Second,
		AuthFailureLimit: 12,
	}
}

func (c *ATCClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &StatusError{
			Method: method, Path: path, Code: resp.StatusCode,
			Body: string(bytes.TrimSpace(msg)),
		}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// TicketPayload mirrors the GetAgentTicket response: envelope + latest spec +
// active-plan tasks, all embedded by ticket-core's handler (the landed
// tickets.TicketDetail{Ticket, Spec, Tasks} shape — contracts §2.1 addendum).
// The three read tools each fetch this ONE payload and project it:
// read_ticket keeps ticket+spec and DROPS tasks; list_tasks projects the task
// skeleton; get_task returns one task's detail. Tasks are never flattened
// into markdown — the structure is preserved and served through typed tools
// (§3.2 read model).
type TicketPayload struct {
	Ticket json.RawMessage `json:"ticket"`
	Spec   json.RawMessage `json:"spec"`
	Tasks  []TicketTask    `json:"tasks"`
}

// TicketTask is one active-plan task as embedded in the GetAgentTicket
// payload. The wire field is "detail" (the landed §2.1 tickets.Task JSON
// tag); get_task projects it as "detail_md" per the frozen §3.2 read model.
// list_tasks omits it, get_task returns it.
type TicketTask struct {
	Ordering int    `json:"ordering"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	DetailMD string `json:"detail"`
}

// GetTicket fetches and decodes the GetAgentTicket payload. It tolerates the
// bare-ticket drift the survey warned about: a response with no "ticket" key
// is treated as the envelope itself (spec null, no tasks).
func (c *ATCClient) GetTicket(ctx context.Context) (*TicketPayload, error) {
	raw, err := c.GetTicketRaw(ctx)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decoding ticket response: %w", err)
	}
	if _, ok := probe["ticket"]; !ok {
		return &TicketPayload{Ticket: json.RawMessage(raw)}, nil
	}
	var payload TicketPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decoding ticket payload: %w", err)
	}
	return &payload, nil
}

// GetTicketRaw returns the GetAgentTicket response body verbatim; ticket-core's
// handler embeds spec+tasks (survey Task 1). Kept for read_ticket, which
// re-projects the payload to envelope + spec ONLY.
func (c *ATCClient) GetTicketRaw(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/agent/tickets/%d", c.ticketID), nil, &raw)
	return raw, err
}

type SpecSubmission struct {
	Title              string              `json:"title"`
	Body               string              `json:"body"`
	AcceptanceCriteria []string            `json:"acceptance_criteria,omitempty"`
	Links              []map[string]string `json:"links,omitempty"`
}

func (c *ATCClient) SubmitSpec(ctx context.Context, spec SpecSubmission) (int, error) {
	var out struct {
		Version int `json:"version"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agent/tickets/%d/spec", c.ticketID), spec, &out)
	return out.Version, err
}

type TaskSubmission struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

func (c *ATCClient) SubmitPlan(ctx context.Context, tasks []TaskSubmission) (int, error) {
	var out struct {
		PlanVersion int `json:"plan_version"`
	}
	body := map[string]any{"tasks": tasks}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agent/tickets/%d/plan", c.ticketID), body, &out)
	return out.PlanVersion, err
}

func (c *ATCClient) UpdateTaskStatus(ctx context.Context, ordering int, status, note string) error {
	body := map[string]string{"status": status}
	if note != "" {
		body["note"] = note
	}
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/agent/tickets/%d/tasks/%d", c.ticketID, ordering), body, nil)
}

func (c *ATCClient) AskQuestion(ctx context.Context, q *Question) (*Question, error) {
	var created Question
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/agent/tickets/%d/questions", c.ticketID), q, &created)
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *ATCClient) GetQuestion(ctx context.Context, questionID int, wait time.Duration) (*Question, error) {
	path := fmt.Sprintf("/api/v1/agent/tickets/%d/questions/%d", c.ticketID, questionID)
	if wait > 0 {
		path += fmt.Sprintf("?wait=%s", wait)
	}
	var q Question
	if err := c.do(ctx, http.MethodGet, path, nil, &q); err != nil {
		return nil, err
	}
	return &q, nil
}

func (c *ATCClient) AnswerQuestion(ctx context.Context, questionID int, answer, answeredBy string) error {
	body := map[string]string{"answer": answer, "answered_by": answeredBy}
	return c.do(ctx, http.MethodPut,
		fmt.Sprintf("/api/v1/agent/tickets/%d/questions/%d/answer", c.ticketID, questionID), body, nil)
}

// AwaitAnswer long-polls until the question is answered (returns q, false),
// the deadline passes (returns nil, true — caller applies the timeout
// policy), or ctx is cancelled. Transport errors and 5xx responses are
// retried indefinitely (parked runs must survive web-node restarts);
// CONSECUTIVE 401/403 responses are counted and become fatal at
// AuthFailureLimit, returning an error wrapping ErrPrincipalRejected. The
// counter resets on any success and on any non-auth error (D6/F31 leg 3).
func (c *ATCClient) AwaitAnswer(ctx context.Context, questionID int, deadline *time.Time) (*Question, bool, error) {
	authFailures := 0
	for {
		if deadline != nil && time.Now().After(*deadline) {
			return nil, true, nil
		}
		wait := c.PollWait
		if deadline != nil {
			if until := time.Until(*deadline); until < wait {
				wait = until
			}
		}
		q, err := c.GetQuestion(ctx, questionID, wait)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			var se *StatusError
			if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
				authFailures++
				if authFailures >= c.AuthFailureLimit {
					return nil, false, fmt.Errorf("question %d: %d consecutive 401/403 responses: %w",
						questionID, c.AuthFailureLimit, ErrPrincipalRejected)
				}
			} else {
				authFailures = 0 // transport or 5xx: retry forever
			}
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(c.RetryInterval):
			}
			continue
		}
		authFailures = 0
		if q.AnsweredAt != 0 {
			return q, false, nil
		}
	}
}
