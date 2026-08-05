package directgit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCommandRunnerUsesFixedImageGitDespitePATH(t *testing.T) {
	tempRoot := t.TempDir()
	marker := filepath.Join(t.TempDir(), "counterfeit-git-ran")
	counterfeitDir := t.TempDir()

	// writeExecutable creates a unique path, so place its executable at the
	// attacker-controlled PATH entry named git.
	counterfeit := writeExecutable(t, fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker))
	if err := os.Rename(counterfeit, filepath.Join(counterfeitDir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", counterfeitDir)

	runner, err := NewCommandRunner(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Command{Args: []string{"--version"}}); err != nil {
		t.Fatal(err)
	}
	if runner.gitPath != "/usr/bin/git" {
		t.Errorf("git path = %q, want fixed image path /usr/bin/git", runner.gitPath)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("counterfeit Git ran: %v", err)
	}
}

func TestCommandRunnerSanitizesGitAndScrubsCredentials(t *testing.T) {
	tempRoot := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "record")
	script := writeExecutable(t, `#!/bin/sh
{
	printf 'args=%s\n' "$*"
	printf 'git_dir=%s\n' "${GIT_DIR-unset}"
	printf 'global=%s\n' "$GIT_CONFIG_GLOBAL"
	printf 'nosystem=%s\n' "$GIT_CONFIG_NOSYSTEM"
	printf 'prompt=%s\n' "$GIT_TERMINAL_PROMPT"
	printf 'no_lazy_fetch=%s\n' "$GIT_NO_LAZY_FETCH"
	printf 'path=%s\n' "$PATH"
	printf 'bash_env=%s\n' "${BASH_ENV-unset}"
	printf 'credential=%s\n' "$CONCOURSE_GIT_CREDENTIAL_FILE"
	printf 'askpass=%s\n' "$GIT_ASKPASS"
} > "$DIRECTGIT_TEST_RECORD"
password=$("$GIT_ASKPASS" "Password for remote")
printf 'stdout contains %s\n' "$password"
printf 'stderr contains %s\n' "$password" >&2
exit 9
`)
	t.Setenv("DIRECTGIT_TEST_RECORD", recordPath)
	t.Setenv("GIT_DIR", "/attacker/repository")
	t.Setenv("GIT_CONFIG_GLOBAL", "/attacker/config")
	t.Setenv("PATH", "/attacker/bin")
	t.Setenv("BASH_ENV", "/attacker/bash-env")
	runner, err := newCommandRunner(script, tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("credential-that-must-not-leak")

	result, err := runner.Run(context.Background(), Command{
		Args:       []string{"status"},
		Credential: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 9 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if strings.Contains(result.Stdout+result.Stderr, string(secret)) {
		t.Fatalf("credential leaked in result: %+v", result)
	}
	if !strings.Contains(result.Stdout, "[REDACTED]") || !strings.Contains(result.Stderr, "[REDACTED]") {
		t.Fatalf("redaction marker missing: %+v", result)
	}

	record, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	values := parseRecord(string(record))
	if values["git_dir"] != "unset" || values["global"] != os.DevNull ||
		values["nosystem"] != "1" || values["prompt"] != "0" ||
		values["no_lazy_fetch"] != "1" ||
		values["path"] != "/usr/bin:/bin" || values["bash_env"] != "unset" {
		t.Fatalf("sanitized environment = %#v", values)
	}
	for _, config := range []string{
		"core.hooksPath=" + os.DevNull,
		"credential.helper=",
		"fetch.recurseSubmodules=false",
		"submodule.recurse=false",
		"http.followRedirects=false",
		"protocol.ext.allow=never",
	} {
		if !strings.Contains(values["args"], config) {
			t.Errorf("git args missing %q: %s", config, values["args"])
		}
	}
	for _, path := range []string{values["credential"], values["askpass"]} {
		if path == "" {
			t.Fatal("runner did not create private credential material")
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("credential material still exists at %q: %v", path, err)
		}
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("credential temp root not empty: %#v", entries)
	}
	if string(secret) != "credential-that-must-not-leak" {
		t.Fatal("runner mutated caller-owned credential bytes")
	}
}

func TestScrubCredentialFileOverwritesBearerConfiguration(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "git-config")
	body := []byte("[http]\n\textraHeader = \"Authorization: Bearer scrub-me\"\n")
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := scrubCredentialFile(root, "git-config"); err != nil {
		t.Fatal(err)
	}
	scrubbed, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(scrubbed) != len(body) {
		t.Fatalf("scrubbed size = %d, want %d", len(scrubbed), len(body))
	}
	for index, value := range scrubbed {
		if value != 0 {
			t.Fatalf("scrubbed byte %d = %d, want zero", index, value)
		}
	}
}

func TestCommandRunnerHonorsCancellationAndDeadline(t *testing.T) {
	script := writeExecutable(t, "#!/bin/sh\nwhile :; do :; done\n")
	for _, test := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			want: context.Canceled,
		},
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 30*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			runner, err := newCommandRunner(script, tempRoot)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := test.context()
			defer cancel()
			_, err = runner.Run(ctx, Command{Args: []string{"status"}, Credential: []byte("secret")})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			entries, readErr := os.ReadDir(tempRoot)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("credential temp root not empty after cancellation: %#v", entries)
			}
		})
	}
}

