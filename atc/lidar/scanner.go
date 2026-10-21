package lidar

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/vars"
)

func NewScanner(
	checkFactory db.CheckFactory,
	planFactory atc.PlanFactory,
	maxConcurrency int,
	resolver imageresolver.Resolver,
	resourceConfigFactory db.ResourceConfigFactory,
) *scanner {
	return &scanner{
		checkFactory:          checkFactory,
		planFactory:           planFactory,
		maxConcurrency:        maxConcurrency,
		resolver:              resolver,
		resourceConfigFactory: resourceConfigFactory,
	}
}

type scanner struct {
	checkFactory          db.CheckFactory
	planFactory           atc.PlanFactory
	maxConcurrency        int
	resolver              imageresolver.Resolver
	resourceConfigFactory db.ResourceConfigFactory
}

func (s *scanner) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx)

	logger.Info("start")
	defer logger.Info("end")

	resources, err := s.checkFactory.Resources()
	if err != nil {
		logger.Error("failed-to-get-resources", err)
		return err
	}

	resourceTypes, err := s.checkFactory.ResourceTypesByPipeline()
	if err != nil {
		logger.Error("failed-to-get-resource-types", err)
		return err
	}

	// Resolve resource type images natively via registry API.
	if s.resolver != nil {
		s.scanResourceTypes(ctx, resourceTypes)
	}

	s.scanResources(ctx, resources, resourceTypes)

	return nil
}

