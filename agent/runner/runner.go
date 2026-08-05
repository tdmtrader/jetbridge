// Package runner is the deterministic agent-step pod entrypoint: it waits
// for the declared MCP sidecars to become healthy, invokes the claude CLI,
// and writes the flight recorder (flight/events.ndjson + flight/results.json).
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/provider"
	schema "github.com/concourse/concourse/agent/schema"
)

// Sidecar health-check cadence (§8.5: every MCP sidecar exposes GET /healthz).
var (
	sidecarHealthInterval  = 2 * time.Second
	sidecarHealthTimeout   = 60 * time.Second
	newLegacyClaudeAdapter = func(path string, args, env []string, stderr io.Writer, waitDelay time.Duration) provider.Adapter {
		return legacyClaudeAdapter{path: path, args: args, env: env, stderr: stderr, waitDelay: waitDelay}
	}
)

// claudeWaitDelay bounds how long cmd.Wait may block on claude's inherited
// stdout/stderr pipes after claude itself is dead (ctx cancellation) or has
// exited. Claude routinely leaks descendants (tool-call subprocesses) that
// inherit the pipe; without this bound cmd.Run blocks until the last one
// exits. On the terminal-end kill path (step timeout / build abort — the
// jetbridge exec SIGTERMs the pod's supervised process group, waits a 10s
// grace, then group-SIGKILLs) a blocked runner is SIGKILLed before it writes
// step.end/results.json, and the ingested row degrades to the zero-cost,
// no-step.end error row the kill exists to prevent (review finding,
// 2026-07-12). Must stay comfortably below that 10s grace and below the §3.2
// park exit grace (AGENT_PARK_EXIT_GRACE_SECONDS, default 30).
var claudeWaitDelay = 5 * time.Second

const maxSummaryChars = 500
const (
	managedOutputBuilderClientProtocolVersion       = "2025-11-25"
	managedOutputBuilderProtocolVersion             = "2024-11-05"
	managedOutputBuilderResponseLimit         int64 = 1 << 20
)

const (
	outputBuilderMarkerEnv = "CONCOURSE_OUTPUT_BUILDER_MCP"
	outputBuilderMCPName   = "output-builder"
	outputBuilderMCPURL    = "http://127.0.0.1:7783/mcp"
	brokerMCPMarkerEnv     = "CONCOURSE_AGENT_BROKER_MCP"
	brokerMCPTokenFileEnv  = "CONCOURSE_AGENT_BROKER_MCP_TOKEN_FILE"
	brokerMCPName          = "agent-broker"
	brokerMCPURL           = "http://127.0.0.1:7784/mcp"
	brokerMCPTokenPath     = "/run/concourse/agent-broker-client/access-token"
)

// Config drives one agent-step execution.
type Config struct {
	Prompt   string
	Model    string
	MaxTurns int

	// ModelTokenKind names the credential the mounted platform token is
	// (schema §8.2 "kind": anthropic_oauth | anthropic_api_key). Empty means
	// oauth — an operator-created secret predating the key, and the shape old
	// runner images assumed.
	ModelTokenKind string

	// Source-format layers (design 2026-07-17 §4).
	SystemPrompt string   // appended to claude's baseline via --append-system-prompt
	Context      string   // pre-concatenated block, injected at session start (prompt prefix)
	Skills       []string // skill names to materialize from SkillsDir
	SkillsDir    string   // mount path of the "skills" input artifact

	FlightDir string
	WorkDir   string
	// SessionDir is the reserved server-supplied provider session directory.
	// Current legacy Claude execution validates it but intentionally does not
	// redirect CLAUDE_CONFIG_DIR until credential persistence is proven safe.
	SessionDir string
	StepName   string
	ClaudePath string
	MCPServers map[string]string
	// OutputBuilderMarker is a server-created activation bit. The runner owns
	// the managed name and endpoint; neither is accepted from authored MCP env.
	OutputBuilderMarker string
	// BrokerMCPMarker is a server-created activation bit. Agent workflow
	// configuration cannot select the managed broker endpoint or name.
	BrokerMCPMarker string
	// BrokerMCPTokenFile is the server-created, main-container-only
	// per-parent capability used to authenticate the managed broker.
	BrokerMCPTokenFile string
	Stdout             io.Writer
	Stderr             io.Writer

	// OutputPaths maps each §8.1 AGENT_OUTPUT_<NAME> env var (its full
	// name, AGENT_OUTPUT_SCHEMA excluded — that row is the schema path,
	// not an output) to its exec-set literal path. Run surfaces these in
	// the prompt so agents work with literal paths instead of expanding
	// the env var per shell call — ticket #16's agent (build 567384) had
	// "$AGENT_OUTPUT_WORKSPACE" expand empty mid-session and cp'd the
	// repo checkout into "/".
	OutputPaths    map[string]string
	InputSnapshots map[string]SnapshotAuthority
	RecordOutputs  map[string]RecordAuthority

	// Step identity for the step.start payload (shared-contracts §5):
	// build_id and plan_id are the correlation key consumers use to join
	// the event stream back to its agent_run_metrics row, so they are NOT
	// optional. Durable workflow identity is server-side (agent_workflow_runs)
	// and never travels through pod env.
	BuildID        int
	PlanID         string
	BudgetSliceUSD float64

	// Adapter is test/integration injection for a trusted provider. Nil keeps
	// the existing Claude CLI behavior through the legacy adapter.
	Adapter provider.Adapter
}

