package jetbridge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/atc/runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs execute the supervisor script with the local POSIX sh to
// verify its runtime semantics: exit-code propagation, taking over a
// still-running command without restarting it, and log replay after
// completion. They spawn real processes but need no cluster.
var _ = Describe("Task exec supervisor script execution", func() {
	var stateID string

	// stateDirOf extracts the supervisor state dir from a generated script.
	stateDirOf := func(script string) string {
		start := strings.Index(script, "S='") + len("S='")
		end := strings.Index(script[start:], "'")
		return script[start : start+end]
	}

	// runSupervisor executes the supervisor for the given shell command and
	// returns combined output and exit code once it completes.
	runSupervisor := func(shellCommand string) (string, int) {
		cmd := supervisorCommand(stateID, runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", shellCommand},
		})
		out, err := exec.Command(cmd[0], cmd[1], cmd[2]).CombinedOutput()
		exitCode := 0
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			Expect(ok).To(BeTrue(), "unexpected non-exit error: %v, output: %s", err, out)
			exitCode = exitErr.ExitCode()
		}
		return string(out), exitCode
	}

	BeforeEach(func() {
		stateID = fmt.Sprintf("test-%d-%d-%d", os.Getpid(), GinkgoParallelProcess(), time.Now().UnixNano())
	})

	AfterEach(func() {
		matches, _ := filepath.Glob(filepath.Join("/tmp", "concourse-task-"+sanitizeForPath(stateID)+"-*"))
		for _, m := range matches {
			os.RemoveAll(m)
		}
	})

	It("runs the command, streams its output, and propagates the exit code", func() {
		out, code := runSupervisor("echo hello from task; exit 7")
		Expect(out).To(ContainSubstring("hello from task"))
		Expect(code).To(Equal(7))
	})

	It("takes over a still-running command without restarting it", func() {
		// web 1: start the supervisor, then kill it mid-command (SIGKILL,
		// like the web process dying). The command must survive.
		cmd1 := supervisorCommand(stateID, runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", "echo run-marker; sleep 3; echo finished; exit 5"},
		})
		web1 := exec.Command(cmd1[0], cmd1[1], cmd1[2])
		Expect(web1.Start()).To(Succeed())

		// Give the supervisor time to start the command, then kill web 1.
		time.Sleep(1500 * time.Millisecond)
		Expect(web1.Process.Kill()).To(Succeed())
		_ = web1.Wait()

		// web 2: re-exec the identical supervisor command. It must attach
		// to the running command (not start a second copy) and report the
		// real exit code.
		out, code := runSupervisor("echo run-marker; sleep 3; echo finished; exit 5")
		Expect(code).To(Equal(5))
		Expect(out).To(ContainSubstring("finished"))
		Expect(strings.Count(out, "run-marker")).To(Equal(1), "command must not be restarted on takeover")
	})

	It("replays the log and exit code when the command already completed", func() {
		out1, code1 := runSupervisor("echo one-shot-output; exit 3")
		Expect(code1).To(Equal(3))
		Expect(out1).To(ContainSubstring("one-shot-output"))

		out2, code2 := runSupervisor("echo one-shot-output; exit 3")
		Expect(code2).To(Equal(3))
		Expect(out2).To(ContainSubstring("one-shot-output"))

		// The command must not have run twice: the log holds exactly one
		// occurrence even after two supervisor invocations.
		cmd := supervisorCommand(stateID, runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", "echo one-shot-output; exit 3"},
		})
		logBytes, err := os.ReadFile(filepath.Join(stateDirOf(cmd[2]), "log"))
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.Count(string(logBytes), "one-shot-output")).To(Equal(1))
	})

	It("terminal-end kill tears down the still-running supervised process tree", func() {
		// A timed-out/aborted agent step must not leave the supervised
		// command running (it would keep spending API dollars until the GC
		// reaper deletes the pod). Start a supervisor whose command would
		// run for a minute, then run the kill script and verify the tree
		// dies well before that.
		spec := runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", "sleep 60; echo survived-terminal-end"},
		}
		cmd := supervisorCommand(stateID, spec)
		web := exec.Command(cmd[0], cmd[1], cmd[2])
		// Mimic the runc tty exec: the supervisor sh runs as a session (and
		// thus process-group) leader. This also isolates the tree's pgid
		// from the test runner so the group kill cannot touch ginkgo.
		web.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		Expect(web.Start()).To(Succeed())
		defer func() {
			_ = web.Process.Kill()
			_ = web.Wait()
		}()

		// Wait for the runner subshell to start and record its pid.
		stateDir := stateDirOf(cmd[2])
		var runnerPid int
		Eventually(func() error {
			pidBytes, err := os.ReadFile(filepath.Join(stateDir, "pid"))
			if err != nil {
				return err
			}
			_, err = fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &runnerPid)
			return err
		}, "5s", "100ms").Should(Succeed())

		kill := supervisorKillCommand(stateID, spec, 5)
		out, err := exec.Command(kill[0], kill[1], kill[2]).CombinedOutput()
		Expect(err).ToNot(HaveOccurred(), "kill script failed: %s", out)

		// The runner subshell is dead (zombies are reaped once the killed
		// session leader's parent collects them, hence Eventually)...
		Eventually(func() error {
			return syscall.Kill(runnerPid, 0)
		}, "10s", "100ms").Should(MatchError(syscall.ESRCH))

		// ...and no exit code was ever recorded: the command was killed,
		// not run to completion.
		_, statErr := os.Stat(filepath.Join(stateDir, "exit"))
		Expect(statErr).To(HaveOccurred())
	})

	It("terminal-end kill is a no-op when the command never started", func() {
		spec := runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "true"}}
		kill := supervisorKillCommand(stateID, spec, 1)
		out, err := exec.Command(kill[0], kill[1], kill[2]).CombinedOutput()
		Expect(err).ToNot(HaveOccurred(), "kill script failed: %s", out)
	})

	It("terminal-end kill is a no-op after the command completed and preserves replay", func() {
		out1, code1 := runSupervisor("echo one-and-done; exit 4")
		Expect(code1).To(Equal(4))
		Expect(out1).To(ContainSubstring("one-and-done"))

		spec := runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", "echo one-and-done; exit 4"},
		}
		kill := supervisorKillCommand(stateID, spec, 1)
		out, err := exec.Command(kill[0], kill[1], kill[2]).CombinedOutput()
		Expect(err).ToNot(HaveOccurred(), "kill script failed: %s", out)

		// Replay after the no-op kill still works.
		out2, code2 := runSupervisor("echo one-and-done; exit 4")
		Expect(code2).To(Equal(4))
		Expect(out2).To(ContainSubstring("one-and-done"))
	})

	It("shields the command from SIGHUP so pty teardown cannot kill it", func() {
		// Send HUP to the runner subshell directly; a HUP-shielded runner
		// keeps going and records its exit code.
		cmd := supervisorCommand(stateID, runtime.ProcessSpec{
			Path: "sh",
			Args: []string{"-c", "sleep 2; exit 9"},
		})
		web := exec.Command(cmd[0], cmd[1], cmd[2])
		Expect(web.Start()).To(Succeed())

		time.Sleep(1 * time.Second)
		pidBytes, err := os.ReadFile(filepath.Join(stateDirOf(cmd[2]), "pid"))
		Expect(err).ToNot(HaveOccurred())
		var runnerPid int
		_, err = fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &runnerPid)
		Expect(err).ToNot(HaveOccurred())
		Expect(syscall.Kill(runnerPid, syscall.SIGHUP)).To(Succeed())

		Expect(web.Wait()).ToNot(Succeed()) // exit 9 surfaces as ExitError
		Expect(web.ProcessState.ExitCode()).To(Equal(9))
	})
})