func (s *scanner) scanResourceTypes(ctx context.Context, resourceTypesMap map[int]db.ResourceTypes) {
	logger := lagerctx.FromContext(ctx)

	// Collect all resource types across pipelines.
	var allTypes []db.ResourceType
	for _, types := range resourceTypesMap {
		allTypes = append(allTypes, types...)
	}

	if len(allTypes) == 0 {
		return
	}

	maxConcurrency := min(s.maxConcurrency, len(allTypes))
	typesChan := make(chan db.ResourceType, len(allTypes))
	waitGroup := sync.WaitGroup{}

	go func() {
		defer close(typesChan)
		for _, rt := range allTypes {
			select {
			case typesChan <- rt:
			case <-ctx.Done():
				return
			}
		}
	}()

	for range maxConcurrency {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for {
				select {
				case rt, open := <-typesChan:
					if !open {
						return
					}

					func() {
						defer func() {
							err := util.DumpPanic(recover(), "scanning resource type %d", rt.ID())
							if err != nil {
								logger.Error("panic-in-resource-type-scan", err)
							}
						}()
						s.resolveResourceType(ctx, rt, resourceTypesMap[rt.PipelineID()])
					}()

				case <-ctx.Done():
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		logger.Debug("resource-type-scan-cancelled")
	}
}

func (s *scanner) resolveResourceType(ctx context.Context, rt db.ResourceType, resourceTypes db.ResourceTypes) {
	logger := lagerctx.FromContext(ctx)
	logger = logger.Session("resolve-resource-type", lager.Data{
		"name":     rt.Name(),
		"pipeline": rt.PipelineName(),
		"team":     rt.TeamName(),
	})

	// Skip resource types with direct image references — already pinned.
	if rt.Image() != "" {
		logger.Debug("skip-direct-image-ref")
		return
	}

	// Skip if check_every is set to never.
	if rt.CheckEvery() != nil && rt.CheckEvery().Never {
		logger.Debug("skip-check-every-never")
		return
	}

	// Respect check interval.
	interval := atc.DefaultResourceTypeInterval
	if rt.CheckEvery() != nil {
		interval = rt.CheckEvery().Interval
	}
	if !rt.LastCheckEndTime().IsZero() && time.Now().Before(rt.LastCheckEndTime().Add(interval)) {
		logger.Debug("skip-interval-not-elapsed")
		return
	}

	// A ((var)) is left to the check of this type that every check of a
	// resource using it plans (atc.ResourceTypes.ImageForType).
	source, native := nativeSource(rt, resourceTypes)
	if !native {
		logger.Debug("skip-source-with-vars")
		return
	}

	// Extract repository and tag from source config.
	repository, _ := source["repository"].(string)
	if repository == "" {
		logger.Error("missing-repository-in-source", nil)
		return
	}
	tag, _ := source["tag"].(string)

	// Extract basic auth if present.
	var auth *imageresolver.BasicAuth
	if username, ok := source["username"].(string); ok && username != "" {
		password, _ := source["password"].(string)
		auth = &imageresolver.BasicAuth{
			Username: username,
			Password: password,
		}
	}

	// Find or create resource config for this resource type.
	// The resource type is backed by "registry-image" at its core.
	resourceConfig, err := s.resourceConfigFactory.FindOrCreateResourceConfig(
		"registry-image",
		source,
		nil, // no parent resource cache — native resolution
	)
	if err != nil {
		logger.Error("failed-to-find-or-create-resource-config", err)
		return
	}

	// Get or create a scope for this resource type.
	scope, err := resourceConfig.FindOrCreateScope(nil)
	if err != nil {
		logger.Error("failed-to-find-or-create-scope", err)
		return
	}

	// Point the resource type to this config scope.
	err = rt.SetResourceConfigScope(scope)
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// Scope was deleted by GC mid-resolve (the resources /
			// resource_types FK to resource_config_scopes). Expected transient
			// race — the next scan retries with a fresh scope, so this is not an
			// error. Mirrors the guard in atc/exec/check_step.go.
			logger.Debug("scope-deleted-before-version-save", lager.Data{"scope": scope.ID()})
			return
		}
		logger.Error("failed-to-set-resource-config-scope", err)
		return
	}

	// Resolve the digest via registry API. The scope is attached first so a
	// failed resolve records a failed check end time, and the check interval
	// gates the retry instead of every scanner pass re-asking the registry.
	digest, err := s.resolve(ctx, repository, tag, auth, rt.CheckTimeout())
	if err != nil {
		recordFailedResolve(ctx, logger, scope, err)
		return
	}

	logger.Debug("resolved-digest", lager.Data{"digest": digest})

	// Save the resolved version.
	version := atc.Version{"digest": digest}
	err = scope.SaveVersions(db.SpanContext{}, []atc.Version{version})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// Scope was deleted by GC between resolve and save (the
			// resource_config_versions FK to resource_config_scopes). Expected
			// transient race — the next scan retries. Mirrors the guard in
			// atc/exec/check_step.go.
			logger.Debug("scope-deleted-during-version-save", lager.Data{"scope": scope.ID()})
			return
		}
		logger.Error("failed-to-save-versions", err)
		return
	}

	// Update check end time for interval tracking.
	_, err = scope.UpdateLastCheckEndTime(true)
	if err != nil {
		logger.Error("failed-to-update-last-check-end-time", err)
		return
	}

	metric.Metrics.ChecksEnqueued.Inc()
	logger.Info("resolved-resource-type", lager.Data{"digest": digest})
}

