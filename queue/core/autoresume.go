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

// PauseReason classifies a pause by its reason: noVerdict is a no-verdict pause,
// which the cool-down ends once per main; held is one whose auto-resume on its
// main is spent, so only an operator or a new main ends it.
func PauseReason(why string) (noVerdict, held bool) {
	return strings.HasPrefix(why, noVerdictWhy), strings.HasPrefix(why, holdPrefix)
}

// resumeOncePerMain auto-resumes the cooled-down pause once per main sha. On a
// main it already auto-resumed, the pause is held with a reason saying so; a
// manual resume or a new main allows one more auto-resume. It never ejects.
func (d *Driver) resumeOncePerMain(ctx context.Context) error {
	head := "unknown" // without Heads every main counts as the same one
	if h, ok := d.Lander.(Heads); ok {
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
	if _, held := PauseReason(d.s.Why); held {
		return nil
	}
	ev := Event{Kind: PausedEvent, Why: fmt.Sprintf(holdWhy, head[:min(8, len(head))]), At: d.now()}
	d.pause(ev.Why)
	return d.announce(ctx, ev)
}
