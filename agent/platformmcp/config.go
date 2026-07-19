// Package platformmcp is the platform-mcp sidecar: the agent's mid-flight
// interaction surface with the platform (shared contracts §3.2). It serves
// MCP streamable HTTP on MCP_LISTEN_ADDR and calls the ATC API with its
// per-run principal token. All tools operate on the ticket in AGENT_TICKET_ID
// — agents cannot address other tickets.
package platformmcp

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ATCURL         string // ATC_EXTERNAL_URL (required)
	PrincipalToken string // AGENT_PRINCIPAL_TOKEN (required)
	TicketID       int    // AGENT_TICKET_ID (required, > 0)
	PipelineRunID  int    // AGENT_PIPELINE_RUN_ID (0 = none)
	BuildID        int    // BUILD_ID (0 = none)
	StepName       string // AGENT_STEP_NAME
	TimeoutPolicy  string // PLATFORM_MCP_ASK_TIMEOUT_POLICY: park|default|fail (default park)
	TimeoutSeconds int    // PLATFORM_MCP_ASK_TIMEOUT_SECONDS (default 0 = indefinite)
	ListenAddr     string // MCP_LISTEN_ADDR (default :7781, §8.1)
	EventsPath     string // PLATFORM_MCP_EVENTS_PATH ("" = stdout; Task 1 addendum)
	// ProgressInterval is the SSE progress-heartbeat interval
	// (PLATFORM_MCP_PROGRESS_INTERVAL, Go duration; SSE seam delta D3).
	// 0 = unset = mcpserver.DefaultHeartbeat (15s). Set values must be
	// > 0 and <= 30s — never clamped, always fatal at startup.
	ProgressInterval time.Duration
	// ShortParkMax is the PARK-V2 §A exit-and-respawn threshold
	// (PLATFORM_MCP_SHORT_PARK_MAX_SECONDS, integer seconds — rendered
	// literally by dispatch from the web flag --agent-short-park-max).
	// 0 = never exit: every park stays a PARK-V1 SSE park (the delta's
	// rollback hatch). Applies to BOTH ask_human and /checkpoint parks,
	// measured from the question row's asked_at.
	ShortParkMax time.Duration
	// ParkPath is the §B1 park-sentinel destination (PLATFORM_MCP_PARK_PATH,
	// Task 1 addendum) — `<flight mount>/park.json` in agent-step pods, set
	// by the agent-step exec via SidecarEnv (F15; plan 07 Task 26 — only the
	// exec knows the flight mount path), where the agent-runner's stat loop
	// watches for it. "" (the default — nothing produces this env yet) =
	// never write a sentinel: the legal checkpoint-pod shape (no flight
	// volume; the 202 response is the exit signal there).
	ParkPath string
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		ATCURL:         os.Getenv("ATC_EXTERNAL_URL"),
		PrincipalToken: os.Getenv("AGENT_PRINCIPAL_TOKEN"),
		StepName:       os.Getenv("AGENT_STEP_NAME"),
		TimeoutPolicy:  os.Getenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY"),
		ListenAddr:     os.Getenv("MCP_LISTEN_ADDR"),
		EventsPath:     os.Getenv("PLATFORM_MCP_EVENTS_PATH"),
		ParkPath:       os.Getenv("PLATFORM_MCP_PARK_PATH"),
	}
	if cfg.ATCURL == "" {
		return cfg, fmt.Errorf("ATC_EXTERNAL_URL is required")
	}
	if cfg.PrincipalToken == "" {
		return cfg, fmt.Errorf("AGENT_PRINCIPAL_TOKEN is required")
	}
	var err error
	if cfg.TicketID, err = intEnv("AGENT_TICKET_ID"); err != nil {
		return cfg, err
	}
	if cfg.TicketID <= 0 {
		return cfg, fmt.Errorf("AGENT_TICKET_ID must be a positive integer")
	}
	if cfg.PipelineRunID, err = intEnv("AGENT_PIPELINE_RUN_ID"); err != nil {
		return cfg, err
	}
	if cfg.BuildID, err = intEnv("BUILD_ID"); err != nil {
		return cfg, err
	}
	if cfg.TimeoutSeconds, err = intEnv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS"); err != nil {
		return cfg, err
	}
	switch cfg.TimeoutPolicy {
	case "":
		cfg.TimeoutPolicy = "park"
	case "park", "default", "fail":
	default:
		return cfg, fmt.Errorf("invalid PLATFORM_MCP_ASK_TIMEOUT_POLICY %q", cfg.TimeoutPolicy)
	}
	// Defense-in-depth (mirrors workflow-store's cross-field check): a
	// default/fail policy with a non-positive timeout would never fire and
	// the sidecar would park indefinitely — the opposite of the operator's
	// intent. A hand-set sidecar env must fail loudly at startup rather than
	// silently degrade to park-forever. (park is the ONE policy where a 0
	// timeout is legal — it means "wait indefinitely".)
	if (cfg.TimeoutPolicy == "default" || cfg.TimeoutPolicy == "fail") && cfg.TimeoutSeconds <= 0 {
		return cfg, fmt.Errorf("PLATFORM_MCP_ASK_TIMEOUT_SECONDS must be > 0 when PLATFORM_MCP_ASK_TIMEOUT_POLICY is %q", cfg.TimeoutPolicy)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":7781"
	}
	// D3 (SSE seam delta, 2026-07-09): the §3.1 progress mandate requires a
	// heartbeat at least every 30s; the empirical cliff is the claude CLI
	// abandoning a progress-free tools/call at exactly 60s. A set-but-invalid
	// value, a value <= 0, or a value > 30s is a FATAL startup error — never
	// clamp silently.
	if raw := os.Getenv("PLATFORM_MCP_PROGRESS_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("PLATFORM_MCP_PROGRESS_INTERVAL must be a Go duration: %w", err)
		}
		if d <= 0 || d > 30*time.Second {
			return cfg, fmt.Errorf("PLATFORM_MCP_PROGRESS_INTERVAL must be > 0 and <= 30s (progress mandate, contracts §3.1), got %s", d)
		}
		cfg.ProgressInterval = d
	}
	// PARK-V2 §A: bounds-validate like the rest of the env contract — a
	// set-but-invalid or negative threshold is FATAL at startup, never
	// clamped. Integer SECONDS, not a Go duration: dispatch renders the
	// flag's rounded seconds literally. Threshold WITHOUT a park path is
	// legal (checkpoint pods) — an ask_human crossing without a path
	// degrades loudly at crossing time instead.
	shortParkSecs, err := intEnv("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS")
	if err != nil {
		return cfg, err
	}
	if shortParkSecs < 0 {
		return cfg, fmt.Errorf("PLATFORM_MCP_SHORT_PARK_MAX_SECONDS must be >= 0 (0 = never exit-and-respawn), got %d", shortParkSecs)
	}
	cfg.ShortParkMax = time.Duration(shortParkSecs) * time.Second
	return cfg, nil
}

func intEnv(name string) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return v, nil
}