func TestDirectGitRejectsUnsafeScratchParents(t *testing.T) {
	script := writeExecutable(t, "#!/bin/sh\nexit 0\n")

	t.Run("group or world writable without sticky bit", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(parent, 0700)
		if _, err := newCommandRunner(script, parent); err == nil {
			t.Fatal("command runner accepted an untrusted scratch parent")
		}
		if _, err := NewBackend(&fakeRunner{}, parent); err == nil {
			t.Fatal("backend accepted an untrusted scratch parent")
		}
	})

	t.Run("unsafe writable ancestor", func(t *testing.T) {
		ancestor := filepath.Join(t.TempDir(), "writable")
		parent := filepath.Join(ancestor, "scratch")
		if err := os.MkdirAll(parent, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ancestor, 0777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(ancestor, 0700)
		if _, err := newCommandRunner(script, parent); err == nil {
			t.Fatal("command runner accepted a scratch parent beneath an unsafe writable ancestor")
		}
		if _, err := NewBackend(&fakeRunner{}, parent); err == nil {
			t.Fatal("backend accepted a scratch parent beneath an unsafe writable ancestor")
		}
	})

	t.Run("final symlink", func(t *testing.T) {
		actual := t.TempDir()
		link := filepath.Join(t.TempDir(), "scratch-link")
		if err := os.Symlink(actual, link); err != nil {
			t.Fatal(err)
		}
		if _, err := newCommandRunner(script, link); err == nil {
			t.Fatal("command runner accepted a symlink scratch parent")
		}
		if _, err := NewBackend(&fakeRunner{}, link); err == nil {
			t.Fatal("backend accepted a symlink scratch parent")
		}
	})

	t.Run("sticky shared directory", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, os.ModeSticky|0777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(parent, 0700)
		if _, err := newCommandRunner(script, parent); err != nil {
			t.Fatalf("command runner rejected a sticky scratch parent: %v", err)
		}
		if _, err := NewBackend(&fakeRunner{}, parent); err != nil {
			t.Fatalf("backend rejected a sticky scratch parent: %v", err)
		}
	})
}

func TestCommandRunnerRejectsAReplacedScratchParentBeforeWritingCredentials(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "scratch")
	moved := filepath.Join(base, "original-scratch")
	marker := filepath.Join(base, "command-ran")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	script := writeExecutable(t, "#!/bin/sh\ntouch \"$DIRECTGIT_TEST_MARKER\"\n")
	t.Setenv("DIRECTGIT_TEST_MARKER", marker)
	runner, err := newCommandRunner(script, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}

	_, err = runner.Run(context.Background(), Command{
		Args:       []string{"status"},
		Credential: []byte("must-not-reach-replacement"),
	})
	if err == nil {
		t.Fatal("runner accepted a replaced scratch parent")
	}
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Git command ran after scratch replacement: %v", statErr)
	}
	for _, directory := range []string{parent, moved} {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("credential residue under %q: %#v", directory, entries)
		}
	}
}

func TestCommandRunnerRejectsScratchAncestorPermissionWeakening(t *testing.T) {
	base := t.TempDir()
	ancestor := filepath.Join(base, "ancestor")
	parent := filepath.Join(ancestor, "scratch")
	marker := filepath.Join(base, "command-ran")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	script := writeExecutable(t, "#!/bin/sh\ntouch \"$DIRECTGIT_TEST_MARKER\"\n")
	t.Setenv("DIRECTGIT_TEST_MARKER", marker)
	runner, err := newCommandRunner(script, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ancestor, 0700)

	_, err = runner.Run(context.Background(), Command{
		Args:       []string{"status"},
		Credential: []byte("must-not-reach-weakened-path"),
	})
	if err == nil {
		t.Fatal("runner accepted a scratch path after an ancestor became unsafe")
	}
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Git command ran after ancestor permissions weakened: %v", statErr)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("credential residue under weakened scratch path: %#v", entries)
	}
}

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseRecord(body string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found {
			values[name] = value
		}
	}
	return values
}
