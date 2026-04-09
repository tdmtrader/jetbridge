package exec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type CheckStep struct {
	planID                atc.PlanID
	plan                  atc.CheckPlan
	metadata              StepMetadata
	containerMetadata     db.ContainerMetadata
	resourceConfigFactory db.ResourceConfigFactory
	delegateFactory       CheckDelegateFactory
	workerPool            Pool
	defaultCheckTimeout   time.Duration
	resolver              imageresolver.Resolver
}

// CheckStepOption configures optional fields on a CheckStep.
type CheckStepOption func(*CheckStep)

// WithCheckResolver sets an image resolver for native registry-image check
// resolution, bypassing the need to spawn a check container.
func WithCheckResolver(r imageresolver.Resolver) CheckStepOption {
	return func(s *CheckStep) {
		s.resolver = r
	}
}

//counterfeiter:generate . CheckDelegateFactory
type CheckDelegateFactory interface {
	CheckDelegate(state RunState) CheckDelegate
}

//counterfeiter:generate . CheckDelegate
type CheckDelegate interface {
	BuildStepDelegate

	FindOrCreateScope(db.ResourceConfig) (db.ResourceConfigScope, error)
	WaitToRun(context.Context, db.ResourceConfigScope) (lock.Lock, bool, error)
	PointToCheckedConfig(db.ResourceConfigScope) error
	UpdateScopeLastCheckStartTime(db.ResourceConfigScope, bool) (bool, int, error)
	UpdateScopeLastCheckEndTime(db.ResourceConfigScope, bool) (bool, error)
}

