package atccmd

import (
	"net/http"

	legacyoutcomes "github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	workflowoutcomesapi "github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/outcomewatcher"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
)

func buildWorkflowOutcomeAPI(
	team db.Team,
	store workflowoutcomesapi.Store,
	authorizer workflowoutcomesapi.Authorizer,
) (*workflowoutcomesapi.Handler, error) {
	return workflowoutcomesapi.NewHandler(workflowoutcomesapi.HandlerConfig{
		TeamID:   team.ID(),
		TeamName: team.Name(),
		Identity: func(request *http.Request) (string, error) {
			return workflowRunCreatorIdentity(accessor.GetAccessor(request).UserInfo())
		},
		Store:      store,
		Authorizer: authorizer,
	})
}

func buildLegacyGenericOutcomeProjector(
	team db.Team,
	resolver outcomewatcher.GenericOutputResolver,
	store workflowoutcomesapi.Store,
	options ...outcomewatcher.DurableGenericProjectorOption,
) (*outcomewatcher.DurableGenericProjector, error) {
	return outcomewatcher.NewDurableGenericProjector(team.ID(), team.Name(), resolver, store, options...)
}

// buildAgentOutcomeWatcher composes the database-only terminal projection in
// every ATC. cache is optional: a nil cache deliberately disables live Git
// merge detection without disabling durable generic outcome reconciliation.
func buildAgentOutcomeWatcher(
	team db.Team,
	ticketsStore tickets.Store,
	legacyStore legacyoutcomes.Store,
	resolver outcomewatcher.GenericOutputResolver,
	genericStore workflowoutcomesapi.Store,
	cache *outcomewatcher.MirrorCache,
	projectorOptions ...outcomewatcher.DurableGenericProjectorOption,
) (*outcomewatcher.Watcher, error) {
	projector, err := buildLegacyGenericOutcomeProjector(team, resolver, genericStore, projectorOptions...)
	if err != nil {
		return nil, err
	}
	return outcomewatcher.New(
		ticketsStore,
		legacyStore,
		cache,
		outcomewatcher.WithGenericProjector(projector),
	), nil
}
