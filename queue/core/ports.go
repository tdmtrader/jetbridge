package core

import "context"

// Verdict is a Runner's one result for a candidate: pass, fail, or none.
type Verdict string

// Decision is what the core does with a batch: land, eject, split, retry, pause, recompose.
type Decision string

// Store loads and saves entries. Save is a compare-and-swap on version.
type Store interface {
	Load(ctx context.Context) (entries []Entry, version string, err error)
	Save(ctx context.Context, entries []Entry, version string) error
}

// Composer merges a batch onto main into a candidate, or names the conflicting entry.
type Composer interface {
	Compose(ctx context.Context, main string, batch []Entry) (candidate string, err error)
}

// Runner tests a candidate commit and reports one Verdict.
type Runner interface {
	Run(ctx context.Context, candidate string) (Verdict, error)
}

// Lander fast-forwards main to the candidate and refuses if main moved.
type Lander interface {
	Land(ctx context.Context, main, candidate string) error
}

// Notifier tells an entry's owner what was decided about it.
type Notifier interface {
	Notify(ctx context.Context, entry Entry, decision Decision) error
}
