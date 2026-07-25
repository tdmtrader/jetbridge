package contracts

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type SelectionBody struct {
	Selected   string                `json:"selected"`
	Candidates []CandidateAssessment `json:"candidates"`
	Rationale  string                `json:"rationale"`
}

type CandidateAssessment struct {
	ID      string       `json:"id"`
	Rank    int          `json:"rank"`
	Summary string       `json:"summary"`
	Scores  []NamedScore `json:"scores"`
}

type NamedScore struct {
	ID    string `json:"id"`
	Score Score  `json:"score"`
}

// candidacy is the set of input ports a selecting step was allowed to choose
// between, together with the label naming where that set came from.
//
// It exists because candidacy is the one load-bearing validator input for
// selection/v1 that is not in the sealed bytes, and the two gates have to source
// it from two different places. Making it a value with two constructors — rather
// than a flag threaded into one predicate — means the sourcing decision is made
// once, at a named entry point, and cannot be re-decided halfway down.
type candidacy struct {
	ports map[string]struct{}
	label string
}

// declaredCandidacy is the SEAL-TIME source: the server's own port declarations,
// compiled from atc.SnapshotInputConfig.Candidate. A producer's record cannot
// influence it, which is the whole point — candidacy is authority, and a
// producer's claims never create authority.
func declaredCandidacy(ports []string) candidacy {
	set := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		set[port] = struct{}{}
	}
	return candidacy{ports: set, label: "declared candidate port"}
}

// sealedCandidacy is the READ-TIME source: the candidate roles on the sealed
// subjects.
//
// Trusting them here does NOT hand a producer authority. Seal-time admission
// already refused to seal these bytes unless every candidate-role subject was a
// declared candidate port and every declared candidate port had a subject, so
// the sealed roles are a platform-certified copy of the declaration. The
// alternative — keeping candidacy only in the process memory of the sealing step
// — makes every stored selection permanently unreadable, which is a defect in
// itself.
func sealedCandidacy(subjects []Subject) candidacy {
	set := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if subject.Role == SubjectRoleCandidate {
			set[subject.Input] = struct{}{}
		}
	}
	return candidacy{ports: set, label: "sealed candidate subject port"}
}

func (c candidacy) covers(port string) bool {
	_, found := c.ports[port]
	return found
}