func (s *scanner) scanResources(ctx context.Context, resources []db.Resource, resourceTypesMap map[int]db.ResourceTypes) {
	logger := lagerctx.FromContext(ctx)
	waitGroup := sync.WaitGroup{}

	numberOfResources := len(resources)
	maxConcurrency := min(s.maxConcurrency, numberOfResources)
	resourcesChan := make(chan db.Resource, numberOfResources)

	go func() {
		defer close(resourcesChan)
		for _, rs := range resources {
			select {
			case resourcesChan <- rs:
			case <-ctx.Done():
				logger.Debug("lidar-scanner-cancelled-sending-work", lager.Data{"error": ctx.Err().Error()})
				return
			}
		}
	}()

	for range maxConcurrency {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for {
				select {
				case rs, open := <-resourcesChan:
					if !open {
						// channel closed, no more work to do
						return
					}

					resourceTypes := resourceTypesMap[rs.PipelineID()]

					// Run check inside a func so we don't lose the worker
					// go routine if there's a panic
					func() {
						defer func() {
							err := util.DumpPanic(recover(), "scanning resource %d", rs.ID())
							if err != nil {
								logger.Error("panic-in-scanner-run", err)
							}
						}()

						// Resolve registry-image resources natively when
						// the resolver is available — no check pod needed.
						// A source holding a ((var)) is checked instead.
						if s.resolver != nil && rs.Type() == "registry-image" {
							if source, native := nativeSource(rs, resourceTypes); native {
								s.resolveResource(ctx, rs, source)
								return
							}
						}

						s.check(ctx, rs, resourceTypes)
					}()

				case <-ctx.Done():
					logger.Debug("lidar-scanner-worker-cancelled", lager.Data{"error": ctx.Err().Error()})
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
		logger.Debug("lidar-scanner-cancelled", lager.Data{"error": ctx.Err().Error()})
		return
	}
}

func (s *scanner) resolveResource(ctx context.Context, rs db.Resource, source atc.Source) {
	logger := lagerctx.FromContext(ctx)
	logger = logger.Session("resolve-resource", lager.Data{
		"name":     rs.Name(),
		"pipeline": rs.PipelineName(),
		"team":     rs.TeamName(),
	})

	// Skip if check_every is set to never.
	if rs.CheckEvery() != nil && rs.CheckEvery().Never {
		logger.Debug("skip-check-every-never")
		return
	}

	// Respect check interval.
	interval := atc.DefaultCheckInterval
	if rs.CheckEvery() != nil && rs.CheckEvery().Interval > 0 {
		interval = rs.CheckEvery().Interval
	}
	if interval > 0 && !rs.LastCheckEndTime().IsZero() && time.Now().Before(rs.LastCheckEndTime().Add(interval)) {
		logger.Debug("skip-interval-not-elapsed")
		return
	}

	// Extract repository and tag from source config.
	repository, _ := source["repository"].(string)
	if repository == "" {
		logger.Error("missing-repository-in-source", nil)
		return
	}
	tag, _ := source["tag"].(string)

	// Extract basic auth if present.
	var auth *imageresolver.BasicAuth
	if username, ok := source["username"].(string); ok && username != "" {
		password, _ := source["password"].(string)
		auth = &imageresolver.BasicAuth{
			Username: username,
			Password: password,
		}
	}

	resourceConfig, err := s.resourceConfigFactory.FindOrCreateResourceConfig(
		"registry-image",
		source,
		nil, // no parent resource cache — native resolution
	)
	if err != nil {
		logger.Error("failed-to-find-or-create-resource-config", err)
		return
	}

	// Get or create a scope for this resource.
	scope, err := resourceConfig.FindOrCreateScope(nil)
	if err != nil {
		logger.Error("failed-to-find-or-create-scope", err)
		return
	}

	// Point the resource to this config scope.
	err = rs.SetResourceConfigScope(scope)
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// Scope was deleted by GC mid-resolve (the resources /
			// resource_types FK to resource_config_scopes). Expected transient
			// race — the next scan retries with a fresh scope, so this is not an
			// error. Mirrors the guard in atc/exec/check_step.go.
			logger.Debug("scope-deleted-before-version-save", lager.Data{"scope": scope.ID()})
			return
		}
		logger.Error("failed-to-set-resource-config-scope", err)
		return
	}

	// Resolve the digest via registry API. The scope is attached first so a
	// failed resolve records a failed check end time, and the check interval
	// gates the retry instead of every scanner pass re-asking the registry.
	digest, err := s.resolve(ctx, repository, tag, auth, rs.CheckTimeout())
	if err != nil {
		recordFailedResolve(ctx, logger, scope, err)
		return
	}

	logger.Debug("resolved-digest", lager.Data{"digest": digest})

	// Save the resolved version.
	version := atc.Version{"digest": digest}
	err = scope.SaveVersions(db.SpanContext{}, []atc.Version{version})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// Scope was deleted by GC between resolve and save (the
			// resource_config_versions FK to resource_config_scopes). Expected
			// transient race — the next scan retries. Mirrors the guard in
			// atc/exec/check_step.go.
			logger.Debug("scope-deleted-during-version-save", lager.Data{"scope": scope.ID()})
			return
		}
		logger.Error("failed-to-save-versions", err)
		return
	}

	// Update check end time for interval tracking.
	_, err = scope.UpdateLastCheckEndTime(true)
	if err != nil {
		logger.Error("failed-to-update-last-check-end-time", err)
		return
	}

	metric.Metrics.ChecksEnqueued.Inc()
	logger.Info("resolved-resource", lager.Data{"digest": digest})
}

