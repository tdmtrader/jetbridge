package pipelinerunserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
)

// A result read spools the whole archive, twice, into web's scratch until the
// response is written. Readers beyond the configured bound are refused with a
// retryable 503 rather than queued without limit.

type completedRun struct{ db.PipelineRun }

func (completedRun) ID() int               { return 7 }
func (completedRun) Status() atc.RunStatus { return atc.RunStatusSucceeded }

type completedRuns struct{ db.PipelineRunFactory }

func (completedRuns) GetRun(db.Pipeline, int) (db.PipelineRun, bool, error) {
	return completedRun{}, true, nil
}

// gatedResults blocks every Read until released, and counts reads in flight.
type gatedResults struct {
	archive  string
	release  chan struct{}
	inFlight atomic.Int32
	peak     atomic.Int32
	reads    atomic.Int32
}

func (g *gatedResults) Read(ctx context.Context, _ int, _ string) (*hangar.CapturedTree, error) {
	g.reads.Add(1)
	now := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		peak := g.peak.Load()
		if now <= peak || g.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &hangar.CapturedTree{ArchivePath: g.archive, ByteSize: 3}, nil
}

func TestResultReadsAreBoundedAndRefusedWhenSaturated(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "canonical.tar")
	if err := os.WriteFile(archive, []byte("tar"), 0600); err != nil {
		t.Fatal(err)
	}
	results := &gatedResults{archive: archive, release: make(chan struct{})}
	server := NewServer(lagertest.NewTestLogger("test"), completedRuns{}, "")
	server.SetServices(Services{Results: results, ResultReadConcurrency: 2})
	handler := server.GetPipelineRunResult(templatePipeline{})

	api := httptest.NewServer(accessor.NewHandler(lagertest.NewTestLogger("test"), atc.GetPipelineRunResult, handler, memberAccess{}, silentAuditor{}, nil))
	defer api.Close()
	client := api.Client()
	client.Timeout = 10 * time.Second
	get := func() *http.Response {
		response, err := client.Get(api.URL + "/api/v1/teams/t/pipelines/review/runs/1/results/findings?:team_name=t&:pipeline_name=review&:number=1&:result_name=findings")
		if err != nil {
			return &http.Response{StatusCode: -1, Body: io.NopCloser(strings.NewReader(err.Error()))}
		}
		return response
	}

	type outcome struct {
		status int
		body   string
	}
	held := make(chan outcome, 2)
	for range 2 {
		go func() {
			response := get()
			body, _ := io.ReadAll(response.Body)
			held <- outcome{response.StatusCode, string(body)}
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for results.inFlight.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("two reads never started; in flight = %d", results.inFlight.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}

	refused := get()
	if refused.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third read status = %d, want 503 while two are in flight", refused.StatusCode)
	}
	if refused.Header.Get("Retry-After") == "" {
		t.Error("a saturated refusal must say when to retry")
	}
	if results.reads.Load() != 2 {
		t.Errorf("reads = %d: a refused request must not reach the reader", results.reads.Load())
	}

	close(results.release)
	for range 2 {
		got := <-held
		if got.status != http.StatusOK || got.body != "tar" {
			t.Errorf("held read = %d %q, want 200 \"tar\"", got.status, got.body)
		}
	}
	if results.peak.Load() != 2 {
		t.Errorf("peak concurrent reads = %d, want 2", results.peak.Load())
	}

	// Slots are returned once a response is written.
	after := get()
	if after.StatusCode != http.StatusOK {
		t.Fatalf("read after release = %d, want 200", after.StatusCode)
	}
}

func TestResultReadBoundDefaultsWhenUnset(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("test"), completedRuns{}, "")
	server.SetServices(Services{})
	if got := cap(server.resultSlots); got != DefaultResultReadConcurrency {
		t.Fatalf("unset bound = %d slots, want the default %d", got, DefaultResultReadConcurrency)
	}
	server.SetResultReader(&gatedResults{})
	if got := cap(server.resultSlots); got != DefaultResultReadConcurrency {
		t.Fatalf("SetResultReader dropped the bound: %d slots", got)
	}
}
