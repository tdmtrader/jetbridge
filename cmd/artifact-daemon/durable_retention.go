package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// RetentionPolicy maps a retention class to how long objects in it are kept.
//
// # Why the daemon may hold this without breaking the seam
//
// The daemon is forbidden from deciding what deserves to be kept — that needs to
// know whether an artifact is re-derivable and whether anything will ask for it
// again, which is the ATC's knowledge. This is not that. A class here is an
// opaque grouping string the daemon never interprets: the operator said
// "whatever lives under this prefix expires after N", and the daemon applies it.
// It is exactly what a bucket lifecycle rule does, moved into a process that can
// also do it for a filesystem store, and that can be held to one source of truth.
//
// # An unconfigured class is kept forever
//
// A class with no entry is never reclaimed. Silence has to mean "keep", because
// the alternative is that a typo in a class name deletes a bucket.
type RetentionPolicy map[string]time.Duration

// Set parses one "class=duration" pair. It implements flag.Value so the flag can
// repeat, one class per occurrence.
func (p *RetentionPolicy) Set(value string) error {
	class, age, found := strings.Cut(value, "=")
	if !found {
		return fmt.Errorf("expected CLASS=DURATION, got %q", value)
	}

	class = strings.Trim(strings.TrimSpace(class), "/")
	if err := durable.ValidateKey(class); err != nil || strings.Contains(class, "/") {
		return fmt.Errorf("invalid retention class %q: must be a single path segment", class)
	}

	d, err := time.ParseDuration(strings.TrimSpace(age))
	if err != nil {
		return fmt.Errorf("invalid duration for class %q: %w", class, err)
	}
	if d <= 0 {
		// Zero would mean "delete everything immediately", which is never what
		// somebody means to type, and is unrecoverable once it has run.
		return fmt.Errorf("retention for class %q must be positive, got %s", class, d)
	}

	if *p == nil {
		*p = RetentionPolicy{}
	}
	if existing, dup := (*p)[class]; dup {
		return fmt.Errorf("retention for class %q given twice (%s and %s)", class, existing, d)
	}
	(*p)[class] = d

	return nil
}

func (p *RetentionPolicy) String() string {
	if p == nil || *p == nil {
		return ""
	}

	pairs := make([]string, 0, len(*p))
	for class, age := range *p {
		pairs = append(pairs, class+"="+age.String())
	}
	sort.Strings(pairs)

	return strings.Join(pairs, ",")
}

// expired reports whether an object should be reclaimed, and is deliberately
// conservative: every uncertainty answers "keep".
//
// The zero-timestamp case is the one that matters. A backend that fails to
// report a write time yields the zero value, which reads as 1970 — so a naive
// age check would treat every object in the store as ancient and delete the lot
// on the first pass. Nothing recovers from that except re-running every build
// that ever produced a cache.
func (p RetentionPolicy) expired(a durable.Attributes, now time.Time) bool {
	if len(p) == 0 {
		return false
	}

	class, _, found := strings.Cut(a.Key, "/")
	if !found {
		// No class prefix: nothing claims this object, so nothing expires it.
		return false
	}

	maxAge, configured := p[class]
	if !configured {
		return false
	}

	if a.Updated.IsZero() {
		return false
	}

	return now.Sub(a.Updated) > maxAge
}
