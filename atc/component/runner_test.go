package component_test

import (
	"context"
	"os"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/component/cmocks"
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

	mockComponent := new(cmocks.Component)
	mockComponent.On("Name").Return(componentName)

	mockBus := new(cmocks.NotificationsBus)

	ranImmediately := make(chan context.Context, 10)

	mockSchedulable := schedulable{
		runImmediately: func(ctx context.Context) {
			ranImmediately <- ctx
		},
	}

	runner := &component.Runner{
		Logger:      lagertest.NewTestLogger("test"),
		Component:   mockComponent,
		Bus:         mockBus,
		Schedulable: mockSchedulable,
	}

	signal := db.NewNotifySignal()

	var process ifrit.Process
	s.Run("listens for component signals on start and fires initial run", func() {
		mockBus.On("ListenSignal", componentName).Return(signal, nil)

		process = ifrit.Background(runner)
		select {
		case <-process.Ready():
		case err := <-process.Wait():
			s.Failf("process exited early", "error: %s", err)
		}

		mockBus.AssertCalled(s.T(), "ListenSignal", componentName)

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
		mockBus.On("UnlistenSignal", componentName, signal).Return(nil)
		process.Signal(os.Interrupt)

		s.NoError(<-process.Wait())
		mockBus.AssertCalled(s.T(), "UnlistenSignal", componentName, signal)
	})
}

type schedulable struct {
	runImmediately func(context.Context)
}

func (s schedulable) RunImmediately(ctx context.Context) {
	s.runImmediately(ctx)
}
