package outcomewatcher

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrGenericOutputAmbiguous                = errors.New("generic workflow outcome: ambiguous output")
	ErrGenericOutputTeamMismatch             = errors.New("generic workflow outcome: team mismatch")
	ErrGenericOutputPortSelectionUnsupported = errors.New("generic workflow outcome: explicit output port selection is unsupported")
)

const legacyProjectionLabel = "legacy-ticket-projection"

type TerminalFactKind string

const (
	TerminalMerged          TerminalFactKind = "merged"
	TerminalMergedWithFixes TerminalFactKind = "merged_with_fixes"
	TerminalAbandoned       TerminalFactKind = "abandoned"
	TerminalConcluded       TerminalFactKind = "concluded"
	TerminalSentBack        TerminalFactKind = "sent_back"
)

type TerminalFact struct {
	TicketID          int
	Kind              TerminalFactKind
	Actor             string
	HumanIntervention bool
}

// GenericProjector bridges a terminal compatibility ticket fact into the
// durable, exact workflow-run output model. A nil projector leaves the legacy
// watcher unchanged.
type GenericProjector interface {
	Project(context.Context, TerminalFact) error
}

type GenericOutputLink struct {
	TeamID           int
	WorkflowRunID    snapshot.WorkflowRunID
	OutputSnapshotID snapshot.SnapshotID
}

type GenericOutputResolver interface {
	// The final boolean is retained for compatibility with the first adapter
	// revision. Implementations must not use it to infer an output from type;
	// multi-output resolution requires GenericOutputPortResolver instead.
	ResolveLegacyTicketOutput(
		ctx context.Context,
		teamID int,
		teamName string,
		ticketID int,
		preferRepositoryChange bool,
	) (GenericOutputLink, bool, error)
}

// GenericOutputPortResolver is the optional exact-port extension used when a
// workflow adapter explicitly designates which declared public output owns a
// terminal disposition. GenericOutputResolver remains the compatibility seam
// for workflows that have exactly one output.
type GenericOutputPortResolver interface {
	ResolveLegacyTicketOutputAtPort(
		ctx context.Context,
		teamID int,
		teamName string,
		ticketID int,
		portName string,
	) (GenericOutputLink, bool, error)
}

type DispositionOutputSelector interface {
	// SelectDispositionOutput returns a declared public workflow output port.
	// selected=false leaves deterministic single-output compatibility lookup in
	// place; it never authorizes a type- or position-based multi-output guess.
	SelectDispositionOutput(context.Context, TerminalFact) (portName string, selected bool, err error)
}

type DispositionOutputSelectorFunc func(context.Context, TerminalFact) (portName string, selected bool, err error)

func (selector DispositionOutputSelectorFunc) SelectDispositionOutput(
	ctx context.Context,
	fact TerminalFact,
) (string, bool, error) {
	return selector(ctx, fact)
}

type DurableGenericProjectorOption func(*DurableGenericProjector) error

func WithDispositionOutputSelector(selector DispositionOutputSelector) DurableGenericProjectorOption {
	return func(projector *DurableGenericProjector) error {
		if selector == nil {
			return fmt.Errorf("generic workflow outcome: disposition output selector is required")
		}
		projector.outputSelector = selector
		return nil
	}
}

type DurableGenericProjector struct {
	teamID         int
	teamName       string
	resolver       GenericOutputResolver
	store          workflowoutcomes.Store
	outputSelector DispositionOutputSelector
}

func NewDurableGenericProjector(
	teamID int,
	teamName string,
	resolver GenericOutputResolver,
	store workflowoutcomes.Store,
	options ...DurableGenericProjectorOption,
) (*DurableGenericProjector, error) {
	if teamID <= 0 || strings.TrimSpace(teamName) == "" {
		return nil, fmt.Errorf("generic workflow outcome: trusted team is required")
	}
	if resolver == nil || store == nil {
		return nil, fmt.Errorf("generic workflow outcome: resolver and store are required")
	}
	projector := &DurableGenericProjector{teamID: teamID, teamName: teamName, resolver: resolver, store: store}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(projector); err != nil {
			return nil, err
		}
	}
	return projector, nil
}

