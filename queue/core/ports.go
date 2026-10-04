package core

import (
	"context"
	"time"
)

// Verdict is a Runner's one result for a candidate: pass, fail, or none.
type Verdict string

// Decision is what the core does with a batch: land, eject, split, retry, pause, recompose.
type Decision string

// Snapshot is all the driver keeps between steps, as plain data. Version is what the Store loaded.
type Snapshot struct {
	Version  string
	Queued   []Entry // admission order
	BuildsOn map[string][]string
	Landed   map[string]bool
	Ejected  map[string]bool
	InFlight []Flight
	Paused   bool
	PauseSeq uint64            // counts the transitions into paused; a resume names the pause it ends
	Why      string            // why the queue is paused
	PausedAt time.Time         // when it paused; zero in a snapshot saved before auto-resume
	Landing  *Landing          // a land asked of the Lander and not yet recorded
	Fence    uint64            // the highest fence handed out: lease token<<32 | sequence
	Refused  []Refusal         // the latest MaxRefused admissions refused, oldest first
	Commits  map[string]string // the commit each id was admitted at, kept once it settles
	Settled  []SettleRecord    // the latest MaxSettled lands, ejects, pauses and flakes, oldest first

	ResumedOnMain string // the main sha the last auto-resume was on; empty in a snapshot saved before it existed
}

const MaxRefused = 20

// MaxSettled bounds Snapshot.Settled: weeks of a busy queue in a small snapshot.
const MaxSettled = 1000

// SettleRecord is one land, eject, refusal, pause or flake kept in the Snapshot.
// At is UTC from the driver's clock; Batch holds the ids settled together; a
// pause of no entry has ID "". A flaky batch is one record: ID joins its ids.
type SettleRecord struct {
	ID, Commit string
	Kind       EventKind
	At         time.Time
	AdmittedAt time.Time // when the entry was admitted; zero for a pause of none
	Why, Cause string
	Run        string
	Batch      []string
	Owner      string // the owner of the entry, kept so an eject says whom it concerns
	Base       string // the sha the run was tested on; kept on a recompose
	Failure
	Waited time.Duration `json:",omitempty"` // a wait-cap record: how long its run waited
}

// Failure is what a red run's log named: the failing test names (at most
// MaxFailed) and the job and build the runner tested on. Kept in an eject or flake record.
type Failure struct {
	Failed   []string `json:",omitempty"`
	FailedOn string   `json:",omitempty"`
}

const MaxFailed = 20

// Refusal is a change refused at admission: never queued, its reason kept.
type Refusal struct{ ID, Commit, Why string }

// Pending is a change waiting to be admitted; a Why refuses it.
type Pending struct {
	ID, Commit string
	BuildsOn   []string
	Why        string
	Owner      string   // the name of whoever admitted it
	Ancestors  []string // the commits of the other pending changes in its history, refused ones too
}

// Admissions is where changes wait. Pending lists each after those it builds
// on, given the queued entries; Done removes one, only if it is still at sha.
type Admissions interface {
	Pending(ctx context.Context, queued []Entry) ([]Pending, error)
	Done(ctx context.Context, id, sha string) error
}

// ResumeRequest asks to end pause number Seq (Snapshot.PauseSeq); SHA is what the request holds.
type ResumeRequest struct {
	Seq uint64
	SHA string
	Why string // if set, the request is refused: recorded and deleted, and it ends no pause
}

// Resumes is where an operator's requests to resume wait: Pending lists them;
// Done removes one only if it still holds the same SHA.
type Resumes interface {
	Pending(ctx context.Context) ([]ResumeRequest, error)
	Done(ctx context.Context, r ResumeRequest) error
}

// Landing is saved before Land is called and cleared once recorded, so a driver
// that finds one on load asks the Lander whether main moved. Fence is the one
// the Land was asked with; its top 32 bits are the lease token.
type Landing struct {
	Main      string
	Candidate string
	Entries   []Entry
	Fence     uint64
}

