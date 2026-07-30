// agent-broker is the deployment-managed MCP sidecar for synchronous review
// and consultation. Its listen address and authority come only from the
// platform configuration file; callers cannot override either on the CLI.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
	brokermcp "github.com/concourse/concourse/agent/broker/mcp"
	"github.com/concourse/concourse/agent/broker/runtime"
	"github.com/concourse/concourse/agent/broker/transport"
	"github.com/concourse/concourse/agent/broker/workspace"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const listenAddress = "127.0.0.1:7784"
const authorityConfigPath = "/run/concourse/agent-broker/authority.json"

type fileConfig struct {
	AuthorityEndpoint       string                     `json:"authority_endpoint"`
	BootstrapCapabilityFile string                     `json:"bootstrap_capability_file"`
	WorkspaceRoot           string                     `json:"workspace_root"`
	ScratchRoot             string                     `json:"scratch_root"`
	AdapterBinaries         map[string]string          `json:"adapter_binaries"`
	OutputSchemas           map[string]string          `json:"output_schemas"`
	CredentialSlots         map[string]string          `json:"credential_slots"`
	Instructions            map[string]instructionFile `json:"instructions"`
	Attachments             map[string]attachmentFile  `json:"attachments"`
	Profiles                []broker.Profile           `json:"profiles"`
	ProfileDigests          map[string]string          `json:"profile_digests"`
	CaptureLimits           workspace.Limits           `json:"capture_limits"`
}
type instructionFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type attachmentFile struct {
	Path    string            `json:"path"`
	Subject contracts.Subject `json:"subject"`
}

func main() {
	config, err := loadConfig(authorityConfigPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-broker:", err)
		os.Exit(1)
	}
	engine, catalog, err := buildEngine(context.Background(), config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-broker:", err)
		os.Exit(1)
	}
	if err := runtime.Preflight(context.Background(), catalog, pinnedProbe{paths: config.AdapterBinaries}); err != nil {
		fmt.Fprintln(os.Stderr, "agent-broker:", err)
		os.Exit(1)
	}
	handler, err := brokermcp.NewServer(engine, catalog)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-broker:", err)
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-broker: listen:", err)
		os.Exit(1)
	}
	server := &http.Server{Handler: brokerHTTPHandler(handler), ReadHeaderTimeout: 5 * time.Second}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "agent-broker: serve:", err)
		os.Exit(1)
	}
}

func brokerHTTPHandler(mcp http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/mcp", mcp)
	return mux
}

