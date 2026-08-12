package component_test

import (
	"context"
	"os"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/component/componentfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/tedsuo/ifrit"
)

func TestRunner(t *testing.T) {
	suite.Run(t, &RunnerSuite{
		Assertions: require.New(t),
	})
}

type RunnerSuite struct {
	suite.Suite
	*require.Assertions
}

func (s *RunnerSuite) TestNotifyDriven() {
	componentName := "some-component"

	mockComponent := new(componentfakes.FakeComponent)
	mockComponent.NameReturns(componentName)

	mockBus := new(componentfakes.FakeNotificationsBus)

	ranImmediately := make(chan context.Context, 10)

	mockSchedulable := new(componentfakes.FakeSchedulable)
	mockSchedulable.RunImmediatelyCalls(func(ctx context.Context) {
		ranImmediately <- ctx
	})

	runner := &component.Runner{
		Logger:      lagertest.NewTestLogger("test"),
		Component:   mockComponent,
		Bus:         mockBus,
		Schedulable: mockSchedulable,
	}

	signal := db.NewNotifySignal()

	var process ifrit.Process
	s.Run("listens for component signals on start and fires initial run", func() {
		mockBus.ListenSignalReturns(signal, nil)

		process = ifrit.Background(runner)
		select {
		case <-process.Ready():
		case err := <-process.Wait():
			s.Failf("process exited early", "error: %s", err)
		}

		s.Equal(1, mockBus.ListenSignalCallCount())
		s.Equal(componentName, mockBus.ListenSignalArgsForCall(0))

		// Runner fires once on startup
		select {
		case <-ranImmediately:
		case <-time.After(time.Second):
			s.Fail("timed out waiting for startup RunImmediately")
		}
	})

	s.Run("runs immediately on signal", func() {
		signal.Signal()
		select {
		case <-ranImmediately:
		case <-time.After(time.Second):
			s.Fail("timed out waiting for RunImmediately")
		}

		signal.Signal()
		select {
		case <-ranImmediately:
		case <-time.After(time.Second):
			s.Fail("timed out waiting for RunImmediately")
		}
	})

	s.Run("coalesces signals", func() {
		// Drain any leftover from prior subtests
		time.Sleep(10 * time.Millisecond)
		for len(ranImmediately) > 0 {
			<-ranImmediately
		}

		// Send many signals — they should coalesce significantly
		for i := 0; i < 100; i++ {
			signal.Signal()
		}

		// Wait for at least one wake-up
		select {
		case <-ranImmediately:
		case <-time.After(time.Second):
			s.Fail("timed out waiting for RunImmediately")
		}

		// Give the runner time to process any additional coalesced signals
		time.Sleep(50 * time.Millisecond)

		// 100 signals should produce far fewer than 100 RunImmediately calls.
		// The exact count depends on scheduling, but it should be small.
		wakeups := 1 + len(ranImmediately)
		s.Less(wakeups, 10, "100 signals should coalesce to far fewer than 10 wake-ups, got %d", wakeups)
	})

	s.Run("unlistens on exit", func() {
		process.Signal(os.Interrupt)

		s.NoError(<-process.Wait())

		s.Equal(1, mockBus.UnlistenSignalCallCount())
		unlistenedName, unlistenedSignal := mockBus.UnlistenSignalArgsForCall(0)
		s.Equal(componentName, unlistenedName)
		s.Equal(signal, unlistenedSignal)
	})
}
