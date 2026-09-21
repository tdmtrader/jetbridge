//go:build !windows

package review

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	c.WaitDelay = time.Second
}
func stopProcess(c *exec.Cmd) {
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
