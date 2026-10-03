package runclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
)

func TestWaitReturnsATerminalRun(t *testing.T) {
	p := newResultPlatform(t)
	succeeded, handle := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	got, err := p.client(t).Wait(bounded(t), handle, 10*time.Millisecond)
	if err != nil || got.ID != succeeded.ID || !got.Succeeded() || got.Settled() != atc.RunStatusSucceeded {
		t.Fatalf("a succeeded Run was observed as %+v, %v", got, err)
	}

	failed := p.Admit(t, p.team, resultTemplate)
	handle = Handle{Team: p.team, Template: resultTemplate, Number: failed.Number}
	// Wait is polling the running Run when it fails.
	c, ctx := p.client(t), bounded(t)
	done := make(chan error, 1)
	go func() {
		var err error
		got, err = c.Wait(ctx, handle, 10*time.Millisecond)
		done <- err
	}()
	p.Fail(t, p.team, resultTemplate, failed.Number)
	if err := <-done; err != nil || got.ID != failed.ID || got.Succeeded() || got.Settled() != atc.RunStatusFailed {
		t.Fatalf("a failed Run was observed as %+v, %v", got, err)
	}
}

func TestWaitEndsWithItsContextWhileTheRunIsRunning(t *testing.T) {
	p := newResultPlatform(t)
	run := p.Admit(t, p.team, resultTemplate)
	handle := Handle{Team: p.team, Template: resultTemplate, Number: run.Number}
	c := p.client(t)

	// The interval is far longer than the deadline: Wait must return on the
	// context, not on its next poll.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := c.Wait(ctx, handle, time.Hour)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context's deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("Wait returned %s after its deadline", elapsed)
	}
	if got.ID != run.ID || got.Status != atc.RunStatusRunning {
		t.Fatalf("the last observation is not the running Run: %+v", got)
	}

	if _, err := c.Wait(bounded(t), handle, 0); err == nil {
		t.Fatal("accepted a zero poll interval")
	}
}
