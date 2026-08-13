package main

import (
	"testing"
	"time"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

func TestRetentionPolicyParsing(t *testing.T) {
	t.Run("accepts a class and a duration", func(t *testing.T) {
		var p RetentionPolicy
		for _, arg := range []string{"resource-caches=720h", "reviews=8760h"} {
			if err := p.Set(arg); err != nil {
				t.Fatalf("Set(%q): %v", arg, err)
			}
		}

		if p["resource-caches"] != 720*time.Hour {
			t.Errorf("resource-caches = %v", p["resource-caches"])
		}
		if p["reviews"] != 8760*time.Hour {
			t.Errorf("reviews = %v", p["reviews"])
		}
	})

	t.Run("rejects input that would delete everything or nothing", func(t *testing.T) {
		for _, arg := range []string{
			"resource-caches",         // no duration
			"resource-caches=",        // empty duration
			"resource-caches=0",       // "delete everything now", never intended
			"resource-caches=-1h",     // same, by another route
			"resource-caches=forever", // not a duration
			"=720h",                   // no class
			"a/b=720h",                // a class is one segment, not a path
			"../etc=720h",             // and certainly not a traversal
		} {
			var p RetentionPolicy
			if err := p.Set(arg); err == nil {
				t.Errorf("Set(%q) was accepted; it must be rejected", arg)
			}
		}
	})

	t.Run("rejects a class given twice", func(t *testing.T) {
		var p RetentionPolicy
		if err := p.Set("resource-caches=1h"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := p.Set("resource-caches=2h"); err == nil {
			t.Error("a repeated class was accepted; one of the two would silently win")
		}
	})
}

// Everything uncertain must answer "keep". This function is the only thing
// standing between a configuration mistake and a deleted bucket.
func TestExpiredKeepsWhateverItCannotJustifyDeleting(t *testing.T) {
	policy := RetentionPolicy{"resource-caches": time.Hour}
	now := time.Now()
	old := now.Add(-24 * time.Hour)

	for _, tc := range []struct {
		name string
		attr durable.Attributes
		want bool
	}{
		{
			name: "old, in a configured class",
			attr: durable.Attributes{Key: "resource-caches/rc-abc", Updated: old},
			want: true,
		},
		{
			// The one that would empty a bucket. A backend that fails to report
			// a write time yields the zero value, which reads as 1970 -- so a
			// naive age check treats every object as ancient and deletes the lot
			// on the first pass. Nothing recovers from that but re-running every
			// build that ever produced a cache.
			name: "no timestamp",
			attr: durable.Attributes{Key: "resource-caches/rc-abc"},
			want: false,
		},
		{
			name: "younger than the retention period",
			attr: durable.Attributes{Key: "resource-caches/rc-abc", Updated: now.Add(-time.Minute)},
			want: false,
		},
		{
			name: "exactly at the retention period",
			attr: durable.Attributes{Key: "resource-caches/rc-abc", Updated: now.Add(-time.Hour)},
			want: false,
		},
		{
			// A class nobody configured is a class nobody asked to expire --
			// including one whose name was mistyped in the flag.
			name: "unconfigured class",
			attr: durable.Attributes{Key: "reviews/rc-abc", Updated: old},
			want: false,
		},
		{
			// Predates the class scheme, or came from something else entirely.
			name: "no class prefix",
			attr: durable.Attributes{Key: "rc-abc", Updated: old},
			want: false,
		},
		{
			// A flat key that happens to spell a configured class name. This is
			// the case the no-prefix check uniquely covers: without it, Cut
			// returns the whole key as the "class", it matches the policy, and
			// an object nobody classified gets expired by a rule that was never
			// about it.
			name: "flat key spelling a configured class",
			attr: durable.Attributes{Key: "resource-caches", Updated: old},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.expired(tc.attr, now); got != tc.want {
				t.Errorf("expired = %v, want %v", got, tc.want)
			}
		})
	}
}

// With no policy at all, nothing is ever reclaimed -- whatever its age, class or
// timestamp. This is the state a daemon runs in until an operator opts in.
func TestAnEmptyPolicyExpiresNothing(t *testing.T) {
	var empty RetentionPolicy
	now := time.Now()

	for _, key := range []string{"resource-caches/rc-abc", "rc-abc", "anything/at-all"} {
		attr := durable.Attributes{Key: key, Updated: now.Add(-10000 * time.Hour)}
		if empty.expired(attr, now) {
			t.Errorf("an unset policy expired %q", key)
		}
	}
}
