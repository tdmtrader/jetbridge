package session

import "os/exec"

// Credential-bearing execution is rejected on Windows before this is used.
func configureProcess(c *exec.Cmd) {}
func stopProcess(c *exec.Cmd) {
	if c.Process != nil {
		_ = c.Process.Kill()
	}
}
