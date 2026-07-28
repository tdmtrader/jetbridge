// Package devvalidate converts the pinned dev-capability CLI's machine result
// into a server-attested validation/v1 record. Candidate files, repository
// configuration, and PATH are deliberately not sources of authority here.
package devvalidate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/goccy/go-yaml"
)

const (
	TrustedCommandPath = "/usr/local/go/bin:/usr/bin:/bin"
	maxResultBytes     = 1 << 20
	maxLogBytes        = 32 << 20
)

type Request struct {
	Candidate            snapshot.SnapshotRef
	Bases                []snapshot.ValidationAuthorityInput
	CandidateInput       string
	WorkspaceRoot        string
	OutputRoot           string
	Profile              workflow.CompiledDevValidationProfile
	WorkflowDefinitionID int
	WorkflowVersion      int
	ChangedPaths         []string
}

type CommandRunner interface {
	Run(context.Context, []string, string, []string) (int, error)
}
type Runner struct{ command CommandRunner }

func NewRunner(command CommandRunner) *Runner {
	if command == nil {
		command = processCommandRunner{}
	}
	return &Runner{command}
}

type cliIdentity struct {
	ProfileDigest         string `json:"profile_digest"`
	ProtectedConfigDigest string `json:"protected_config_digest"`
}
type cliFailure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}
type cliAttempt struct {
	CheckID         string       `json:"check_id"`
	Number          int          `json:"number"`
	Status          string       `json:"status"`
	Summary         string       `json:"summary"`
	DurationSeconds float64      `json:"duration_seconds"`
	OutputTail      string       `json:"output_tail"`
	FullLogPath     string       `json:"full_log_path"`
	Failures        []cliFailure `json:"failures"`
}
type cliResult struct {
	ProfileIdentity cliIdentity  `json:"profile_identity"`
	Status          string       `json:"status"`
	DurationSeconds float64      `json:"duration_seconds"`
	Attempts        []cliAttempt `json:"attempts"`
}

// DecodeResult is strict on both JSON shape and the CLI-owned filenames. This
// makes a result produced by a candidate command unusable as an authority blob.
func DecodeResult(raw []byte) (cliResult, error) {
	if len(raw) > maxResultBytes {
		return cliResult{}, fmt.Errorf("dev validation: result exceeds %d bytes", maxResultBytes)
	}
	var result cliResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return cliResult{}, fmt.Errorf("dev validation: decode strict CLI result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cliResult{}, fmt.Errorf("dev validation: CLI result has trailing data")
	}
	if len(result.Attempts) == 0 || !finite(result.DurationSeconds) {
		return cliResult{}, fmt.Errorf("dev validation: CLI result has invalid attempts or duration")
	}
	if result.Status != "passed" && result.Status != "failed" && result.Status != "error" {
		return cliResult{}, fmt.Errorf("dev validation: invalid CLI status %q", result.Status)
	}
	for i, attempt := range result.Attempts {
		if attempt.CheckID == "" || attempt.Number <= 0 || !finite(attempt.DurationSeconds) || attempt.Failures == nil {
			return cliResult{}, fmt.Errorf("dev validation: invalid CLI attempt %d", i)
		}
		if attempt.Status != "ok" && attempt.Status != "failed" && attempt.Status != "error" {
			return cliResult{}, fmt.Errorf("dev validation: invalid CLI attempt status")
		}
		if attempt.FullLogPath != fmt.Sprintf("attempt-%04d.log", i+1) {
			return cliResult{}, fmt.Errorf("dev validation: CLI attempt %d has forged full_log_path", i)
		}
	}
	return result, nil
}

func (runner *Runner) Run(ctx context.Context, request Request) (contracts.Record[contracts.ValidationBody], error) {
	if ctx == nil || runner == nil || runner.command == nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: context and command runner are required")
	}
	if err := request.Candidate.Validate(); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: candidate: %w", err)
	}
	if request.CandidateInput == "" || request.WorkspaceRoot == "" || request.OutputRoot == "" || request.WorkflowDefinitionID <= 0 || request.WorkflowVersion <= 0 {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: missing server-owned request authority")
	}
	if err := validateProfile(request.Profile); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	workspace, err := plainDir(request.WorkspaceRoot)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: workspace: %w", err)
	}
	output, err := plainEmptyDir(request.OutputRoot)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: output: %w", err)
	}
	if overlaps(workspace, output) {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: workspace and output overlap")
	}
	stage, err := os.MkdirTemp("", "concourse-devvalidate-")
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0700); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	profilePath, configPath := filepath.Join(stage, "profile.yml"), filepath.Join(stage, "config.yml")
	changedPath, resultPath, logsPath := filepath.Join(stage, "changed-paths.json"), filepath.Join(stage, "result.json"), filepath.Join(stage, "logs")
	for _, file := range []struct {
		path string
		data []byte
	}{{profilePath, request.Profile.Profile}, {configPath, request.Profile.ProtectedConfig}, {changedPath, changedPathsJSON(request.ChangedPaths)}} {
		if err := os.WriteFile(file.path, file.data, 0600); err != nil {
			return contracts.Record[contracts.ValidationBody]{}, err
		}
	}
	env := []string{"HOME=" + filepath.Join(stage, "home"), "TMPDIR=" + filepath.Join(stage, "tmp"), "PATH=" + TrustedCommandPath, "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	if err := os.MkdirAll(filepath.Join(stage, "home"), 0700); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if err := os.MkdirAll(filepath.Join(stage, "tmp"), 0700); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	args := []string{request.Profile.Command[0], request.Profile.Command[1], "--config", configPath, "--profile", profilePath, "--workspace", workspace, "--changed-paths", changedPath, "--result", resultPath, "--logs", logsPath}
	exit, err := runner.command.Run(ctx, args, workspace, env)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: execute pinned CLI: %w", err)
	}
	raw, err := regularFile(resultPath, maxResultBytes)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	result, err := DecodeResult(raw)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if err := validateIdentity(result, request.Profile, exit); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	logs, err := retainLogs(logsPath, output, result.Attempts)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	record, err := makeRecord(request, result, logs)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if err := os.WriteFile(filepath.Join(output, "record.json"), append(encoded, '\n'), 0600); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	return record, nil
}
func changedPathsJSON(paths []string) []byte {
	if paths == nil {
		paths = []string{}
	}
	raw, _ := json.Marshal(paths)
	return append(raw, '\n')
}

