package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
	"github.com/concourse/concourse/queue/wire"
)

// drainLines writes what drain prints: "ROW <id> <sha> inflight|queued", the
// rows in flight (a land in progress too) first and then the queued ones in
// queue order; one "PUSH idle|mid-flight"; one "LEASE none" or
// "LEASE <holder> [<run id>]", the run being the first in flight.
func drainLines(s core.Snapshot, l core.Lease, now time.Time, out io.Writer) {
	seen := map[string]bool{}
	row := func(e core.Entry, state string) {
		if !seen[e.ID] {
			seen[e.ID] = true
			fmt.Fprintf(out, "ROW %s %s %s\n", e.ID, e.Commit, state)
		}
	}
	for _, f := range s.InFlight {
		for _, e := range f.Run.Entries {
			row(e, "inflight")
		}
	}
	push := "idle"
	if s.Landing != nil {
		push = "mid-flight"
		for _, e := range s.Landing.Entries {
			row(e, "inflight")
		}
	}
	for _, e := range s.Queued {
		row(e, "queued")
	}
	fmt.Fprintf(out, "PUSH %s\n", push)
	holder := strings.Join(strings.Fields(l.Owner), "_")
	if holder == "" || !now.Before(l.Expires) {
		fmt.Fprintln(out, "LEASE none")
		return
	}
	if len(s.InFlight) > 0 {
		holder += " " + s.InFlight[0].Run.ID
	}
	fmt.Fprintln(out, "LEASE "+holder)
}

// loadConfig reads --config, or for health and drain without it the queue resource's
// source JSON ({"source": {...}}) from --source or stdin, as the resource does.
func loadConfig(verb, file, srcFile string) (config.Config, func(), error) {
	none := func() {}
	if verb != "drain" && verb != "health" || file != "" {
		if srcFile != "" {
			return config.Config{}, none, errors.New("--source is for health or drain without --config")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return config.Config{}, none, err
		}
		c, err := config.Parse(data)
		return c, none, err
	}
	in := io.Reader(os.Stdin)
	if srcFile != "" {
		f, err := os.Open(srcFile)
		if err != nil {
			return config.Config{}, none, err
		}
		defer f.Close()
		in = f
	}
	var req struct{ Source wire.Source }
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return config.Config{}, none, errors.New("the source is not valid JSON")
	}
	cleanup, err := wire.SSHKey(req.Source)
	if err != nil {
		return config.Config{}, none, err
	}
	c, err := wire.LoadConfig(req.Source)
	if err != nil {
		cleanup()
		return config.Config{}, none, err
	}
	return c, cleanup, nil
}
