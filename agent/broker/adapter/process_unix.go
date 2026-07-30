//go:build unix

package adapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"
)

// Execute runs one already-constructed native invocation directly, without a
// shell. A dedicated process group lets cancellation reap harness descendants
// as well as the top-level CLI.
func Execute(
	ctx context.Context,
	prepared PreparedInvocation,
	prompt string,
	maxStreamBytes int,
) (StreamResult, error) {
	invocation := prepared.invocation
	name := prepared.identity.Name
	if invocation.Binary == "" || invocation.WorkDir == "" {
		return StreamResult{}, fmt.Errorf("broker adapter: prepared invocation binary and working directory are required")
	}
	if prepared.identity.Binary != invocation.Binary || prepared.identity.Version == "" {
		return StreamResult{}, fmt.Errorf("broker adapter: prepared invocation identity is invalid")
	}
	command := exec.Command(invocation.Binary, invocation.Args...)
	command.Dir = invocation.WorkDir
	command.Env = controlledEnvironment(invocation.Env)
	command.Stdin = bytes.NewBufferString(prompt)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout limitedBuffer
	stdout.limit = maxStreamBytes + 1
	var stderr limitedBuffer
	stderr.limit = 64 << 10
	command.Stdout = &stdout
	command.Stderr = &stderr

	started := time.Now()
	if err := command.Start(); err != nil {
		return StreamResult{}, fmt.Errorf("broker adapter: start native process: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-waited
		return StreamResult{}, fmt.Errorf("broker adapter: native process cancelled: %w", ctx.Err())
	case err := <-waited:
		if err != nil {
			return StreamResult{}, fmt.Errorf("broker adapter: native process exited unsuccessfully")
		}
	}
	if stdout.overflow {
		return StreamResult{}, fmt.Errorf("broker adapter: native stream exceeds byte limit %d", maxStreamBytes)
	}
	result, err := DecodeStream(name, bytes.NewReader(stdout.Bytes()), maxStreamBytes)
	if err != nil {
		return StreamResult{}, err
	}
	if result.Usage.Duration == nil {
		duration := time.Since(started)
		result.Usage.Duration = &duration
	}
	return result, nil
}

func controlledEnvironment(values map[string]string) []string {
	environment := map[string]string{
		"LC_ALL": "C",
		"PATH":   os.Getenv("PATH"),
	}
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value := os.Getenv(name); value != "" {
			environment[name] = value
		}
	}
	for name, value := range values {
		environment[name] = value
	}
	keys := make([]string, 0, len(environment))
	for name := range environment {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+environment[name])
	}
	return result
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.overflow = true
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}
