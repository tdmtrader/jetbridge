package core

import (
	"regexp"
	"slices"
)

var userinfo = regexp.MustCompile(`://[^/\s]*@`)

// Redact hides the user and password of every URL in text: scheme://user:pass@ becomes scheme://***@.
func Redact(text string) string { return userinfo.ReplaceAllString(text, "://***@") }

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
