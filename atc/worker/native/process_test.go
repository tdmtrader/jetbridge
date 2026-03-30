package native_test

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Process", func() {
	Describe("ID", func() {
		It("returns the PID as a string", func() {
			cmd := exec.Command("/bin/sleep", "60")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())
			defer cmd.Process.Kill()

			process := native.NewProcess(cmd)
			Expect(process.ID()).ToNot(BeEmpty())
		})
	})

	Describe("Wait", func() {
		It("returns exit status 0 for successful command", func() {
			cmd := exec.Command("/bin/sh", "-c", "true")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())

			process := native.NewProcess(cmd)
			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(0))
		})

		It("returns non-zero exit status for failed command", func() {
			cmd := exec.Command("/bin/sh", "-c", "false")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())

			process := native.NewProcess(cmd)
			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).ToNot(Equal(0))
		})

		It("returns the exact exit code", func() {
			cmd := exec.Command("/bin/sh", "-c", "exit 42")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())

			process := native.NewProcess(cmd)
			result, err := process.Wait(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(42))
		})

		It("sends SIGTERM then SIGKILL on context cancellation", func() {
			cmd := exec.Command("/bin/sleep", "300")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())

			process := native.NewProcess(cmd)

			ctx, cancel := context.WithCancel(context.Background())

			done := make(chan struct{})
			var result runtime.ProcessResult
			var waitErr error
			go func() {
				result, waitErr = process.Wait(ctx)
				close(done)
			}()

			// Give the process a moment to start, then cancel.
			time.Sleep(100 * time.Millisecond)
			cancel()

			// Wait should return within a reasonable time (well under
			// the 10s grace period since sleep responds to SIGTERM).
			Eventually(done, 5*time.Second).Should(BeClosed())
			Expect(waitErr).To(HaveOccurred())
			Expect(result.ExitStatus).To(Equal(-1))
		})
	})

	Describe("SetTTY", func() {
		It("returns an error", func() {
			cmd := exec.Command("/bin/sleep", "60")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())
			defer cmd.Process.Kill()

			process := native.NewProcess(cmd)
			err := process.SetTTY(runtime.TTYSpec{})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not support TTY"))
		})
	})
})
