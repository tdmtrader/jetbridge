package jetbridge

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/creack/pty"

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

	Context("Exact execution on Linux", func() {
		BeforeEach(func() {
			if goruntime.GOOS != "linux" {
				Skip("exact task supervision requires Linux setsid; exercised by the Linux CI and Brine tiers")
			}
			_, err := exec.LookPath("setsid")
			Expect(err).NotTo(HaveOccurred(), "exact execution requires setsid in the task image")
		})

		runExact := func(shellCommand string) (string, int) {
			cmd := exactSupervisorCommand(stateID, runtime.ProcessSpec{
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

		exactStateDir := func(shellCommand string) string {
			return stateDirOf(exactSupervisorCommand(stateID, runtime.ProcessSpec{
				Path: "sh",
				Args: []string{"-c", shellCommand},
			})[2])
		}

		It("records the exact start before the command can finish, and still reports its exit code", func() {
			const command = "sleep 2; echo done; exit 4"
			cmd := exactSupervisorCommand(stateID, runtime.ProcessSpec{
				Path: "sh", Args: []string{"-c", command},
			})
			web := exec.Command(cmd[0], cmd[1], cmd[2])
			Expect(web.Start()).To(Succeed())

			// The start record is durable BEFORE the outcome is, which is the
			// ordering Req 4 puts around the finish witness: while the command is
			// still running there is a start and no exit.
			Eventually(func() bool {
				_, err := os.Stat(filepath.Join(exactStateDir(command), "start"))
				return err == nil
			}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())
			_, err := os.Stat(filepath.Join(exactStateDir(command), "exit"))
			Expect(os.IsNotExist(err)).To(BeTrue(), "an outcome was recorded while the command was still running")

			Expect(web.Wait()).ToNot(Succeed())
			Expect(web.ProcessState.ExitCode()).To(Equal(4))
		})

		It("never runs the command again once its start is recorded and its outcome is unprovable", func() {
			// The state today's supervisor relaunches into, and the one Req 6
			// forbids: the command really started, the web died, and the runner
			// died with it. There is no exit file and nothing is alive, and an
			// ordinary task's supervisor would start the command over.
			const command = "echo run-marker; sleep 30"
			cmd := exactSupervisorCommand(stateID, runtime.ProcessSpec{
				Path: "sh", Args: []string{"-c", command},
			})
			web1 := exec.Command(cmd[0], cmd[1], cmd[2])
			Expect(web1.Start()).To(Succeed())

			state := exactStateDir(command)
			Eventually(func() bool {
				_, err := os.Stat(filepath.Join(state, "start"))
				return err == nil
			}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())

			pidBytes, err := os.ReadFile(filepath.Join(state, "pid"))
			Expect(err).ToNot(HaveOccurred())
			var runnerPid int
			_, err = fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &runnerPid)
			Expect(err).ToNot(HaveOccurred())

			Expect(web1.Process.Kill()).To(Succeed())
			_ = web1.Wait()
			Expect(syscall.Kill(runnerPid, syscall.SIGKILL)).To(Succeed())
			Eventually(func() error {
				return syscall.Kill(runnerPid, 0)
			}, 3*time.Second, 50*time.Millisecond).Should(HaveOccurred())

			out, code := runExact(command)
			Expect(code).To(Equal(ExactUnresolvedExitCode),
				"the re-exec did not report an unresolved outcome; output: %s", out)

			// And the proof it did not run again is in the command's own log,
			// which the replay printed: one marker, not two.
			logBytes, err := os.ReadFile(filepath.Join(state, "log"))
			Expect(err).ToNot(HaveOccurred())
			Expect(strings.Count(string(logBytes), "run-marker")).To(Equal(1),
				"the producer was executed a second time after its exact start was recorded")
		})

		It("still takes over a still-running command without restarting it", func() {
			const command = "echo exact-marker; sleep 3; echo finished; exit 5"
			cmd := exactSupervisorCommand(stateID, runtime.ProcessSpec{
				Path: "sh", Args: []string{"-c", command},
			})
			web1 := exec.Command(cmd[0], cmd[1], cmd[2])
			Expect(web1.Start()).To(Succeed())

			time.Sleep(1500 * time.Millisecond)
			Expect(web1.Process.Kill()).To(Succeed())
			_ = web1.Wait()

			out, code := runExact(command)
			Expect(code).To(Equal(5))
			Expect(out).To(ContainSubstring("finished"))
			Expect(strings.Count(out, "exact-marker")).To(Equal(1),
				"a live command was restarted rather than attached to")
		})

		It("replays a recorded outcome rather than reporting it unresolved", func() {
			out1, code1 := runExact("echo exact-once; exit 3")
			Expect(code1).To(Equal(3))
			Expect(out1).To(ContainSubstring("exact-once"))

			out2, code2 := runExact("echo exact-once; exit 3")
			Expect(code2).To(Equal(3), "a completed command was reported unresolved: %s", out2)
		})

	})

	It("leaves the ordinary supervisor script alone", func() {
		ordinary := supervisorCommand(stateID, runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "true"}})[2]
		Expect(ordinary).ToNot(ContainSubstring("/start"),
			"the ordinary script grew the exact start record; an ordinary task's re-exec must "+
				"still restart a command whose runner died, which is the whole reason the "+
				"supervisor exists")
		// Match the statement, not the digits: the script embeds a state path
		// carrying a pid and a nanosecond timestamp, and "254" turns up inside
		// those often enough to fail a release check on nothing.
		Expect(ordinary).ToNot(ContainSubstring("exit " + strconv.Itoa(ExactUnresolvedExitCode)))
	})

	Context("Detached from the exec session on Linux", func() {
		BeforeEach(func() {
			if goruntime.GOOS != "linux" {
				Skip("detaching a task command requires Linux setsid; exercised by the Linux CI tier")
			}
			_, err := exec.LookPath("setsid")
			Expect(err).NotTo(HaveOccurred(), "the Linux test image has no setsid")
		})

		It("keeps a command that takes SIGHUP back, and its children, through the exec session's hangup", func() {
			// The shape that failed on a web restart: dockerd takes SIGHUP for
			// config reload, so the docker-proxy processes it starts have
			// SIGHUP at its default action again, and they died with the exec
			// session. hupreload does the same with a shell child.
			helper, err := buildHupReload()
			Expect(err).NotTo(HaveOccurred())
			command := "echo run-marker; exec " + shellQuote(helper) +
				" sh -c 'echo child-started; sleep 5; echo child-finished; exit 6'"

			// web 1 runs the supervisor the way the kubelet runs a TTY exec:
			// session leader of a fresh pty, which is its controlling terminal.
			cmd := supervisorCommand(stateID, runtime.ProcessSpec{Path: "sh", Args: []string{"-c", command}})
			web1 := exec.Command(cmd[0], cmd[1], cmd[2])
			ptmx, err := startWithHUPDefault(web1)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = ptmx.Close() })

			// Read only until the child has started, then stop. The terminal
			// is closed below with no read outstanding on it: closed under a
			// blocked read, the hangup still kills the session leader but
			// never reaches its process group, and the old script passes.
			started := make(chan struct{})
			go func() {
				var seen []byte
				buf := make([]byte, 4096)
				for !strings.Contains(string(seen), "child-started") {
					n, err := ptmx.Read(buf)
					if err != nil {
						return
					}
					seen = append(seen, buf[:n]...)
				}
				close(started)
			}()
			Eventually(started, 10*time.Second).Should(BeClosed())

			// Web 1's own tail is the witness: it is in the exec session's
			// foreground group with SIGHUP at its default action, so it dies
			// if and only if the hangup reached that group.
			tails := func() []int { return liveInSession(web1.Process.Pid, "tail") }
			Expect(tails()).NotTo(BeEmpty(), "web 1's supervisor has no tail following the log")

			// The web goes away: its end of the terminal closes, and the
			// kernel hangs the pty up -- SIGHUP to the session leader and,
			// as it exits, to the foreground process group, which is every
			// process of the session the command did not leave.
			Expect(ptmx.Close()).To(Succeed())
			err = web1.Wait()
			Expect(err).To(HaveOccurred())
			status, ok := web1.ProcessState.Sys().(syscall.WaitStatus)
			Expect(ok && status.Signaled() && status.Signal() == syscall.SIGHUP).To(BeTrue(),
				"web 1's supervisor did not die of the hangup: %v", web1.ProcessState)
			Eventually(tails, 5*time.Second, 50*time.Millisecond).Should(BeEmpty(),
				"the hangup never reached the exec session's process group, so this spec proves nothing")

			// web 2 takes over and sees the child finish: nothing of the
			// command died with the session.
			out, code := runSupervisor(command)
			Expect(out).To(ContainSubstring("child-finished"))
			Expect(out).NotTo(ContainSubstring("child killed"))
			Expect(code).To(Equal(6))
			Expect(strings.Count(out, "run-marker")).To(Equal(1), "command must not be restarted on takeover")
		})
	})

	It("falls back to the SIGHUP shield, and says so, in an image without setsid", func() {
		// A PATH holding what the supervisor needs and not setsid.
		bin := filepath.Join(tempRoot, fmt.Sprintf("no-setsid-%d", GinkgoParallelProcess()))
		Expect(os.MkdirAll(bin, 0o755)).To(Succeed())
		DeferCleanup(os.RemoveAll, bin)
		for _, tool := range []string{"sh", "cat", "tail", "mv", "mkdir", "sleep"} {
			path, err := exec.LookPath(tool)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Symlink(path, filepath.Join(bin, tool))).To(Succeed())
		}

		cmd := supervisorCommand(stateID, runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "echo fallback-ran; exit 8"}})
		supervisor := exec.Command(filepath.Join(bin, "sh"), cmd[1], cmd[2])
		supervisor.Env = []string{"PATH=" + bin}
		out, err := supervisor.CombinedOutput()
		Expect(err).To(HaveOccurred())
		Expect(supervisor.ProcessState.ExitCode()).To(Equal(8), "output: %s", out)
		Expect(string(out)).To(ContainSubstring("fallback-ran"))
		Expect(string(out)).To(ContainSubstring(supervisorNoSetsidNotice))
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

