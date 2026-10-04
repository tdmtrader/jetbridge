package core

import (
	"context"
	"fmt"
	"strings"
)

const (
	autoResumeWhy = "auto-resume after cool-down"
	holdWhy       = "no verdict twice on main %s; nothing auto-resumes it — run `queue resume`"
	holdPrefix    = "no verdict twice on "
)

// MainHeader is an optional Lander capability: the sha main is at now. Without
// it every main counts as the same one.
type MainHeader interface {
	Head(ctx context.Context, main string) (string, error)
}

// noVerdict reports whether a pause's reason is one a main move may still auto-resume.
func noVerdict(why string) bool {
	return strings.HasPrefix(why, noVerdictWhy) || strings.HasPrefix(why, holdPrefix)
}

// resumeOncePerMain auto-resumes the cooled-down pause once per main sha. On a
// main it already auto-resumed, the pause is held with a reason saying so; a
// manual resume or a new main allows one more auto-resume. It never ejects.
func (d *Driver) resumeOncePerMain(ctx context.Context) error {
	head := "unknown"
	if h, ok := d.Lander.(MainHeader); ok {
		var err error
		if head, err = h.Head(ctx, d.Main); err != nil {
			d.logf("auto-resume: main: %v", err)
			return nil
		}
	}
	if d.s.ResumedOnMain != head {
		d.s.ResumedOnMain = head
		return d.resume(ctx, d.s.PauseSeq, autoResumeWhy)
	}
	if strings.HasPrefix(d.s.Why, holdPrefix) {
		return nil
	}
	ev := Event{Kind: PausedEvent, Why: fmt.Sprintf(holdWhy, head[:min(8, len(head))]), At: d.now()}
	d.pause(ev.Why)
	d.settled(ev)
	if err := d.save(ctx); err != nil {
		return err
	}
	d.notify(ctx, ev)
	return nil
}
