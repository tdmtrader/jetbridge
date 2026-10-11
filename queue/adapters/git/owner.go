package git

import (
	"context"
	"strings"
	"unicode"
)

// maxOwner bounds an owner's name: it is written to logs and notices.
const maxOwner = 64

// owner is the author name of commit: a name only, never an address; "" if unreadable.
// A name that looks like an address, a URL or a path is replaced whole by "unknown".
func (a *Admissions) owner(ctx context.Context, commit string) string {
	out, err := a.Lander.git(ctx, "log", "-1", "--format=%an", commit)
	if err != nil {
		return ""
	}
	return safeOwner(out)
}

func safeOwner(raw string) string {
	name := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)), " ")
	if strings.ContainsAny(name, `@/\`) || strings.Contains(name, "://") || colonThenText(name) {
		return "unknown"
	}
	if r := []rune(name); len(r) > maxOwner {
		name = string(r[:maxOwner])
	}
	return name
}

func colonThenText(s string) bool {
	i := strings.IndexByte(s, ':')
	for i >= 0 && i+1 < len(s) {
		if s[i+1] != ' ' {
			return true
		}
		j := strings.IndexByte(s[i+1:], ':')
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return false
}
