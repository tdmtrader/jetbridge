package core

import "context"

// Verdict is a Runner's one result for a candidate: pass, fail, or none.
type Verdict string

// Decision is what the core does with a batch: land, eject, split, retry, pause, recompose.
type Decision string

// Landing is saved before Land is called and cleared once recorded, so a driver
// that finds one on load asks the Lander whether main moved. Fence is the one
// the Land was asked with; its top 32 bits are the lease token.
type Landing struct {
	Main      string
	Candidate string
	Entries   []Entry
	Fence     uint64
}

// Composer merges entries onto base into a candidate, or names the conflicting
// entry with a ConflictError. ComposeVerdict reads any other error as None.
type Composer interface {
	Compose(ctx context.Context, base string, entries []Entry) (candidate string, err error)
}

// Lander fast-forwards main to the candidate and refuses if main moved.
// Contains reports whether main already holds the candidate. Both take a
// fence, higher on every call even from the same lease holder, and refuse one
// below the highest seen, so once Contains returns no earlier Land, however
// delayed, can move main. The git adapter moves a lease ref to the fence in the
// same atomic push as main.
type Lander interface {
	Land(ctx context.Context, main, candidate string, fence uint64) error
	Contains(ctx context.Context, main, candidate string, fence uint64) (bool, error)
}