type SnapshotAuthority struct {
	Type   string
	Digest string
}

type RecordAuthority struct {
	Type   string
	Schema string
}

var (
	mcpURLPattern             = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)_MCP_URL$`)
	outputEnvPattern          = regexp.MustCompile(`^AGENT_OUTPUT_[A-Z0-9_]+$`)
	inputAuthorityEnvPattern  = regexp.MustCompile(`^(AGENT_INPUT_[A-Z0-9_]+)_SNAPSHOT_(TYPE|DIGEST)$`)
	outputAuthorityEnvPattern = regexp.MustCompile(`^(AGENT_OUTPUT_[A-Z0-9_]+)_RECORD_(TYPE|SCHEMA)$`)
)

// FromEnv builds a Config from the §8.1 environment contract set by the
// agent-step exec. MCP servers are discovered by scanning the environment
// for variables matching ^([A-Z][A-Z0-9_]*)_MCP_URL$.
// CODE_REVIEW_MCP_URL becomes the admitted server key "code-review".
func FromEnv() Config {
	wd, _ := os.Getwd()

	cfg := Config{
		Prompt:              os.Getenv("AGENT_PROMPT"),
		Model:               os.Getenv("AGENT_MODEL"),
		ModelTokenKind:      os.Getenv("AGENT_MODEL_TOKEN_KIND"),
		SystemPrompt:        os.Getenv("AGENT_SYSTEM_PROMPT"),
		Context:             os.Getenv("AGENT_CONTEXT"),
		SkillsDir:           os.Getenv("AGENT_SKILLS_DIR"),
		FlightDir:           os.Getenv("AGENT_FLIGHT_DIR"),
		SessionDir:          os.Getenv("AGENT_SESSION_DIR"),
		StepName:            os.Getenv("AGENT_STEP_NAME"),
		WorkDir:             wd,
		MCPServers:          map[string]string{},
		OutputBuilderMarker: os.Getenv(outputBuilderMarkerEnv),
		BrokerMCPMarker:     os.Getenv(brokerMCPMarkerEnv),
		BrokerMCPTokenFile:  os.Getenv(brokerMCPTokenFileEnv),
		OutputPaths:         map[string]string{},
		InputSnapshots:      map[string]SnapshotAuthority{},
		RecordOutputs:       map[string]RecordAuthority{},

		// §5 step.start identity: BUILD_ID is jetbridge/exec-injected and
		// AGENT_PLAN_ID is set by the agent-step exec (never public YAML).
		PlanID:  os.Getenv("AGENT_PLAN_ID"),
		BuildID: envInt("BUILD_ID"),
	}

	if v := os.Getenv("AGENT_BUDGET_SLICE_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.BudgetSliceUSD = f
		}
	}

	if v := os.Getenv("AGENT_MAX_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxTurns = n
		}
	}

	if v := os.Getenv("AGENT_SKILLS"); v != "" {
		cfg.Skills = strings.Split(v, ",")
	}

	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		if m := mcpURLPattern.FindStringSubmatch(name); m != nil {
			cfg.MCPServers[strings.ReplaceAll(strings.ToLower(m[1]), "_", "-")] = value
		}
		if match := inputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := cfg.InputSnapshots[match[1]]
			if match[2] == "TYPE" {
				authority.Type = value
			} else {
				authority.Digest = value
			}
			cfg.InputSnapshots[match[1]] = authority
			continue
		}
		if match := outputAuthorityEnvPattern.FindStringSubmatch(name); match != nil {
			authority := cfg.RecordOutputs[match[1]]
			if match[2] == "TYPE" {
				authority.Type = value
			} else {
				authority.Schema = value
			}
			cfg.RecordOutputs[match[1]] = authority
			continue
		}
		if name != "AGENT_OUTPUT_SCHEMA" && outputEnvPattern.MatchString(name) {
			cfg.OutputPaths[name] = value
		}
	}

	return cfg
}

func admittedMCPServers(marker string, authored map[string]string) (map[string]string, bool, error) {
	servers := make(map[string]string, len(authored)+1)
	for name, endpoint := range authored {
		if name == outputBuilderMCPName {
			return nil, false, fmt.Errorf("managed MCP name %q is reserved", outputBuilderMCPName)
		}
		if endpoint == outputBuilderMCPURL {
			return nil, false, fmt.Errorf("managed MCP endpoint %q is reserved", outputBuilderMCPURL)
		}
		if name == brokerMCPName {
			return nil, false, fmt.Errorf("managed MCP name %q is reserved", brokerMCPName)
		}
		if endpoint == brokerMCPURL {
			return nil, false, fmt.Errorf("managed MCP endpoint %q is reserved", brokerMCPURL)
		}
		servers[name] = endpoint
	}
	switch marker {
	case "":
		return servers, false, nil
	case "1":
		servers[outputBuilderMCPName] = outputBuilderMCPURL
		return servers, true, nil
	default:
		return nil, false, fmt.Errorf("%s must be exactly 1 when present", outputBuilderMarkerEnv)
	}
}

func admitBrokerMCP(marker string, servers map[string]string) (map[string]string, bool, error) {
	cloned := make(map[string]string, len(servers)+1)
	for name, endpoint := range servers {
		cloned[name] = endpoint
	}
	switch marker {
	case "":
		return cloned, false, nil
	case "1":
		cloned[brokerMCPName] = brokerMCPURL
		return cloned, true, nil
	default:
		return nil, false, fmt.Errorf("%s must be exactly 1 when present", brokerMCPMarkerEnv)
	}
}

// envInt parses name as a positive integer; anything else (unset, empty,
// malformed, non-positive) means absent and returns 0.
func envInt(name string) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// validateSessionDir accepts only the server-reserved location under this
// agent's workdir. The legacy adapter intentionally does not set
// CLAUDE_CONFIG_DIR: Claude may persist credentials there and this slice has
// not established a credential-free export contract. HOME is never changed.
func validateSessionDir(workdir, supplied string) (string, error) {
	if supplied == "" {
		return "", nil
	}
	if workdir == "" || !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return "", errors.New("agent session directory requires a canonical workdir")
	}
	expected := filepath.Join(workdir, ".concourse", "session")
	if !filepath.IsAbs(supplied) || filepath.Clean(supplied) != supplied || supplied != expected {
		return "", fmt.Errorf("agent session directory must be %q", expected)
	}
	return supplied, nil
}

// Run executes one agent step: resolve the prompt, wait for sidecars,
// invoke claude, and write the flight recorder. The returned exit code
// follows the step contract: 0 = pass, 2 = platform error (including
// claude CLI errors). A non-nil error means the runner itself broke
// before the outcome could be recorded.
func Run(ctx context.Context, cfg Config) (int, error) {
	start := time.Now()

	stdout := cfg.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	mcpServers, outputBuilderEnabled, err := admittedMCPServers(cfg.OutputBuilderMarker, cfg.MCPServers)
	if err != nil {
		return 2, fmt.Errorf("admit MCP configuration: %w", err)
	}
	mcpServers, brokerEnabled, err := admitBrokerMCP(cfg.BrokerMCPMarker, mcpServers)
	if err != nil {
		return 2, fmt.Errorf("admit broker MCP configuration: %w", err)
	}
	brokerToken := ""
	if brokerEnabled {
		if cfg.BrokerMCPTokenFile != brokerMCPTokenPath {
			return 2, fmt.Errorf("managed broker MCP token file must be %q", brokerMCPTokenPath)
		}
		token, readErr := os.ReadFile(cfg.BrokerMCPTokenFile)
		if readErr != nil {
			return 2, fmt.Errorf("read managed broker MCP token: %w", readErr)
		}
		brokerToken = strings.TrimSpace(string(token))
		if brokerToken == "" || len(brokerToken) > 4096 {
			return 2, errors.New("managed broker MCP token is invalid")
		}
	}
	// 1. The compiler always inlines the prompt.
	prompt := cfg.Prompt
	if prompt == "" {
		return 2, errors.New("no prompt configured")
	}
	sessionDir, err := validateSessionDir(cfg.WorkDir, cfg.SessionDir)
	if err != nil {
		return 2, err
	}
	systemPrompt := cfg.SystemPrompt

	// Materialize the step's selected skills from the mounted "skills"
	// artifact into <workdir>/.claude/skills — claude's CWD is WorkDir,
	// so these are its project skills; the workspace repo's git tree is
	// untouched (design 2026-07-17 §4). A missing skill means the
	// renderer and runner disagree about the artifact — platform error.
	for _, name := range cfg.Skills {
		src := filepath.Join(cfg.SkillsDir, name)
		if _, err := os.Stat(src); err != nil {
			return 2, fmt.Errorf("skill %q not present in skills artifact %s: %w", name, cfg.SkillsDir, err)
		}
		dst := filepath.Join(cfg.WorkDir, ".claude", "skills", name)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(stderr, "agent-runner: overwriting existing project skill %q with the workflow's copy\n", name)
			if err := os.RemoveAll(dst); err != nil {
				return 2, fmt.Errorf("replace project skill %q: %w", name, err)
			}
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return 2, fmt.Errorf("create skill dir %q: %w", name, err)
		}
		if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
			return 2, fmt.Errorf("materialize skill %q: %w", name, err)
		}
	}

	// Resolved output paths: surface the §8.1 AGENT_OUTPUT_<NAME> literals
	// in the prompt itself, right above the step prompt that references
	// them. Prompts told agents to expand the env var per shell call;
	// ticket #16's agent (build 567384) had one such expansion come up
	// empty and `cp -a repo/. "$AGENT_OUTPUT_WORKSPACE/"` ran against "/".
	// A literal in the transcript cannot regress that way.
	if len(cfg.OutputPaths) > 0 {
		names := make([]string, 0, len(cfg.OutputPaths))
		for name := range cfg.OutputPaths {
			names = append(names, name)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString("# Step outputs (platform-resolved absolute paths)\n\n")
		for _, name := range names {
			fmt.Fprintf(&b, "$%s = %s\n", name, cfg.OutputPaths[name])
		}
		b.WriteString("\nUse these literal paths in commands; do not depend on the env vars expanding in every shell call.\n")
		prompt = b.String() + "\n---\n\n" + prompt
	}

	prompt = decorateOutputContractPrompt(prompt, outputBuilderEnabled, cfg.InputSnapshots, cfg.RecordOutputs)

	// Session-start context (design §4): superpowers-style injection,
	// done platform-side — the block precedes the step prompt.
	if cfg.Context != "" {
		prompt = "# Workflow context\n\n" + cfg.Context + "\n\n---\n\n" + prompt
	}

	// Open the flight recorder up-front so every exit path can honor the
	// contract that the event stream starts with step.start and ends with
	// step.end.
	if err := os.MkdirAll(cfg.FlightDir, 0o755); err != nil {
		return 2, fmt.Errorf("create flight dir: %w", err)
	}
	eventsFile, err := os.Create(filepath.Join(cfg.FlightDir, "events.ndjson"))
	if err != nil {
		return 2, fmt.Errorf("open events.ndjson: %w", err)
	}
	defer eventsFile.Close()
	events := schema.NewEventWriter(eventsFile)

	// step.start carries the full §5 identity payload: build_id and plan_id
	// are the correlation key consumers (scorecards, process-intel drill-down)
	// use to join this event stream back to its agent_run_metrics row, so
	// they must never be left zero (review finding, 2026-07-12).
	writeEvent(events, schema.EventStepStart, schema.StepStartData{
		StepName:       cfg.StepName,
		BuildID:        cfg.BuildID,
		PlanID:         cfg.PlanID,
		BudgetSliceUSD: cfg.BudgetSliceUSD,
	})

	// 2. Wait for every declared MCP sidecar to become healthy. A sidecar
	// that never comes up is a platform error, not an agent failure.
	if err := waitForSidecars(ctx, mcpServers); err != nil {
		return finishBeforeModelPlatformError(events, cfg.FlightDir, cfg.StepName, start, err)
	}
	if outputBuilderEnabled {
		client := &http.Client{Timeout: sidecarHealthInterval}
		if err := preflightManagedOutputBuilder(ctx, client, mcpServers[outputBuilderMCPName]); err != nil {
			return finishBeforeModelPlatformError(events, cfg.FlightDir, cfg.StepName, start, fmt.Errorf("managed output builder protocol preflight failed: %w", err))
		}
	}

	// 3b. Prove the model endpoint is reachable. A hermetic pod runs under a
	// deny-all egress NetworkPolicy, and with networkPolicy.hermeticEgressTo
	// unset the CLI's first call hung ~5 minutes and died with a bare client
	// timeout that named neither the network nor the policy. Failing here
	// takes seconds and says which egress rule is missing. Skipped behind a
	// proxy, where a direct probe would prove nothing about the real path.
	if !proxyConfigured(os.Environ()) {
		if err := modelEgressPreflight(ctx, modelEndpointHostPort(os.Environ())); err != nil {
			return finishBeforeModelPlatformError(events, cfg.FlightDir, cfg.StepName, start, err)
		}
	}

	// 4. Invoke the claude CLI. stream-json emits the turn-by-turn NDJSON
	// (system init, assistant tool_use, user tool_result, final result) that
	// becomes flight/transcript.ndjson — the actual tool-call transcript.
	// --verbose is REQUIRED for stream-json in claude-code 2.x; without it
	// the CLI rejects the flag combination. The final result line is the same
	// shape as the old --output-format json envelope, so parseEnvelope (which
	// reads the last non-empty line) still yields the run's model/cost/turns.
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxTurns))
	}
	if cfg.BudgetSliceUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(cfg.BudgetSliceUSD, 'f', -1, 64))
	}
	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	args = append(args, "--dangerously-skip-permissions")

	// Always provide the complete admitted MCP set, including an empty set,
	// and make it strict. Without --strict-mcp-config Claude may discover a
	// repository-provided project MCP file and silently regain an undeclared
	// live-system tool despite the workflow capability boundary.
	mcpConfigPath, cleanupMCPConfig, err := writeMCPConfig(mcpServers, brokerToken)
	if err != nil {
		return 2, fmt.Errorf("write mcp config: %w", err)
	}
	defer func() {
		if err := cleanupMCPConfig(); err != nil {
			fmt.Fprintf(stderr, "agent-runner: remove private mcp config: %v\n", err)
		}
	}()
	args = append(args, "--mcp-config", mcpConfigPath, "--strict-mcp-config")

	adapter := cfg.Adapter
	if adapter == nil {
		claudePath := cfg.ClaudePath
		if claudePath == "" {
			claudePath = "/usr/local/bin/claude"
		}
		adapter = newLegacyClaudeAdapter(claudePath, args, claudeEnv(os.Environ(), cfg.ModelTokenKind), stderr, claudeWaitDelay)
	}
	if err := adapter.Identity().Validate(); err != nil {
		return 2, fmt.Errorf("invalid provider adapter: %w", err)
	}
	var adapterControl provider.BoundaryControl
	startRequest := provider.StartRequest{
		Prompt: prompt, SystemPrompt: systemPrompt, WorkDir: cfg.WorkDir, SessionDir: sessionDir, Stdout: stdout,
	}
	var running provider.RunningSession
	var startErr error
	running, startErr = adapter.Start(ctx, startRequest, adapterControl)
	var providerResult provider.Result
	runErr := startErr
	if startErr == nil {
		if running == nil {
			runErr = errors.New("provider adapter returned no running session")
		} else {
			providerResult, runErr = running.Wait(ctx)
		}
	}
	stream := providerResult.Stream
	if runErr != nil {
		label := "provider"
		if _, legacy := adapter.(legacyClaudeAdapter); legacy {
			label = "claude"
		}
		fmt.Fprintf(stderr, "agent-runner: %s: %v\n", label, runErr)
	}

	// Persist the captured stream-json stdout as the tool-call transcript.
	// Best-effort observability: a write error must never fail the run, so
	// log it and continue.
	if err := writeTranscript(cfg.FlightDir, stream); err != nil {
		fmt.Fprintf(stderr, "agent-runner: write transcript: %v\n", err)
	}
	if outputBuilderEnabled && managedMCPReadyFromProviderStream(stream, outputBuilderMCPName) {
		writeEvent(events, schema.EventMCPReady, schema.MCPReadyData{Server: outputBuilderMCPName, ProtocolVersion: managedOutputBuilderProtocolVersion, Tools: []string{"describe_output", "validate_output", "write_output"}})
	}

	// 5. Parse the last non-empty stdout line as the CLI envelope,
	// tolerating leading non-JSON output.
	env, parseErr := parseEnvelope(stream)

	writeEvent(events, schema.EventCostRecord, schema.CostRecordData{
		Source:              "agent_step",
		Provider:            "anthropic",
		Model:               env.Model,
		InputTokens:         env.Usage.InputTokens,
		OutputTokens:        env.Usage.OutputTokens,
		CacheReadTokens:     env.Usage.CacheReadInputTokens,
		CacheCreationTokens: env.Usage.CacheCreationInputTokens,
		Turns:               env.NumTurns,
		CostUSD:             env.ResolvedCostUSD(),
	})

	// 6. Map the outcome onto the results.json wire status.
	status := schema.StatusPass
	if runErr != nil || parseErr != nil || env.IsError {
		status = schema.StatusError
	}
	exitCode := 0
	if status == schema.StatusError {
		exitCode = 2
	}

	summary := summaryFromResult(env.Result)
	if summary == "" {
		switch {
		case runErr != nil:
			summary = runErr.Error()
		case parseErr != nil:
			summary = fmt.Sprintf("unparseable claude output: %v", parseErr)
		default:
			summary = "(no result)"
		}
	}
	summary = truncate(summary, maxSummaryChars)

	results := schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return 2, fmt.Errorf("marshal results: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.FlightDir, "results.json"), resultsJSON, 0o644); err != nil {
		return 2, fmt.Errorf("write results.json: %w", err)
	}

	// 7. Close out the event stream.
	threeWay, _ := schema.ThreeWayStatus(status)
	writeEvent(events, schema.EventStepEnd, schema.StepEndData{
		StepName:        cfg.StepName,
		Status:          threeWay,
		Summary:         summary,
		WallTimeSeconds: int(time.Since(start).Seconds()),
		CostUSD:         env.ResolvedCostUSD(),
		Turns:           env.NumTurns,
	})

	return exitCode, nil
}

// Model-credential kinds, mirroring credentials.KindAnthropic* (duplicated
// rather than imported: the runner ships inside the agent-runner image and
// must not drag the web's credential package into the pod binary).
const (
	kindAnthropicOAuth  = "anthropic_oauth"
	kindAnthropicAPIKey = "anthropic_api_key"
)

// claudeEnv maps the mounted platform token onto the credential variable the
// claude CLI expects for its kind. The pod always receives the token as
// CLAUDE_CODE_OAUTH_TOKEN (one secretKeyRef, §8.2); only the vault knows
// whether it is really an OAuth token or a raw API key. An API key must be
// exported as ANTHROPIC_API_KEY *and* the OAuth variable dropped — leaving
// both set makes the CLI prefer the OAuth path and fail to authenticate.
// Absent/unknown kind is oauth: that is what every secret written before the
// "kind" key holds.
func claudeEnv(env []string, kind string) []string {
	if kind != kindAnthropicAPIKey {
		return env
	}
	out := make([]string, 0, len(env)+1)
	token := ""
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && name == "CLAUDE_CODE_OAUTH_TOKEN" {
			token = value
			continue
		}
		if ok && name == "ANTHROPIC_API_KEY" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "ANTHROPIC_API_KEY="+token)
}

func sortedAuthorityNames[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decorateOutputContractPrompt(
	prompt string,
	outputBuilderEnabled bool,
	inputSnapshots map[string]SnapshotAuthority,
	recordOutputs map[string]RecordAuthority,
) string {
	// Exact authority remains visible even when the managed builder is enabled:
	// an unavailable MCP must not force the model to invent input digests or the
	// current schema digest for a directly authored fallback record.
	if len(inputSnapshots) > 0 || len(recordOutputs) > 0 {
		var b strings.Builder
		b.WriteString("# Sealed record authority (platform-resolved)\n\n")
		for _, name := range sortedAuthorityNames(inputSnapshots) {
			authority := inputSnapshots[name]
			fmt.Fprintf(&b, "$%s_SNAPSHOT_TYPE = %s\n", name, authority.Type)
			fmt.Fprintf(&b, "$%s_SNAPSHOT_DIGEST = %s\n", name, authority.Digest)
		}
		for _, name := range sortedAuthorityNames(recordOutputs) {
			authority := recordOutputs[name]
			fmt.Fprintf(&b, "$%s_RECORD_TYPE = %s\n", name, authority.Type)
			fmt.Fprintf(&b, "$%s_RECORD_SCHEMA = %s\n", name, authority.Schema)
		}
		b.WriteString("\nCopy these exact values into record.json; they are verified again when the output is sealed.\n")
		b.WriteString("Sort record subjects and every body entity list (findings, hypotheses, actions, checks, candidates, metrics) lexicographically by id with no duplicates — including any id-reference list inside an entry, such as an action's addresses; unsorted ids are rejected when the output is sealed.\n")
		prompt = b.String() + "\n---\n\n" + prompt
	}
	if outputBuilderEnabled {
		prompt = "# Structured output builder (platform-managed MCP)\n\nUse describe_output, write_output, and validate_output to author declared records. Builder validation is preflight only; Concourse independently captures, validates, and seals every output after this step. If the managed builder is unavailable, use the exact sealed-record authority below to author the declared record directly.\n\n---\n\n" + prompt
	}
	return prompt
}

// waitForSidecars polls GET <url with /mcp replaced by /healthz> for every
// declared MCP server until each responds 200, checking in deterministic
// (sorted-name) order.
func waitForSidecars(ctx context.Context, servers map[string]string) error {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	client := &http.Client{Timeout: sidecarHealthInterval}
	for _, name := range names {
		if err := waitHealthy(ctx, client, name, healthzURL(servers[name])); err != nil {
			return err
		}
	}
	return nil
}

func finishBeforeModelPlatformError(events *schema.EventWriter, flightDir, stepName string, start time.Time, err error) (int, error) {
	summary := truncate(err.Error(), maxSummaryChars)
	writeEvent(events, schema.EventError, map[string]string{"message": err.Error()})
	writeResults(flightDir, schema.StatusError, summary)
	writeEvent(events, schema.EventStepEnd, schema.StepEndData{StepName: stepName, Status: schema.RunStatusError, Summary: summary, WallTimeSeconds: int(time.Since(start).Seconds())})
	return 2, nil
}

func managedMCPReadyFromProviderStream(stream []byte, server string) bool {
	type mcpServerStatus struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	type toolUse struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	type providerStreamEvent struct {
		Type       string            `json:"type"`
		Subtype    string            `json:"subtype"`
		MCPServers []mcpServerStatus `json:"mcp_servers"`
		Message    struct {
			Content []toolUse `json:"content"`
		} `json:"message"`
	}

	for len(stream) > 0 {
		line := stream
		if newline := bytes.IndexByte(stream, '\n'); newline >= 0 {
			line, stream = stream[:newline], stream[newline+1:]
		} else {
			stream = nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) == 0 || int64(len(line)) > managedOutputBuilderResponseLimit {
			continue
		}

		var event providerStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type == "system" && event.Subtype == "init" {
			for _, mcpServer := range event.MCPServers {
				if mcpServer.Name == server && mcpServer.Status == "connected" {
					return true
				}
			}
		}
		if event.Type == "assistant" {
			for _, content := range event.Message.Content {
				if content.Type != "tool_use" {
					continue
				}
				switch content.Name {
				case "mcp__output_builder__describe_output", "mcp__output_builder__validate_output", "mcp__output_builder__write_output",
					"mcp__output-builder__describe_output", "mcp__output-builder__validate_output", "mcp__output-builder__write_output":
					return true
				}
			}
		}
	}
	return false
}

func preflightManagedOutputBuilder(ctx context.Context, client *http.Client, endpoint string) error {
	post := func(value any, want int, result any) error {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			io.Copy(io.Discard, io.LimitReader(resp.Body, managedOutputBuilderResponseLimit))
			return fmt.Errorf("unexpected MCP status %d", resp.StatusCode)
		}
		if result == nil {
			data, err := io.ReadAll(io.LimitReader(resp.Body, managedOutputBuilderResponseLimit+1))
			if err != nil {
				return err
			}
			if int64(len(data)) > managedOutputBuilderResponseLimit {
				return errors.New("MCP response too large")
			}
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, managedOutputBuilderResponseLimit+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > managedOutputBuilderResponseLimit {
			return errors.New("MCP response too large")
		}
		return json.Unmarshal(data, result)
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": managedOutputBuilderClientProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "concourse-agent-runner", "version": "1"}}}, http.StatusOK, &initialized); err != nil {
		return err
	}
	if initialized.Result.ProtocolVersion != managedOutputBuilderProtocolVersion {
		return errors.New("unexpected MCP protocol version")
	}
	if err := post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, http.StatusNoContent, nil); err != nil {
		return err
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Type string `json:"type"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, http.StatusOK, &listed); err != nil {
		return err
	}
	if len(listed.Result.Tools) != 3 {
		return errors.New("unexpected MCP tool count")
	}
	names := make([]string, 0, 3)
	for _, tool := range listed.Result.Tools {
		if tool.InputSchema.Type != "object" {
			return errors.New("MCP tool schema is not object")
		}
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "describe_output,validate_output,write_output" {
		return errors.New("unexpected MCP tools")
	}
	return nil
}

