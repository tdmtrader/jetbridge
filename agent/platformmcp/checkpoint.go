package platformmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

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
// human approves or rejects — or, past the PARK-V2 short-park threshold,
// answers 202 {"parked": true} so the client exits 3 and the step becomes
// the §B5 carrier. Checkpoints always park — no timeout policy.
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

	// Reserve (or join) the open question for this name. Since PARK-V2 §E the
	// map is a same-pod OPTIMIZATION only — the authority is the DB unique key
	// (pipeline_run_id, step_name, kind, question_hash): a continuation pod's
	// fresh sidecar re-POSTs, AskQuestion find-or-creates, and gets the SAME
	// row back even though its map is empty.
	s.ckMu.Lock()
	reservation, inFlight := s.ckOpen[req.Name]
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
		if created.AnsweredAt != 0 {
			// PARK-V2 §E resume fast path: the re-POST joined a row a human
			// already resolved — return it immediately; no park, no
			// reservation.
			s.ckMu.Unlock()
			s.respondCheckpointResolved(w, created.ID, created.Answer, created.AnsweredBy)
			return
		}
		reservation = ckReservation{ID: created.ID, AskedAt: created.AskedAt}
		s.ckOpen[req.Name] = reservation
		s.ckMu.Unlock()

		s.events.Emit(schema.EventCheckpointWait, map[string]any{
			"question_id": reservation.ID,
			"checkpoint":  req.Name,
		})
	} else {
		// A POST for this name is already parked on the row; fall through and
		// await the same row. Only the first filer emits checkpoint.wait.
		s.ckMu.Unlock()
	}

	// PARK-V2 §A/§B4: the short-park threshold bounds the blocking response.
	// AwaitAnswer's deadline leg doubles as the threshold timer, measured
	// from the row's asked_at — a re-POST joins the ORIGINAL park clock.
	var parkDeadline *time.Time
	if s.cfg.ShortParkMax > 0 {
		d := time.Unix(reservation.AskedAt, 0).Add(s.cfg.ShortParkMax)
		parkDeadline = &d
	}

	answered, crossedThreshold, err := s.client.AwaitAnswer(r.Context(), reservation.ID, parkDeadline)
	if err != nil {
		// Transport error awaiting the answer: leave the reservation in place
		// so a client retry re-awaits the same open row rather than re-filing.
		// A consecutive-401/403 fatal (D6/F31 leg 3) keeps the frozen
		// "principal rejected:" prefix so the client echoes it and exits 1.
		if errors.Is(err, ErrPrincipalRejected) {
			http.Error(w, fmt.Sprintf("principal rejected: awaiting checkpoint: %s", err), http.StatusBadGateway)
			return
		}
		http.Error(w, fmt.Sprintf("awaiting checkpoint: %s", err), http.StatusBadGateway)
		return
	}
	if crossedThreshold {
		// §B4: answer the blocked POST 202 — the client exits 3 and the
		// TaskStep fails as the §B5 carrier. The row STAYS OPEN (the durable
		// representation of the wait) and the reservation is kept so a
		// same-pod retry re-awaits the same row. Best-effort sentinel for
		// flight provenance when a flight volume is mounted (normally it is
		// not in checkpoint pods — the 202 IS the exit signal there).
		if s.cfg.ParkPath != "" {
			_ = writeSentinelAtomic(s.cfg.ParkPath, map[string]any{
				"question_id":       reservation.ID,
				"kind":              "checkpoint",
				"step_name":         s.cfg.StepName,
				"asked_at":          time.Unix(reservation.AskedAt, 0).UTC().Format(time.RFC3339),
				"threshold_seconds": int(s.cfg.ShortParkMax / time.Second),
				"crossed_at":        time.Now().UTC().Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]bool{"parked": true})
		return
	}

	// Resolved: release the name so a later distinct checkpoint files fresh.
	s.ckMu.Lock()
	if s.ckOpen[req.Name].ID == reservation.ID {
		delete(s.ckOpen, req.Name)
	}
	s.ckMu.Unlock()

	s.respondCheckpointResolved(w, reservation.ID, answered.Answer, answered.AnsweredBy)
}

// respondCheckpointResolved emits checkpoint.release and writes the frozen
// 200 body — shared by the await path and the §E answered-row fast path.
func (s *Server) respondCheckpointResolved(w http.ResponseWriter, questionID int, answer, answeredBy string) {
	approved := answer == "approve"
	s.events.Emit(schema.EventCheckpointRelease, map[string]any{
		"question_id": questionID,
		"approved":    approved,
		"answered_by": answeredBy,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(checkpointResponse{
		Approved: approved, Answer: answer, AnsweredBy: answeredBy,
	})
}
