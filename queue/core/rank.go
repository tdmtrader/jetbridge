package core

import (
	"cmp"
	"context"
	"path"
	"slices"
	"strings"
)

// ChangeLister is an optional Composer capability: the files each entry
// changed since it forked from main, by ID. It is read for hints only.
type ChangeLister interface {
	Changed(ctx context.Context, entries []Entry) (map[string][]string, error)
}

// Rank orders a red batch's entries by how well the failed test names point at
// the directories of the files each changed: per name, the longest such
// directory it holds, summed. Most first, batch order on a tie; an entry no
// name points at is left out. It is a hint only: a suspect is ejected only
// after it fails on its own.
func Rank(failed []string, batch []Entry, changed map[string][]string) []string {
	type scored struct {
		id    string
		score int
	}
	var out []scored
	for _, e := range batch {
		score := 0
		for _, name := range failed {
			best := 0
			for _, f := range changed[e.ID] {
				if d := path.Dir(f); d != "." && strings.Contains(name, d) {
					best = max(best, len(d))
				}
			}
			score += best
		}
		if score > 0 {
			out = append(out, scored{e.ID, score})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int { return cmp.Compare(b.score, a.score) })
	ids := make([]string, len(out))
	for i, s := range out {
		ids[i] = s.id
	}
	return ids
}
