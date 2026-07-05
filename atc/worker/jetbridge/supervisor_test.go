package jetbridge

import (
	"github.com/concourse/concourse/atc/runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Task exec supervisor", func() {
	Describe("supervisorCommand", func() {
		var command []string

		BeforeEach(func() {
			command = supervisorCommand("task", runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello && exit 0"},
			})
		})

		It("runs the script via sh -c", func() {
			Expect(command).To(HaveLen(3))
			Expect(command[0]).To(Equal("sh"))
			Expect(command[1]).To(Equal("-c"))
		})

		It("derives the state dir from the process ID and command hash", func() {
			Expect(command[2]).To(MatchRegexp(`'/tmp/concourse-task-task-[0-9a-f]{8}'`))
		})

		It("gives the identical command the identical state dir (reattach resumes)", func() {
			again := supervisorCommand("task", runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hello && exit 0"},
			})
			Expect(again).To(Equal(command))
		})

		It("gives a different command a different state dir (hijack runs fresh)", func() {
			other := supervisorCommand("task", runtime.ProcessSpec{
				Path: "/bin/sh",
				Args: []string{"-c", "echo hijack-works"},
			})
			Expect(other[2]).NotTo(Equal(command[2]))
			stateDir := func(script string) string {
				return script[:len("S='/tmp/concourse-task-task-xxxxxxxx'")]
			}
			Expect(stateDir(other[2])).NotTo(Equal(stateDir(command[2])))
		})

		It("embeds the quoted original command", func() {
			Expect(command[2]).To(ContainSubstring(`'/bin/sh' '-c' 'echo hello && exit 0'`))
		})

		It("shields the command from the pty HUP on web death", func() {
			Expect(command[2]).To(ContainSubstring(`trap '' HUP`))
		})

		It("records pid and exit code and replays the log from the start", func() {
			Expect(command[2]).To(ContainSubstring(`echo $! >"$S/pid"`))
			Expect(command[2]).To(ContainSubstring(`mv "$S/exit.tmp" "$S/exit"`))
			Expect(command[2]).To(ContainSubstring(`tail -n +1 -f "$S/log"`))
		})

		It("only starts the command when it is not already running or finished", func() {
			Expect(command[2]).To(ContainSubstring(`if [ ! -f "$S/exit" ] && ! alive; then`))
		})

		It("treats a missing or empty pid file as not-running (busybox kill -0 \"\" exits 0)", func() {
			Expect(command[2]).To(ContainSubstring(`[ -n "$pid" ] && kill -0 "$pid"`))
		})

		It("appends to the log instead of truncating so reattach keeps prior output", func() {
			Expect(command[2]).To(ContainSubstring(`>>"$S/log" 2>&1`))
			Expect(command[2]).NotTo(ContainSubstring(` >"$S/log"`))
		})

		It("exits with the recorded code, or 255 if the runner vanished", func() {
			Expect(command[2]).To(ContainSubstring(`exit "$(cat "$S/exit")"`))
			Expect(command[2]).To(ContainSubstring("exit 255"))
		})

		It("sanitizes process IDs for filesystem use", func() {
			cmd := supervisorCommand("some/weird id", runtime.ProcessSpec{Path: "true"})
			Expect(cmd[2]).To(MatchRegexp(`'/tmp/concourse-task-some-weird-id-[0-9a-f]{8}'`))
		})
	})

	Describe("shellQuote", func() {
		It("wraps plain words in single quotes", func() {
			Expect(shellQuote("hello")).To(Equal("'hello'"))
		})

		It("escapes embedded single quotes", func() {
			Expect(shellQuote("it's")).To(Equal(`'it'\''s'`))
		})

		It("preserves shell metacharacters literally", func() {
			Expect(shellQuote(`$HOME "x" ;&|`)).To(Equal(`'$HOME "x" ;&|'`))
		})
	})
})
