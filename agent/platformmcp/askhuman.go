package platformmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/schema"
)

// TunePolling shortens the client long-poll and retry intervals (tests only).
func (s *Server) TunePolling(pollWait, retry time.Duration) {
	s.client.PollWait = pollWait
	s.client.RetryInterval = retry
}

type askHumanInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Default  string   `json:"default"`
}

type askHumanResult struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
	TimedOut   bool   `json:"timed_out"`
}

// askHuman implements the §3.2 park/resume protocol: insert the question row,
// emit human.ask, then BLOCK the MCP call on a resilient long-poll until the
// row is answered. The sidecar itself enforces the timeout and, on expiry, is
// the writer that resolves the row (policy default/fail) so a timed-out row
// never stays open. Policy park = no deadline: only a human resolves it.
//
// ask_human is a MUST-stream tool (D4, 2026-07-09 SSE seam delta): it can
// block unboundedly, so it is served over the SSE progress path. The
// progress call below sets the parked message once; the server's heartbeat
// ticker repeats the latest message every interval (<60s — the claude CLI's
// empirical abandonment bound, F13). A consecutive-401/403 fatal from
// AwaitAnswer surfaces as a LOUD "principal rejected:" tool error
// (isError=true) instead of an eternal park (D6/F31 leg 3).
func (s *Server) askHuman(ctx context.Context, args json.RawMessage, progress func(string)) (any, error) {
	var in askHumanInput
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Question == "" {
		return nil, fmt.Errorf("question is required")
	}
	if s.cfg.TimeoutPolicy == "default" && in.Default == "" {
		return nil, fmt.Errorf("'default' is required: this workflow's ask_human timeout policy is 'default'")
	}
	if in.Default != "" && len(in.Options) > 0 && !containsOption(in.Options, in.Default) {
		return nil, fmt.Errorf("'default' must be one of options")
	}

	q := s.newQuestion("question", in.Question, in.Options, in.Default)
	created, err := s.client.AskQuestion(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("filing question: %w", err)
	}
	s.events.Emit(schema.EventHumanAsk, map[string]any{
		"question_id": created.ID,
		"kind":        "question",
		"question":    in.Question,
		"options":     in.Options,
	})
	// PARK-V2 §E resume fast path: find-or-create returned an ALREADY-ANSWERED
	// row — the continuation's re-issued call gets its answer immediately (no
	// park, no threshold timer, no SSE wait).
	if created.AnsweredAt != 0 {
		s.events.Emit(schema.EventHumanAnswer, map[string]any{
			"question_id":  created.ID,
			"answer":       created.Answer,
			"answered_by":  created.AnsweredBy,
			"wait_seconds": created.AnsweredAt - created.AskedAt,
			"timed_out":    false,
			"resumed":      true,
		})
		return askHumanResult{Answer: created.Answer, AnsweredBy: created.AnsweredBy, TimedOut: false}, nil
	}
	// Park-start progress line (D4): emitted once here, repeated by the SSE
	// heartbeat ticker for the whole park.
	progress(fmt.Sprintf("parked: waiting for human answer to question %d", created.ID))

	// PARK-V2 §A/§B1: arm the exit-and-respawn threshold for this park.
	stopParkExit := s.armParkExit(created)
	defer stopParkExit()

	answered, timedOut, err := s.awaitWithPolicy(ctx, created.ID, created.AskedAt, in.Default)
	if err != nil {
		if errors.Is(err, ErrPrincipalRejected) {
			// Fail LOUDLY: the step errors instead of parking forever on a
			// revoked/expired principal (D6/F31 leg 3).
			return nil, fmt.Errorf("principal rejected: %w", err)
		}
		return nil, err
	}
	waitSeconds := time.Now().Unix() - created.AskedAt
	s.events.Emit(schema.EventHumanAnswer, map[string]any{
		"question_id":  created.ID,
		"answer":       answered.Answer,
		"answered_by":  answered.AnsweredBy,
		"wait_seconds": waitSeconds,
		"timed_out":    timedOut,
	})
	return askHumanResult{Answer: answered.Answer, AnsweredBy: answered.AnsweredBy, TimedOut: timedOut}, nil
}

// awaitWithPolicy blocks until answered or the policy resolves the timeout.
// The deadline is absolute from the row's asked_at, so a joined row in a
// respawned step re-arms from the ORIGINAL ask, never a fresh clock.
func (s *Server) awaitWithPolicy(ctx context.Context, questionID int, askedAt int64, defaultAnswer string) (*resolvedAnswer, bool, error) {
	var deadline *time.Time
	if s.cfg.TimeoutPolicy != "park" && s.cfg.TimeoutSeconds > 0 {
		d := time.Unix(askedAt, 0).Add(time.Duration(s.cfg.TimeoutSeconds) * time.Second)
		deadline = &d
	}

	q, timedOut, err := s.client.AwaitAnswer(ctx, questionID, deadline)
	if err != nil {
		return nil, false, fmt.Errorf("awaiting answer: %w", err)
	}
	if !timedOut {
		return &resolvedAnswer{Answer: q.Answer, AnsweredBy: q.AnsweredBy}, false, nil
	}

	// Timeout: the sidecar resolves the row (§3.2). A concurrent human answer
	// wins the Answer race (409) — in that case fetch and use theirs.
	resolution := ""
	if s.cfg.TimeoutPolicy == "default" {
		resolution = defaultAnswer
	}
	answerErr := s.client.AnswerQuestion(ctx, questionID, resolution, "platform-mcp")
	if answerErr != nil {
		if latest, gerr := s.client.GetQuestion(ctx, questionID, 0); gerr == nil && latest.AnsweredAt != 0 {
			return &resolvedAnswer{Answer: latest.Answer, AnsweredBy: latest.AnsweredBy}, false, nil
		}
		return nil, false, fmt.Errorf("resolving timed-out question: %w", answerErr)
	}

	if s.cfg.TimeoutPolicy == "fail" {
		return nil, false, fmt.Errorf("ask_human timed out after %ds (timeout_policy=fail)", s.cfg.TimeoutSeconds)
	}
	return &resolvedAnswer{Answer: resolution, AnsweredBy: "platform-mcp"}, true, nil
}

type resolvedAnswer struct {
	Answer     string
	AnsweredBy string
}

// newQuestion builds the wire row for this run: the payload IS Question — the
// ask route ignores client-set id/timestamps and overrides TicketID from the
// URL, so no separate payload type is needed. Checkpoint rows override
// TimeoutPolicy to park/0 explicitly after calling this.
func (s *Server) newQuestion(kind, question string, options []string, defaultAnswer string) *Question {
	q := &Question{
		Kind:           kind,
		Question:       question,
		Options:        options,
		TimeoutPolicy:  s.cfg.TimeoutPolicy,
		TimeoutSeconds: s.cfg.TimeoutSeconds,
		DefaultAnswer:  defaultAnswer,
		BuildID:        s.cfg.BuildID,
		StepName:       s.cfg.StepName,
	}
	if s.cfg.PipelineRunID > 0 {
		runID := s.cfg.PipelineRunID
		q.PipelineRunID = &runID
	}
	return q
}

func containsOption(opts []string, s string) bool {
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}
