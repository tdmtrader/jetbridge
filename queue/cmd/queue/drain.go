package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/core"
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
