package lidar

import (
	"context"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
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
						s.resolveResourceType(ctx, rt)
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

func (s *scanner) resolveResourceType(ctx context.Context, rt db.ResourceType) {
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

	// Extract repository and tag from source config.
	source := rt.Source()
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

	// Resolve the digest via registry API.
	digest, err := s.resolver.Resolve(ctx, repository, tag, auth)
	if err != nil {
		logger.Error("failed-to-resolve-digest", err)
		return
	}

	logger.Debug("resolved-digest", lager.Data{"digest": digest})

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
		logger.Error("failed-to-set-resource-config-scope", err)
		return
	}

	// Save the resolved version.
	version := atc.Version{"digest": digest}
	err = scope.SaveVersions(db.SpanContext{}, []atc.Version{version})
	if err != nil {
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
						if s.resolver != nil && rs.Type() == "registry-image" {
							s.resolveResource(ctx, rs)
							return
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

func (s *scanner) resolveResource(ctx context.Context, rs db.Resource) {
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
	source := rs.Source()
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

	// Resolve the digest via registry API.
	digest, err := s.resolver.Resolve(ctx, repository, tag, auth)
	if err != nil {
		logger.Error("failed-to-resolve-digest", err)
		return
	}

	logger.Debug("resolved-digest", lager.Data{"digest": digest})

	// Find or create resource config.
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
		logger.Error("failed-to-set-resource-config-scope", err)
		return
	}

	// Save the resolved version.
	version := atc.Version{"digest": digest}
	err = scope.SaveVersions(db.SpanContext{}, []atc.Version{version})
	if err != nil {
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