func NewCheckStep(
	planID atc.PlanID,
	plan atc.CheckPlan,
	metadata StepMetadata,
	resourceConfigFactory db.ResourceConfigFactory,
	containerMetadata db.ContainerMetadata,
	pool Pool,
	delegateFactory CheckDelegateFactory,
	defaultCheckTimeout time.Duration,
	opts ...CheckStepOption,
) Step {
	s := &CheckStep{
		planID:                planID,
		plan:                  plan,
		metadata:              metadata,
		resourceConfigFactory: resourceConfigFactory,
		containerMetadata:     containerMetadata,
		workerPool:            pool,
		delegateFactory:       delegateFactory,
		defaultCheckTimeout:   defaultCheckTimeout,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (step *CheckStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	attrs := tracing.Attrs{
		"name": step.plan.Name,
	}

	if step.plan.Resource != "" {
		attrs["resource"] = step.plan.Resource
	}

	if step.plan.ResourceType != "" {
		attrs["resource_type"] = step.plan.ResourceType
	}

	delegate := step.delegateFactory.CheckDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "check", attrs)

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "check", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *CheckStep) run(ctx context.Context, state RunState, delegate CheckDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("check-step", lager.Data{
		"step-name": step.plan.Name,
	})

	oteltrace.SpanFromContext(ctx).AddEvent("step.initializing")
	delegate.Initializing(logger)

	source, err := creds.NewSource(state, step.plan.Source).Evaluate()
	if err != nil {
		return false, fmt.Errorf("resource config creds evaluation: %w", err)
	}

	var imageSpec runtime.ImageSpec
	var imageResourceCache db.ResourceCache
	if step.plan.TypeImage.ImageRef != "" {
		imageSpec.ImageURL = step.plan.TypeImage.ImageRef
		imageSpec.Privileged = step.plan.TypeImage.Privileged
	} else if step.plan.TypeImage.GetPlan != nil {
		var err error
		imageSpec, imageResourceCache, err = delegate.FetchImage(ctx, *step.plan.TypeImage.GetPlan, step.plan.TypeImage.CheckPlan, step.plan.TypeImage.Privileged)
		if err != nil {
			return false, err
		}
	} else {
		imageSpec.ResourceType = step.plan.TypeImage.BaseType
	}

	resourceConfig, err := step.resourceConfigFactory.FindOrCreateResourceConfig(step.plan.Type, source, imageResourceCache)
	if err != nil {
		return false, fmt.Errorf("create resource config: %w", err)
	}

	// XXX(check-refactor): we should remove scopes as soon as it's safe to do
	// so, i.e. global resources is on by default. i think this can be done when
	// time resource becomes time var source (resolving thundering herd problem)
	// and IAM is handled via var source prototypes (resolving unintentionally
	// shared history problem)
	scope, err := delegate.FindOrCreateScope(resourceConfig)
	if err != nil {
		return false, fmt.Errorf("create resource config scope: %w", err)
	}

	// Point scope to resource before check runs. Because a resource's check build
	// summary is associated with scope, only after pointing to scope, check status
	// can be fetched.
	err = delegate.PointToCheckedConfig(scope)
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			logger.Info("scope-deleted-before-check", lager.Data{"scope": scope.ID()})
			delegate.Finished(logger, false)
			return false, nil
		}
		return false, fmt.Errorf("update resource config scope: %w", err)
	}

	lock, run, err := delegate.WaitToRun(ctx, scope)
	if err != nil {
		return false, fmt.Errorf("wait: %w", err)
	}

	logger.Debug("after-wait-to-run", lager.Data{"run": run, "scope": scope.ID()})

	if run {
		defer func() {
			err := lock.Release()
			if err != nil {
				logger.Error("failed-to-release-lock", err)
			}
		}()

		fromVersion := step.plan.FromVersion
		if fromVersion == nil {
			latestVersion, found, err := scope.LatestVersion()
			if err != nil {
				return false, fmt.Errorf("get latest version: %w", err)
			}

			if found {
				fromVersion = atc.Version(latestVersion.Version())
			}
		}

		metric.Metrics.ChecksStarted.Inc()

		_, buildId, err := delegate.UpdateScopeLastCheckStartTime(scope, !step.plan.IsResourceCheck())
		if err != nil {
			return false, fmt.Errorf("update check start time: %w", err)
		}

		if buildId != 0 {
			// Update build id in logger as in-memory build's id is only generated when starts to run check.
			logger = logger.WithData(lager.Data{"build": buildId})
			ctx = lagerctx.NewContext(ctx, logger)
		}

		var versions []atc.Version
		var runErr error

		// Native resolution for registry-image — resolve digest via OCI
		// registry API without spawning a check container. Uses
		// google.Keychain for Workload Identity / ADC on GCP.
		if step.resolver != nil && step.plan.Type == "registry-image" {
			versions, runErr = step.resolveNatively(ctx, logger, source)
		} else {
			var processResult runtime.ProcessResult
			versions, processResult, runErr = step.runCheck(ctx, logger, delegate, imageSpec, resourceConfig, source, fromVersion)
			if processResult.ExitStatus != 0 {
				metric.Metrics.ChecksFinishedWithError.Inc()

				if _, err := delegate.UpdateScopeLastCheckEndTime(scope, false); err != nil {
					return false, fmt.Errorf("update check end time: %w", err)
				}

				oteltrace.SpanFromContext(ctx).AddEvent("step.finished")
				delegate.Finished(logger, false)
				return false, nil
			}
		}

		if runErr != nil {
			metric.Metrics.ChecksFinishedWithError.Inc()

			if _, err := delegate.UpdateScopeLastCheckEndTime(scope, false); err != nil {
				return false, fmt.Errorf("update check end time: %w", err)
			}

			if errors.Is(runErr, context.DeadlineExceeded) {
				oteltrace.SpanFromContext(ctx).AddEvent("step.errored")
				delegate.Errored(logger, TimeoutLogMessage)
				return false, nil
			}

			return false, fmt.Errorf("run check: %w", runErr)
		}

		metric.Metrics.ChecksFinishedWithSuccess.Inc()

		err = scope.SaveVersions(db.NewSpanContext(ctx), versions)
		if err != nil {
			if db.IsForeignKeyViolation(err) {
				logger.Info("scope-deleted-during-check", lager.Data{"scope": scope.ID()})
				delegate.Finished(logger, false)
				return false, nil
			}
			return false, fmt.Errorf("save versions: %w", err)
		}

		if len(versions) > 0 {
			state.StoreResult(step.planID, versions[len(versions)-1])
		}

		_, err = delegate.UpdateScopeLastCheckEndTime(scope, true)
		if err != nil {
			return false, fmt.Errorf("update check end time: %w", err)
		}
	} else {
		latestVersion, found, err := scope.LatestVersion()
		if err != nil {
			return false, fmt.Errorf("get latest version: %w", err)
		}

		if found {
			state.StoreResult(step.planID, atc.Version(latestVersion.Version()))
		}
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.finished")
	delegate.Finished(logger, true)

	return true, nil
}

