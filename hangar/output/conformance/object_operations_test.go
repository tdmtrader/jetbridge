package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/concourse/concourse/hangar/objectstore"
)

func TestInterruptedUploadDoesNotPublishAPartialObject(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		const key = "operation-contract/interrupted"
		cause := errors.New("source interrupted")
		source := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("partial"), 1024)), brokenSource{cause})
		if _, err := tier.client.CreateAbsent(ctx, tier.bucket, key, nil, source); err == nil {
			t.Fatal("interrupted source reported a committed upload")
		}
		if _, err := tier.client.StatCurrent(ctx, tier.bucket, key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("partial object became visible: %v", err)
		}
		complete, err := tier.client.CreateAbsent(ctx, tier.bucket, key, nil, bytes.NewReader([]byte("complete")))
		if err != nil || complete.Size != 8 {
			t.Fatalf("retry could not publish a complete object: %+v, %v", complete, err)
		}
	})
}

type brokenSource struct{ err error }

func (source brokenSource) Read([]byte) (int, error) { return 0, source.err }

func TestConcurrentCreatesChooseOneImmutableWinner(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		const key = "operation-contract/create-race"
		const competitors = 8
		type result struct {
			attrs objectstore.Attrs
			body  string
			err   error
		}
		results := make(chan result, competitors)
		start := make(chan struct{})
		var wait sync.WaitGroup
		for i := 0; i < competitors; i++ {
			wait.Add(1)
			go func(i int) {
				defer wait.Done()
				<-start
				body := fmt.Sprintf("candidate %d", i)
				attrs, err := tier.client.CreateAbsent(ctx, tier.bucket, key, map[string]string{"candidate": body}, bytes.NewReader([]byte(body)))
				results <- result{attrs, body, err}
			}(i)
		}
		close(start)
		wait.Wait()
		close(results)
		var winner result
		successes := 0
		for result := range results {
			if result.err == nil {
				successes++
				winner = result
			} else if !errors.Is(result.err, objectstore.ErrPreconditionFailed) {
				t.Errorf("unexpected create error: %v", result.err)
			}
		}
		if successes != 1 {
			t.Fatalf("%d creates succeeded at the same key", successes)
		}
		attrs, err := tier.client.StatCurrent(ctx, tier.bucket, key)
		if err != nil {
			t.Fatal(err)
		}
		if attrs.Generation != winner.attrs.Generation || attrs.Metadata["candidate"] != winner.body {
			t.Fatalf("winner bytes and metadata were replaced: %+v", attrs)
		}
		reader, err := tier.client.OpenExact(ctx, tier.bucket, key, winner.attrs.Generation)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || string(body) != winner.body {
			t.Fatalf("winner body = %q, read %v, close %v", body, readErr, closeErr)
		}
	})
}
