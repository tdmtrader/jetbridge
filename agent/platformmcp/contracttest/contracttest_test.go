package contracttest_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
	"github.com/concourse/concourse/agent/platformmcp/contracttest"
)

func TestPlatformMCPContract(t *testing.T) {
	stub := contracttest.NewStubATC(t, 42)
	stub.AutoAnswer("yes", "contract-bot", 50*time.Millisecond)

	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:         stub.URL(),
		PrincipalToken: contracttest.StubToken,
		TicketID:       42,
		BuildID:        1,
		StepName:       "contract",
		TimeoutPolicy:  "park",
		ListenAddr:     ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)

	endpoint := httptest.NewServer(srv.Mux())
	defer endpoint.Close()

	contracttest.Run(t, endpoint.URL)
}

// TestPlatformMCPSSEHeartbeats (D4/D7, 2026-07-09 SSE seam delta): a server
// with a fast heartbeat and a slow-answering stub must stream >= 2
// notifications/progress frames (spaced < 30s) before the final result frame
// on a parked ask_human. This is the contract-kit twin of gateway 10 Task 7's
// 40s-fake-adapter test; the REAL claude-CLI cadence is pinned by Task 18b.
func TestPlatformMCPSSEHeartbeats(t *testing.T) {
	stub := contracttest.NewStubATC(t, 42)
	stub.AutoAnswer("yes", "contract-bot", 500*time.Millisecond)

	srv, err := platformmcp.NewServer(platformmcp.Config{
		ATCURL:           stub.URL(),
		PrincipalToken:   contracttest.StubToken,
		TicketID:         42,
		BuildID:          1,
		StepName:         "contract",
		TimeoutPolicy:    "park",
		ListenAddr:       ":0",
		ProgressInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.TunePolling(50*time.Millisecond, 20*time.Millisecond)

	endpoint := httptest.NewServer(srv.Mux())
	defer endpoint.Close()

	contracttest.RunSSEHeartbeats(t, endpoint.URL)
}
