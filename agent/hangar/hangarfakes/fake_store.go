package hangarfakes

import (
	"context"
	"io"
	"sync"

	"github.com/concourse/concourse/agent/hangar"
)

type EnsureCall struct {
	Context              context.Context
	Kind                 hangar.Kind
	Digest               hangar.Digest
	Source               io.Reader
	MaxUncompressedBytes int64
}

type InspectCall struct {
	Context              context.Context
	Kind                 hangar.Kind
	Digest               hangar.Digest
	MaxUncompressedBytes int64
}

type OpenCall struct {
	Context              context.Context
	Ref                  hangar.ObjectRef
	MaxUncompressedBytes int64
}

type DeleteCall struct {
	Context context.Context
	Ref     hangar.ObjectRef
}

type FakeStore struct {
	mutex sync.Mutex

	EnsureStub  func(context.Context, hangar.Kind, hangar.Digest, io.Reader, int64) (hangar.Attributes, error)
	InspectStub func(context.Context, hangar.Kind, hangar.Digest, int64) (hangar.Attributes, error)
	OpenStub    func(context.Context, hangar.ObjectRef, int64) (io.ReadCloser, hangar.Attributes, error)
	DeleteStub  func(context.Context, hangar.ObjectRef) error

	ensureCalls  []EnsureCall
	inspectCalls []InspectCall
	openCalls    []OpenCall
	deleteCalls  []DeleteCall

	ensureResult struct {
		attributes hangar.Attributes
		err        error
	}
	inspectResult struct {
		attributes hangar.Attributes
		err        error
	}
	openResult struct {
		content    io.ReadCloser
		attributes hangar.Attributes
		err        error
	}
	deleteResult error
}

var _ hangar.Store = (*FakeStore)(nil)

func (fake *FakeStore) Ensure(
	ctx context.Context,
	kind hangar.Kind,
	digest hangar.Digest,
	source io.Reader,
	maxUncompressedBytes int64,
) (hangar.Attributes, error) {
	fake.mutex.Lock()
	fake.ensureCalls = append(fake.ensureCalls, EnsureCall{
		Context:              ctx,
		Kind:                 kind,
		Digest:               digest,
		Source:               source,
		MaxUncompressedBytes: maxUncompressedBytes,
	})
	stub := fake.EnsureStub
	result := fake.ensureResult
	fake.mutex.Unlock()

	if stub != nil {
		return stub(ctx, kind, digest, source, maxUncompressedBytes)
	}
	return result.attributes, result.err
}

func (fake *FakeStore) Inspect(
	ctx context.Context,
	kind hangar.Kind,
	digest hangar.Digest,
	maxUncompressedBytes int64,
) (hangar.Attributes, error) {
	fake.mutex.Lock()
	fake.inspectCalls = append(fake.inspectCalls, InspectCall{
		Context:              ctx,
		Kind:                 kind,
		Digest:               digest,
		MaxUncompressedBytes: maxUncompressedBytes,
	})
	stub := fake.InspectStub
	result := fake.inspectResult
	fake.mutex.Unlock()

	if stub != nil {
		return stub(ctx, kind, digest, maxUncompressedBytes)
	}
	return result.attributes, result.err
}

func (fake *FakeStore) Open(
	ctx context.Context,
	ref hangar.ObjectRef,
	maxUncompressedBytes int64,
) (io.ReadCloser, hangar.Attributes, error) {
	fake.mutex.Lock()
	fake.openCalls = append(fake.openCalls, OpenCall{
		Context:              ctx,
		Ref:                  ref,
		MaxUncompressedBytes: maxUncompressedBytes,
	})
	stub := fake.OpenStub
	result := fake.openResult
	fake.mutex.Unlock()

	if stub != nil {
		return stub(ctx, ref, maxUncompressedBytes)
	}
	return result.content, result.attributes, result.err
}

func (fake *FakeStore) Delete(ctx context.Context, ref hangar.ObjectRef) error {
	fake.mutex.Lock()
	fake.deleteCalls = append(fake.deleteCalls, DeleteCall{Context: ctx, Ref: ref})
	stub := fake.DeleteStub
	result := fake.deleteResult
	fake.mutex.Unlock()

	if stub != nil {
		return stub(ctx, ref)
	}
	return result
}

func (fake *FakeStore) EnsureCalls() []EnsureCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]EnsureCall(nil), fake.ensureCalls...)
}

func (fake *FakeStore) InspectCalls() []InspectCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]InspectCall(nil), fake.inspectCalls...)
}

func (fake *FakeStore) OpenCalls() []OpenCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]OpenCall(nil), fake.openCalls...)
}

func (fake *FakeStore) DeleteCalls() []DeleteCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]DeleteCall(nil), fake.deleteCalls...)
}

func (fake *FakeStore) SetEnsureStub(
	stub func(context.Context, hangar.Kind, hangar.Digest, io.Reader, int64) (hangar.Attributes, error),
) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.EnsureStub = stub
}

func (fake *FakeStore) SetInspectStub(
	stub func(context.Context, hangar.Kind, hangar.Digest, int64) (hangar.Attributes, error),
) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.InspectStub = stub
}

func (fake *FakeStore) SetOpenStub(
	stub func(context.Context, hangar.ObjectRef, int64) (io.ReadCloser, hangar.Attributes, error),
) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.OpenStub = stub
}

func (fake *FakeStore) SetDeleteStub(stub func(context.Context, hangar.ObjectRef) error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.DeleteStub = stub
}

func (fake *FakeStore) EnsureReturns(attributes hangar.Attributes, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.EnsureStub = nil
	fake.ensureResult.attributes = attributes
	fake.ensureResult.err = err
}

func (fake *FakeStore) InspectReturns(attributes hangar.Attributes, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.InspectStub = nil
	fake.inspectResult.attributes = attributes
	fake.inspectResult.err = err
}

func (fake *FakeStore) OpenReturns(content io.ReadCloser, attributes hangar.Attributes, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.OpenStub = nil
	fake.openResult.content = content
	fake.openResult.attributes = attributes
	fake.openResult.err = err
}

func (fake *FakeStore) DeleteReturns(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.DeleteStub = nil
	fake.deleteResult = err
}
