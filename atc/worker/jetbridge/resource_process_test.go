package jetbridge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resource command cancellation scripts", func() {
	It("refuses to launch when cancellation arrived before the PID record", func() {
		if _, err := os.Stat("/proc/self/stat"); err != nil {
			Skip("resource command lifetime checks require Linux procfs")
		}
		marker := filepath.Join(GinkgoT().TempDir(), "command-ran")
		command, state := cancellableResourceCommand([]string{"sh", "-c", `printf invoked > "$1"`, "test-command", marker})
		// Claim the generated directory before registering its deletion. No
		// shared or caller-selected path can be removed by this test.
		Expect(os.Mkdir(state, 0700)).To(Succeed())
		DeferCleanup(func() { Expect(os.RemoveAll(state)).To(Succeed()) })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "sh", "-c", cancelResourceScript, "cancel-resource", state).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
		output, err = exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
		var exited *exec.ExitError
		Expect(errors.As(err, &exited)).To(BeTrue(), "command unexpectedly started: %s", output)
		Expect(exited.ExitCode()).To(Equal(130))
		_, err = os.Stat(marker)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})
