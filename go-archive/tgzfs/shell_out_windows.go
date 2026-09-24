package tgzfs

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func tarCompress(tarPath string, dest io.Writer, workDir string, paths ...string) error {
	out := new(bytes.Buffer)

	tarCmd := exec.Command(tarPath, "-czf", "-", "--null", "-T", "-")
	tarCmd.Dir = workDir
	tarCmd.Stderr = out
	tarCmd.Stdout = dest

	tarCmd.Stdin = bytes.NewBufferString(strings.Join(paths, "\x00"))

	err := tarCmd.Run()
	if err != nil {
		return fmt.Errorf("tar compress failed (%s). output: %q", err, out.String())
	}

	return nil
}
