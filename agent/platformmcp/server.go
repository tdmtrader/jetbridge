package platformmcp

import (
	"net/http"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

// Server assembles the sidecar: the MCP endpoint at POST /mcp, the pod
// readiness probe at GET /healthz (§8.5 — the F34 healthz-wait-before-first-use
// seam agent-runner's waitForSidecars polls), and the internal checkpoint
// endpoint at POST /checkpoint (Task 14 / §3.2 addendum).
type Server struct {
	cfg    Config
	client *ATCClient
	events *EventLog
	mcp    *mcpserver.Server
	mux    *http.ServeMux

	ckMu   sync.Mutex               // guards ckOpen
	ckOpen map[string]ckReservation // checkpoint name -> open row; same-pod optimization only
}

// ckReservation is the same-pod fast path for a repeated checkpoint POST; the
// cross-pod (continuation) dedup is DB-enforced by agent_run_questions_dedup
// (PARK-V2 §E — the map is an optimization, never the authority).
type ckReservation struct {
	ID      int
	AskedAt int64
}

func NewServer(cfg Config) (*Server, error) {
	events, err := NewEventLog(cfg.EventsPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:    cfg,
		client: NewATCClient(cfg.ATCURL, cfg.PrincipalToken, cfg.TicketID),
		events: events,
		// SSE heartbeat interval from PLATFORM_MCP_PROGRESS_INTERVAL; 0 =
		// mcpserver.DefaultHeartbeat (15s). The SSE-upgraded server keeps a
		// parked ask_human alive past the claude CLI's 60s abandonment (F13).
		mcp:    mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval),
		mux:    http.NewServeMux(),
		ckOpen: map[string]ckReservation{},
	}
	s.registerTools()
	s.mux.Handle("/mcp", s.mcp)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("POST /checkpoint", s.handleCheckpoint)
	return s, nil
}

func (s *Server) Mux() *http.ServeMux { return s.mux }

// ListenAndServe serves /mcp, /healthz, and /checkpoint. SERVER-TIMEOUT RULE
// (D4, 2026-07-09 SSE seam delta — frozen for all three sidecar binaries):
// WriteTimeout and IdleTimeout MUST be 0 — any nonzero WriteTimeout severs
// long SSE streams and blocking /checkpoint responses mid-park.
// ReadHeaderTimeout 5s is allowed (and set) to bound slow-header abuse.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       0,
	}
	return srv.ListenAndServe()
}
