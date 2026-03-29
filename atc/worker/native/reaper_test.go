package native_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/native"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Reaper", func() {
	var (
		ctx            context.Context
		logger         = lagertest.NewTestLogger("reaper-test")
		fakeContRepo   *dbfakes.FakeContainerRepository
		reaper         *native.Reaper
		config         native.Config
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeContRepo = new(dbfakes.FakeContainerRepository)

		config = native.Config{
			WorkDir:    filepath.Join(GinkgoT().TempDir(), "work"),
			CacheDir:   filepath.Join(GinkgoT().TempDir(), "cache"),
			Platform:   "darwin",
			WorkerName: "native-darwin",
		}

		reaper = native.NewReaper(logger, config, fakeContRepo)
	})

	Describe("Run", func() {
		It("removes scratch dirs for destroying containers", func() {
			// Create a container directory.
			containerDir := filepath.Join(config.WorkDir, "containers", "destroy-me")
			Expect(os.MkdirAll(containerDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(containerDir, "data.txt"), []byte("x"), 0644)).To(Succeed())

			fakeContRepo.FindDestroyingContainersReturns([]string{"destroy-me"}, nil)
			fakeContRepo.RemoveDestroyingContainersReturns(1, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("removing the directory")
			Expect(containerDir).ToNot(BeADirectory())

			By("calling RemoveDestroyingContainers with empty keep list")
			Expect(fakeContRepo.RemoveDestroyingContainersCallCount()).To(Equal(1))
			workerName, keepHandles := fakeContRepo.RemoveDestroyingContainersArgsForCall(0)
			Expect(workerName).To(Equal("native-darwin"))
			Expect(keepHandles).To(BeEmpty())
		})

		It("keeps failed containers in DB (Bug 3 regression)", func() {
			// Create a container directory that we'll make unremovable.
			containerDir := filepath.Join(config.WorkDir, "containers", "fail-me")
			Expect(os.MkdirAll(containerDir, 0755)).To(Succeed())
			// Make it unremovable by removing write permission on parent.
			parentDir := filepath.Dir(containerDir)
			Expect(os.Chmod(parentDir, 0555)).To(Succeed())
			defer os.Chmod(parentDir, 0755) // Restore for cleanup.

			fakeContRepo.FindDestroyingContainersReturns([]string{"fail-me"}, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("passing the failed handle in the keep list")
			Expect(fakeContRepo.RemoveDestroyingContainersCallCount()).To(Equal(1))
			_, keepHandles := fakeContRepo.RemoveDestroyingContainersArgsForCall(0)
			Expect(keepHandles).To(ContainElement("fail-me"))
		})

		It("kills process from PID file before removal", func() {
			// Start a real sleep process.
			cmd := exec.Command("/bin/sleep", "300")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			Expect(cmd.Start()).To(Succeed())
			pid := cmd.Process.Pid

			// Create container dir with PID file.
			containerDir := filepath.Join(config.WorkDir, "containers", "kill-me")
			Expect(os.MkdirAll(containerDir, 0755)).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(containerDir, "kill-me.pid"),
				[]byte(fmt.Sprintf("%d", pid)),
				0644,
			)).To(Succeed())

			fakeContRepo.FindDestroyingContainersReturns([]string{"kill-me"}, nil)
			fakeContRepo.RemoveDestroyingContainersReturns(1, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			By("verifying the process was killed")
			// cmd.Wait will return an error because the process was killed.
			waitErr := cmd.Wait()
			Expect(waitErr).To(HaveOccurred())
		})
	})

	Describe("startupSweep (first Run call)", func() {
		It("removes leftover container dirs on first run", func() {
			staleDir := filepath.Join(config.WorkDir, "containers", "stale-container")
			Expect(os.MkdirAll(staleDir, 0755)).To(Succeed())

			fakeContRepo.FindDestroyingContainersReturns(nil, nil)

			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())

			Expect(staleDir).ToNot(BeADirectory())
		})

		It("handles missing containers dir gracefully", func() {
			fakeContRepo.FindDestroyingContainersReturns(nil, nil)

			// config.WorkDir/containers doesn't exist — should not error.
			err := reaper.Run(ctx)
			Expect(err).ToNot(HaveOccurred())
		})

		It("only runs startup sweep once", func() {
			staleDir := filepath.Join(config.WorkDir, "containers", "stale-2")
			Expect(os.MkdirAll(staleDir, 0755)).To(Succeed())

			fakeContRepo.FindDestroyingContainersReturns(nil, nil)

			By("first run cleans up stale dirs")
			Expect(reaper.Run(ctx)).To(Succeed())
			Expect(staleDir).ToNot(BeADirectory())

			By("second run does not sweep (create new stale dir)")
			Expect(os.MkdirAll(staleDir, 0755)).To(Succeed())
			Expect(reaper.Run(ctx)).To(Succeed())
			// The stale dir should still exist because startup sweep doesn't run again.
			Expect(staleDir).To(BeADirectory())
		})
	})
})