func healthzURL(mcpURL string) string {
	return strings.TrimSuffix(mcpURL, "/mcp") + "/healthz"
}

func waitHealthy(ctx context.Context, client *http.Client, name, url string) error {
	deadline := time.Now().Add(sidecarHealthTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar %s never became healthy", name)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("sidecar %s never became healthy", name)
		case <-time.After(sidecarHealthInterval):
		}
	}
}

// writeMCPConfig writes the claude CLI --mcp-config file mapping each
// declared sidecar to its HTTP MCP endpoint.
func writeMCPConfig(servers map[string]string, brokerToken string) (string, func() error, error) {
	type serverEntry struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers,omitempty"`
	}
	entries := make(map[string]serverEntry, len(servers))
	for name, url := range servers {
		entry := serverEntry{Type: "http", URL: url}
		if name == brokerMCPName {
			if brokerToken == "" {
				return "", nil, errors.New("managed broker MCP token is required")
			}
			entry.Headers = map[string]string{"Authorization": "Bearer " + brokerToken}
		}
		entries[name] = entry
	}
	payload, err := json.Marshal(map[string]any{"mcpServers": entries})
	if err != nil {
		return "", nil, err
	}

	dir, err := os.MkdirTemp("", "agent-runner-mcp-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() error { return os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", nil, errors.Join(err, cleanup())
	}
	f, err := os.OpenFile(filepath.Join(dir, "mcp.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, errors.Join(err, cleanup())
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return "", nil, errors.Join(err, cleanup())
	}
	if err := f.Close(); err != nil {
		return "", nil, errors.Join(err, cleanup())
	}
	return f.Name(), cleanup, nil
}

