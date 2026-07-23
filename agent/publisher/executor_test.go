package publisher_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
)

type routingExecutor struct {
	calls    int
	response publisher.Publication
}

func (executor *routingExecutor) Execute(context.Context, publisher.Request) (publisher.Publication, error) {
	executor.calls++
	return executor.response, nil
}

func TestRouterSelectsOnlyTheDeclaredPublisher(t *testing.T) {
	git := &routingExecutor{response: publisher.Publication{ID: 1}}
	workItem := &routingExecutor{response: publisher.Publication{ID: 2}}
	router, err := publisher.NewRouter(git, workItem)
	if err != nil {
		t.Fatal(err)
	}

	publication, err := router.Execute(context.Background(), branchRequest())
	if err != nil || publication.ID != 1 || git.calls != 1 || workItem.calls != 0 {
		t.Fatalf("git route = (%+v, %v), calls git=%d work-item=%d", publication, err, git.calls, workItem.calls)
	}
	request := branchRequest()
	request.Publisher = publisher.WorkItemPublisher
	request.Mode = publisher.ModeComment
	request.Parameters = map[string]string{"body": "review complete"}
	publication, err = router.Execute(context.Background(), request)
	if err != nil || publication.ID != 2 || workItem.calls != 1 {
		t.Fatalf("work-item route = (%+v, %v), calls=%d", publication, err, workItem.calls)
	}
}

func TestRouterFailsClosedForMissingOrUnknownServices(t *testing.T) {
	if _, err := publisher.NewRouter(nil, &routingExecutor{}); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("nil service error = %v", err)
	}
	router, err := publisher.NewRouter(&routingExecutor{}, &routingExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	request.Publisher = "unknown/v1"
	if _, err := router.Execute(context.Background(), request); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("unknown publisher error = %v", err)
	}
}
