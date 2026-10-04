package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxViewBytes is the largest view the panel accepts.
const MaxViewBytes = 64 << 10

// userinfo is a URL's user and password: after :// (or :\/\/, escaped once or more), up to the last @
// before the first whitespace, / ? or #. Quotes, commas and backslashes count as password: it fails
// closed, hiding too much rather than too little. It is the fallback for secrets not in Secrets.
var userinfo = regexp.MustCompile(`(://|:(?:\\+/){2})[^\s/?#]*@`)

// Redact hides every configured secret in any encoding, then the user and password of every
// other URL: scheme://user:pass@ becomes scheme://***@. It is the one filter every reason
// passes through before the driver saves, announces or logs it, and every output line.
func Redact(text string) string { return Secrets.Redact(text) }

// Secrets holds the process's configured secrets; Redact hides each of them in every encoding.
var Secrets = &SecretSet{}

// SecretSet is a set of secrets and the encoded forms they take in text; safe for concurrent use.
type SecretSet struct {
	mu    sync.RWMutex
	forms []string // longest first, so a longer form wins where two start together
}

var unescapeHTML = strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">")

// Add registers s raw, query- and path-escaped, as URL userinfo, JSON-escaped (with and
// without HTML escapes, and with escaped slashes) and Go-quoted, then each escaped once more.
// A secret shorter than 4 bytes is ignored: hiding it would mangle ordinary text.
func (r *SecretSet) Add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(s) < 4 || slices.Contains(r.forms, s) {
		return
	}
	forms := []string{s, url.QueryEscape(s), url.PathEscape(s), url.User(s).String(), url.UserPassword("", s).String()[1:]}
	for range 2 {
		for _, f := range forms {
			b, _ := json.Marshal(f) // a string always marshals
			q, h := strconv.Quote(f), string(b[1:len(b)-1])
			forms = append(forms, q[1:len(q)-1])
			for _, j := range []string{h, unescapeHTML.Replace(h)} {
				forms = append(forms, j, strings.ReplaceAll(j, "/", `\/`))
			}
		}
	}
	r.forms = append(r.forms, forms...)
	slices.SortFunc(r.forms, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	r.forms = slices.Compact(r.forms)
}

// Redact replaces every registered form with ***, then hides the userinfo of any other URL.
func (r *SecretSet) Redact(text string) string {
	r.mu.RLock()
	for _, f := range r.forms {
		text = strings.ReplaceAll(text, f, "***")
	}
	r.mu.RUnlock()
	return userinfo.ReplaceAllString(text, "${1}***@")
}

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

// MarshalSnapshot encodes s with every string value redacted, never a key, a timestamp or the
// JSON syntax, and refuses unless the bytes decode back to the same shape and values.
func MarshalSnapshot(s Snapshot) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	v = redactValues(v)
	var back Snapshot
	out, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(out, &back)
	}
	if err == nil {
		out, err = json.Marshal(back)
	}
	if err != nil {
		return nil, fmt.Errorf("redacted snapshot: %w", err)
	}
	if again, err := decodeJSON(out); err != nil || !reflect.DeepEqual(again, v) {
		return nil, errors.New("redacted snapshot does not decode to the same state; not writing it")
	}
	return out, nil
}

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// redactValues redacts each string in v, keeping map keys and timestamps as they are.
func redactValues(v any) any {
	switch t := v.(type) {
	case string:
		if _, err := time.Parse(time.RFC3339Nano, t); err != nil {
			return Redact(t)
		}
	case []any:
		for i := range t {
			t[i] = redactValues(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = redactValues(t[k])
		}
	}
	return v
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