var (
	hupReloadBuild    sync.Once
	hupReloadBinary   string
	hupReloadBuildErr error
)

// buildHupReload builds testdata/hupreload once per test process, the way
// buildOutputDaemon does.
func buildHupReload() (string, error) {
	hupReloadBuild.Do(func() {
		binary := filepath.Join(tempRoot, "hupreload")
		build := exec.Command("go", "build", "-o", binary, "./atc/worker/jetbridge/testdata/hupreload")
		build.Dir = repositoryRoot()
		build.Env = append(os.Environ(), "TMPDIR="+tempRoot)
		if out, err := build.CombinedOutput(); err != nil {
			hupReloadBuildErr = fmt.Errorf("building hupreload: %w\n%s", err, out)

			return
		}
		hupReloadBinary = binary
	})

	return hupReloadBinary, hupReloadBuildErr
}

// startWithHUPDefault starts cmd the way the kubelet starts a TTY exec:
// session leader of a fresh pty, with SIGHUP at its default action. A CI task
// shell may hand this process SIGHUP ignored, and an ignored signal stays
// ignored in a child, where no shell can undo it -- the supervisor would then
// ride out the hangup and the spec would test nothing. A signal Go handles is
// reset to the default in a child, so SIGHUP is handled, briefly, around the
// start.
func startWithHUPDefault(cmd *exec.Cmd) (*os.File, error) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Reset(syscall.SIGHUP)

	return pty.Start(cmd)
}

// liveInSession lists the processes named comm in session sid that have not
// exited. A zombie counts as exited: nothing may reap it where PID 1 does not.
func liveInSession(sid int, comm string) []int {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	var pids []int
	for _, path := range stats {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		stat := string(raw)
		open, closing := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
		if open < 0 || closing < open {
			continue
		}
		// After "(comm) ": state, ppid, pgrp, session.
		fields := strings.Fields(stat[closing+1:])
		if len(fields) < 4 || stat[open+1:closing] != comm || fields[0] == "Z" || fields[3] != strconv.Itoa(sid) {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(stat[:open]))
		pids = append(pids, pid)
	}

	return pids
}
