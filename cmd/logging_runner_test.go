package cmd_test

import (
	"errors"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/concourse/concourse/cmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("LoggingRunner", func() {
	It("forwards lifecycle signals and reports the child's exit", func() {
		exitErr := errors.New("some-error")
		receivedSignals := make(chan os.Signal, 1)

		child := ifrit.RunFunc(func(signals <-chan os.Signal, ready chan<- struct{}) error {
			close(ready)
			receivedSignals <- <-signals
			return exitErr
		})

		logger := lagertest.NewTestLogger("foo")
		process := ifrit.Background(NewLoggingRunner(logger, child))
		finished := false
		DeferCleanup(func() {
			if finished {
				return
			}

			process.Signal(os.Interrupt)
			select {
			case <-process.Wait():
			case <-time.After(time.Second):
				Fail("logging runner did not stop during cleanup")
			}
		})

		Eventually(process.Ready(), time.Second).Should(BeClosed())
		process.Signal(os.Interrupt)
		Eventually(receivedSignals, time.Second).Should(Receive(Equal(os.Interrupt)))
		Eventually(process.Wait(), time.Second).Should(Receive(MatchError(exitErr)))
		finished = true

		Expect(logger.LogMessages()).To(ContainElement("foo.logging-runner-exited"))
	})
})