// recordFailedResolve records a failed resolve as a failed check end time on
// the scope, so the check interval gates the retry. A resolve cut short by the
// scan's own cancellation (the web shutting down) was never refused by the
// registry: it records nothing, and the next scan resolves again.
func recordFailedResolve(ctx context.Context, logger lager.Logger, scope db.ResourceConfigScope, err error) {
	if ctx.Err() != nil {
		logger.Debug("resolve-cancelled", lager.Data{"error": err.Error()})
		return
	}

	logger.Error("failed-to-resolve-digest", err)
	if _, err := scope.UpdateLastCheckEndTime(false); err != nil {
		logger.Error("failed-to-update-last-check-end-time", err)
	}
}

// nativeResolveTimeout bounds one native registry-image resolve. A scan waits
// for every resolve it starts while it holds the scanner's task lock, which no
// other web can take, so a registry that accepts a connection and never answers
// would otherwise stop lidar for every team. The bound is a minute, the default
// resource checking interval, per resolve: resource types and resources resolve
// in two phases one after the other, so stalled registries behind both can hold
// a scan for about one bound per phase, and more stalled resolves than the
// scanner's concurrency hold each phase for one bound per batch.
var nativeResolveTimeout = time.Minute

// resolve asks the registry for a digest within nativeResolveTimeout, or within
// the resource's check_timeout when that is shorter.
func (s *scanner) resolve(ctx context.Context, repository, tag string, auth *imageresolver.BasicAuth, checkTimeout string) (string, error) {
	timeout := nativeResolveTimeout
	if d, err := time.ParseDuration(checkTimeout); err == nil && d > 0 && d < timeout {
		timeout = d
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.resolver.Resolve(ctx, repository, tag, auth)
}

// nativeSource returns the source native resolution uses: the checkable's
// source over the same defaults a check merges under it (the parent type's,
// else the base resource type's), so the resource config lidar finds is the
// one a build looks up. It reports false when that source still holds a
// ((var)): only a build's RunState evaluates one, and the registry would be
// sent the literal placeholder text.
func nativeSource(checkable db.Checkable, resourceTypes db.ResourceTypes) (atc.Source, bool) {
	defaults := atc.Source{}
	if parentType, found := resourceTypes.Parent(checkable); found {
		defaults = parentType.Defaults()
	} else if base, found := atc.FindBaseResourceTypeDefaults(checkable.Type()); found {
		defaults = base
	}

	source := defaults.Merge(checkable.Source())
	payload, err := json.Marshal(source)
	if err != nil {
		return nil, false
	}
	return source, len(vars.NewTemplate(payload).ExtraVarNames()) == 0
}

func (s *scanner) check(ctx context.Context, checkable db.Checkable, resourceTypes db.ResourceTypes) {
	logger := lagerctx.FromContext(ctx)

	version := checkable.CurrentPinnedVersion()

	if checkable.CheckEvery() != nil && checkable.CheckEvery().Never {
		return
	}

	_, created, err := s.checkFactory.TryCreateCheck(lagerctx.NewContext(ctx, logger), checkable, resourceTypes, version, false, false, false)
	if err != nil {
		logger.Error("failed-to-create-check", err)
		return
	}

	if !created {
		logger.Debug("check-already-exists")
	} else {
		metric.Metrics.ChecksEnqueued.Inc()
	}
}

func (s *scanner) Drain(ctx context.Context) {
	s.checkFactory.Drain()
}
