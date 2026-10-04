package core

import (
	"cmp"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// userinfo is a URL's user and password: after :// (or :\/\/, escaped once or more), up to the last @
// before the first whitespace, / \ " ' , ? or #. It is a best-effort net for text no config
// named: config refuses a URL holding credentials, and every registered secret is hidden by value.
var userinfo = regexp.MustCompile(`(://|:(?:\\+/){2})(?:[^\s/?#@"\\',]*@)+`)

// Redact hides every configured secret in any encoding, then the user and password of every
// other URL: scheme://user:pass@ becomes scheme://***@. It is the one filter every reason
// passes through before the driver saves, announces or logs it, and every output line.
func Redact(text string) string { return Secrets.Redact(text) }

// Secrets holds the process's configured secrets; Redact hides each of them in every encoding.
var Secrets = &SecretSet{}

// SecretSet is a set of secrets and the encoded forms they take in text; safe for concurrent use.
type SecretSet struct {
	mu    sync.RWMutex
	forms []string // longest first, so a longer form wins where two start together
}

var unescapeHTML = strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">")

// Add registers s raw, query- and path-escaped, as URL userinfo, JSON-escaped (with and
// without HTML escapes, and with escaped slashes) and Go-quoted, then each escaped once more.
// A secret shorter than 4 bytes is ignored: hiding it would mangle ordinary text.
func (r *SecretSet) Add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(s) < 4 || slices.Contains(r.forms, s) {
		return
	}
	forms := []string{s, url.QueryEscape(s), url.PathEscape(s), url.User(s).String(), url.UserPassword("", s).String()[1:]}
	for range 2 {
		for _, f := range forms {
			b, _ := json.Marshal(f) // a string always marshals
			q, h := strconv.Quote(f), string(b[1:len(b)-1])
			forms = append(forms, q[1:len(q)-1])
			for _, j := range []string{h, unescapeHTML.Replace(h)} {
				forms = append(forms, j, strings.ReplaceAll(j, "/", `\/`))
			}
		}
	}
	r.forms = append(r.forms, forms...)
	slices.SortFunc(r.forms, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	r.forms = slices.Compact(r.forms)
}

// Redact replaces every registered form with ***, then hides the userinfo of any other URL.
func (r *SecretSet) Redact(text string) string {
	r.mu.RLock()
	for _, f := range r.forms {
		text = strings.ReplaceAll(text, f, "***")
	}
	r.mu.RUnlock()
	return userinfo.ReplaceAllString(text, "${1}***@")
}

// RedactSnapshot is s with its free-text fields redacted: the pause reason and each settle
// record's and refusal's Why and Cause. Nothing else is touched: ids, map keys, commits, refs,
// BuildsOn and Ejected are identifiers the queue matches on, and a changed one loses state.
// s itself is not changed.
func RedactSnapshot(s Snapshot) Snapshot {
	s.Why, s.Settled, s.Refused = Redact(s.Why), slices.Clone(s.Settled), slices.Clone(s.Refused)
	for i := range s.Settled {
		s.Settled[i].Why, s.Settled[i].Cause = Redact(s.Settled[i].Why), Redact(s.Settled[i].Cause)
	}
	for i := range s.Refused {
		s.Refused[i].Why = Redact(s.Refused[i].Why)
	}
	return s
}