func validateProfile(profile workflow.CompiledDevValidationProfile) error {
	hash, err := workflow.DevValidationProvenanceHash([]workflow.CompiledDevValidationProfile{profile})
	if err != nil {
		return err
	}
	if err := workflow.ValidateDevValidationAuthority([]workflow.CompiledDevValidationProfile{profile}, hash); err != nil {
		return fmt.Errorf("dev validation: compiled authority: %w", err)
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func plainEmptyDir(path string) (string, error) {
	a, err := plainDir(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(a)
	if err != nil || len(entries) != 0 {
		return "", fmt.Errorf("must start empty")
	}
	return a, nil
}
func plainDir(path string) (string, error) {
	a, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(a)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("must be a non-symlink directory")
	}
	return a, nil
}
func overlaps(a, b string) bool {
	r, err := filepath.Rel(a, b)
	return err == nil && (r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))))
}
func regularFile(path string, max int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dev validation: result must be a regular non-symlink file")
	}
	if info.Size() > int64(max) {
		return nil, fmt.Errorf("dev validation: result too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("dev validation: open result: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("dev validation: result changed while opening: %w", statErr)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(max)+1))
	after, afterErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := os.Lstat(path)
	if readErr != nil || afterErr != nil || closeErr != nil || pathErr != nil {
		return nil, fmt.Errorf("dev validation: read result: %w", errors.Join(readErr, afterErr, closeErr, pathErr))
	}
	if int64(len(raw)) > int64(max) || !os.SameFile(info, after) || !os.SameFile(info, pathAfter) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, fmt.Errorf("dev validation: result changed while reading")
	}
	return raw, nil
}
func validateIdentity(result cliResult, profile workflow.CompiledDevValidationProfile, exit int) error {
	if result.ProfileIdentity.ProfileDigest != profile.ProfileDigest.String() || result.ProfileIdentity.ProtectedConfigDigest != profile.ProtectedConfigDigest.String() {
		return fmt.Errorf("dev validation: CLI identity does not match compiled authority")
	}
	want := map[string]int{"passed": 0, "failed": 1, "error": 2}[result.Status]
	if exit != want {
		return fmt.Errorf("dev validation: CLI exit/result mismatch")
	}
	return validateExpectedChecks(result, profile.Profile)
}

func validateExpectedChecks(result cliResult, rawProfile []byte) error {
	var profile struct {
		SchemaVersion int    `yaml:"schema_version"`
		Name          string `yaml:"name"`
		Checks        []struct {
			ID         string   `yaml:"id"`
			Operation  string   `yaml:"operation"`
			Scope      string   `yaml:"scope"`
			Timeout    string   `yaml:"timeout"`
			Retries    int      `yaml:"retries"`
			Components []string `yaml:"components,omitempty"`
		} `yaml:"checks"`
	}
	if err := yaml.UnmarshalWithOptions(rawProfile, &profile, yaml.Strict()); err != nil {
		return fmt.Errorf("dev validation: decode compiled profile: %w", err)
	}
	if len(profile.Checks) == 0 {
		return fmt.Errorf("dev validation: compiled profile has no checks")
	}
	positions := make(map[string]int, len(profile.Checks))
	for i, check := range profile.Checks {
		if check.ID == "" {
			return fmt.Errorf("dev validation: compiled profile has empty check ID")
		}
		positions[check.ID] = i
	}
	last, seen := -1, make([]bool, len(profile.Checks))
	numbers := make(map[string]int, len(profile.Checks))
	for index, attempt := range result.Attempts {
		pos, found := positions[attempt.CheckID]
		if !found || pos < last {
			return fmt.Errorf("dev validation: CLI check sequence does not match compiled profile")
		}
		if attempt.Number != numbers[attempt.CheckID]+1 {
			return fmt.Errorf("dev validation: CLI attempt numbers do not match compiled profile")
		}
		if index > 0 && pos > last && result.Attempts[index-1].Status != "ok" {
			return fmt.Errorf("dev validation: CLI continued after terminal attempt")
		}
		numbers[attempt.CheckID] = attempt.Number
		last = pos
		seen[pos] = true
	}
	if result.Status == "passed" {
		for i := range seen {
			if !seen[i] {
				return fmt.Errorf("dev validation: CLI passed without compiled check %q", profile.Checks[i].ID)
			}
		}
		for _, attempt := range result.Attempts {
			if attempt.Status != "ok" {
				return fmt.Errorf("dev validation: CLI passed with non-successful attempt")
			}
		}
	} else if result.Attempts[len(result.Attempts)-1].Status != result.Status {
		return fmt.Errorf("dev validation: CLI terminal status does not match attempts")
	}
	return nil
}