func loadConfig(path string) (fileConfig, error) {
	if err := absoluteRegularFile(path, "configuration"); err != nil {
		return fileConfig{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config fileConfig
	if err := decoder.Decode(&config); err != nil {
		return fileConfig{}, fmt.Errorf("decode configuration: %w", err)
	}
	if decoder.Decode(&struct{}{}) != ioEOF {
		return fileConfig{}, fmt.Errorf("decode configuration: trailing JSON")
	}
	if err := validateConfig(config); err != nil {
		return fileConfig{}, err
	}
	return config, nil
}

var ioEOF = io.EOF

func validateConfig(config fileConfig) error {
	if err := safeEndpoint(config.AuthorityEndpoint); err != nil {
		return err
	}
	if err := validateExactKeys("adapter_binaries", config.AdapterBinaries, []string{"codex", "claude", "cursor-agent"}); err != nil {
		return err
	}
	if err := validateExactKeys("output_schemas", config.OutputSchemas, []string{"request_review", "consult_agent"}); err != nil {
		return err
	}
	if err := validateExactKeys("instructions", config.Instructions, []string{"request_review", "consult_agent"}); err != nil {
		return err
	}
	for label, path := range map[string]string{"bootstrap capability file": config.BootstrapCapabilityFile, "workspace root": config.WorkspaceRoot, "scratch root": config.ScratchRoot} {
		if err := absolutePath(path, label); err != nil {
			return err
		}
	}
	if filepath.Clean(config.WorkspaceRoot) == filepath.Clean(config.ScratchRoot) || strings.HasPrefix(config.ScratchRoot+string(filepath.Separator), config.WorkspaceRoot+string(filepath.Separator)) || strings.HasPrefix(config.WorkspaceRoot+string(filepath.Separator), config.ScratchRoot+string(filepath.Separator)) {
		return fmt.Errorf("broker configuration: workspace and scratch roots must not overlap")
	}
	for name, path := range config.AdapterBinaries {
		if err := absoluteRegularFile(path, "adapter binary "+name); err != nil {
			return err
		}
	}
	for _, name := range []string{"codex", "claude", "cursor-agent"} {
		if config.AdapterBinaries[name] == "" {
			return fmt.Errorf("broker configuration: exact adapter binary %q is required", name)
		}
	}
	for _, tool := range []string{"request_review", "consult_agent"} {
		if err := absoluteRegularFile(config.OutputSchemas[tool], "output schema "+tool); err != nil {
			return err
		}
		instruction := config.Instructions[tool]
		if err := absoluteRegularFile(instruction.Path, "instructions "+tool); err != nil {
			return err
		}
		if !digestFileMatches(instruction.Path, instruction.Digest) {
			return fmt.Errorf("broker configuration: instruction digest for %s does not match", tool)
		}
	}
	for slot, path := range config.CredentialSlots {
		if strings.TrimSpace(slot) == "" {
			return fmt.Errorf("broker configuration: credential slot is required")
		}
		if err := absoluteRegularFile(path, "credential slot "+slot); err != nil {
			return err
		}
	}
	for name, attachment := range config.Attachments {
		if err := absoluteRegularFile(attachment.Path, "attachment "+name); err != nil {
			return err
		}
		if err := attachment.Subject.Validate(); err != nil {
			return fmt.Errorf("broker configuration: attachment %s: %w", name, err)
		}
	}
	if len(config.Profiles) == 0 {
		return fmt.Errorf("broker configuration: frozen profiles are required")
	}
	profileIDs := make([]string, 0, len(config.Profiles))
	for _, profile := range config.Profiles {
		profileIDs = append(profileIDs, profile.ID)
	}
	if err := validateExactKeys("profile_digests", config.ProfileDigests, profileIDs); err != nil {
		return err
	}
	for index, profile := range config.Profiles {
		if profile.Digest != "" {
			return fmt.Errorf("broker configuration: profile[%d] digest must be derived", index)
		}
		if config.CredentialSlots[profile.CredentialSlot] == "" {
			return fmt.Errorf("broker configuration: profile %q has unknown credential slot", profile.ID)
		}
		if !isDigest(config.ProfileDigests[profile.ID]) {
			return fmt.Errorf("broker configuration: pinned digest for profile %q is required", profile.ID)
		}
		for _, tool := range profile.Tools {
			if config.Instructions[string(tool)].Digest != profile.InstructionsDigest {
				return fmt.Errorf("broker configuration: profile %q instruction digest does not match %s", profile.ID, tool)
			}
		}
	}
	return nil
}

func validateExactKeys[T any](label string, values map[string]T, allowed []string) error {
	if len(values) != len(allowed) {
		return fmt.Errorf("broker configuration: %s has missing or orphan entries", label)
	}
	for _, key := range allowed {
		if _, found := values[key]; !found {
			return fmt.Errorf("broker configuration: %s is missing %q", label, key)
		}
	}
	for key := range values {
		found := false
		for _, wanted := range allowed {
			if key == wanted {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("broker configuration: %s contains orphan %q", label, key)
		}
	}
	return nil
}

func buildEngine(ctx context.Context, config fileConfig) (*broker.Engine, *broker.Catalog, error) {
	catalog, err := broker.NewCatalog(config.Profiles)
	if err != nil {
		return nil, nil, err
	}
	for _, profile := range config.Profiles {
		for _, tool := range profile.Tools {
			resolved, err := catalog.Resolve(tool, profile.Selector)
			if err != nil || resolved.ID != profile.ID || resolved.Digest != config.ProfileDigests[profile.ID] {
				return nil, nil, fmt.Errorf("broker configuration: pinned profile %q does not match its exact catalog resolution", profile.ID)
			}
		}
	}
	capability, err := readSlot(config.BootstrapCapabilityFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read bootstrap capability: %w", err)
	}
	authority, err := transport.NewClient(transport.Config{Endpoint: config.AuthorityEndpoint, BootstrapCapability: capability})
	if err != nil {
		return nil, nil, err
	}
	runner, err := runtime.NewRunner(runtime.RunnerConfig{WorkspaceRoot: config.WorkspaceRoot, ScratchRoot: config.ScratchRoot, OutputSchemas: map[broker.Tool]string{broker.ToolRequestReview: config.OutputSchemas["request_review"], broker.ToolConsultAgent: config.OutputSchemas["consult_agent"]}, CaptureLimits: config.CaptureLimits, Probe: pinnedProbe{paths: config.AdapterBinaries}})
	if err != nil {
		return nil, nil, err
	}
	instructions := map[broker.Tool]string{}
	for tool, entry := range config.Instructions {
		content, err := os.ReadFile(entry.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("read fixed instructions: %w", err)
		}
		instructions[broker.Tool(tool)] = string(content)
	}
	return broker.NewEngine(broker.EngineConfig{Catalog: catalog, Authority: authority, Workspace: runner, Attachments: attachmentResolver{attachments: config.Attachments}, Credentials: credentialResolver{slots: config.CredentialSlots}, Runner: runner, Instructions: instructions}), catalog, nil
}

type credentialResolver struct{ slots map[string]string }

func (r credentialResolver) Resolve(_ context.Context, slot string) (string, error) {
	path, ok := r.slots[slot]
	if !ok {
		return "", fmt.Errorf("credential slot unavailable")
	}
	return readSlot(path)
}

type attachmentResolver struct{ attachments map[string]attachmentFile }

func (r attachmentResolver) Resolve(_ context.Context, names []string) ([]broker.Attachment, error) {
	result := make([]broker.Attachment, 0, len(names))
	for _, name := range names {
		input, ok := r.attachments[name]
		if !ok {
			return nil, fmt.Errorf("attachment unavailable")
		}
		bytes, err := os.ReadFile(input.Path)
		if err != nil || len(bytes) > 8<<20 {
			return nil, fmt.Errorf("attachment unavailable")
		}
		result = append(result, broker.Attachment{Name: name, Prompt: string(bytes), Subject: input.Subject})
	}
	return result, nil
}

type pinnedProbe struct{ paths map[string]string }

func (p pinnedProbe) LookPath(binary string) (string, error) {
	path, ok := p.paths[binary]
	if !ok {
		return "", fmt.Errorf("configured binary unavailable")
	}
	return path, nil
}
func (p pinnedProbe) Output(ctx context.Context, binary string, args []string, env []string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	return command.Output()
}

func absolutePath(path, label string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("broker configuration: %s must be an absolute clean non-root path", label)
	}
	return nil
}
func absoluteRegularFile(path, label string) error {
	if err := absolutePath(path, label); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("broker configuration: inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("broker configuration: %s must be a regular non-symlink file", label)
	}
	return nil
}
func readSlot(path string) (string, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("slot unavailable")
	}
	value := strings.TrimSpace(string(bytes))
	if value == "" {
		return "", fmt.Errorf("slot unavailable")
	}
	return value, nil
}
func digestFileMatches(path, digest string) bool {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(bytes)
	return digest == "sha256:"+hex.EncodeToString(sum[:])
}

func isDigest(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[len("sha256:"):], "0123456789abcdef") == ""
}
func safeEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("broker configuration: authority endpoint is unsafe")
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host != "localhost" && net.ParseIP(host) == nil || net.ParseIP(host) != nil && !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("broker configuration: non-loopback authority must use HTTPS")
		}
	}
	return nil
}

var _ adapter.VersionProbe = pinnedProbe{}
