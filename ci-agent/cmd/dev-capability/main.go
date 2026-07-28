// Command dev-capability executes a promoted development-validation profile
// without serving MCP or loading configuration from a candidate workspace.
// The fresh hermetic task that supplies its protected paths is deliberately
// owned by the later validation-runtime slice.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/ci-agent/devmcp"
)

const maxInputBytes = 4 << 20

type commandOptions struct {
	configPath       string
	profilePath      string
	workspacePath    string
	changedPathsPath string
	resultPath       string
	logsPath         string
}

type validationOutput struct {
	ProfileIdentity devmcp.ProfileIdentity `json:"profile_identity"`
	Status          string                 `json:"status"`
	DurationSeconds float64                `json:"duration_seconds"`
	Error           string                 `json:"error,omitempty"`
	Attempts        []validationAttempt    `json:"attempts"`
}

type validationAttempt struct {
	CheckID         string           `json:"check_id"`
	Number          int              `json:"number"`
	Status          string           `json:"status"`
	Summary         string           `json:"summary"`
	DurationSeconds float64          `json:"duration_seconds"`
	OutputTail      string           `json:"output_tail"`
	FullLogPath     string           `json:"full_log_path"`
	Failures        []devmcp.Failure `json:"failures"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(runCommandContext(ctx, os.Args[1:], os.Stderr))
}

// runCommand exists so the CLI's machine contract is exercised without a
// subprocess. Production uses signal-aware runCommandContext.
func runCommand(args []string, stderr io.Writer) int {
	return runCommandContext(context.Background(), args, stderr)
}

func runCommandContext(ctx context.Context, args []string, stderr io.Writer) int {
	if stderr == nil {
		stderr = io.Discard
	}
	options, err := parseCommandOptions(args)
	if err != nil {
		return commandError(stderr, err)
	}
	if err := requireProtectedPaths(options); err != nil {
		return commandError(stderr, err)
	}
	if err := preflightOutputs(options.resultPath, options.logsPath); err != nil {
		return commandError(stderr, err)
	}

	configBytes, err := readBoundedInput(options.configPath, "config")
	if err != nil {
		return commandError(stderr, err)
	}
	profileBytes, err := readBoundedInput(options.profilePath, "profile")
	if err != nil {
		return commandError(stderr, err)
	}
	profile, identity, err := devmcp.ParseValidationProfile(profileBytes, configBytes)
	if err != nil {
		return commandError(stderr, fmt.Errorf("parse profile/config: %w", err))
	}
	config, err := devmcp.Parse(configBytes)
	if err != nil {
		return commandError(stderr, fmt.Errorf("parse config: %w", err))
	}
	changedPaths, err := readChangedPaths(options.changedPathsPath)
	if err != nil {
		return commandError(stderr, err)
	}
	core, err := devmcp.NewCore(config, options.workspacePath)
	if err != nil {
		return commandError(stderr, fmt.Errorf("create core: %w", err))
	}

	started := time.Now()
	validation, validationErr := devmcp.ValidateProfile(ctx, core, devmcp.ValidationRequest{
		Profile:      profile,
		Identity:     identity,
		ChangedPaths: changedPaths,
	}, nil)
	duration := time.Since(started)
	if validationErr != nil {
		validation.Status = devmcp.ValidationStatusError
	}
	output := buildValidationOutput(identity, validation, duration, validationErr)
	if err := copyCompleteLogs(options.workspacePath, options.logsPath, validation.Attempts, output.Attempts); err != nil {
		return commandError(stderr, fmt.Errorf("retain complete logs: %w", err))
	}
	encoded, err := marshalValidationOutput(output)
	if err != nil {
		return commandError(stderr, fmt.Errorf("encode result: %w", err))
	}
	if err := atomicWrite(options.resultPath, encoded); err != nil {
		return commandError(stderr, fmt.Errorf("write result: %w", err))
	}

	switch output.Status {
	case devmcp.ValidationStatusPassed:
		return 0
	case devmcp.ValidationStatusFailed:
		return 1
	case devmcp.ValidationStatusError:
		return 2
	default:
		return commandError(stderr, fmt.Errorf("unknown validation status %q", output.Status))
	}
}

func commandError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "dev-capability: %s\n", err)
	return 2
}

func parseCommandOptions(args []string) (commandOptions, error) {
	if len(args) == 0 || args[0] != "validate" {
		return commandOptions{}, fmt.Errorf("validate subcommand is required")
	}
	flags := flag.NewFlagSet("dev-capability validate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options commandOptions
	flags.StringVar(&options.configPath, "config", "", "protected config path")
	flags.StringVar(&options.profilePath, "profile", "", "validation profile path")
	flags.StringVar(&options.workspacePath, "workspace", "", "candidate workspace path")
	flags.StringVar(&options.changedPathsPath, "changed-paths", "", "platform-supplied changed paths JSON path")
	flags.StringVar(&options.resultPath, "result", "", "result JSON path")
	flags.StringVar(&options.logsPath, "logs", "", "complete log directory")
	if err := flags.Parse(args[1:]); err != nil {
		return commandOptions{}, err
	}
	if flags.NArg() != 0 {
		return commandOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	required := []struct{ name, value string }{
		{"--config", options.configPath}, {"--profile", options.profilePath}, {"--workspace", options.workspacePath},
		{"--changed-paths", options.changedPathsPath}, {"--result", options.resultPath}, {"--logs", options.logsPath},
	}
	missing := make([]string, 0)
	for _, item := range required {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) != 0 {
		return commandOptions{}, fmt.Errorf("required flags are missing: %s", strings.Join(missing, ", "))
	}
	return options, nil
}

// requireProtectedPaths provides a local fail-closed boundary before the
// hermetic task is introduced: authority inputs and results cannot be placed
// in the candidate workspace, where a test command could replace them.
func requireProtectedPaths(options commandOptions) error {
	workspace, err := resolveExistingPath(options.workspacePath)
	if err != nil {
		return fmt.Errorf("resolve candidate workspace: %w", err)
	}
	for _, item := range []struct{ label, path string }{
		{"config", options.configPath}, {"profile", options.profilePath}, {"changed paths", options.changedPathsPath},
	} {
		inside, err := existingPathWithin(workspace, item.path)
		if err != nil {
			return fmt.Errorf("resolve %s path: %w", item.label, err)
		}
		if inside {
			return fmt.Errorf("%s path must be outside candidate workspace", item.label)
		}
	}
	for _, item := range []struct{ label, path string }{{"result", options.resultPath}, {"logs", options.logsPath}} {
		inside, err := pathWithin(workspace, item.path)
		if err != nil {
			return fmt.Errorf("resolve %s path: %w", item.label, err)
		}
		if inside {
			return fmt.Errorf("%s path must be outside candidate workspace", item.label)
		}
	}
	return nil
}

func resolveExistingPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absPath)
}

func existingPathWithin(parent, candidate string) (bool, error) {
	resolvedCandidate, err := resolveExistingPath(candidate)
	if err != nil {
		return false, err
	}
	return pathWithin(parent, resolvedCandidate)
}

func pathWithin(parent, candidate string) (bool, error) {
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(parent, absCandidate)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func preflightOutputs(resultPath, logsPath string) error {
	result, err := filepath.Abs(resultPath)
	if err != nil {
		return fmt.Errorf("resolve result path: %w", err)
	}
	logs, err := filepath.Abs(logsPath)
	if err != nil {
		return fmt.Errorf("resolve logs path: %w", err)
	}
	if pathsContainOneAnother(result, logs) {
		return fmt.Errorf("result and logs paths must not contain one another")
	}
	if info, err := os.Lstat(result); err == nil && info.IsDir() {
		return fmt.Errorf("result path is a directory: %s", resultPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect result path: %w", err)
	}
	if _, err := os.Lstat(logs); err == nil {
		return fmt.Errorf("logs path already exists: %s", logsPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect logs path: %w", err)
	}
	return nil
}

func pathsContainOneAnother(first, second string) bool {
	firstContainsSecond, err := pathWithin(first, second)
	if err != nil {
		return false
	}
	secondContainsFirst, err := pathWithin(second, first)
	return firstContainsSecond || (err == nil && secondContainsFirst)
}

func readBoundedInput(path, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if len(raw) > maxInputBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxInputBytes)
	}
	return raw, nil
}

func readChangedPaths(path string) ([]string, error) {
	raw, err := readBoundedInput(path, "changed paths")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var paths []string
	if err := decoder.Decode(&paths); err != nil {
		return nil, fmt.Errorf("parse changed paths: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse changed paths: trailing JSON value")
		}
		return nil, fmt.Errorf("parse changed paths: %w", err)
	}
	if paths == nil {
		return nil, fmt.Errorf("parse changed paths: JSON array is required")
	}
	for index, path := range paths {
		clean := filepath.Clean(path)
		if path == "" || strings.ContainsRune(path, '\x00') || filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("parse changed paths: paths[%d] must be a repository-relative file path", index)
		}
	}
	return append([]string(nil), paths...), nil
}

func buildValidationOutput(identity devmcp.ProfileIdentity, result devmcp.ValidationResult, duration time.Duration, validationErr error) validationOutput {
	output := validationOutput{ProfileIdentity: identity, Status: result.Status, DurationSeconds: duration.Seconds(), Attempts: make([]validationAttempt, len(result.Attempts))}
	if validationErr != nil {
		output.Error = validationErr.Error()
	}
	for index, attempt := range result.Attempts {
		failures := append([]devmcp.Failure{}, attempt.Result.Failures...)
		if failures == nil {
			failures = []devmcp.Failure{}
		}
		output.Attempts[index] = validationAttempt{
			CheckID: attempt.CheckID, Number: attempt.Number, Status: attempt.Result.Status, Summary: attempt.Result.Summary,
			DurationSeconds: attempt.Result.DurationSeconds, OutputTail: attempt.Result.OutputTail,
			FullLogPath: fmt.Sprintf("attempt-%04d.log", index+1), Failures: failures,
		}
	}
	return output
}

func copyCompleteLogs(workspace, logsPath string, attempts []devmcp.CheckAttempt, outputAttempts []validationAttempt) error {
	if len(attempts) != len(outputAttempts) {
		return fmt.Errorf("attempt/log projection length mismatch")
	}
	if err := os.Mkdir(logsPath, 0o755); err != nil {
		return err
	}
	for index, attempt := range attempts {
		if attempt.FullLogPath == "" {
			return fmt.Errorf("attempt %d has no complete log", index+1)
		}
		source, err := safeWorkspacePath(workspace, attempt.FullLogPath)
		if err != nil {
			return fmt.Errorf("attempt %d log path: %w", index+1, err)
		}
		input, err := os.Open(source)
		if err != nil {
			return fmt.Errorf("read attempt %d complete log: %w", index+1, err)
		}
		destination := filepath.Join(logsPath, outputAttempts[index].FullLogPath)
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, copyErr := io.Copy(output, input)
			closeErr := output.Close()
			if copyErr != nil {
				err = copyErr
			} else {
				err = closeErr
			}
		}
		input.Close()
		if err != nil {
			return fmt.Errorf("copy attempt %d complete log: %w", index+1, err)
		}
	}
	return nil
}

func safeWorkspacePath(workspace, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" || strings.ContainsRune(relative, '\x00') {
		return "", fmt.Errorf("must be a nonempty relative path")
	}
	path := filepath.Join(workspace, relative)
	inside, err := pathWithin(workspace, path)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("escapes workspace")
	}
	return path, nil
}

func marshalValidationOutput(output validationOutput) ([]byte, error) {
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func atomicWrite(path string, contents []byte) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".dev-capability-result-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
