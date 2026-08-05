package directgit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const (
	maxCommandOutputBytes = 64 << 10
	askpassUser           = "oauth2"
	controlledPath        = "/usr/bin:/bin"
	imageGitPath          = "/usr/bin/git"
)

// AuthenticationMode selects the credential transport for one controlled Git
// invocation. The zero value preserves the historical askpass behavior when a
// credential is present; callers that need a provider-specific transport must
// select it explicitly.
type AuthenticationMode string

const (
	AuthenticationDefault AuthenticationMode = ""
	AuthenticationAskpass AuthenticationMode = "askpass"
)

// Command is one controlled Git invocation. Credential is an ephemeral copy
// that must never appear in Args or be retained by a Runner.
type Command struct {
	Dir            string
	Args           []string
	Credential     []byte
	Authentication AuthenticationMode
	// NoRepository prevents Git from discovering repository-local config.
	// It is required for remote reads that must use the exact policy URL.
	NoRepository bool
}

func (command Command) clone() Command {
	command.Args = append([]string(nil), command.Args...)
	command.Credential = append([]byte(nil), command.Credential...)
	return command
}

func (command Command) validate() error {
	if len(command.Args) == 0 {
		return fmt.Errorf("direct git: command arguments are required")
	}
	if command.Dir != "" && (!filepath.IsAbs(command.Dir) || filepath.Clean(command.Dir) != command.Dir) {
		return fmt.Errorf("direct git: command directory must be an absolute clean path")
	}
	if command.NoRepository && command.Dir != "" {
		return fmt.Errorf("direct git: repository-free command must not select a repository directory")
	}
	for _, argument := range command.Args {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("direct git: command argument contains NUL")
		}
	}
	switch command.Authentication {
	case AuthenticationDefault:
	case AuthenticationAskpass:
		if len(command.Credential) == 0 {
			return fmt.Errorf("direct git: selected authentication requires a credential")
		}
	default:
		return fmt.Errorf("direct git: authentication mode is invalid")
	}
	return nil
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Runner interface {
	Run(context.Context, Command) (CommandResult, error)
}

// CommandRunner executes Git without ambient Git configuration and supplies a
// destination secret through a per-command private askpass directory.
type CommandRunner struct {
	gitPath    string
	tempParent trustedTempParent
}

func NewCommandRunner(tempDir string) (*CommandRunner, error) {
	return newCommandRunner(imageGitPath, tempDir)
}

func newCommandRunner(gitPath, tempDir string) (*CommandRunner, error) {
	if gitPath == "" || !filepath.IsAbs(gitPath) || filepath.Clean(gitPath) != gitPath {
		return nil, fmt.Errorf("direct git: git executable must be an absolute clean path")
	}
	info, err := os.Stat(gitPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("direct git: git executable is unavailable")
	}
	tempParent, err := newTrustedTempParent(tempDir)
	if err != nil {
		return nil, err
	}
	return &CommandRunner{gitPath: gitPath, tempParent: tempParent}, nil
}

