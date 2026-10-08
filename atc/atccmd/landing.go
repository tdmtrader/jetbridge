package atccmd

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/landing"
)

// landingQueueComponent runs the landing queue's engine (ADR-0009) on one web
// node at a time. It exists only where the core admitter does: a node with no
// run-input authority admits no Runs and so can land nothing. The Run
// lifecycle wakes it; the interval is the net under a lost wake-up.
func (cmd *RunCommand) landingQueueComponent(logger lager.Logger, dbConn db.DbConn) (RunnableComponent, bool) {
	if cmd.runAdmitter == nil {
		return RunnableComponent{}, false
	}
	engine := &landing.Engine{
		Logger:  logger.Session("landing-queue"),
		Conn:    dbConn,
		Queues:  db.NewLandingQueueFactory(dbConn),
		Runs:    db.NewPipelineRunFactory(dbConn, nil),
		Results: cmd.runResultReader,
		Port:    cmd.runAdmitter,
		Epoch:   cmd.PipelineRunActivationEpoch,
	}
	return RunnableComponent{
		Component: atc.Component{Name: atc.ComponentLandingQueue},
		Runnable:  component.RunFunc(engine.Run),
		Interval:  10 * time.Second,
	}, true
}
