package steps

// Review scenarios use the public commands, real Git and real stdio. The
// provider executable is the sole double: its output is deterministic and it
// never calls a model. Tmpfs enforcement and credential cleanup remain real.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/review"
)

const reviewSyntheticAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`

type reviewBinaries struct{ Root, CLI, Worker, Provider string }
type reviewWorkspace struct{ Root, Runtime string }

type ReviewChange struct {
	Workspace                                     *reviewWorkspace
	Binaries                                      reviewBinaries
	Repo, Input, Output, Plan, Base, Head, Digest string
	Stdout, Stderr                                []byte
	CommandErr                                    error
	ReaderError                                   bool
	Mode                                          string
	RunID                                         int
}

func ReviewResourceDefinitions() []brine.ResourceDefinition {
	return []brine.ResourceDefinition{
		{
			Name: "review-binaries", Scope: brine.ScopeSuite,
			Factory: func(map[string]any) (any, error) {
				root, err := os.Getwd()
				if err != nil {
					return nil, err
				}
				for {
					if _, err := os.Stat(filepath.Join(root, "agent/review/codex-version")); err == nil {
						break
					}
					parent := filepath.Dir(root)
					if parent == root {
						return nil, errors.New("cannot locate review source root")
					}
					root = parent
				}
				dir, err := AttributedTempDir("brine-review-binaries-")
				if err != nil {
					return nil, err
				}
				b := reviewBinaries{Root: dir, CLI: filepath.Join(dir, "jb"), Worker: filepath.Join(dir, "jb-review-worker"), Provider: filepath.Join(dir, "provider")}
				for _, build := range []struct{ output, pkg string }{{b.CLI, "./cmd/jb"}, {b.Worker, "./cmd/jb-review-worker"}, {b.Provider, "./agent/review/testdata/provider"}} {
					cmd := exec.Command("go", "build", "-o", build.output, build.pkg)
					cmd.Dir = root
					if out, err := cmd.CombinedOutput(); err != nil {
						os.RemoveAll(dir)
						return nil, fmt.Errorf("build review executable: %w: %s", err, out)
					}
				}
				return b, nil
			},
			Disposer: func(v any) error { return os.RemoveAll(v.(reviewBinaries).Root) },
		},
		{
			Name: "review-workspace", Scope: brine.ScopeScenario,
			Factory: func(map[string]any) (any, error) {
				dir, err := AttributedTempDir("brine-review-")
				return &reviewWorkspace{Root: dir}, err
			},
			Disposer: func(v any) error {
				w := v.(*reviewWorkspace)
				var err error
				if w.Runtime != "" {
					err = os.RemoveAll(w.Runtime)
				}
				return errors.Join(err, os.RemoveAll(w.Root))
			},
		},
	}
}

func ReviewDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ReviewChange]("a committed review change with a deleted file and an external plan", []string{"review-binaries", "review-workspace"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ReviewChange, error) {
				return newReviewChange(res)
			}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review repository contains {string}", func(in ReviewChange, p brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			kind, _ := p.GetString(0)
			var err error
			switch kind {
			case "dirty":
				err = os.WriteFile(filepath.Join(in.Repo, "parser.go"), []byte("dirty"), 0600)
			case "untracked":
				err = os.WriteFile(filepath.Join(in.Repo, "extra"), []byte("untracked"), 0600)
			case "submodule":
				if _, err = in.git("update-index", "--add", "--cacheinfo", "160000,"+in.Head+",sub"); err != nil {
					return in, err
				}
				if err = os.Mkdir(filepath.Join(in.Repo, "sub"), 0700); err != nil {
					return in, err
				}
				if _, err = in.git("commit", "-qm", "submodule"); err != nil {
					return in, err
				}
				in.Head, err = in.git("rev-parse", "HEAD")
			case "lfs":
				if err = os.WriteFile(filepath.Join(in.Repo, "asset"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 12\n"), 0600); err != nil {
					return in, err
				}
				in.Head, err = in.commit("lfs")
			default:
				err = fmt.Errorf("unknown repository state %q", kind)
			}
			return in, err
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review change is captured from the command line", func(in ReviewChange, _ brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			in.Stdout, in.Stderr, in.CommandErr = reviewCommand(in.Binaries.CLI, in.captureArgs(in.Input), "")
			if in.CommandErr == nil {
				var reply struct {
					Digest string `json:"input_digest"`
				}
				if err := json.Unmarshal(in.Stdout, &reply); err != nil {
					return in, err
				}
				in.Digest = reply.Digest
			}
			return in, nil
		}),
		CheckThat[ReviewChange]("the captured trees and plan match the committed inputs", func(in ReviewChange) error {
			b, err := in.bundle()
			if err != nil {
				return err
			}
			if b.Manifest.BaseCommit != in.Base || b.Manifest.HeadCommit != in.Head || b.Manifest.PlanDigest == nil {
				return errors.New("capture did not bind both exact commits and plan")
			}
			for name, want := range map[string]string{"head/parser.go": "package parser\nfunc First(s string) byte { return s[1] }\n", "base/deleted.txt": "removed line\n", "plan.md": "Return the first byte.\n"} {
				got, err := os.ReadFile(filepath.Join(in.Input, name))
				if err != nil || string(got) != want {
					return fmt.Errorf("captured %s differs from submitted content: %v", name, err)
				}
			}
			return nil
		}),
		CheckThat[ReviewChange]("recapturing the same change produces the same input digest", func(in ReviewChange) error {
			if _, err := in.bundle(); err != nil {
				return err
			}
			out, stderr, err := reviewCommand(in.Binaries.CLI, in.captureArgs(filepath.Join(in.Workspace.Root, "second-input")), "")
			if err != nil {
				return fmt.Errorf("second capture: %w: %s", err, stderr)
			}
			var result map[string]string
			if err := json.Unmarshal(out, &result); err != nil {
				return err
			}
			if result["input_digest"] != in.Digest {
				return errors.New("same input changed digest")
			}
			return nil
		}),
		check[ReviewChange]("capture fails naming {string} without publishing a bundle", func(in ReviewChange, p brine.Params) error {
			want, _ := p.GetString(0)
			if in.CommandErr == nil || !bytes.Contains(in.Stderr, []byte(want)) {
				return fmt.Errorf("capture did not fail naming %q: %v: %s", want, in.CommandErr, in.Stderr)
			}
			return reviewAbsent(in.Input)
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the captured head file is modified", func(in ReviewChange, _ brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			if _, err := in.bundle(); err != nil {
				return in, err
			}
			return in, os.WriteFile(filepath.Join(in.Input, "head/parser.go"), []byte("tampered\n"), 0600)
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("a fresh review input reader reads {string}", func(in ReviewChange, p brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			if in.CommandErr != nil {
				return in, fmt.Errorf("capture failed: %w: %s", in.CommandErr, in.Stderr)
			}
			path, _ := p.GetString(0)
			var stdin bytes.Buffer
			enc := json.NewEncoder(&stdin)
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "brine-review", "version": "1"}}})
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "read", "arguments": map[string]any{"path": path}}})
			in.Stdout, in.Stderr, in.CommandErr = reviewCommand(in.Binaries.Worker, []string{"input-tools", "--input", in.Input}, stdin.String())
			if in.CommandErr != nil {
				return in, nil
			}
			dec := json.NewDecoder(bytes.NewReader(in.Stdout))
			var init map[string]any
			if err := dec.Decode(&init); err != nil {
				return in, err
			}
			var result struct {
				ID     int `json:"id"`
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := dec.Decode(&result); err != nil {
				return in, err
			}
			if result.ID != 2 {
				return in, errors.New("reader response belongs to another request")
			}
			in.ReaderError = result.Result.IsError
			return in, nil
		}),
		check[ReviewChange]("the review reader returns text containing {string}", func(in ReviewChange, p brine.Params) error {
			want, _ := p.GetString(0)
			if in.CommandErr != nil || in.ReaderError || !bytes.Contains(in.Stdout, []byte(want)) {
				return fmt.Errorf("reader did not serve captured text: %v: %s", in.CommandErr, in.Stderr)
			}
			return nil
		}),
		CheckThat[ReviewChange]("the review reader refuses the path", func(in ReviewChange) error {
			if in.CommandErr != nil || !in.ReaderError || bytes.Contains(in.Stdout, []byte("synthetic-")) {
				return errors.New("reader did not refuse the credential path")
			}
			return nil
		}),
		CheckThat[ReviewChange]("the review reader refuses to start", func(in ReviewChange) error {
			if in.CommandErr == nil || len(in.Stdout) != 0 {
				return errors.New("reader served a modified bundle")
			}
			return nil
		}),
	}
}

func (in ReviewChange) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", in.Repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "core.fsmonitor=false", "-c", "user.name=Review fixture", "-c", "user.email=review@example.test"}, args...)...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git fixture: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (in ReviewChange) commit(message string) (string, error) {
	if _, err := in.git("add", "."); err != nil {
		return "", err
	}
	if _, err := in.git("commit", "-qm", message); err != nil {
		return "", err
	}
	return in.git("rev-parse", "HEAD")
}

func (in ReviewChange) captureArgs(output string) []string {
	return []string{"review", "capture", "--repo", in.Repo, "--base", in.Base, "--head", in.Head, "--plan", in.Plan, "--output", output}
}

func (in ReviewChange) bundle() (*review.Bundle, error) {
	if in.Digest == "" {
		return nil, fmt.Errorf("capture did not produce a digest: %v: %s", in.CommandErr, in.Stderr)
	}
	b, err := review.LoadBundle(in.Input)
	if err != nil {
		return nil, err
	}
	if b.Digest != in.Digest {
		return nil, errors.New("bundle differs from CLI digest")
	}
	return b, nil
}

func reviewCommand(binary string, args []string, stdin string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	return out.Bytes(), stderr.Bytes(), err
}

func reviewAbsent(path string) error {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("unexpected published path %s: %v", path, err)
	}
	return nil
}

func (in *ReviewChange) memoryRuntime() error {
	if runtime.GOOS != "linux" {
		return errors.New("review worker scenarios require Linux tmpfs; locally exclude @review-linux")
	}
	if in.Workspace.Runtime != "" {
		return nil
	}
	dir, err := os.MkdirTemp("/dev/shm", "brine-review-")
	in.Workspace.Runtime = dir
	return err
}

func reviewNoCredentials(in ReviewChange) error {
	if in.Workspace.Runtime == "" {
		return errors.New("no credential session was attempted")
	}
	entries, err := os.ReadDir(in.Workspace.Runtime)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("review session retained original, refreshed or temporary credentials")
	}
	if bytes.Contains(in.Stdout, []byte("synthetic-")) || bytes.Contains(in.Stderr, []byte("synthetic-")) {
		return errors.New("credential contents reached command output")
	}
	if err := reviewAbsent(in.Output); err == nil {
		return nil
	}
	return filepath.WalkDir(in.Output, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte("synthetic-")) {
			return errors.New("credential contents reached published report")
		}
		return nil
	})
}

// Keep process signaling here so the behavioral steps exercise the actual CLI's
// signal handler rather than cancelling a substituted Worker interface.
func reviewTerminate(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.SIGTERM) }

func newReviewChange(res brine.Resources) (ReviewChange, error) {
	w := res.Get("review-workspace").(*reviewWorkspace)
	in := ReviewChange{Workspace: w, Binaries: res.Get("review-binaries").(reviewBinaries), Repo: filepath.Join(w.Root, "repo"), Input: filepath.Join(w.Root, "input"), Output: filepath.Join(w.Root, "report"), Plan: filepath.Join(w.Root, "plan.md")}
	if err := os.Mkdir(in.Repo, 0700); err != nil {
		return in, err
	}
	if _, err := in.git("init", "-q"); err != nil {
		return in, err
	}
	for name, text := range map[string]string{
		"repo/parser.go":      "package parser\nfunc First(s string) byte { return s[0] }\n",
		"repo/deleted.txt":    "removed line\n",
		"repo/.gitattributes": "parser.go export-ignore\n",
		"plan.md":             "Return the first byte.\n",
		"auth.json":           reviewSyntheticAuth,
	} {
		if err := os.WriteFile(filepath.Join(w.Root, name), []byte(text), 0600); err != nil {
			return in, err
		}
	}
	var err error
	if in.Base, err = in.commit("base"); err != nil {
		return in, err
	}
	if err := os.Remove(filepath.Join(in.Repo, "deleted.txt")); err != nil {
		return in, err
	}
	if err := os.WriteFile(filepath.Join(in.Repo, "parser.go"), []byte("package parser\nfunc First(s string) byte { return s[1] }\n"), 0600); err != nil {
		return in, err
	}
	in.Head, err = in.commit("head")
	return in, err
}
