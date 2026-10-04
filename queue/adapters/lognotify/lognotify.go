// Package lognotify is a core.Notifier that writes each event as one JSON line.
package lognotify

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Config is the notify section of a queue's config file. Path "-" is stdout.
type Config struct{ Kind, Path string }

var known = map[string][]string{"notify": {"kind", "path"}}

// Parse reads the notify section strictly.
func Parse(n *yaml.Node) (Config, error) {
	if err := config.Strict(n, "notify", known); err != nil {
		return Config{}, err
	}
	var c Config
	if err := n.Decode(&c); err != nil {
		return Config{}, err
	}
	switch {
	case c.Kind != "log":
		return Config{}, fmt.Errorf("notify.kind %q is not allowed; use one of: log", c.Kind)
	case c.Path == "":
		return Config{}, errors.New(`notify.path is required: a file, or "-" for stdout`)
	}
	return c, nil
}

// Notifier writes one line per event; one write at a time.
type Notifier struct {
	mu sync.Mutex
	w  io.Writer
}

func New(w io.Writer) *Notifier { return &Notifier{w: w} }

// Open appends to the file c names, creating it; "-" is stdout.
func Open(c Config) (*Notifier, error) {
	if c.Path == "-" {
		return New(os.Stdout), nil
	}
	f, err := os.OpenFile(c.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return New(f), nil
}

// Close closes the file Open made; stdout and a plain writer stay open.
func (n *Notifier) Close() error {
	if f, ok := n.w.(*os.File); ok && f != os.Stdout {
		return f.Close()
	}
	return nil
}

// entry is one change in full; AdmittedAt is UTC RFC3339, left out when unset.
type entry struct {
	ID         string `json:"id"`
	Commit     string `json:"commit,omitempty"`
	Ref        string `json:"ref,omitempty"`
	AdmittedAt string `json:"admitted_at,omitempty"`
}

func entries(in []core.Entry) []entry {
	var out []entry
	for _, e := range in {
		x := entry{ID: e.ID, Commit: e.Commit, Ref: e.Ref}
		if !e.AdmittedAt.IsZero() {
			x.AdmittedAt = e.AdmittedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, x)
	}
	return out
}

type line struct {
	Time       time.Time      `json:"time"`
	Kind       core.EventKind `json:"kind"`
	Entries    []entry        `json:"entries,omitempty"`
	Run        string         `json:"run,omitempty"`
	RunBase    string         `json:"run_base,omitempty"`
	RunEntries []entry        `json:"run_entries,omitempty"`
	Verdict    core.Verdict   `json:"verdict,omitempty"`
	Why        string         `json:"why,omitempty"`
	Cause      string         `json:"cause,omitempty"`
	Parent     string         `json:"parent,omitempty"`
}

func (n *Notifier) Notify(_ context.Context, e core.Event) error {
	l := line{Time: cmp.Or(e.At, time.Now()).UTC(), Kind: e.Kind, Entries: entries(e.Entries), Run: e.Run.ID,
		RunBase: e.Run.Base, RunEntries: entries(e.Run.Entries), Verdict: e.Verdict, Why: e.Why, Cause: e.Cause, Parent: e.Parent}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err = n.w.Write(append(b, '\n'))
	return err
}
