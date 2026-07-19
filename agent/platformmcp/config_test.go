package platformmcp_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/platformmcp"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("AGENT_TICKET_ID", "42")
	t.Setenv("AGENT_PIPELINE_RUN_ID", "7")
	t.Setenv("BUILD_ID", "1001")
	t.Setenv("AGENT_STEP_NAME", "implement")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "default")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "300")

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ATCURL != "https://concourse.home" || cfg.PrincipalToken != "cap1.9.secret" ||
		cfg.TicketID != 42 || cfg.PipelineRunID != 7 || cfg.BuildID != 1001 ||
		cfg.StepName != "implement" || cfg.TimeoutPolicy != "default" || cfg.TimeoutSeconds != 300 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.ListenAddr != ":7781" {
		t.Fatalf("expected default listen addr :7781, got %q", cfg.ListenAddr)
	}
	if cfg.EventsPath != "" {
		t.Fatalf("expected empty default events path, got %q", cfg.EventsPath)
	}
	if cfg.ParkPath != "" {
		t.Fatalf("expected empty default park path, got %q", cfg.ParkPath)
	}
}

func TestConfigFromEnvDefaultsAndErrors(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("AGENT_TICKET_ID", "42")
	t.Setenv("BUILD_ID", "")
	t.Setenv("AGENT_PIPELINE_RUN_ID", "")
	t.Setenv("AGENT_STEP_NAME", "")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "")
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "")
	t.Setenv("MCP_LISTEN_ADDR", "127.0.0.1:9999")

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.TimeoutPolicy != "park" || cfg.TimeoutSeconds != 0 || cfg.ListenAddr != "127.0.0.1:9999" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	t.Setenv("ATC_EXTERNAL_URL", "")
	if _, err := platformmcp.ConfigFromEnv(); err == nil {
		t.Fatal("expected error when ATC_EXTERNAL_URL is missing")
	}
}

// TestConfigFromEnvTimeoutPolicyRequiresPositiveSeconds is the defense-in-depth
// cross-field check (mirrors workflow-store): a non-park policy with a
// non-positive timeout can never fire, so a hand-set sidecar env must fail
// loudly at startup rather than park forever. park+0 stays legal (indefinite).
func TestConfigFromEnvTimeoutPolicyRequiresPositiveSeconds(t *testing.T) {
	base := func() {
		t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
		t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
		t.Setenv("AGENT_TICKET_ID", "42")
		t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "0")
	}
	for _, policy := range []string{"default", "fail"} {
		base()
		t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", policy)
		if _, err := platformmcp.ConfigFromEnv(); err == nil {
			t.Fatalf("policy %q + 0 seconds: expected error, got nil", policy)
		}
	}

	// park + 0 is legal (wait indefinitely).
	base()
	t.Setenv("PLATFORM_MCP_ASK_TIMEOUT_POLICY", "park")
	if _, err := platformmcp.ConfigFromEnv(); err != nil {
		t.Fatalf("park + 0 seconds: expected no error, got %v", err)
	}
}

// TestConfigFromEnvProgressInterval is the D3 SSE-heartbeat validation
// (2026-07-09 SSE seam delta): unset = 0 (server defaults to 15s); a set
// value must parse as a Go duration, be > 0, and be <= 30s (contracts §3.1
// progress mandate — the claude CLI abandons a progress-free tools/call at
// exactly 60s). Never clamp silently: invalid/<=0/>30s are fatal at startup.
func TestConfigFromEnvProgressInterval(t *testing.T) {
	base := func() {
		t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
		t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
		t.Setenv("AGENT_TICKET_ID", "42")
	}

	base()
	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.ProgressInterval != 0 {
		t.Fatalf("unset PLATFORM_MCP_PROGRESS_INTERVAL: expected 0 (server default), got %s", cfg.ProgressInterval)
	}

	base()
	t.Setenv("PLATFORM_MCP_PROGRESS_INTERVAL", "10s")
	cfg, err = platformmcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv with 10s: %v", err)
	}
	if cfg.ProgressInterval != 10*time.Second {
		t.Fatalf("expected 10s, got %s", cfg.ProgressInterval)
	}

	for _, bad := range []string{"bogus", "0s", "-5s", "45s", "31s"} {
		base()
		t.Setenv("PLATFORM_MCP_PROGRESS_INTERVAL", bad)
		if _, err := platformmcp.ConfigFromEnv(); err == nil {
			t.Fatalf("PLATFORM_MCP_PROGRESS_INTERVAL=%q: expected fatal error, got nil", bad)
		}
	}
}
