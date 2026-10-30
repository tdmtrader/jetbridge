package wrappa

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/tedsuo/rata"
)

func NewAccessorWrappa(
	logger lager.Logger,
	accessFactory accessor.AccessFactory,
	customRoles map[string]string,
) *accessorWrappa {
	return &accessorWrappa{
		logger:        logger,
		accessFactory: accessFactory,
		customRoles:   customRoles,
	}
}

type accessorWrappa struct {
	logger        lager.Logger
	accessFactory accessor.AccessFactory
	customRoles   map[string]string
}

func (w *accessorWrappa) Wrap(handlers rata.Handlers) rata.Handlers {
	wrapped := rata.Handlers{}

	for name, handler := range handlers {
		wrapped[name] = accessor.NewHandler(
			w.logger,
			name,
			handler,
			w.accessFactory,
			w.customRoles,
		)
	}

	return wrapped
}
