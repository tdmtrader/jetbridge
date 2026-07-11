package devmcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("runCommand", func() {
	var workdir string

	BeforeEach(func() {
		workdir = GinkgoT().TempDir()
	})

	run := func(spec CommandSpec, extra []string, progress ProgressFunc) ToolResult {
		if progress == nil {
			progress = func(string) {}
		}
		return runCommand(context.Background(), workdir, "test-app", spec, extra, progress)
	}

	It("classifies exit 0 as ok and captures output, duration, and a log file", func() {
		res := run(CommandSpec{Cmd: []string{"sh", "-c", "echo one; echo two"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusOK))
		Expect(res.Summary).To(ContainSubstring("ok"))
		Expect(res.DurationSeconds).To(BeNumerically(">=", 0))
		Expect(res.OutputTail).To(ContainSubstring("one"))
		Expect(res.OutputTail).To(ContainSubstring("two"))

		Expect(res.LogPath).To(HavePrefix(filepath.Join(".dev-mcp", "logs")))
		logged, err := os.ReadFile(filepath.Join(workdir, res.LogPath))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(logged)).To(ContainSubstring("one\ntwo\n"))
	})

	It("classifies exit 1 as failed by default", func() {
		res := run(CommandSpec{Cmd: []string{"sh", "-c", "echo boom; exit 1"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusFailed))
		Expect(res.Summary).To(ContainSubstring("exit 1"))
	})

	It("honors failed_exit_codes and treats unlisted codes as error", func() {
		spec := CommandSpec{Cmd: []string{"sh", "-c", "exit 2"}, FailedExitCodes: []int{2}}
		Expect(run(spec, nil, nil).Status).To(Equal(StatusFailed))

		spec = CommandSpec{Cmd: []string{"sh", "-c", "exit 1"}, FailedExitCodes: []int{2}}
		Expect(run(spec, nil, nil).Status).To(Equal(StatusError))
	})

	It("classifies spawn failures as error", func() {
		res := run(CommandSpec{Cmd: []string{"definitely-not-a-real-binary-xyz"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusError))
	})

	It("appends extra args (the focus flag)", func() {
		res := run(CommandSpec{Cmd: []string{"echo", "base"}}, []string{"--focus=MySpec"}, nil)
		Expect(res.Status).To(Equal(StatusOK))
		Expect(res.OutputTail).To(ContainSubstring("base --focus=MySpec"))
	})

	It("keeps only the last 200 output lines in the tail", func() {
		script := "i=1; while [ $i -le 300 ]; do echo line-$i; i=$((i+1)); done"
		res := run(CommandSpec{Cmd: []string{"sh", "-c", script}}, nil, nil)
		Expect(res.OutputTail).NotTo(ContainSubstring("line-100\n"))
		Expect(res.OutputTail).To(ContainSubstring("line-101"))
		Expect(res.OutputTail).To(ContainSubstring("line-300"))
	})

	It("reports each completed output line to the progress func", func() {
		var mu sync.Mutex
		var lines []string
		run(CommandSpec{Cmd: []string{"sh", "-c", "echo alpha; echo beta"}}, nil, func(msg string) {
			mu.Lock()
			lines = append(lines, msg)
			mu.Unlock()
		})
		mu.Lock()
		defer mu.Unlock()
		Expect(lines).To(ContainElements("alpha", "beta"))
	})

	It("runs in the spec's dir relative to the workdir", func() {
		Expect(os.MkdirAll(filepath.Join(workdir, "sub"), 0o755)).To(Succeed())
		res := run(CommandSpec{Cmd: []string{"pwd"}, Dir: "sub"}, nil, nil)
		Expect(res.OutputTail).To(HaveSuffix(fmt.Sprintf("%c%s", filepath.Separator, "sub")))
	})
})
