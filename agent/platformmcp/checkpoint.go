package platformmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/agent/schema"
)

type checkpointRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type checkpointResponse struct {
	Approved   bool   `json:"approved"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"`
}

// handleCheckpoint is the internal (non-MCP) checkpoint endpoint (§3.2 +
// Task 1 addendum). It files a kind=checkpoint question and BLOCKS until a
// human approves or rejects. Checkpoints always park — no timeout.
//
// Per-name dedup: if the checkpoint CLIENT process restarts mid-park it
// re-POSTs the same name; without a guard that files a second open row.
// ckOpen maps name -> the open question id for this sidecar's lifetime; a
// concurrent/repeat POST for a name already in flight re-awaits the SAME
// row instead of filing a new one. The guard is released once the row
// resolves (so a later, distinct checkpoint of the same name still files a
// fresh question).
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req checkpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %s", err), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Reserve (or join) the open question for this name.
	s.ckMu.Lock()
	questionID, inFlight := s.ckOpen[req.Name]
	if !inFlight {
		question := req.Description
		if question == "" {
			question = fmt.Sprintf("Approve checkpoint %q for ticket %d?", req.Name, s.cfg.TicketID)
		}
		q := s.newQuestion("checkpoint", question, []string{"approve", "reject"}, "")
		q.TimeoutPolicy = "park"
		q.TimeoutSeconds = 0
		created, err := s.client.AskQuestion(r.Context(), q)
		if err != nil {
			s.ckMu.Unlock()
			http.Error(w, fmt.Sprintf("filing checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		questionID = created.ID
		s.ckOpen[req.Name] = questionID
		s.ckMu.Unlock()

		s.events.Emit(schema.EventCheckpointWait, map[string]any{
			"question_id": questionID,
			"checkpoint":  req.Name,
		})
	} else {
		// A POST for this name is already parked on questionID; fall through
		// and await the same row. Only the first filer emits checkpoint.wait.
		s.ckMu.Unlock()
	}

	answered, _, err := s.client.AwaitAnswer(r.Context(), questionID, nil)
	if err != nil {
		// Transport error awaiting the answer: leave the reservation in place
		// so a client retry re-awaits the same open row rather than re-filing.
		// A consecutive-401/403 fatal (D6/F31 leg 3) is surfaced with the
		// frozen "principal rejected:" prefix so the checkpoint client can
		// echo it verbatim to stderr before exiting 1.
		if errors.Is(err, ErrPrincipalRejected) {
			http.Error(w, fmt.Sprintf("principal rejected: awaiting checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		http.Error(w, fmt.Sprintf("awaiting checkpoint: %s", err), http.StatusBadGateway)
		return
	}

	// Resolved: release the name so a later distinct checkpoint files fresh.
	s.ckMu.Lock()
	if s.ckOpen[req.Name] == questionID {
		delete(s.ckOpen, req.Name)
	}
	s.ckMu.Unlock()

	approved := answered.Answer == "approve"
	s.events.Emit(schema.EventCheckpointRelease, map[string]any{
		"question_id": questionID,
		"approved":    approved,
		"answered_by": answered.AnsweredBy,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checkpointResponse{
		Approved: approved, Answer: answered.Answer, AnsweredBy: answered.AnsweredBy,
	})
}
