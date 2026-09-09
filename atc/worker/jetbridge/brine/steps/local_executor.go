package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/creack/pty"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// localExecutor runs real commands on the host. root optionally supplies a
// resource image and a filesystem for tar's pod-absolute paths; present models
// which containers exist. With client, destinations and tar mount paths are
// resolved against actual pod objects. These options do not record calls. Kubernetes scheduling,
// mount namespaces and the remote exec transport still require cluster tests.
type localExecutor struct {
	root           string
	failure        string
	present        map[string]bool
	client         kubernetes.Interface
	supervisorRoot string
}

func (l localExecutor) ExecInPod(
	ctx context.Context, namespace, podName string, container string, command []string,
	stdin io.Reader, stdout, stderr io.Writer, tty bool, _ jetbridge.ExecAttrs,
) error {
	if l.failure != "" {
		return errors.New(l.failure)
	}
	if l.present != nil && !l.present[container] {
		return fmt.Errorf("container %q not found in pod (has: %s)", container, strings.Join(sortedKeys(l.present), ", "))
	}
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	var pod *corev1.Pod
	if l.client != nil {
		var err error
		pod, err = l.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		found := false
		for _, c := range pod.Spec.Containers {
			found = found || c.Name == container
		}
		if !found {
			return fmt.Errorf("container %q does not exist in pod %q", container, podName)
		}
	}
	args := append([]string(nil), command...)
	if err := scopeSupervisor(args, l.supervisorRoot); err != nil {
		return err
	}
	if l.root != "" {
		program := filepath.Join(l.root, filepath.Clean("/"+args[0]))
		if _, err := os.Stat(program); err == nil {
			args[0] = program
		}
		for i := 1; i < len(args); i++ {
			if command[i-1] == "-C" {
				args[i] = filepath.Join(l.root, filepath.Clean("/"+args[i]))
				if err := os.MkdirAll(args[i], 0o755); err != nil {
					return fmt.Errorf("prepare %q: %w", args[i], err)
				}
			}
		}
	}
	if pod != nil {
		for i := 1; i < len(args); i++ {
			if command[i-1] == "-C" {
				var err error
				args[i], err = podHostDir(pod, podName, command[i])
				if err != nil {
					return err
				}
				if err := os.MkdirAll(args[i], 0o755); err != nil {
					return err
				}
			}
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	cmd.WaitDelay = 2 * time.Second
	// Cancellation kills descendants too, including a supervisor's log tail.
	// Every command owns a process group (or, for a PTY, a new session).
	cleanup := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = cleanup
	defer func() { _ = cleanup() }()
	if tty {
		// pty.Start establishes the session and controlling terminal itself.
		terminal, err := pty.Start(cmd)
		if err != nil {
			return fmt.Errorf("allocate pty: %w", err)
		}
		defer terminal.Close()
		if stdin != nil {
			go func() {
				_, _ = io.Copy(terminal, stdin)
				// End canonical input, including a final line without a newline.
				_, _ = terminal.Write([]byte{4, 4})
			}()
		}
		if stdout == nil {
			stdout = io.Discard
		}
		drained := make(chan struct{})
		go func() { _, _ = io.Copy(stdout, terminal); close(drained) }()
		err = cmd.Wait()
		_ = cleanup()
		<-drained
		return execExitError(ctx, err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var captured bytes.Buffer
	if stderr == nil {
		stderr = &captured
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := execExitError(ctx, cmd.Run())
	if err != nil && captured.Len() > 0 {
		return fmt.Errorf("exec %v: %w: %s", command, err, strings.TrimSpace(captured.String()))
	}
	return err
}

const supervisorStateDirectory = "supervisor"

// scopeSupervisor maps the pod's /tmp state into an owned fixture workspace.
// Preserve the production-generated basename (process ID and command hash),
// and the rest of the script byte-for-byte. Never inspect or delete host state.
func scopeSupervisor(args []string, root string) error {
	if root == "" || len(args) != 3 || args[0] != "sh" || args[1] != "-c" || !strings.HasPrefix(args[2], "S=") {
		return nil
	}
	header, body, ok := strings.Cut(args[2], "\n")
	if !ok || !strings.HasPrefix(header, "S='") || !strings.HasSuffix(header, "'") {
		return fmt.Errorf("unsupported supervisor state assignment %q", header)
	}
	state := header[3 : len(header)-1]
	if !filepath.IsAbs(root) || filepath.Clean(root) == "/" ||
		filepath.Dir(state) != "/tmp" || filepath.Clean(state) != state || strings.ContainsAny(state, "'\r\n") {
		return fmt.Errorf("unsafe supervisor state mapping %q into %q", state, root)
	}
	mapped := filepath.Join(root, supervisorStateDirectory, filepath.Base(state))
	args[2] = "S='" + strings.ReplaceAll(mapped, "'", "'\\''") + "'\n" + body
	return nil
}

func execExitError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &jetbridge.ExecExitError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

// A dead child can remain a zombie until init reaps it. It must not remain
// alive after the shared executor returns from cancellation.
func processStopped(pid string) error {
	if n, err := strconv.Atoi(pid); err != nil || n <= 0 {
		return fmt.Errorf("invalid child PID %q", pid)
	}
	state, err := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect child %s: %w", pid, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
		return fmt.Errorf("child %s still running after cancellation: %s", pid, state)
	}
	return nil
}
