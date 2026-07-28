// Command dev-capability executes a promoted development-validation profile
// without serving MCP or loading configuration from a candidate workspace.
// The fresh hermetic task that supplies its protected paths is deliberately
// owned by the later validation-runtime slice.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"golang.org/x/sys/unix"
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
	workspace, err := resolveExistingPath(options.workspacePath)
	if err != nil {
		return commandError(stderr, fmt.Errorf("resolve candidate workspace: %w", err))
	}
	outputs, err := bindOutputRoots(options.resultPath, options.logsPath, workspace)
	if err != nil {
		return commandError(stderr, fmt.Errorf("bind outputs: %w", err))
	}
	defer outputs.Close()

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
	if err := copyCompleteLogs(options.workspacePath, outputs, validation.Attempts, output.Attempts); err != nil {
		return commandError(stderr, fmt.Errorf("retain complete logs: %w", err))
	}
	encoded, err := marshalValidationOutput(output)
	if err != nil {
		return commandError(stderr, fmt.Errorf("encode result: %w", err))
	}
	if err := atomicWrite(outputs.result, encoded); err != nil {
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

func copyCompleteLogs(workspace string, outputs *boundOutputRoots, attempts []devmcp.CheckAttempt, outputAttempts []validationAttempt) error {
	if len(attempts) != len(outputAttempts) {
		return fmt.Errorf("attempt/log projection length mismatch")
	}
	if outputs == nil {
		return fmt.Errorf("bound output roots are required")
	}
	stageName, stage, err := randomDirectoryAt(outputs.logs.parent, "."+outputs.logs.name+".tmp-")
	if err != nil {
		return fmt.Errorf("create staged logs: %w", err)
	}
	committed := false
	defer func() {
		stage.Close()
		if !committed {
			_ = unix.Unlinkat(int(outputs.logs.parent.Fd()), stageName, unix.AT_REMOVEDIR)
		}
	}()
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
		outputName := outputAttempts[index].FullLogPath
		output, createErr := createExclusiveFileAt(stage, outputName, 0o644)
		if createErr != nil {
			input.Close()
			return fmt.Errorf("create attempt %d complete log: %w", index+1, createErr)
		}
		_, copyErr := io.Copy(output, input)
		syncErr := output.Sync()
		closeErr := output.Close()
		inputCloseErr := input.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || inputCloseErr != nil {
			return fmt.Errorf("copy attempt %d complete log: %w", index+1, errors.Join(copyErr, syncErr, closeErr, inputCloseErr))
		}
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync staged complete logs: %w", err)
	}
	if err := unix.Renameat(int(outputs.logs.parent.Fd()), stageName, int(outputs.logs.parent.Fd()), outputs.logs.name); err != nil {
		return fmt.Errorf("publish complete logs: %w", err)
	}
	committed = true
	if err := outputs.logs.parent.Sync(); err != nil {
		return fmt.Errorf("sync complete logs parent: %w", err)
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

func atomicWrite(target boundOutputTarget, contents []byte) error {
	temporaryName, temporary, err := randomFileAt(target.parent, "."+target.name+".tmp-", 0o644)
	if err != nil {
		return err
	}
	temporaryPresent := true
	defer func() {
		if temporaryPresent {
			_ = unix.Unlinkat(int(target.parent.Fd()), temporaryName, 0)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(target.parent.Fd()), temporaryName, int(target.parent.Fd()), target.name); err != nil {
		return err
	}
	temporaryPresent = false
	return target.parent.Sync()
}

type boundOutputTarget struct {
	parent *os.File
	name   string
}

type boundOutputRoots struct {
	result boundOutputTarget
	logs   boundOutputTarget
}

func bindOutputRoots(resultPath, logsPath, workspace string) (*boundOutputRoots, error) {
	result, err := bindOutputTarget(resultPath, true, workspace)
	if err != nil {
		return nil, fmt.Errorf("bind result: %w", err)
	}
	logs, err := bindOutputTarget(logsPath, false, workspace)
	if err != nil {
		result.parent.Close()
		return nil, fmt.Errorf("bind logs: %w", err)
	}
	return &boundOutputRoots{result: result, logs: logs}, nil
}

func (roots *boundOutputRoots) Close() error {
	if roots == nil {
		return nil
	}
	return errors.Join(roots.result.parent.Close(), roots.logs.parent.Close())
}

func bindOutputTarget(path string, allowExistingRegular bool, workspace string) (boundOutputTarget, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return boundOutputTarget{}, err
	}
	name := filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) || strings.ContainsRune(name, '\x00') {
		return boundOutputTarget{}, fmt.Errorf("output target must be one path component")
	}
	parentPath, err := resolveDirectoryPath(filepath.Dir(absolute))
	if err != nil {
		return boundOutputTarget{}, err
	}
	insideWorkspace, err := pathWithin(workspace, parentPath)
	if err != nil {
		return boundOutputTarget{}, err
	}
	if insideWorkspace {
		return boundOutputTarget{}, fmt.Errorf("output parent resolves inside candidate workspace")
	}
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, true)
	if err != nil {
		return boundOutputTarget{}, fmt.Errorf("output ancestor is a symlink, dangling link, or non-directory: %w", err)
	}
	var stat unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
		return boundOutputTarget{parent: parent, name: name}, nil
	case err != nil:
		parent.Close()
		return boundOutputTarget{}, err
	case !allowExistingRegular:
		parent.Close()
		return boundOutputTarget{}, fmt.Errorf("output path already exists: %s", path)
	case stat.Mode&unix.S_IFMT != unix.S_IFREG:
		parent.Close()
		return boundOutputTarget{}, fmt.Errorf("result path is not a regular file: %s", path)
	default:
		return boundOutputTarget{parent: parent, name: name}, nil
	}
}

