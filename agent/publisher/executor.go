package publisher

import "context"

// Executor is the explicit external side-effect boundary used by the
// publish_snapshot engine step. GitService and WorkItemService both implement
// it; a Router selects between those concrete services by publisher type.
type Executor interface {
	Execute(context.Context, Request) (Publication, error)
}

type Router struct {
	git      Executor
	workItem Executor
}

func NewRouter(git Executor, workItem Executor) (*Router, error) {
	if nilInterface(git) || nilInterface(workItem) {
		return nil, ErrInvalidRequest
	}
	return &Router{git: git, workItem: workItem}, nil
}

func (router *Router) Execute(ctx context.Context, request Request) (Publication, error) {
	if router == nil {
		return Publication{}, ErrInvalidRequest
	}
	switch request.Publisher {
	case GitPublisher:
		return router.git.Execute(ctx, request)
	case WorkItemPublisher:
		return router.workItem.Execute(ctx, request)
	default:
		return Publication{}, ErrInvalidRequest
	}
}

var _ Executor = (*GitService)(nil)
var _ Executor = (*WorkItemService)(nil)
var _ Executor = (*Router)(nil)
