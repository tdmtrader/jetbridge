package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// AheadOf is a Strategy that names the queued entries that would land before
// es now. Its ejects stand only if each run was composed with exactly those
// entries ahead of the ejected ones (SameAhead); any other is a recompose.
type AheadOf interface {
	WouldLandBefore(v View, es []Entry) []string
}

// aheadOf is what a run composed on base has ahead of its own entries: base's
// entries and all ahead of them. A run composed on main has none.
func aheadOf(base Flight) []string {
	out := slices.Clone(base.Ahead)
	for _, e := range base.Run.Entries {
		out = append(out, e.ID)
	}
	return out
}

// ejectRefused checks an eject of es by run f at the moment of use: main is
// read again, and must still be the main the driver holds; then, under an
// AheadOf Strategy, the entries f was composed with ahead of es (those landed
// since are in main, which the first check proves) must be exactly those that
// would land before es now. It returns why the eject is refused, or "".
func (d *Driver) ejectRefused(ctx context.Context, f Flight, st Settle) string {
	if f.Run.ID == "" || st.Cause == ParentEjected {
		return "" // no run's red: an ancestor was ejected
	}
	if h, ok := d.Lander.(Heads); ok {
		sha, err := h.Head(ctx, d.Main)
		if err != nil {
			return fmt.Sprintf("main cannot be read before the eject: %v", err)
		}
		if sha != d.main {
			return fmt.Sprintf("main moved %s to %s", cmp.Or(d.main, "unknown"), sha)
		}
	}
	a, ok := d.st.(AheadOf)
	if !ok {
		return ""
	}
	tested := []string{}
	for _, id := range append(slices.Clone(f.Ahead), aheadOf(Flight{Run: f.Run})...) {
		if !d.s.Landed[id] && !slices.ContainsFunc(st.Entries, func(e Entry) bool { return e.ID == id }) {
			tested = append(tested, id)
		}
	}
	if same, why := SameAhead(tested, a.WouldLandBefore(d.view(), st.Entries)); !same {
		return why
	}
	return ""
}
