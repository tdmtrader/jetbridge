package core

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// MaxViewBytes is the largest view the panel accepts.
const MaxViewBytes = 64 << 10

// userinfo is a URL's user and password: after :// (or the JSON-escaped :\/\/), up to the last @
// before the first whitespace, / \ " ? or #. Commas, quotes and the like are userinfo, so they are hidden.
var userinfo = regexp.MustCompile(`(://|:\\/\\/)(?:[^\s/?#@"\\]*@)+`)

// Redact hides the user and password of every URL in text: scheme://user:pass@ becomes scheme://***@.
// It is the one filter every reason passes through before the driver saves, announces or logs it.
func Redact(text string) string { return userinfo.ReplaceAllString(text, "${1}***@") }

// RedactSnapshot is s with the reasons it holds redacted; s itself is not changed.
func RedactSnapshot(s Snapshot) Snapshot {
	s.Why, s.Settled, s.Refused = Redact(s.Why), slices.Clone(s.Settled), slices.Clone(s.Refused)
	for i := range s.Settled {
		s.Settled[i].Why = Redact(s.Settled[i].Why)
	}
	for i := range s.Refused {
		s.Refused[i].Why = Redact(s.Refused[i].Why)
	}
	return s
}

type viewRow struct {
	ID    string            `json:"id"`
	Style string            `json:"style,omitempty"`
	Cells map[string]string `json:"cells"`
	Tip   string            `json:"tip,omitempty"`
}

type viewGroup struct {
	Key   string    `json:"key"`
	Title string    `json:"title"`
	Rows  []viewRow `json:"rows"`
	Empty string    `json:"empty"`
}

type viewTile struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Alert  bool   `json:"alert,omitempty"`
}

// PanelView folds the queue into the panel's generic view/v1 JSON, at most
// MaxViewBytes: each list is cut with a "+N more" row, the same way every time.
// A list is at most the panel's 200 rows, the marker among them. URL passwords are hidden.
func PanelView(s Snapshot, sum Summary, now time.Time) ([]byte, error) {
	s = RedactSnapshot(s)
	groups := viewGroups(s)
	for keep := 199; ; keep /= 2 {
		out, err := marshalView(s, sum, now, groups, keep)
		if err != nil || len(out) <= MaxViewBytes {
			return out, err
		}
		if keep == 0 {
			return nil, errors.New("view: too large to publish")
		}
	}
}

func marshalView(s Snapshot, sum Summary, now time.Time, groups []viewGroup, keep int) ([]byte, error) {
	cut := make([]viewGroup, len(groups))
	for i, g := range groups {
		cut[i] = g
		if n := len(g.Rows) - keep; n > 0 {
			cut[i].Rows = append(slices.Clone(g.Rows[:keep]), viewRow{ID: fmt.Sprintf("+%d more", n), Style: "dim", Cells: map[string]string{}})
		}
	}
	badge, style := fmt.Sprintf("%d queued", sum.Queued), "green"
	banner := ""
	if s.Paused {
		badge, style, banner = "paused", "red", "The queue is paused: "+cmp.Or(s.Why, "no reason given")
	}
	why := fmt.Sprintf("%d testing, %d queued, %d landed and %d ejected in the window", sum.InFlight, sum.Queued, sum.Landed, ejects(sum))
	return json.Marshal(map[string]any{
		"schema": "view/v1", "badge": badge, "badge_style": style, "summary": why, "banner": banner,
		"as_of": now.Unix(), "stale_after": 600,
		"columns": []map[string]string{{"key": "id", "heading": "Change"}, {"key": "commit", "heading": "Commit", "type": "sha"}, {"key": "note", "heading": "Note"}, {"key": "at", "heading": "When", "type": "age"}},
		"legend": []map[string]string{{"style": "blue", "label": "testing"}, {"style": "green", "label": "landed"},
			{"style": "red", "label": "ejected"}, {"style": "amber", "label": "flake", "meaning": "failed in a batch, passed alone"}},
		"groups": cut, "tiles": viewTiles(sum), "tables": []any{},
	})
}

func ejects(sum Summary) int {
	e := sum.Ejected
	return e.Culprit + e.ParentEjected + e.Refused + e.Other
}

func viewTiles(sum Summary) []viewTile {
	wait := func(sec float64) string { return (time.Duration(sec) * time.Second).String() }
	e := sum.Ejected
	return []viewTile{
		{Label: "Landed per hour", Value: fmt.Sprintf("%.1f", sum.LandedPerHour), Detail: fmt.Sprintf("%d landed", sum.Landed)},
		{Label: "Admitted per hour", Value: fmt.Sprint(sum.Admitted)},
		{Label: "Ejects", Value: fmt.Sprint(ejects(sum)), Detail: fmt.Sprintf("culprit %d, parent ejected %d, refused %d, other %d", e.Culprit, e.ParentEjected, e.Refused, e.Other), Alert: ejects(sum) > 0},
		{Label: "Flakes", Value: fmt.Sprint(sum.Flakes), Alert: sum.Flakes > 0},
		{Label: "Median time in queue", Value: wait(sum.MedianQueueSeconds)},
		{Label: "P90 time in queue", Value: wait(sum.P90QueueSeconds)},
		{Label: "Depth", Value: fmt.Sprint(sum.Queued), Detail: fmt.Sprintf("%d testing", sum.InFlight)},
	}
}

func viewGroups(s Snapshot) []viewGroup {
	cells := func(id, commit, note string, at time.Time) map[string]string {
		c := map[string]string{"id": id, "commit": commit, "note": note}
		if !at.IsZero() {
			c["at"] = fmt.Sprint(at.Unix())
		}
		return c
	}
	testing := viewGroup{Key: "testing", Title: "Testing", Empty: "Nothing is being tested."}
	for _, f := range s.InFlight {
		testing.Rows = append(testing.Rows, viewRow{ID: f.Run.ID, Style: "blue", Cells: cells(f.Run.ID, f.Candidate, "being tested", time.Time{})})
	}
	queued := viewGroup{Key: "queued", Title: "Queued, in order", Empty: "The queue is empty."}
	for _, e := range s.Queued {
		r := viewRow{ID: e.ID, Cells: cells(e.ID, e.Commit, "", e.AdmittedAt)}
		if p := s.BuildsOn[e.ID]; len(p) > 0 {
			r.Cells["note"] = "builds on " + strings.Join(p, ", ")
		}
		queued.Rows = append(queued.Rows, r)
	}
	landed := viewGroup{Key: "landed", Title: "Recently landed", Empty: "Nothing has landed yet."}
	ejected := viewGroup{Key: "ejected", Title: "Ejected", Empty: "Nothing has been ejected."}
	for _, r := range slices.Backward(s.Settled) {
		note := strings.Trim(r.Cause+": "+r.Why, ": ")
		row := viewRow{ID: r.ID, Cells: cells(r.ID, r.Commit, note, r.At), Tip: r.Why}
		switch r.Kind {
		case LandedEvent:
			row.Style = "green"
			landed.Rows = append(landed.Rows, row)
		case EjectedEvent, RefusedEvent:
			row.Style = "red"
			ejected.Rows = append(ejected.Rows, row)
		case FlakeEvent:
			row.Style, row.Cells["note"] = "amber", "flake: "+cmp.Or(r.Why, "failed in a batch, passed alone")
			ejected.Rows = append(ejected.Rows, row)
		}
	}
	return []viewGroup{testing, queued, landed, ejected}
}
