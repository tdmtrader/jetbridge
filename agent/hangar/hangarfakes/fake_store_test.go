package hangarfakes_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
)

func TestFakeStoreRecordsConcurrentCallsAndReturnsCopies(t *testing.T) {
	t.Parallel()

	fake := &hangarfakes.FakeStore{
		EnsureStub: func(context.Context, hangar.Kind, hangar.Digest, io.Reader, int64) (hangar.Attributes, error) {
			return hangar.Attributes{}, nil
		},
	}

	const calls = 32
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			_, _ = fake.Ensure(
				context.Background(),
				hangar.KindSnapshot,
				hangar.Digest("sha256:"+strings.Repeat("a", 64)),
				bytes.NewReader(nil),
				1024,
			)
		}()
	}
	wg.Wait()

	recorded := fake.EnsureCalls()
	if len(recorded) != calls {
		t.Fatalf("recorded %d calls, want %d", len(recorded), calls)
	}
	recorded[0].MaxUncompressedBytes = -1
	if fake.EnsureCalls()[0].MaxUncompressedBytes != 1024 {
		t.Fatal("EnsureCalls exposed the fake's mutable backing slice")
	}
}