func (runner *CommandRunner) Run(ctx context.Context, command Command) (result CommandResult, returnedErr error) {
	if ctx == nil {
		return CommandResult{}, fmt.Errorf("direct git: context is required")
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if runner == nil {
		return CommandResult{}, fmt.Errorf("direct git: runner is required")
	}
	if err := command.validate(); err != nil {
		return CommandResult{}, err
	}

	secret := append([]byte(nil), command.Credential...)
	defer wipeBytes(secret)
	invocation, err := runner.prepareInvocation(secret, command.Authentication)
	if err != nil {
		return CommandResult{}, err
	}
	defer func() {
		if cleanupErr := invocation.Close(); cleanupErr != nil {
			result = CommandResult{}
			returnedErr = errors.Join(
				returnedErr,
				fmt.Errorf("direct git: scrub credential material: %w", cleanupErr),
			)
		}
	}()
	if err := invocation.Verify(); err != nil {
		return CommandResult{}, err
	}

	args := append(controlledGitArguments(), command.Args...)
	cmd := exec.CommandContext(ctx, runner.gitPath, args...)
	cmd.Dir = command.Dir
	if cmd.Dir == "" {
		// Authorized commands run from their new private credential directory,
		// never from ATC's potentially repository-local working directory.
		cmd.Dir = invocation.path
	}
	noRepositoryGitDir := ""
	if command.NoRepository {
		noRepositoryGitDir = filepath.Join(invocation.path, "no-repository")
	}
	cmd.Env = controlledGitEnvironment(
		invocation.credentialFile,
		invocation.askpass,
		invocation.path,
		noRepositoryGitDir,
	)
	var stdout, stderr boundedBuffer
	stdout.maximum = maxCommandOutputBytes
	stderr.maximum = maxCommandOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	commandResult := CommandResult{
		Stdout: redactCommandText(stdout.String(), secret, invocation.path),
		Stderr: redactCommandText(stderr.String(), secret, invocation.path),
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return CommandResult{}, contextErr
	}
	if runErr == nil {
		return commandResult, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		commandResult.ExitCode = exitErr.ExitCode()
		return commandResult, nil
	}
	return CommandResult{}, fmt.Errorf("direct git: execute git command: %w", runErr)
}

type trustedTempParent struct {
	path string
	info os.FileInfo
}

func newTrustedTempParent(tempDir string) (trustedTempParent, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if !filepath.IsAbs(tempDir) || filepath.Clean(tempDir) != tempDir {
		return trustedTempParent{}, fmt.Errorf("direct git: scratch parent must be an absolute clean path")
	}
	if strings.ContainsRune(tempDir, os.PathListSeparator) {
		return trustedTempParent{}, fmt.Errorf("direct git: scratch parent contains the platform path-list separator")
	}
	suppliedInfo, err := os.Lstat(tempDir)
	if err != nil {
		return trustedTempParent{}, fmt.Errorf("direct git: inspect scratch parent: %w", err)
	}
	if suppliedInfo.Mode()&os.ModeSymlink != 0 {
		return trustedTempParent{}, fmt.Errorf("direct git: scratch parent must not be a symlink")
	}
	resolved, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		return trustedTempParent{}, fmt.Errorf("direct git: resolve scratch parent: %w", err)
	}
	if !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return trustedTempParent{}, fmt.Errorf("direct git: resolved scratch parent is invalid")
	}
	if strings.ContainsRune(resolved, os.PathListSeparator) {
		return trustedTempParent{}, fmt.Errorf("direct git: resolved scratch parent contains the platform path-list separator")
	}
	if err := validateTrustedTempAncestors(resolved); err != nil {
		return trustedTempParent{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(suppliedInfo, info) {
		return trustedTempParent{}, fmt.Errorf("direct git: scratch parent is unavailable")
	}
	if err := snapshot.ValidateTempDir(resolved); err != nil {
		return trustedTempParent{}, fmt.Errorf("direct git: validate scratch parent: %w", err)
	}
	return trustedTempParent{path: resolved, info: info}, nil
}

func validateTrustedTempAncestors(path string) error {
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(path, current)
	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("direct git: inspect scratch ancestor: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("direct git: scratch path contains a non-directory or symlink ancestor")
		}
		if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("direct git: scratch path has a group- or other-writable ancestor without the sticky bit")
		}
	}
	return nil
}