type retainedLog struct {
	path   string
	digest snapshot.Digest
	size   int64
}

func retainLogs(root, output string, attempts []cliAttempt) ([]retainedLog, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("dev validation: logs must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(attempts) {
		return nil, fmt.Errorf("dev validation: incomplete logs")
	}
	dest := filepath.Join(output, "content", "logs")
	if err := os.MkdirAll(dest, 0700); err != nil {
		return nil, err
	}
	logs := make([]retainedLog, len(attempts))
	for i, a := range attempts {
		source := filepath.Join(root, a.FullLogPath)
		bytes, err := regularFile(source, maxLogBytes)
		if err != nil {
			return nil, fmt.Errorf("dev validation: attempt log %d: %w", i+1, err)
		}
		sum := sha256.Sum256(bytes)
		target := filepath.Join(dest, a.FullLogPath)
		if err := os.WriteFile(target, bytes, 0600); err != nil {
			return nil, err
		}
		logs[i] = retainedLog{"content/logs/" + a.FullLogPath, snapshot.Digest("sha256:" + hex.EncodeToString(sum[:])), int64(len(bytes))}
	}
	return logs, nil
}
func makeRecord(request Request, result cliResult, logs []retainedLog) (contracts.Record[contracts.ValidationBody], error) {
	checks := []contracts.ValidationCheck{}
	index := map[string]int{}
	for i, a := range result.Attempts {
		pos, found := index[a.CheckID]
		if !found {
			pos = len(checks)
			index[a.CheckID] = pos
			// The deterministic CLI result carries a check ID but not a schema
			// check kind. Keep it within the sealed validation/v1 vocabulary;
			// profile-specific operation names remain in the trusted profile.
			checks = append(checks, contracts.ValidationCheck{ID: a.CheckID, Kind: "custom", Name: a.CheckID})
		}
		status := a.Status
		if status == "ok" {
			status = "passed"
		}
		checks[pos].Status = status
		checks[pos].Attempts = append(checks[pos].Attempts, contracts.ValidationAttempt{Number: a.Number, Status: status, Duration: time.Duration(a.DurationSeconds * float64(time.Second)).String(), Log: contracts.ValidationLog{Path: logs[i].path, Digest: logs[i].digest, Size: logs[i].size, MediaType: "text/plain; charset=utf-8"}, Evidence: []contracts.Anchor{}, Detail: strings.TrimSpace(a.Summary + "\n" + a.OutputTail)})
	}
	subjects := []contracts.Subject{contracts.SubjectFromInput("candidate", contracts.SubjectRolePrimary, request.CandidateInput, request.Candidate)}
	bases := append([]snapshot.ValidationAuthorityInput(nil), request.Bases...)
	sort.Slice(bases, func(i, j int) bool { return bases[i].Input < bases[j].Input })
	attBases := make([]contracts.ValidationBaseInput, len(bases))
	for i, b := range bases {
		subjects = append(subjects, contracts.SubjectFromInput(b.Input, contracts.SubjectRoleBase, b.Input, b.Ref))
		attBases[i] = contracts.ValidationBaseInput{Input: b.Input, Type: b.Ref.Type, Digest: b.Ref.Digest}
	}
	body := contracts.ValidationBody{Conclusion: contracts.DeriveValidationConclusion(checks), Summary: "authoritative development capability validation", Checks: checks, Attestation: contracts.ValidationAttestation{CandidateDigest: request.Candidate.Digest, BaseInputs: attBases, ProfileDigest: request.Profile.ProfileDigest, ProtectedConfigDigest: request.Profile.ProtectedConfigDigest, CapabilityImage: request.Profile.CapabilityImage, CapabilityImageDigest: request.Profile.CapabilityImageDigest, WorkflowDefinitionID: request.WorkflowDefinitionID, WorkflowVersion: request.WorkflowVersion, Toolchain: "dev-capability/" + request.Profile.CapabilityImageDigest.String()}}
	if body.Conclusion != result.Status {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("dev validation: CLI status %q does not match its attempts (%q)", result.Status, body.Conclusion)
	}
	return contracts.NewRecord(snapshot.TypeRef("validation/v1"), subjects, body)
}

type processCommandRunner struct{}

func (processCommandRunner) Run(ctx context.Context, args []string, dir string, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}
