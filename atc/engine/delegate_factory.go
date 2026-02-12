package engine

import (
	"code.cloudfoundry.org/clock"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
)

type DelegateFactory struct {
	build              db.Build
	plan               atc.Plan
	rateLimiter        RateLimiter
	policyChecker      policy.Checker
	dbWorkerFactory    db.WorkerFactory
	lockFactory        lock.LockFactory
	nativeImageFetch   bool

	resourceConfigFactory db.ResourceConfigFactory
	resourceCacheFactory  db.ResourceCacheFactory
}

// configureDelegate injects resource factories and nativeImageFetch into the
// underlying buildStepDelegate of any delegate created by this factory. This
// enables the metadata-only FetchImage path on K8s for all delegate types.
func (df DelegateFactory) configureDelegate(d any) {
	var bsd *buildStepDelegate
	switch v := d.(type) {
	case *buildStepDelegate:
		bsd = v
	case *getDelegate:
		bsd, _ = v.BuildStepDelegate.(*buildStepDelegate)
	case *putDelegate:
		bsd, _ = v.BuildStepDelegate.(*buildStepDelegate)
	case *taskDelegate:
		bsd, _ = v.BuildStepDelegate.(*buildStepDelegate)
	case *checkDelegate:
		bsd, _ = v.BuildStepDelegate.(*buildStepDelegate)
	case *setPipelineStepDelegate:
		bsd = &v.buildStepDelegate
	}
	if bsd != nil {
		bsd.nativeImageFetch = df.nativeImageFetch
		bsd.resourceConfigFactory = df.resourceConfigFactory
		bsd.resourceCacheFactory = df.resourceCacheFactory
	}
}

func (delegate DelegateFactory) GetDelegate(state exec.RunState) exec.GetDelegate {
	d := NewGetDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker, delegate.nativeImageFetch)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) PutDelegate(state exec.RunState) exec.PutDelegate {
	d := NewPutDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) TaskDelegate(state exec.RunState) exec.TaskDelegate {
	d := NewTaskDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker, delegate.dbWorkerFactory, delegate.lockFactory, delegate.nativeImageFetch)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) RunDelegate(state exec.RunState) exec.RunDelegate {
	d := NewBuildStepDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker, atc.DisableRedactSecrets, delegate.nativeImageFetch)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) CheckDelegate(state exec.RunState) exec.CheckDelegate {
	d := NewCheckDelegate(delegate.build, delegate.plan, state, clock.NewClock(), delegate.rateLimiter, delegate.policyChecker)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) BuildStepDelegate(state exec.RunState) exec.BuildStepDelegate {
	d := NewBuildStepDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker, atc.DisableRedactSecrets, delegate.nativeImageFetch)
	delegate.configureDelegate(d)
	return d
}

func (delegate DelegateFactory) SetPipelineStepDelegate(state exec.RunState) exec.SetPipelineStepDelegate {
	d := NewSetPipelineStepDelegate(delegate.build, delegate.plan.ID, state, clock.NewClock(), delegate.policyChecker)
	delegate.configureDelegate(d)
	return d
}
