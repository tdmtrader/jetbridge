package imageresolvertesting

import (
	"context"
	"sync"

	"github.com/concourse/concourse/atc/imageresolver"
)

type FakeResolver struct {
	mutex       sync.Mutex
	resolveStub func(context.Context, string, string, *imageresolver.BasicAuth) (string, error)
	calls       []resolveCall
	returns     struct {
		digest string
		err    error
	}
}

type resolveCall struct {
	Ctx        context.Context
	Repository string
	Tag        string
	Auth       *imageresolver.BasicAuth
}

func (f *FakeResolver) Resolve(ctx context.Context, repository string, tag string, auth *imageresolver.BasicAuth) (string, error) {
	f.mutex.Lock()
	f.calls = append(f.calls, resolveCall{
		Ctx:        ctx,
		Repository: repository,
		Tag:        tag,
		Auth:       auth,
	})
	stub := f.resolveStub
	ret := f.returns
	f.mutex.Unlock()

	if stub != nil {
		return stub(ctx, repository, tag, auth)
	}
	return ret.digest, ret.err
}

func (f *FakeResolver) ResolveCallCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return len(f.calls)
}

func (f *FakeResolver) ResolveArgsForCall(i int) (context.Context, string, string, *imageresolver.BasicAuth) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	c := f.calls[i]
	return c.Ctx, c.Repository, c.Tag, c.Auth
}

func (f *FakeResolver) ResolveReturns(digest string, err error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.returns.digest = digest
	f.returns.err = err
	f.resolveStub = nil
}

func (f *FakeResolver) ResolveStub(stub func(context.Context, string, string, *imageresolver.BasicAuth) (string, error)) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.resolveStub = stub
}
