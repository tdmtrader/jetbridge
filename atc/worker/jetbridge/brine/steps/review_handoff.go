package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
)

func ReviewHandoffDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[ReviewChange, ReviewChange]("the review socket handoff encounters {string}", func(in ReviewChange, p brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			mode, _ := p.GetString(0)
			return in.runSocketHandoff(mode)
		}),
	}
}

func (in ReviewChange) runSocketHandoff(mode string) (ReviewChange, error) {
	if _, err := in.bundle(); err != nil {
		return in, err
	}
	if err := in.memoryRuntime(); err != nil {
		return in, err
	}
	in.RunID, in.Mode = 417, "handoff-wait"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	socket := filepath.Join(in.Workspace.Runtime, "auth.sock")
	timeout := "800ms"
	if mode == "disconnect after readiness" || mode == "cancel after readiness" || mode == "socket opens late" {
		timeout = "5s"
	}
	type helperResult struct {
		out, stderr []byte
		err         error
	}
	var early chan helperResult
	if mode == "socket opens late" {
		early = make(chan helperResult, 1)
		go func() {
			out, stderr, err := reviewCommand(in.Binaries.Worker, []string{"auth-handoff", "--socket", socket, "--run-id", "417", "--timeout", "3s"}, reviewSyntheticAuth)
			early <- helperResult{out, stderr, err}
		}()
		select {
		case result := <-early:
			return in, fmt.Errorf("handoff exited before its worker socket opened: %v", result.err)
		case <-time.After(250 * time.Millisecond):
		}
	}
	cmd := exec.CommandContext(ctx, in.Binaries.Worker,
		"--input", in.Input, "--output", in.Output, "--runtime-dir", in.Workspace.Runtime,
		"--codex", in.Binaries.Provider, "--model", in.Mode, "--timeout", "10s",
		"--auth-socket", socket, "--handoff-timeout", timeout, "--run-id", "417")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return in, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if early != nil {
			break
		}
		st, err := os.Lstat(socket)
		if err == nil && st.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case err := <-done:
			joined = true
			return in, fmt.Errorf("worker never opened its credential socket: %v: %s", err, stderr.Bytes())
		case <-ctx.Done():
			return in, errors.New("credential socket was never ready")
		case <-ticker.C:
		}
	}
	var helperOut, helperErr []byte
	switch mode {
	case "no connection":
	case "cancel before handoff":
		if err := reviewTerminate(cmd); err != nil {
			return in, err
		}
	case "open credential stream":
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return in, err
		}
		defer conn.Close()
		if _, err = conn.Write([]byte("jb-review-auth/v1 417\n{\"tokens\":")); err != nil {
			return in, err
		}
	default:
		runID, auth := "417", reviewSyntheticAuth
		if mode == "wrong Run" {
			runID = "418"
		}
		if mode == "invalid credentials" {
			auth = `{"OPENAI_API_KEY":"synthetic-key"}`
		}
		if mode == "oversized credentials" {
			auth = strings.Repeat("synthetic-access", 5000)
		}
		var err error
		if early != nil {
			result := <-early
			helperOut, helperErr, err = result.out, result.stderr, result.err
		} else {
			helperOut, helperErr, err = reviewCommand(in.Binaries.Worker, []string{"auth-handoff", "--socket", socket, "--run-id", runID, "--timeout", "3s"}, auth)
		}
		if mode == "disconnect after readiness" || mode == "cancel after readiness" || mode == "socket opens late" {
			if err != nil || string(helperOut) != "{\"run_id\":417,\"status\":\"ready\"}\n" {
				return in, fmt.Errorf("handoff was not acknowledged: %v: %s", err, helperErr)
			}
			// The helper has exited. Observe actual staged bytes and permissions
			// while the model subprocess waits for its fixture release.
			files, err := filepath.Glob(filepath.Join(in.Workspace.Runtime, "jb-review-*", "codex", "auth.json"))
			if err != nil || len(files) != 1 {
				return in, errors.New("readiness did not leave exactly one staged credential file")
			}
			data, err := os.ReadFile(files[0])
			if err != nil || string(data) != reviewSyntheticAuth {
				return in, errors.New("acknowledgement preceded credential staging")
			}
			for path, want := range map[string]os.FileMode{files[0]: 0600, filepath.Dir(files[0]): 0700, filepath.Dir(filepath.Dir(files[0])): 0700} {
				st, err := os.Stat(path)
				if err != nil || st.Mode().Perm() != want {
					return in, errors.New("credential runtime permissions are not private")
				}
			}
			if err := reviewAbsent(in.Output); err != nil {
				return in, errors.New("review completed before the fixture model was released")
			}
			secondOut, secondErr, second := reviewCommand(in.Binaries.Worker, []string{"auth-handoff", "--socket", socket, "--run-id", "417", "--timeout", "500ms"}, reviewSyntheticAuth)
			if second == nil || len(secondOut) != 0 || bytes.Contains(secondErr, []byte("synthetic-")) {
				return in, errors.New("worker accepted a second credential handoff")
			}
			if mode == "cancel after readiness" {
				if err := reviewTerminate(cmd); err != nil {
					return in, err
				}
			} else if err := os.WriteFile(filepath.Join(filepath.Dir(files[0]), "continue-review"), nil, 0600); err != nil {
				return in, err
			}
		} else if err == nil || len(helperOut) != 0 {
			return in, errors.New("invalid handoff received readiness")
		}
	}
	in.CommandErr = <-done
	joined = true
	if ctx.Err() != nil {
		return in, errors.New("socket handoff exceeded test deadline")
	}
	in.Stdout = append(stdout.Bytes(), helperOut...)
	in.Stderr = append(stderr.Bytes(), helperErr...)
	if mode == "no connection" || mode == "open credential stream" {
		if !bytes.Contains(in.Stderr, []byte("credential handoff timed out")) {
			return in, fmt.Errorf("abandoned handoff did not expire explicitly: %s", in.Stderr)
		}
	}
	return in, nil
}
