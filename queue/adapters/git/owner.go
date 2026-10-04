package git

import (
	"context"
	"strings"

	"github.com/concourse/concourse/queue/core"
)

// maxOwner bounds an owner's name: it is written to logs and notices.
const maxOwner = 64

// owner is the author name of commit: a name only, never the email address; "" if unreadable.
func (a *Admissions) owner(ctx context.Context, commit string) string {
	out, err := a.Lander.git(ctx, "log", "-1", "--format=%an", commit)
	if err != nil {
		return ""
	}
	name := core.Redact(strings.Join(strings.Fields(out), " "))
	if r := []rune(name); len(r) > maxOwner {
		name = string(r[:maxOwner])
	}
	return name
}