// parseEnvelope finds the last non-empty line of the CLI's stdout and
// parses it as the --output-format json envelope.
func parseEnvelope(out []byte) (schema.CLIEnvelope, error) {
	var env schema.CLIEnvelope

	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return schema.CLIEnvelope{}, fmt.Errorf("parse CLI envelope: %w", err)
		}
		return env, nil
	}
	return schema.CLIEnvelope{}, errors.New("no CLI output to parse")
}

// summaryFromResult extracts a human-readable summary from the envelope's
// result field. The CLI encodes the agent's textual output as a JSON string;
// unquote it once when possible.
func summaryFromResult(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var unquoted string
		if json.Unmarshal(raw, &unquoted) == nil {
			return unquoted
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// writeResults persists a minimal, valid results.json for an early-exit path
// (e.g. sidecar-health failure) so ingestion can read the real Status/Summary
// instead of degrading to "flight recorder output missing". Best-effort: the
// step.end event is the primary contract, so a failed write must not change
// the runner's exit code.
func writeResults(flightDir string, status schema.Status, summary string) {
	results := schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(flightDir, "results.json"), raw, 0o644)
}

// maxTranscriptBytes bounds flight/transcript.ndjson to the last 512 KiB.
// The tail is the diagnostic part — commits and failures happen at the end
// of the stream — so truncation drops the head, not the tail.
const maxTranscriptBytes = 512 * 1024

// writeTranscript persists the captured claude stream-json stdout as the
// tool-call transcript at flight/transcript.ndjson. It is bounded to the last
// maxTranscriptBytes; when the raw stream is larger the head is dropped and a
// marker line is prepended:
//
//	{"type":"transcript_truncated","dropped_bytes":<N>,"note":"head-truncated to last 512KiB"}
//
// so consumers can tell the transcript was clipped. The kept tail is aligned
// to the next line boundary to avoid emitting a partial leading JSON line.
func writeTranscript(flightDir string, raw []byte) error {
	if len(raw) <= maxTranscriptBytes {
		return os.WriteFile(filepath.Join(flightDir, "transcript.ndjson"), raw, 0o644)
	}

	tail := raw[len(raw)-maxTranscriptBytes:]
	// Drop the partial first line so the tail starts on a clean NDJSON record.
	if nl := bytes.IndexByte(tail, '\n'); nl >= 0 && nl+1 < len(tail) {
		tail = tail[nl+1:]
	}
	dropped := len(raw) - len(tail)

	marker := fmt.Sprintf(
		`{"type":"transcript_truncated","dropped_bytes":%d,"note":"head-truncated to last 512KiB"}`+"\n",
		dropped,
	)

	out := make([]byte, 0, len(marker)+len(tail))
	out = append(out, marker...)
	out = append(out, tail...)
	return os.WriteFile(filepath.Join(flightDir, "transcript.ndjson"), out, 0o644)
}

// writeEvent marshals data and appends it to the flight recorder,
// best-effort: an unwritable event must not abort the run itself.
func writeEvent(w *schema.EventWriter, t schema.EventType, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = w.Write(schema.Event{Type: t, Data: raw})
}