// Flight is a started run and the candidate it tests. BaseSHA is the sha the
// candidate was composed on: main's head, or the base run's candidate; "" when
// main's head could not be read and the run composed on the branch name.
// Ahead holds the IDs composed ahead of the run's entries, from its base run.
type Flight struct {
	Run       Run
	Candidate string
	BaseSHA   string
	Started   time.Time // when the run started; zero in state saved before it was kept
	Ahead     []string  `json:",omitempty"`
	Build     string    `json:",omitempty"` // a Resumer runner's hold on the run's build; "" in state saved before it was kept
	Fence     uint64    `json:",omitempty"` // the snapshot's fence when the run started: a run started again has a higher one
}

// Store loads and saves the Snapshot. Save is a compare-and-swap: it refuses if
// the stored snapshot is no longer s.Version, or if token is below the current
// lease's. Acquire takes or renews the queue's one lease for ttl, refusing
// while another owner's is unexpired; a new owner gets a greater Token.
type Store interface {
	Load(ctx context.Context) (Snapshot, error)
	Save(ctx context.Context, token uint64, s Snapshot) (version string, err error)
	Acquire(ctx context.Context, owner string, ttl time.Duration) (Lease, error)
}

// Lease is the right to drive a queue until Expires.
type Lease struct {
	Owner   string
	Token   uint64
	Expires time.Time
}

// Composer merges entries onto base into a candidate, or names the conflicting
// entry with a ConflictError. ComposeVerdict reads any other error as None.
type Composer interface {
	Compose(ctx context.Context, base string, entries []Entry) (candidate string, err error)
}

// Runner tests a candidate. Starting an ID again on another candidate replaces its earlier run. Poll
// reports the Verdict once done; an error, or no result inside the runner's
// cap, is no verdict: never red.
type Runner interface {
	Start(ctx context.Context, run Run, candidate string) error
	Poll(ctx context.Context, runID string) (v Verdict, done bool, err error)
}

// Resumer is an optional Runner capability: Build is its hold on a run, saved in
// the Flight; Resume gives it back in a new process, so no build is triggered twice.
type Resumer interface {
	Build(runID string) string
	Resume(runID, build string)
}

// WaitCapper is an optional Runner capability: whether a run's no verdict was
// its wait cap running out, recorded with how long the run waited.
type WaitCapper interface {
	Expired(runID string) bool
}

// Errorer is an optional Runner capability: whether a run's no verdict was its
// test job erroring (cancelled or timed out), recorded as such.
type Errorer interface {
	Errored(runID string) bool
}

// FailureReporter is an optional Runner capability: the distinct test names a
// failed run's log gave, as a hint only. Empty when there are none.
type FailureReporter interface {
	FailedTests(runID string) []string
}

// Heads reads the sha a branch points at now. A Lander that is also Heads has
// main read at the start of every Step, so each run composes on main's sha and
// a verdict is used only if its run was tested on the main it would land on.
type Heads interface {
	Head(ctx context.Context, branch string) (sha string, err error)
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

// EventKind names what an Event announces.
type EventKind string

const (
	BatchStarted   EventKind = "batch-started"
	VerdictIn      EventKind = "verdict"
	LandedEvent    EventKind = "landed"
	EjectedEvent   EventKind = "ejected"
	FlakeEvent     EventKind = "flaky"
	PausedEvent    EventKind = "paused"
	ResumedEvent   EventKind = "resumed"
	RefusedEvent   EventKind = "refused"
	RecomposeEvent EventKind = "recompose"
	WaitCapEvent   EventKind = "wait-cap" // a run gave no verdict inside its runner's wait cap
	ErroredEvent   EventKind = "errored"  // a run's test job errored: no verdict, at once
)

// Event is one thing the driver announces. Why, Cause and Parent are copied
// from the Settle; Run is empty when no run was involved.
type Event struct {
	Kind    EventKind
	Entries []Entry
	Run     Run
	Verdict Verdict
	Why     string
	Cause   string
	Parent  string
	At      time.Time // when the driver announced it, from its clock
	Base    string    // the sha the run was tested on; set on a recompose
	Failure
}

// Notifier announces events; its error is logged and dropped, never changing a decision.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}
