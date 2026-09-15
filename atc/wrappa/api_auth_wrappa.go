package wrappa

import (
	"fmt"

	"github.com/concourse/concourse/atc/api/auth"
	"github.com/tedsuo/rata"
)

type APIAuthWrappa struct {
	checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
	checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
}

func NewAPIAuthWrappa(
	checkPipelineAccessHandlerFactory auth.CheckPipelineAccessHandlerFactory,
	checkBuildReadAccessHandlerFactory auth.CheckBuildReadAccessHandlerFactory,
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory,
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory,
) *APIAuthWrappa {
	return &APIAuthWrappa{
		checkPipelineAccessHandlerFactory:   checkPipelineAccessHandlerFactory,
		checkBuildReadAccessHandlerFactory:  checkBuildReadAccessHandlerFactory,
		checkBuildWriteAccessHandlerFactory: checkBuildWriteAccessHandlerFactory,
		checkWorkerTeamAccessHandlerFactory: checkWorkerTeamAccessHandlerFactory,
	}
}

func (wrappa *APIAuthWrappa) Wrap(handlers rata.Handlers) rata.Handlers {
	wrapped := rata.Handlers{}

	rejector := auth.UnauthorizedRejector{}

	for name, handler := range handlers {
		newHandler := handler

		kind, known := auth.AuthorizationKindForAction(name)
		if !known {
			panic(fmt.Sprintf("you missed a spot: %q", name))
		}
		switch kind {
		case auth.AuthorizationBuildRead:
			newHandler = wrappa.checkBuildReadAccessHandlerFactory.AnyJobHandler(handler, rejector)
		case auth.AuthorizationBuildOutput:
			newHandler = wrappa.checkBuildReadAccessHandlerFactory.CheckIfPrivateJobHandler(handler, rejector)
		case auth.AuthorizationBuildWrite:
			newHandler = wrappa.checkBuildWriteAccessHandlerFactory.HandlerFor(handler, rejector)
		case auth.AuthorizationPipelineRead:
			newHandler = wrappa.checkPipelineAccessHandlerFactory.HandlerFor(handler, rejector)
		case auth.AuthorizationAuthenticated:
			newHandler = auth.CheckAuthenticationHandler(handler, rejector)
		case auth.AuthorizationDelegated:
			newHandler = auth.CheckAuthenticationIfProvidedHandler(handler, rejector)
		case auth.AuthorizationAdmin:
			newHandler = auth.CheckAdminHandler(handler, rejector)
		case auth.AuthorizationTeam:
			newHandler = auth.CheckAuthorizationHandler(handler, rejector)
		default:
			panic(fmt.Sprintf("you missed an authorization kind: %v", kind))
		}

		wrapped[name] = newHandler
	}

	return wrapped
}