func (parent trustedTempParent) Open() (*os.Root, error) {
	if err := validateTrustedTempAncestors(parent.path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(parent.path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() ||
		!os.SameFile(parent.info, before) {
		return nil, fmt.Errorf("direct git: scratch parent changed after validation")
	}
	if err := snapshot.ValidateTempDir(parent.path); err != nil {
		return nil, fmt.Errorf("direct git: revalidate scratch parent: %w", err)
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return nil, fmt.Errorf("direct git: anchor scratch parent: %w", err)
	}
	opened, openedErr := root.Stat(".")
	after, afterErr := os.Lstat(parent.path)
	if openedErr != nil || afterErr != nil ||
		after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(parent.info, opened) ||
		!os.SameFile(parent.info, after) {
		_ = root.Close()
		return nil, fmt.Errorf("direct git: scratch parent changed while opening")
	}
	return root, nil
}

func (parent trustedTempParent) Verify() error {
	root, err := parent.Open()
	if err != nil {
		return err
	}
	return root.Close()
}

// privateScratch owns a unique, descriptor-anchored directory beneath a
// validated scratch parent. Callers must Verify before passing its pathname to
// a subprocess, and Close always removes it through the retained parent root.
type privateScratch struct {
	parent     trustedTempParent
	parentRoot *os.Root
	root       *os.Root
	name       string
	path       string
	info       os.FileInfo
}

type privateInvocation struct {
	*privateScratch
	credentialFile string
	askpass        string
}

func (runner *CommandRunner) prepareInvocation(
	secret []byte,
	authentication AuthenticationMode,
) (*privateInvocation, error) {
	invocation, err := runner.tempParent.createPrivateInvocation()
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 {
		return invocation, nil
	}
	invocation.credentialFile = filepath.Join(invocation.path, "credential")
	if err := writePrivateFile(invocation.root, "credential", secret, 0600); err != nil {
		return nil, errors.Join(
			fmt.Errorf("direct git: write credential: %w", err),
			invocation.Close(),
		)
	}
	invocation.askpass = filepath.Join(invocation.path, "askpass")
	if err := writePrivateFile(invocation.root, "askpass", []byte(askpassScript), 0700); err != nil {
		return nil, errors.Join(
			fmt.Errorf("direct git: write askpass helper: %w", err),
			invocation.Close(),
		)
	}
	return invocation, nil
}

func (parent trustedTempParent) createPrivateInvocation() (*privateInvocation, error) {
	scratch, err := parent.createPrivateScratch("concourse-direct-git-")
	if err != nil {
		return nil, err
	}
	return &privateInvocation{privateScratch: scratch}, nil
}

func (parent trustedTempParent) createPrivateScratch(prefix string) (*privateScratch, error) {
	if prefix == "" || strings.ContainsAny(prefix, `/\\`) {
		return nil, fmt.Errorf("direct git: private scratch prefix is invalid")
	}
	parentRoot, err := parent.Open()
	if err != nil {
		return nil, err
	}
	for attempts := 0; attempts < 100; attempts++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			_ = parentRoot.Close()
			return nil, fmt.Errorf("direct git: generate private scratch name: %w", err)
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := parentRoot.Mkdir(name, 0700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			_ = parentRoot.Close()
			return nil, fmt.Errorf("direct git: create private invocation directory: %w", err)
		}
		path := filepath.Join(parent.path, name)
		info, statErr := parentRoot.Lstat(name)
		root, openErr := parentRoot.OpenRoot(name)
		var opened os.FileInfo
		if openErr == nil {
			opened, openErr = root.Stat(".")
		}
		pathInfo, pathErr := os.Lstat(path)
		if statErr != nil || openErr != nil || pathErr != nil ||
			!info.IsDir() || info.Mode().Perm()&0077 != 0 ||
			!os.SameFile(info, opened) || !os.SameFile(info, pathInfo) {
			if root != nil {
				_ = root.Close()
			}
			_ = parentRoot.RemoveAll(name)
			_ = parentRoot.Close()
			return nil, fmt.Errorf("direct git: private scratch directory changed while opening")
		}
		scratch := &privateScratch{
			parent: parent, parentRoot: parentRoot, root: root,
			name: name, path: path, info: info,
		}
		if err := scratch.Verify(); err != nil {
			return nil, errors.Join(err, scratch.Close())
		}
		return scratch, nil
	}
	_ = parentRoot.Close()
	return nil, fmt.Errorf("direct git: allocate private scratch directory")
}

func (scratch *privateScratch) Verify() error {
	if scratch == nil || scratch.parentRoot == nil || scratch.root == nil {
		return fmt.Errorf("direct git: private scratch directory is unavailable")
	}
	parentPathInfo, parentPathErr := os.Lstat(scratch.parent.path)
	parentOpened, parentOpenedErr := scratch.parentRoot.Stat(".")
	childPathInfo, childPathErr := os.Lstat(scratch.path)
	childBoundInfo, childBoundErr := scratch.parentRoot.Lstat(scratch.name)
	childOpened, childOpenedErr := scratch.root.Stat(".")
	if parentPathErr != nil || parentOpenedErr != nil ||
		childPathErr != nil || childBoundErr != nil || childOpenedErr != nil ||
		!os.SameFile(scratch.parent.info, parentPathInfo) ||
		!os.SameFile(scratch.parent.info, parentOpened) ||
		!os.SameFile(scratch.info, childPathInfo) ||
		!os.SameFile(scratch.info, childBoundInfo) ||
		!os.SameFile(scratch.info, childOpened) {
		return fmt.Errorf("direct git: scratch parent or private scratch directory was replaced")
	}
	return nil
}

func (scratch *privateScratch) Close() error {
	if scratch == nil {
		return nil
	}
	var cleanupErr error
	if scratch.root != nil {
		entries, err := fs.ReadDir(scratch.root.FS(), ".")
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else {
			for _, entry := range entries {
				cleanupErr = errors.Join(cleanupErr, scratch.root.RemoveAll(entry.Name()))
			}
		}
		cleanupErr = errors.Join(cleanupErr, scratch.root.Close())
		scratch.root = nil
	}
	if scratch.parentRoot != nil {
		cleanupErr = errors.Join(
			cleanupErr,
			scratch.parentRoot.RemoveAll(scratch.name),
			scratch.parentRoot.Close(),
		)
		scratch.parentRoot = nil
	}
	return cleanupErr
}

func (invocation *privateInvocation) Close() error {
	if invocation == nil || invocation.privateScratch == nil {
		return nil
	}
	var cleanupErr error
	if invocation.root != nil {
		cleanupErr = errors.Join(
			cleanupErr,
			scrubCredentialFile(invocation.root, "credential"),
			scrubCredentialFile(invocation.root, "git-config"),
		)
	}
	return errors.Join(cleanupErr, invocation.privateScratch.Close())
}

const askpassScript = `#!/bin/sh
case "$1" in
  *sername*|*Username*|*login*|*Login*) printf '%s\n' '` + askpassUser + `' ;;
  *) exec cat "$CONCOURSE_GIT_CREDENTIAL_FILE" ;;
esac
`

func writePrivateFile(root *os.Root, name string, body []byte, mode os.FileMode) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func scrubCredentialFile(root *os.Root, name string) error {
	if root == nil || name == "" {
		return nil
	}
	file, err := root.OpenFile(name, os.O_WRONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr == nil && info.Size() > 0 {
		zeroes := make([]byte, min(info.Size(), int64(32<<10)))
		remaining := info.Size()
		for remaining > 0 {
			chunk := min(remaining, int64(len(zeroes)))
			if _, err := file.Write(zeroes[:chunk]); err != nil {
				statErr = errors.Join(statErr, err)
				break
			}
			remaining -= chunk
		}
	}
	if syncErr := file.Sync(); syncErr != nil {
		statErr = errors.Join(statErr, syncErr)
	}
	return errors.Join(statErr, file.Close())
}

func controlledGitArguments() []string {
	config := []string{
		"core.hooksPath=" + os.DevNull,
		"core.fsmonitor=false",
		"core.autocrlf=false",
		"core.safecrlf=false",
		"core.attributesFile=" + os.DevNull,
		"core.excludesFile=" + os.DevNull,
		"credential.helper=",
		"credential.interactive=never",
		"fetch.recurseSubmodules=false",
		"submodule.recurse=false",
		"filter.lfs.process=",
		"filter.lfs.smudge=",
		"filter.lfs.clean=",
		"http.followRedirects=false",
		"protocol.ext.allow=never",
		"protocol.file.allow=always",
	}
	args := make([]string, 0, len(config)*2)
	for _, value := range config {
		args = append(args, "-c", value)
	}
	return args
}

func controlledGitEnvironment(
	credentialFile, askpass, neutralDirectory, noRepositoryGitDir string,
) []string {
	environment := make([]string, 0, len(os.Environ())+12)
	for _, variable := range os.Environ() {
		name := variable
		if separator := strings.IndexByte(variable, '='); separator >= 0 {
			name = variable[:separator]
		}
		if strings.HasPrefix(name, "GIT_") ||
			strings.HasPrefix(name, "SSH_") ||
			strings.HasPrefix(name, "LD_") ||
			strings.HasPrefix(name, "DYLD_") ||
			name == "LC_ALL" ||
			name == "CONCOURSE_GIT_CREDENTIAL_FILE" ||
			name == "DISPLAY" ||
			name == "PATH" ||
			name == "HOME" ||
			name == "XDG_CONFIG_HOME" ||
			name == "BASH_ENV" ||
			name == "ENV" ||
			name == "CDPATH" ||
			name == "SHELLOPTS" {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CEILING_DIRECTORIES="+neutralDirectory,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_SSH_COMMAND=ssh -F "+os.DevNull+" -oBatchMode=no -oIdentitiesOnly=yes",
		"LC_ALL=C",
		"PATH="+controlledPath,
		"HOME=/nonexistent",
		"XDG_CONFIG_HOME=/nonexistent",
	)
	if noRepositoryGitDir != "" {
		environment = append(environment, "GIT_DIR="+noRepositoryGitDir)
	}
	if credentialFile != "" {
		environment = append(environment,
			"CONCOURSE_GIT_CREDENTIAL_FILE="+credentialFile,
			"GIT_ASKPASS="+askpass,
			"GIT_ASKPASS_REQUIRE=force",
			"SSH_ASKPASS="+askpass,
			"SSH_ASKPASS_REQUIRE=force",
			"DISPLAY=concourse-direct-git",
		)
	}
	return environment
}

func redactCommandText(text string, secret []byte, credentialDir string) string {
	if len(secret) > 0 {
		text = strings.ReplaceAll(text, string(secret), "[REDACTED]")
		if trimmed := strings.TrimSpace(string(secret)); trimmed != "" {
			text = strings.ReplaceAll(text, trimmed, "[REDACTED]")
		}
	}
	if credentialDir != "" {
		text = strings.ReplaceAll(text, credentialDir, "[CREDENTIAL]")
	}
	return text
}

func wipeBytes(body []byte) {
	for index := range body {
		body[index] = 0
	}
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func (buffer *boundedBuffer) Write(body []byte) (int, error) {
	original := len(body)
	if remaining := buffer.maximum - buffer.buffer.Len(); remaining > 0 {
		if len(body) > remaining {
			body = body[:remaining]
			buffer.truncated = true
		}
		_, _ = buffer.buffer.Write(body)
	} else if len(body) > 0 {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	if !buffer.truncated {
		return buffer.buffer.String()
	}
	var output strings.Builder
	_, _ = io.Copy(&output, bytes.NewReader(buffer.buffer.Bytes()))
	output.WriteString("\n[output truncated]")
	return output.String()
}