func (step *CheckStep) runCheck(
	ctx context.Context,
	logger lager.Logger,
	delegate CheckDelegate,
	imageSpec runtime.ImageSpec,
	resourceConfig db.ResourceConfig,
	source atc.Source,
	fromVersion atc.Version,
) ([]atc.Version, runtime.ProcessResult, error) {
	workerSpec := worker.Spec{
		TeamID: step.metadata.TeamID,
	}

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,

		ImageSpec: imageSpec,
		Env:       step.metadata.Env(),
		Type:      db.ContainerTypeCheck,

		CertsBindMount: true,
	}
	tracing.Inject(ctx, &containerSpec)

	containerOwner := step.containerOwner(delegate, resourceConfig)

	err := delegate.BeforeSelectWorker(logger)
	if err != nil {
		return nil, runtime.ProcessResult{}, err
	}

	worker, err := step.workerPool.FindOrSelectWorker(ctx, containerOwner, containerSpec, workerSpec)
	if err != nil {
		return nil, runtime.ProcessResult{}, err
	}

	delegate.SelectedWorker(logger, worker.Name())

	ctx, cancel, err := MaybeTimeout(ctx, step.plan.Timeout, step.defaultCheckTimeout)
	if err != nil {
		return nil, runtime.ProcessResult{}, err
	}

	defer cancel()

	container, _, err := worker.FindOrCreateContainer(ctx, containerOwner, step.containerMetadata, containerSpec, delegate)
	if err != nil {
		return nil, runtime.ProcessResult{}, err
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.starting")
	delegate.Starting(logger)
	return resource.Resource{
		Source:  source,
		Version: fromVersion,
	}.Check(ctx, container, delegate.Stderr())
}

// resolveNatively resolves a registry-image resource's digest via the OCI
// registry API without spawning a check container. The source has already
// been through credential evaluation so ((var)) placeholders are resolved.
func (step *CheckStep) resolveNatively(ctx context.Context, logger lager.Logger, source atc.Source) ([]atc.Version, error) {
	repository, _ := source["repository"].(string)
	if repository == "" {
		return nil, fmt.Errorf("native check: missing repository in source")
	}
	tag, _ := source["tag"].(string)

	var auth *imageresolver.BasicAuth
	if username, ok := source["username"].(string); ok && username != "" {
		password, _ := source["password"].(string)
		auth = &imageresolver.BasicAuth{
			Username: username,
			Password: password,
		}
	}

	digest, err := step.resolver.Resolve(ctx, repository, tag, auth)
	if err != nil {
		return nil, fmt.Errorf("native check: resolving digest for %s: %w", repository, err)
	}

	logger.Info("native-check-resolved", lager.Data{"repository": repository, "digest": digest})

	return []atc.Version{{"digest": digest}}, nil
}

func (step *CheckStep) containerOwner(delegate CheckDelegate, resourceConfig db.ResourceConfig) db.ContainerOwner {
	// Every check gets its own container scoped to the build + plan.
	// The old resourceConfigCheckSessionContainerOwner reused containers
	// across check builds for the same resource config, which was an
	// optimization for Garden (long-lived containers). In K8s, pods are
	// ephemeral and the reaper aggressively deletes pods with exit-status
	// annotations, creating a race where a reused container's pod is
	// terminated between checks.
	return delegate.ContainerOwner(step.planID)
}