func (c candidacy) sorted() []string {
	ports := make([]string, 0, len(c.ports))
	for port := range c.ports {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

// AdmitForSeal is the SEAL-TIME gate for a selection body. Candidacy comes from
// the server-side port declarations the platform compiled for the producing
// step, never from the record.
//
// The integrity property is that a judge may only select from what it was
// exposed to: the subject set must be exactly the set of declared candidate
// ports, one subject per port. Non-candidate inputs may be exposed alongside
// them — a base repository, a rubric, a prior review — but are not selectable
// and must not appear as subjects.
func (body SelectionBody) AdmitForSeal(subjects []Subject, declarations snapshot.ValidationContext) error {
	candidatePorts := declarations.CandidatePorts()
	if len(candidatePorts) == 0 {
		return fmt.Errorf("selection requires at least one declared candidate input port")
	}
	return body.validate(subjects, declaredCandidacy(candidatePorts))
}

// RevalidateSealed is the READ-TIME gate for a selection body. Candidacy comes
// from the sealed subject roles that seal-time admission already certified, so a
// stored selection re-validates with no live declarations at all.
func (body SelectionBody) RevalidateSealed(subjects []Subject) error {
	return body.validate(subjects, sealedCandidacy(subjects))
}

func (body SelectionBody) validate(subjects []Subject, candidates candidacy) error {
	if err := ValidateIdentifier("selected", body.Selected); err != nil {
		return err
	}
	if strings.TrimSpace(body.Rationale) == "" {
		return fmt.Errorf("rationale is required")
	}
	if len(subjects) == 0 {
		return fmt.Errorf("selection requires at least one candidate subject")
	}
	candidateType := subjects[0].Type
	subjectIDs := make(map[string]struct{}, len(subjects))
	// claimedPorts is one-subject-per-port coverage, not uniqueness. Uniqueness of
	// subject.Input is already an envelope invariant for every record type
	// (record.go validateEnvelopeShape), and both of this validator's callers run
	// the envelope gate first, so a second duplicate check here would be
	// unreachable. TestSelectionDuplicateCandidatePortIsRejectedByTheEnvelopeGate
	// pins the rule at the layer that actually enforces it.
	claimedPorts := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if !candidates.covers(subject.Input) {
			return fmt.Errorf("selection subject %q input %q is not a %s", subject.ID, subject.Input, candidates.label)
		}
		if subject.Role != SubjectRoleCandidate {
			return fmt.Errorf("selection subject %q must have candidate role", subject.ID)
		}
		claimedPorts[subject.Input] = struct{}{}
		if subject.Type != candidateType {
			return fmt.Errorf("selection candidate subjects must have one common snapshot type")
		}
		subjectIDs[subject.ID] = struct{}{}
	}
	for _, port := range candidates.sorted() {
		if _, claimed := claimedPorts[port]; !claimed {
			return fmt.Errorf("%s %q has no selection subject", candidates.label, port)
		}
	}
	ids := make([]string, len(body.Candidates))
	ranks := make(map[int]struct{}, len(body.Candidates))
	selectedCount := 0
	for index, candidate := range body.Candidates {
		ids[index] = candidate.ID
		if _, found := subjectIDs[candidate.ID]; !found {
			return fmt.Errorf("candidates[%d].id %q is not a declared candidate subject", index, candidate.ID)
		}
		if candidate.ID == body.Selected {
			selectedCount++
		}
		if candidate.Rank < 1 || candidate.Rank > len(body.Candidates) {
			return fmt.Errorf("candidates[%d].rank must be contiguous from one", index)
		}
		if _, found := ranks[candidate.Rank]; found {
			return fmt.Errorf("candidates[%d].rank %d is duplicate", index, candidate.Rank)
		}
		ranks[candidate.Rank] = struct{}{}
		if strings.TrimSpace(candidate.Summary) == "" {
			return fmt.Errorf("candidates[%d].summary is required", index)
		}
		scoreIDs := make([]string, len(candidate.Scores))
		for scoreIndex, named := range candidate.Scores {
			scoreIDs[scoreIndex] = named.ID
			if err := named.Score.Validate(); err != nil {
				return fmt.Errorf("candidates[%d].scores[%d]: %w", index, scoreIndex, err)
			}
		}
		if err := ValidateEntityIDs(fmt.Sprintf("candidates[%d].scores", index), scoreIDs); err != nil {
			return err
		}
	}
	if err := ValidateEntityIDs("candidates", ids); err != nil {
		return err
	}
	if len(body.Candidates) != len(subjects) {
		return fmt.Errorf("candidates must assess every candidate subject exactly once")
	}
	if selectedCount != 1 {
		return fmt.Errorf("selected candidate must occur exactly once in candidates")
	}
	return nil
}

// ResolveSelectionForSeal turns an admitted selection into the exact snapshot
// reference the judge chose.
//
// It is a seal-time operation by construction: resolving the choice into a live
// reference needs the step's input bindings, which only exist while the step is
// running. A reader that only wants to know what was chosen reads the sealed
// subject, whose type and digest are inside the sealed bytes.
func ResolveSelectionForSeal(record Record[SelectionBody], declarations snapshot.ValidationContext) (snapshot.SnapshotRef, error) {
	if err := admitSelectionRecordForSeal(record, declarations); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	for _, subject := range record.Subjects {
		if subject.ID == record.Body.Selected {
			ref, found := declarations.Input(subject.Input)
			if !found {
				return snapshot.SnapshotRef{}, fmt.Errorf("selected candidate input %q is unavailable", subject.Input)
			}
			return ref, nil
		}
	}
	return snapshot.SnapshotRef{}, fmt.Errorf("selected candidate %q is not declared", record.Body.Selected)
}

func admitSelectionRecordForSeal(record Record[SelectionBody], declarations snapshot.ValidationContext) error {
	if err := record.AdmitForSeal(selectionType, declarations); err != nil {
		return err
	}
	// AdmitForSeal has already bound every subject to an exposed input with an
	// exactly matching type and digest. What remains is candidate-port coverage,
	// which SelectionBody.AdmitForSeal reads from the same server-side
	// declaration. Requiring subjects to cover every exposed input — the rule
	// this replaces — made a judge unable to receive anything but candidates.
	return record.Body.AdmitForSeal(record.Subjects, declarations)
}

// ReadSealedSelectionRecord re-validates one stored selection/v1 tree at the
// READ-TIME gate, with no candidate-port declarations available. This is the
// entry point that makes a sealed selection readable at all: the declarations
// that admitted it existed only in the sealing step's process memory.
func ReadSealedSelectionRecord(ctx context.Context, root *os.Root) (Record[SelectionBody], error) {
	record, err := readSealedRecord[SelectionBody](ctx, root, selectionType)
	if err != nil {
		return Record[SelectionBody]{}, err
	}
	if err := record.Body.RevalidateSealed(record.Subjects); err != nil {
		return Record[SelectionBody]{}, fmt.Errorf("snapshot contracts: selection record: %w", err)
	}
	return record, nil
}

type selectionValidator struct{}

func (selectionValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[SelectionBody](ctx, root, selectionType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := record.Body.AdmitForSeal(record.Subjects, declarations); err != nil {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: selection record: %w", err)
	}
	return snapshot.ValidationResult{}, nil
}

func (selectionValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	_, err := ReadSealedSelectionRecord(ctx, root)
	return snapshot.ValidationResult{}, err
}
