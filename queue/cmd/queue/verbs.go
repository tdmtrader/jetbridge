package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/concourse/concourse/queue/core"
)

// readVerbs only read the stored snapshot, like status.
var readVerbs = []string{"list", "ejected", "explain"}

type listed struct {
	ID, Commit string
	AdmittedAt time.Time
	BuildsOn   []string
}

// history is everything the snapshot holds about one id.
type history struct {
	ID, State, Commit, Ref string
	AdmittedAt             time.Time `json:",omitzero"`
	BuildsOn               []string  `json:",omitempty"`
	Runs                   []string  `json:",omitempty"`
	Settled                []core.SettleRecord
	Refused                []core.Refusal
}

// readVerb prints list, ejected or explain from s. The caller's writer redacts every
// line; the snapshot is redacted here too so the JSON is safe on its own.
func readVerb(verb string, s core.Snapshot, id string, asJSON bool, out io.Writer) error {
	s = core.RedactSnapshot(s)
	switch verb {
	case "list":
		rows := []listed{}
		for _, e := range s.Queued {
			rows = append(rows, listed{e.ID, e.Commit, e.AdmittedAt, s.BuildsOn[e.ID]})
		}
		if asJSON {
			return json.NewEncoder(out).Encode(rows)
		}
		for _, r := range rows {
			fmt.Fprintf(out, "%s %s admitted %s builds-on %v\n", r.ID, short(r.Commit), r.AdmittedAt.Format(time.RFC3339), r.BuildsOn)
		}
	case "ejected":
		rows := []ejection{}
		for _, r := range slices.Backward(s.Settled) { // newest first
			if r.Kind == core.EjectedEvent {
				rows = append(rows, ejection{r.ID, r.Why, r.Cause, r.At})
			}
		}
		if asJSON {
			return json.NewEncoder(out).Encode(rows)
		}
		for _, r := range rows {
			fmt.Fprintf(out, "%s why %q cause %q at %s\n", r.ID, r.Why, r.Cause, r.At.Format(time.RFC3339))
		}
	default:
		h, ok := explain(s, id)
		if !ok {
			return fmt.Errorf("no entry %q: it was never admitted, refused or settled here", id)
		}
		if asJSON {
			return json.NewEncoder(out).Encode(h)
		}
		fmt.Fprintf(out, "%s: %s, commit %s on %q, admitted %s, builds-on %v\n", h.ID, h.State, short(h.Commit), h.Ref, h.AdmittedAt.Format(time.RFC3339), h.BuildsOn)
		for _, r := range h.Runs {
			fmt.Fprintf(out, "run %s\n", r)
		}
		for _, r := range h.Settled {
			fmt.Fprintf(out, "settle %s at %s why %q cause %q run %q\n", r.Kind, r.At.Format(time.RFC3339), r.Why, r.Cause, r.Run)
		}
		for _, r := range h.Refused {
			fmt.Fprintf(out, "refused %s: %s\n", short(r.Commit), r.Why)
		}
	}
	return nil
}

func explain(s core.Snapshot, id string) (h history, ok bool) {
	h = history{ID: id, State: "unknown", Commit: s.Commits[id], BuildsOn: s.BuildsOn[id], Settled: []core.SettleRecord{}, Refused: []core.Refusal{}}
	switch {
	case s.Landed[id]:
		h.State = "landed"
	case s.Ejected[id]:
		h.State = "ejected"
	}
	for _, e := range s.Queued {
		if e.ID == id {
			h.State, h.Commit, h.Ref, h.AdmittedAt = "queued", e.Commit, e.Ref, e.AdmittedAt
		}
	}
	for _, f := range s.InFlight {
		if slices.ContainsFunc(f.Run.Entries, func(e core.Entry) bool { return e.ID == id }) {
			h.Runs = append(h.Runs, f.Run.ID)
		}
	}
	for _, r := range s.Settled {
		if r.ID == id || slices.Contains(r.Batch, id) {
			h.Settled = append(h.Settled, r)
			if h.AdmittedAt.IsZero() && r.ID == id {
				h.AdmittedAt = r.AdmittedAt
			}
		}
	}
	for _, r := range s.Refused {
		if r.ID == id {
			h.Refused = append(h.Refused, r)
		}
	}
	return h, h.State != "unknown" || len(h.Settled) > 0 || len(h.Refused) > 0 || h.Commit != ""
}

func short(sha string) string { return sha[:min(len(sha), 8)] }
