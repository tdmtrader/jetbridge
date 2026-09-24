//go:build !windows
// +build !windows

package tgzfs

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

func tarCompress(tarPath string, dest io.Writer, workDir string, paths ...string) error {
	out := new(bytes.Buffer)

	args := []string{"-czf", "-", "--null", "-T", "-"}
	if runtime.GOOS == "darwin" {
		args = append([]string{"--no-mac-metadata"}, args...)
	}
	tarCmd := exec.Command(tarPath, args...)
	tarCmd.Dir = workDir
	tarCmd.Stderr = out
	tarCmd.Stdout = dest

	tarCmd.Stdin = bytes.NewBufferString(strings.Join(paths, "\x00"))

	// prevent ctrl+c and such from killing tar process
	tarCmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	err := tarCmd.Run()
	if err != nil {
		return fmt.Errorf("tar compress failed (%s). output: %q", err, out.String())
	}

	return nil
}