func (projector *DurableGenericProjector) Project(ctx context.Context, fact TerminalFact) error {
	if ctx == nil {
		return fmt.Errorf("generic workflow outcome: context is required")
	}
	if fact.TicketID <= 0 || strings.TrimSpace(fact.Actor) == "" {
		return fmt.Errorf("generic workflow outcome: invalid terminal fact")
	}
	disposition, publication, humanModified, err := mapTerminalFact(fact.Kind)
	if err != nil {
		return err
	}
	link, found, err := projector.resolveOutput(ctx, fact)
	if err != nil || !found {
		return err
	}
	if link.TeamID != projector.teamID {
		return ErrGenericOutputTeamMismatch
	}
	if err := link.WorkflowRunID.Validate(); err != nil {
		return fmt.Errorf("generic workflow outcome: invalid resolved run: %w", err)
	}
	if err := link.OutputSnapshotID.Validate(); err != nil {
		return fmt.Errorf("generic workflow outcome: invalid resolved output: %w", err)
	}
	existing, exists, err := projector.store.Get(
		ctx, projector.teamID, link.WorkflowRunID, link.OutputSnapshotID,
	)
	if err != nil {
		return err
	}
	if exists && !hasOutcomeLabel(existing.Labels, legacyProjectionLabel) {
		// Generic API/publisher state is authoritative once it owns the exact
		// output. The compatibility bridge must not clobber human labels or
		// exact publication evidence on each reconciliation tick.
		return nil
	}
	interventionCount := 0
	if fact.HumanIntervention {
		interventionCount = 1
	}
	_, _, err = projector.store.Record(ctx, projector.teamID, workflowoutcomes.RecordRequest{
		WorkflowRunID:     link.WorkflowRunID,
		OutputSnapshotID:  link.OutputSnapshotID,
		Disposition:       disposition,
		PublicationState:  publication,
		HumanModified:     humanModified,
		InterventionCount: interventionCount,
		Labels:            []string{legacyProjectionLabel},
		Actor:             fact.Actor,
	})
	return err
}

func (projector *DurableGenericProjector) resolveOutput(
	ctx context.Context,
	fact TerminalFact,
) (GenericOutputLink, bool, error) {
	if projector.outputSelector != nil {
		portName, selected, err := projector.outputSelector.SelectDispositionOutput(ctx, fact)
		if err != nil {
			return GenericOutputLink{}, false, err
		}
		if selected {
			if portName == "" || portName != strings.TrimSpace(portName) {
				return GenericOutputLink{}, false, fmt.Errorf("generic workflow outcome: invalid disposition output port %q", portName)
			}
			resolver, ok := projector.resolver.(GenericOutputPortResolver)
			if !ok {
				return GenericOutputLink{}, false, ErrGenericOutputPortSelectionUnsupported
			}
			return resolver.ResolveLegacyTicketOutputAtPort(
				ctx,
				projector.teamID,
				projector.teamName,
				fact.TicketID,
				portName,
			)
		}
	}
	return projector.resolver.ResolveLegacyTicketOutput(
		ctx,
		projector.teamID,
		projector.teamName,
		fact.TicketID,
		false,
	)
}

func hasOutcomeLabel(labels []string, wanted string) bool {
	for _, label := range labels {
		if label == wanted {
			return true
		}
	}
	return false
}

func mapTerminalFact(kind TerminalFactKind) (
	workflowoutcomes.Disposition,
	workflowoutcomes.PublicationState,
	bool,
	error,
) {
	switch kind {
	case TerminalMerged:
		return workflowoutcomes.DispositionMerged, workflowoutcomes.PublicationNotRequested, false, nil
	case TerminalMergedWithFixes:
		return workflowoutcomes.DispositionMerged, workflowoutcomes.PublicationNotRequested, true, nil
	case TerminalAbandoned:
		return workflowoutcomes.DispositionAbandoned, workflowoutcomes.PublicationNotRequested, false, nil
	case TerminalConcluded:
		return workflowoutcomes.DispositionAccepted, workflowoutcomes.PublicationNotRequested, false, nil
	case TerminalSentBack:
		return workflowoutcomes.DispositionRejected, workflowoutcomes.PublicationNotRequested, false, nil
	default:
		return "", "", false, fmt.Errorf("generic workflow outcome: unsupported terminal fact %q", kind)
	}
}
