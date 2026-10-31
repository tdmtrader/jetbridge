// The Hangar output plane and the Run components beside it: the capture
// coordinator and its capture and status components (registered only when the
// output plane is enabled), the Run results and Run cancellation components
// every web node registers, and the transactor that adapts a connection to the
// coordinator.

package atccmd

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// hangarOutputComponents is everything the output plane contributes to the
// component table, and the one place the plane's own flag decides whether any
// of it runs.
//
// THE CAPTURE ADVANCER ONCE RAN ON EVERY DEPLOYMENT. It was appended outside
// both the Kubernetes block and any output-plane check, so an upgraded
// deployment that never opted in -- a non-Kubernetes one included -- grew a
// `components` row, an advisory lock and a query a minute, for a plane it does
// not have. Each pass is one bounded indexed SELECT rolled back immediately,
// so the cost was small; a deployment with capture disabled must behave as it
// did before, and "small" is not that.
//
// The status component keeps its own additional condition, and it is a
// different question: the flag says this deployment HAS an output plane, and
// a configured bucket says it is configured enough to describe. A status
// surface reporting "0 live generations, not at risk" about a plane nobody
// has configured is an alert rule that will never fire looking exactly like
// coverage.
func (cmd *RunCommand) hangarOutputComponents(dbConn db.DbConn) []RunnableComponent {
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	components := []RunnableComponent{
		cmd.hangarOutputCaptureComponent(dbConn),
	}
	if status := cmd.hangarOutputStatusComponent(dbConn); status != nil {
		components = append(components, *status)
	}

	return components
}

// hangarOutputCoordinator is the capture coordinator: the capture rows in
// PostgreSQL and the node daemons the rows name.
func (cmd *RunCommand) hangarOutputCoordinator(dbConn db.DbConn) *hangaroutput.Coordinator {
	coordinator := &hangaroutput.Coordinator{
		Transactor: hangarOutputTransactor{conn: dbConn},
		Rows:       db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()),
	}
	// With no runtime source the coordinator's Dial stays nil, and it refuses
	// every node-side step with a typed error.
	if source := cmd.hangarOutputSource; source != nil {
		coordinator.Dial = func(ctx context.Context, node string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
			dial, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return source.CaptureControl(dial, node, uid)
		}
	}

	return coordinator
}

// hangarOutputCaptureComponent runs the capture sequence and its recovery:
// every pending capture through its seal and publish, every publishing one
// through recovery, every pending one past its deadline to failed, and every
// terminal one's marker released.
//
// It is a component and not a goroutine beside the step because the process
// that started a capture is exactly the process that may be gone: what has to
// survive is the row, and the ability to read it and take the next step. The
// component runner's lease makes one ATC own one pass; the CAS on the row
// makes a second one benign.
//
// A ten-second interval: a seal waits for its node's Pod to stop, and the
// pass is what asks again.
func (cmd *RunCommand) hangarOutputCaptureComponent(dbConn db.DbConn) RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputCapture},
		Runnable:  cmd.hangarOutputCoordinator(dbConn),
		Interval:  10 * time.Second,
	}
}

// runComponents is what every web node registers for Runs, with or without an
// output plane.
//
// The cancellation worker is here and not among the output plane's components
// because the cancel route answers on every node: a fence accepted where
// the worker is absent would never converge. Its own operations (build
// abort, candidate settlement, terminalization) need no output plane. Without
// one the node admits no exact execution, so no capture is ever discarded.
//
// The web fleet must agree on the output plane. The worker's lease is one
// row that a live owner renews on every pass, so a node without the plane
// that holds it keeps it; an execution admitted by a node with the plane is
// then retried, its Run staying running, until the lease holder stops. The
// chart renders every web node alike.
func (cmd *RunCommand) runComponents(dbConn db.DbConn) []RunnableComponent {
	return []RunnableComponent{
		cmd.runResultsComponent(),
		cmd.runCancellationComponent(dbConn),
	}
}

// runResultsComponent makes each settled Run's one terminal publication. It
// is registered on every web node, with or without an output plane; a Run whose
// captures are still unsettled is simply left for a later pass. Build
// completion wakes it; the interval is the net under a lost wake-up.
func (cmd *RunCommand) runResultsComponent() RunnableComponent {
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunResults},
		Runnable: component.RunFunc(func(ctx context.Context) error {
			if cmd.runResultFinalizer == nil {
				return nil
			}
			return cmd.runResultFinalizer.Run(ctx)
		}),
		Interval: 10 * time.Second,
	}
}

// Cancellation has its own bounded pass and nonzero fallback. It shares the
// source coordinator and the Run's immutable identities with normal completion.
func (cmd *RunCommand) runCancellationComponent(dbConn db.DbConn) RunnableComponent {
	factory := db.NewPipelineRunFactory(dbConn, nil)
	if cmd.runResultFinalizer != nil {
		factory = cmd.runResultFinalizer.Factory
	}
	executions := &runs.CancellationExecutions{Conn: dbConn, Factory: factory, Source: cmd.runCancellationSource}
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRunCancellation},
		Interval:  runs.CancellationPollInterval,
		Runnable: &runs.CancellationWorker{Conn: dbConn, Factory: factory, OwnerID: uuid.NewString(),
			Actions: runs.CancellationActionSet{factory, executions, runs.CancellationActionFunc(factory.ExecuteCancellationFinality)}},
	}
}

// hangarOutputTransactor adapts the connection to the coordinator's
// Transactor.
//
// It lives here rather than in atc/db because the interface belongs to the
// coordinator and atc/db has no business importing it: an interface is a
// statement of what a consumer uses, and the package that satisfies one should
// not have to know it exists.
type hangarOutputTransactor struct{ conn db.DbConn }

func (transactor hangarOutputTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	// The COMMIT answers in the output leaf's vocabulary. Two of this plane's
	// constraint triggers are DEFERRED, so their refusals arrive here and
	// nowhere earlier, and a coordinator handed an unclassified commit failure
	// would read a denial as an ambiguous commit and retry it forever.
	return db.HangarOutputTx{Tx: tx}, nil
}

// hangarOutputStatusComponent publishes the output plane's operational state:
// the in-service flag, the residue a drain waits on, and the open integrity
// findings.
//
// Nil when no output plane is configured, and that is the honest answer rather
// than a component emitting zeroes: a status surface reporting "0 residue, not
// at risk" about a plane that does not exist is an alert rule that will never
// fire looking exactly like coverage.
func (cmd *RunCommand) hangarOutputStatusComponent(dbConn db.DbConn) *RunnableComponent {
	// The same condition the admin status API answers on.
	if !cmd.Kubernetes.OutputPlaneEnabled {
		return nil
	}

	return &RunnableComponent{
		Component: atc.Component{Name: atc.ComponentHangarOutputStatus},
		Runnable: &hangaroutput.StatusPublisher{
			Reader: cmd.hangarOutputStatusReader(dbConn),
		},
		Interval: time.Minute,
	}
}