// resolveDirectoryPath canonicalizes the existing ancestor chain, including
// system aliases such as macOS /var, while retaining any missing suffix for
// no-follow creation. bindOutputTarget then opens that canonical chain one
// component at a time and rejects a resolved candidate-workspace parent.
func resolveDirectoryPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	missing := make([]string, 0)
	current := absolute
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func openAbsoluteDirectoryNoFollow(absolute string, create bool) (*os.File, error) {
	clean, err := filepath.Abs(absolute)
	if err != nil {
		return nil, err
	}
	if filepath.VolumeName(clean) != "" {
		return nil, fmt.Errorf("volume-qualified output paths are unsupported")
	}
	root, err := openDirectoryNoFollow(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	if clean == string(filepath.Separator) {
		return root, nil
	}
	current := root
	for _, segment := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		nextFD, openErr := unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), segment, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				current.Close()
				return nil, mkdirErr
			}
			nextFD, openErr = unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		}
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), segment)
		current.Close()
		current = next
	}
	return current, nil
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func randomDirectoryAt(parent *os.File, prefix string) (string, *os.File, error) {
	for range 128 {
		name, err := randomEntryName(prefix)
		if err != nil {
			return "", nil, err
		}
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", nil, err
		}
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, err
		}
		return name, os.NewFile(uintptr(fd), name), nil
	}
	return "", nil, fmt.Errorf("could not allocate a unique staged log directory")
}

func randomFileAt(parent *os.File, prefix string, mode uint32) (string, *os.File, error) {
	for range 128 {
		name, err := randomEntryName(prefix)
		if err != nil {
			return "", nil, err
		}
		file, err := createExclusiveFileAt(parent, name, mode)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, file, nil
	}
	return "", nil, fmt.Errorf("could not allocate a unique staged result")
}

func randomEntryName(prefix string) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(entropy[:]), nil
}

func createExclusiveFileAt(parent *os.File, name string, mode uint32) (*os.File, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, '\x00') {
		return nil, fmt.Errorf("file name is not one safe path component")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
