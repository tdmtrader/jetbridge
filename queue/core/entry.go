package core

import "time"

// Entry is one admitted change waiting to be tested and landed.
type Entry struct {
	ID         string
	Commit     string
	Ref        string // the branch the change is on
	AdmittedAt time.Time
}

// State: Queued from admission until settled as Landed or Ejected (final).
type State string

const (
	Queued  State = "queued"
	Landed  State = "landed"
	Ejected State = "ejected"
)
