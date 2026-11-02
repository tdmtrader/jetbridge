package lidar_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"code.cloudfoundry.org/clock"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/vars"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type Scanner interface {
	Run(ctx context.Context) error
}

// fkViolation builds an error with the shape the production check/resolve paths
// see when the GC deletes a resource_config_scope mid-flight: a *pgconn.PgError
// (SQLSTATE 23503) wrapped with context, matching db.IsForeignKeyViolation.
func fkViolation(prefix string) error {
	return fmt.Errorf("%s: %w", prefix, &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation})
}

// loggedAt reports whether logs contain an entry at the given level whose
// message ends with suffix (lager prefixes the session path, so match the tail).
func loggedAt(logs []lager.LogFormat, level lager.LogLevel, suffix string) bool {
	for _, l := range logs {
		if l.LogLevel == level && strings.HasSuffix(l.Message, suffix) {
			return true
		}
	}
	return false
}

var _ = Describe("Lidar PostgreSQL fixture", func() {
	It("reads persisted pipeline state through a separately constructed factory", func() {
		fixture := useLidarDB()
		team, pipeline := persistLidarPipeline(
			fixture,
			"fixture-team",
			"fixture-pipeline",
			atc.Config{Resources: atc.ResourceConfigs{{
				Name: "fixture-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "fixture"},
			}}},
		)

		loadedTeam, found, err := db.NewTeamFactory(fixture.Conn, fixture.LockFactory).FindTeam(team.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loadedTeam.ID()).To(Equal(team.ID()))
		loadedPipeline, found, err := loadedTeam.Pipeline(atc.PipelineRef{
			Name:         pipeline.Name(),
			InstanceVars: pipeline.InstanceVars(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loadedPipeline.ID()).To(Equal(pipeline.ID()))
	})
})

var _ = Describe("Scanner", func() {
	const (
		teamName     = "scanner-team"
		pipelineName = "scanner-pipeline"
	)

	newScanner := func(factory db.CheckFactory, maxConcurrency int) Scanner {
		return lidar.NewScanner(factory, atc.NewPlanFactory(0), maxConcurrency, nil, nil, nil, nil)
	}

	It("returns the real-backed enumeration failure", func() {
		fixture := useLidarDB()
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		factory.FailResources(errors.New("nope"))

		Expect(newScanner(factory, 10).Run(context.Background())).To(MatchError("nope"))
		Expect(factory.Calls()).To(BeEmpty())
	})

	It("does not schedule a check for an already-cancelled empty enumeration", func() {
		fixture := useLidarDB()
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(newScanner(factory, 10).Run(ctx)).To(Succeed())
		Expect(factory.Calls()).To(BeEmpty())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("returns the resource-type enumeration failure after loading real resources", func() {
		fixture := useLidarDB()
		_, _ = persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "enumerated-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "enumerated"},
			}}, nil,
		))
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		factory.FailResourceTypes(errors.New("nope"))

		Expect(newScanner(factory, 10).Run(context.Background())).To(MatchError("nope"))
		Expect(factory.Calls()).To(BeEmpty())
	})

	It("naturally excludes a persisted check_every never resource", func() {
		fixture := useLidarDB()
		_, _ = persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "never-resource", Type: dbtest.BaseResourceType,
				Source:     atc.Source{"repository": "never"},
				CheckEvery: &atc.CheckEvery{Never: true},
			}}, nil,
		))
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(BeEmpty())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("creates an in-memory check from a persisted base-type resource", func() {
		fixture := useLidarDB()
		resourceSource := atc.Source{"repository": "base-resource"}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "base-resource", Type: dbtest.BaseResourceType,
				Source: resourceSource, Tags: atc.Tags{"tag-a", "tag-b"},
				CheckEvery:   &atc.CheckEvery{Interval: 23 * time.Minute},
				CheckTimeout: "7m",
			}}, nil,
		))
		resource := lidarPipelineResource(pipeline, "base-resource")
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		call := factory.Calls()[0]
		Expect(call.checkable.Name()).To(Equal(resource.Name()))
		Expect(call.resourceTypes).To(BeNil())
		Expect(call.from).To(BeNil())
		Expect(call.manuallyTriggered).To(BeFalse())
		Expect(call.skipIntervalRecursively).To(BeFalse())
		Expect(call.toDB).To(BeFalse())

		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		plan := build.PrivatePlan()
		Expect(plan.Check).NotTo(BeNil())
		Expect(plan.Check.Name).To(Equal("base-resource"))
		Expect(plan.Check.Resource).To(Equal("base-resource"))
		Expect(plan.Check.Type).To(Equal(dbtest.BaseResourceType))
		Expect(plan.Check.Source).To(Equal(resourceSource))
		Expect(plan.Check.Tags).To(Equal(atc.Tags{"tag-a", "tag-b"}))
		Expect(plan.Check.Timeout).To(Equal("7m"))
		Expect(plan.Check.Interval).To(Equal(atc.CheckEvery{Interval: 23 * time.Minute}))
		Expect(plan.Check.TypeImage.BaseType).To(Equal(dbtest.BaseResourceType))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("creates a real check plan with its persisted custom parent type", func() {
		fixture := useLidarDB()
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "custom-resource", Type: "custom-type",
				Source: atc.Source{"repository": "custom-resource"},
			}},
			atc.ResourceTypes{{
				Name: "custom-type", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "custom-image"},
				Tags:   atc.Tags{"type-tag"},
			}},
		))
		resource := lidarPipelineResource(pipeline, "custom-resource")
		resourceType := lidarPipelineResourceType(pipeline, "custom-type")
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		call := factory.Calls()[0]
		Expect(call.checkable.Name()).To(Equal(resource.Name()))
		Expect(call.resourceTypes).To(HaveLen(1))
		Expect(call.resourceTypes[0].ID()).To(Equal(resourceType.ID()))

		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		plan := build.PrivatePlan()
		Expect(plan.Check).NotTo(BeNil())
		Expect(plan.Check.Name).To(Equal("custom-resource"))
		Expect(plan.Check.Resource).To(Equal("custom-resource"))
		Expect(plan.Check.Type).To(Equal("custom-type"))
		Expect(plan.Check.Source).To(Equal(atc.Source{"repository": "custom-resource"}))
		Expect(plan.Check.TypeImage.BaseType).To(Equal(dbtest.BaseResourceType))
		// The type's image is its registry-image reference, resolved natively;
		// the check build plans no check or get for it.
		Expect(plan.Check.TypeImage.GetPlan).NotTo(BeNil())
		Expect(plan.Check.TypeImage.GetPlan.Get.Name).To(Equal("custom-type"))
		Expect(plan.Check.TypeImage.GetPlan.Get.Type).To(Equal(dbtest.BaseResourceType))
		Expect(plan.Check.TypeImage.GetPlan.Get.Source).To(Equal(atc.Source{"repository": "custom-image"}))
		Expect(plan.Check.TypeImage.GetPlan.Get.Tags).To(Equal(atc.Tags{"type-tag"}))
		Expect(plan.Check.TypeImage.GetPlan.Get.VersionFrom).To(BeNil())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("recovers when real resource scheduling crosses the explicit panic seam", func() {
		fixture := useLidarDB()
		_, _ = persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "panic-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "panic"},
			}}, nil,
		))
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		factory.PanicOnTryCreate()

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("forwards a persisted API pin to the production CheckFactory", func() {
		fixture := useLidarDB()
		team, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "pinned-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "pinned"},
			}}, nil,
		))
		version := atc.Version{"ref": "pinned-version"}
		scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
		scenario.Run(fixture.Builder.WithResourceVersions("pinned-resource", version))
		resource := scenario.Resource("pinned-resource")
		persistedVersion := scenario.ResourceVersion("pinned-resource", version)
		changed, err := resource.PinVersion(persistedVersion.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		call := factory.Calls()[0]
		Expect(call.resource().ID()).To(Equal(resource.ID()))
		Expect(call.resourceTypes).To(BeNil())
		Expect(call.from).To(Equal(version))
		Expect(call.manuallyTriggered).To(BeFalse())
		Expect(call.skipIntervalRecursively).To(BeFalse())
		Expect(call.toDB).To(BeFalse())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.FromVersion).To(Equal(version))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("forwards a nil pin from an unpinned persisted resource", func() {
		fixture := useLidarDB()
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "unpinned-resource", Type: dbtest.BaseResourceType,
				Source: atc.Source{"repository": "unpinned"},
			}}, nil,
		))
		resource := lidarPipelineResource(pipeline, "unpinned-resource")
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(resource.ID()))
		Expect(factory.Calls()[0].from).To(BeNil())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("excludes a steady-state put-only resource after a successful scoped check", func() {
		fixture := useLidarDB()
		team, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, atc.Config{
			Resources: atc.ResourceConfigs{
				{Name: "input-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "input"}},
				{Name: "put-only-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"repository": "put-only"}},
			},
			Jobs: atc.JobConfigs{{
				Name: "scan-job",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "input-resource"}},
					{Config: &atc.PutStep{Name: "put-only-resource"}},
				},
			}},
		})
		scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
		scenario.Run(fixture.Builder.WithResourceVersions("put-only-resource", atc.Version{"ref": "complete"}))
		input := scenario.Resource("input-resource")
		putOnly := scenario.Resource("put-only-resource")
		Expect(putOnly.ResourceConfigScopeID()).NotTo(BeZero())
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 10).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(input.ID()))
		Expect(factory.Calls()[0].resource().ID()).NotTo(Equal(putOnly.ID()))
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(input.ID()))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	// Attach a distinct scope before scanning so the in-flight dedup guard
	// must distinguish resources even when they share a pipeline.
	It("checks all persisted resources beyond the worker concurrency limit", func() {
		fixture := useLidarDB()
		resources := make(atc.ResourceConfigs, 0, 20)
		for i := range 20 {
			resources = append(resources, atc.ResourceConfig{
				Name:   fmt.Sprintf("resource-%02d", i),
				Type:   dbtest.BaseResourceType,
				Source: atc.Source{"index": fmt.Sprintf("%02d", i)},
			})
		}
		_, pipeline := persistLidarPipeline(
			fixture, teamName, pipelineName, lidarConfigWithGets(resources, nil),
		)
		persisted, err := pipeline.Resources()
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted).To(HaveLen(20))
		scopeIDs := make(map[int]struct{}, 20)
		for _, resource := range persisted {
			scope := attachLidarResourceScope(fixture, resource)
			Expect(scope.ID()).NotTo(BeZero())
			scopeIDs[scope.ID()] = struct{}{}
		}
		Expect(scopeIDs).To(HaveLen(20))
		factory := observeLidarCheckFactory(fixture.CheckFactory)

		Expect(newScanner(factory, 5).Run(context.Background())).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(20))
		builds := drainLidarCheckBuilds(fixture, 20)
		seenIDs := make(map[int]struct{}, 20)
		seenNames := make(map[string]struct{}, 20)
		for _, build := range builds {
			seenIDs[build.ResourceID()] = struct{}{}
			seenNames[build.ResourceName()] = struct{}{}
			Expect(build.PrivatePlan().Check.Resource).To(Equal(build.ResourceName()))
			Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		}
		Expect(seenIDs).To(HaveLen(20))
		Expect(seenNames).To(HaveLen(20))
		persisted, err = pipeline.Resources()
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted).To(HaveLen(20))
		freshScopeIDs := make(map[int]struct{}, 20)
		for _, resource := range persisted {
			Expect(resource.ResourceConfigScopeID()).NotTo(BeZero())
			freshScopeIDs[resource.ResourceConfigScopeID()] = struct{}{}
		}
		Expect(freshScopeIDs).To(Equal(scopeIDs))
	})
})

var _ = Describe("Scanner Resource Type Resolution", func() {
	const (
		teamName         = "resource-type-team"
		pipelineName     = "resource-type-pipeline"
		resourceTypeName = "my-custom-type"
	)

	defaultResourceType := func() atc.ResourceType {
		return atc.ResourceType{
			Name: resourceTypeName,
			Type: "registry-image",
			Source: atc.Source{
				"repository": "my-registry/my-image",
				"tag":        "latest",
			},
		}
	}

	persistResourceType := func(fixture *lidarDB, team, pipeline string, config atc.ResourceType) (db.Pipeline, db.ResourceType) {
		GinkgoHelper()
		_, savedPipeline := persistLidarPipeline(
			fixture, team, pipeline, atc.Config{ResourceTypes: atc.ResourceTypes{config}},
		)
		return savedPipeline, lidarPipelineResourceType(savedPipeline, config.Name)
	}

	runScanner := func(
		fixture *lidarDB,
		factory db.CheckFactory,
		resolver *imageresolvertesting.FakeResolver,
		resourceConfigFactory db.ResourceConfigFactory,
		logger *lagertest.TestLogger,
	) error {
		GinkgoHelper()
		return lidar.NewScanner(
			factory, atc.NewPlanFactory(0), 10, resolver, resourceConfigFactory, &dummy.Secrets{}, nil,
		).Run(lagerctx.NewContext(context.Background(), logger))
	}

	It("persists the resolved digest, scope, and check end time", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		pipeline, resourceType := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:abc123", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, tag, auth := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("my-registry/my-image"))
		Expect(tag).To(Equal("latest"))
		Expect(auth).To(BeNil())

		freshType := lidarPipelineResourceType(pipeline, resourceType.Name())
		Expect(freshType.ID()).To(Equal(resourceType.ID()))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshType.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		scope := resolvedLidarResourceTypeScope(fixture, freshType)
		Expect(scope.ResourceID()).To(BeNil())
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:abc123"})
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeTrue())
		Expect(freshType.LastCheckEndTime()).To(Equal(lastCheck.EndTime))
		Expect(freshType.ResolvedImage()).To(Equal("my-registry/my-image@sha256:abc123"))
	})

	It("treats a scope deletion during version save as a debug-level race", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			config.Type, config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:ignored", nil)
		logger := lagertest.NewTestLogger("test")
		configFactory := lidarResourceConfigFactory{
			ResourceConfigFactory: fixture.ResourceConfigFactory,
			saveVersionsErr:       fkViolation("save versions"),
		}

		Expect(runScanner(fixture, fixture.CheckFactory, resolver, configFactory, logger)).To(Succeed())
		Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-during-version-save")).To(BeTrue())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-save-versions")).To(BeFalse())
		scope := resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName))
		Expect(scope.ID()).To(Equal(baselineScope.ID()))
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("treats a scope deletion before attachment as a debug-level race", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		pipeline, resourceType := persistResourceType(fixture, teamName, pipelineName, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			config.Type, config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		factory.FailResourceTypeScope(resourceType.ID(), fkViolation("set resource scope"))
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:ignored", nil)
		logger := lagertest.NewTestLogger("test")

		Expect(runScanner(fixture, factory, resolver, fixture.ResourceConfigFactory, logger)).To(Succeed())
		Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-before-version-save")).To(BeTrue())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-set-resource-config-scope")).To(BeFalse())
		freshType := lidarPipelineResourceType(pipeline, resourceTypeName)
		Expect(freshType.ResourceConfigScopeID()).To(BeZero())
		resourceConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			config.Type, config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		scope, err := resourceConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(scope.ID()).To(Equal(baselineScope.ID()))
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("logs a non-FK version-save failure as an error", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			config.Type, config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:ignored", nil)
		logger := lagertest.NewTestLogger("test")
		configFactory := lidarResourceConfigFactory{
			ResourceConfigFactory: fixture.ResourceConfigFactory,
			saveVersionsErr:       errors.New("connection refused"),
		}

		Expect(runScanner(fixture, fixture.CheckFactory, resolver, configFactory, logger)).To(Succeed())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-save-versions")).To(BeTrue())
		scope := resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName))
		Expect(scope.ID()).To(Equal(baselineScope.ID()))
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("passes persisted basic-auth credentials to the resolver", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Source = atc.Source{
			"repository": "private-registry/image",
			"tag":        "v2",
			"username":   "user",
			"password":   "pass",
		}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:private", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, tag, auth := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("private-registry/image"))
		Expect(tag).To(Equal("v2"))
		Expect(auth).NotTo(BeNil())
		Expect(auth.Username).To(Equal("user"))
		Expect(auth.Password).To(Equal("pass"))
		scope := resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName))
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:private"})
	})

	It("resolves a resource type over the base resource type defaults", func() {
		atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{
			"registry-image": {"username": "default-user", "password": "default-pass"},
		})
		DeferCleanup(atc.LoadBaseResourceTypeDefaults, map[string]atc.Source{})
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Source = atc.Source{"repository": "private-registry/image"}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:defaults", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, _, _, auth := resolver.ResolveArgsForCall(0)
		Expect(auth).NotTo(BeNil())
		Expect(auth.Username).To(Equal("default-user"))
		Expect(auth.Password).To(Equal("default-pass"))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig("registry-image", atc.Source{
			"repository": "private-registry/image",
			"username":   "default-user",
			"password":   "default-pass",
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigID()).To(Equal(expectedConfig.ID()))
	})

	It("leaves a resource type whose ((var)) does not evaluate to the check of a resource using it", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Source = atc.Source{"repository": "((repo))"}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{Name: "custom-resource", Type: resourceTypeName, Source: atc.Source{}}},
			atc.ResourceTypes{config},
		))
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:never", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())

		build := drainLidarCheckBuilds(fixture, 1)[0]
		// The type's image stays a reference the delegate evaluates and
		// resolves at check time; no check is planned for it.
		typeImage := build.PrivatePlan().Check.TypeImage
		Expect(typeImage.ImageRef).To(BeEmpty())
		Expect(typeImage.GetPlan).NotTo(BeNil())
		Expect(typeImage.GetPlan.Get.Source).To(Equal(atc.Source{"repository": "((repo))"}))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("skips a persisted direct image reference", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Image = "direct-image:sha256"
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("skips a persisted check_every never resource type", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.CheckEvery = &atc.CheckEvery{Never: true}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("gives up on a registry that never answers within the native resolve bound", func() {
		DeferCleanup(lidar.SetNativeResolveTimeout(time.Second))
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Source = stalledRegistrySource()
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		logger := lagertest.NewTestLogger("test")

		Eventually(runNativeScanner(fixture, logger)).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-resolve-digest")).To(BeTrue())
		expectFailedLidarResolve(resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName)))
	})

	It("skips a persisted resource type whose nonzero interval has not elapsed", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, resourceType := persistResourceType(fixture, teamName, pipelineName, config)
		scope := attachLidarResourceTypeScope(fixture, resourceType)
		updated, err := scope.UpdateLastCheckEndTime(true)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(BeTrue())
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		freshScope := resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName))
		_, found, err := freshScope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := freshScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
	})

	It("records a failed resolve so the check interval gates the retry", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("", errors.New("registry down"))

		for range 2 {
			Expect(runScanner(
				fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
				lagertest.NewTestLogger("test"),
			)).To(Succeed())
		}
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		freshType := lidarPipelineResourceType(pipeline, resourceTypeName)
		scope := resolvedLidarResourceTypeScope(fixture, freshType)
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeFalse())
		Expect(freshType.LastCheckEndTime()).To(Equal(lastCheck.EndTime))
	})

	It("does not call the resolver when persisted source has no repository", func() {
		fixture := useLidarDB()
		config := defaultResourceType()
		config.Source = atc.Source{"tag": "latest"}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("resolves persisted resource types across independent pipelines", func() {
		fixture := useLidarDB()
		firstConfig := defaultResourceType()
		firstPipeline, _ := persistResourceType(fixture, teamName, pipelineName, firstConfig)
		secondConfig := atc.ResourceType{
			Name: "other-type", Type: "registry-image",
			Source: atc.Source{"repository": "other-registry/other-image"},
		}
		secondPipeline, _ := persistResourceType(
			fixture, "other-team", "other-pipeline", secondConfig,
		)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:def456", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(2))
		repositories := make([]string, 0, 2)
		for i := range 2 {
			_, repository, _, _ := resolver.ResolveArgsForCall(i)
			repositories = append(repositories, repository)
		}
		Expect(repositories).To(ConsistOf("my-registry/my-image", "other-registry/other-image"))
		firstType := lidarPipelineResourceType(firstPipeline, resourceTypeName)
		secondType := lidarPipelineResourceType(secondPipeline, secondConfig.Name)
		expectedFirstConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", firstConfig.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		expectedSecondConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", secondConfig.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstType.ResourceConfigID()).To(Equal(expectedFirstConfig.ID()))
		Expect(secondType.ResourceConfigID()).To(Equal(expectedSecondConfig.ID()))
		Expect(firstType.ResourceConfigID()).NotTo(Equal(secondType.ResourceConfigID()))

		firstScope := resolvedLidarResourceTypeScope(fixture, firstType)
		secondScope := resolvedLidarResourceTypeScope(fixture, secondType)
		Expect(firstScope.ID()).NotTo(Equal(secondScope.ID()))
		expectLidarLatestVersion(firstScope, atc.Version{"digest": "sha256:def456"})
		expectLidarLatestVersion(secondScope, atc.Version{"digest": "sha256:def456"})
	})
})

var _ = Describe("Scanner Native Resource Resolution", func() {
	const (
		teamName     = "native-resource-team"
		pipelineName = "native-resource-pipeline"
		resourceName = "native-image"
	)

	defaultResource := func() atc.ResourceConfig {
		return atc.ResourceConfig{
			Name: resourceName,
			Type: "registry-image",
			Source: atc.Source{
				"repository": "us-docker.pkg.dev/my-project/repo/app",
				"tag":        "latest",
			},
		}
	}

	persistResource := func(fixture *lidarDB, config atc.ResourceConfig) (db.Pipeline, db.Resource) {
		GinkgoHelper()
		_, pipeline := persistLidarPipeline(
			fixture, teamName, pipelineName,
			lidarConfigWithGets(atc.ResourceConfigs{config}, nil),
		)
		return pipeline, lidarPipelineResource(pipeline, config.Name)
	}

	runScanner := func(
		fixture *lidarDB,
		factory db.CheckFactory,
		resolver *imageresolvertesting.FakeResolver,
		resourceConfigFactory db.ResourceConfigFactory,
		logger *lagertest.TestLogger,
	) error {
		GinkgoHelper()
		return lidar.NewScanner(
			factory, atc.NewPlanFactory(0), 10, resolver, resourceConfigFactory, &dummy.Secrets{}, nil,
		).Run(lagerctx.NewContext(context.Background(), logger))
	}

	It("persists a native digest, exact resource config, scope, and check end time", func() {
		fixture := useLidarDB()
		config := defaultResource()
		pipeline, resource := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:nativeresource123", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, tag, auth := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("us-docker.pkg.dev/my-project/repo/app"))
		Expect(tag).To(Equal("latest"))
		Expect(auth).To(BeNil())

		freshResource := lidarPipelineResource(pipeline, resource.Name())
		Expect(freshResource.ID()).To(Equal(resource.ID()))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshResource.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		scope := resolvedLidarResourceScope(fixture, freshResource)
		Expect(scope.ResourceID()).To(BeNil())
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:nativeresource123"})
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeTrue())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("treats a scope deletion during native version save as a debug-level race", func() {
		fixture := useLidarDB()
		config := defaultResource()
		pipeline, _ := persistResource(fixture, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:ignored", nil)
		logger := lagertest.NewTestLogger("test")
		configFactory := lidarResourceConfigFactory{
			ResourceConfigFactory: fixture.ResourceConfigFactory,
			saveVersionsErr:       fkViolation("save versions"),
		}

		Expect(runScanner(fixture, fixture.CheckFactory, resolver, configFactory, logger)).To(Succeed())
		Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-during-version-save")).To(BeTrue())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-save-versions")).To(BeFalse())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(Equal(persistedConfig.ID()))
		scope := resolvedLidarResourceScope(fixture, freshResource)
		Expect(scope.ID()).To(Equal(baselineScope.ID()))
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("treats a scope deletion before native resource attachment as a debug-level race", func() {
		fixture := useLidarDB()
		config := defaultResource()
		pipeline, resource := persistResource(fixture, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		factory.FailResourceScope(resource.ID(), fkViolation("set resource scope"))
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:ignored", nil)
		logger := lagertest.NewTestLogger("test")

		Expect(runScanner(fixture, factory, resolver, fixture.ResourceConfigFactory, logger)).To(Succeed())
		Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-before-version-save")).To(BeTrue())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-set-resource-config-scope")).To(BeFalse())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
		_, found, err := baselineScope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("passes persisted native basic-auth credentials to the resolver", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.Source = atc.Source{
			"repository": "private-registry/app",
			"tag":        "v2",
			"username":   "myuser",
			"password":   "mypass",
		}
		pipeline, _ := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:private", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, tag, auth := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("private-registry/app"))
		Expect(tag).To(Equal("v2"))
		Expect(auth).NotTo(BeNil())
		Expect(auth.Username).To(Equal("myuser"))
		Expect(auth.Password).To(Equal("mypass"))
		scope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:private"})
	})

	It("checks a native resource whose ((var)) does not evaluate instead of resolving it", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.Source = atc.Source{
			"repository": "private-registry/app",
			"username":   "((registry-user))",
			"password":   "((registry-password))",
		}
		pipeline, resource := persistResource(fixture, config)
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:never", nil)

		Expect(runScanner(
			fixture, factory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(lidarPipelineResource(pipeline, resourceName).ResourceConfigScopeID()).To(BeZero())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(resource.ID()))
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.Source).To(Equal(config.Source))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("resolves a native resource over the base resource type defaults", func() {
		atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{
			"registry-image": {"username": "default-user", "password": "default-pass"},
		})
		DeferCleanup(atc.LoadBaseResourceTypeDefaults, map[string]atc.Source{})
		fixture := useLidarDB()
		config := defaultResource()
		config.Source = atc.Source{"repository": "private-registry/app", "username": "myuser"}
		pipeline, _ := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:defaults", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, _, _, auth := resolver.ResolveArgsForCall(0)
		Expect(auth).NotTo(BeNil())
		Expect(auth.Username).To(Equal("myuser"))
		Expect(auth.Password).To(Equal("default-pass"))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig("registry-image", atc.Source{
			"repository": "private-registry/app",
			"username":   "myuser",
			"password":   "default-pass",
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		expectLidarLatestVersion(resolvedLidarResourceScope(fixture, freshResource), atc.Version{"digest": "sha256:defaults"})
	})

	It("skips a persisted native resource with check_every never", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.CheckEvery = &atc.CheckEvery{Never: true}
		pipeline, _ := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("skips a persisted native resource whose nonzero interval has not elapsed", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, resource := persistResource(fixture, config)
		scope := attachLidarNativeResourceScope(fixture, resource)
		checkBuild, created, err := resource.CreateBuild(
			context.Background(), true,
			atc.Plan{ID: "previous-check", Check: &atc.CheckPlan{Name: resource.Name()}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		updated, err := scope.UpdateLastCheckStartTime(checkBuild.ID(), checkBuild.PublicPlan())
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(BeTrue())
		Expect(checkBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
		updated, err = scope.UpdateLastCheckEndTime(true)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(BeTrue())
		baselineLastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lidarPipelineResource(pipeline, resourceName).LastCheckEndTime()).To(Equal(baselineLastCheck.EndTime))
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		freshScope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		_, found, err := freshScope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := freshScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("does not resolve again before the interval elapses after a native resolve that made no check build", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, _ := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:nativeinterval", nil)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		freshResource := lidarPipelineResource(pipeline, resourceName)
		lastCheck, err := resolvedLidarResourceScope(fixture, freshResource).LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(freshResource.LastCheckEndTime()).To(Equal(lastCheck.EndTime))

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1), "second scanner pass within check_every resolved again")
	})

	It("keeps the check interval for a resource whose last check build errored before it reached a scope", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, resource := persistResource(fixture, config)

		// A manual check whose build errored before it reached a scope leaves
		// the resource's in-memory check build behind.
		_, created, err := fixture.CheckFactory.TryCreateCheck(
			context.Background(), resource, db.ResourceTypes{}, nil, true, false, false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(drainLidarCheckBuilds(fixture, 1)[0].Finish(db.BuildStatusErrored)).To(Succeed())

		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:afterthecheckbuild", nil)
		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		freshResource := lidarPipelineResource(pipeline, resourceName)
		lastCheck, err := resolvedLidarResourceScope(fixture, freshResource).LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(freshResource.LastCheckEndTime()).To(Equal(lastCheck.EndTime))

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1), "second scanner pass within check_every resolved again")
	})

	It("records a failed native resolve as a failed check end time on the attached scope", func() {
		fixture := useLidarDB()
		pipeline, _ := persistResource(fixture, defaultResource())
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("", errors.New("registry down"))

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		scope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		_, found, err := scope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeFalse())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("records no failed check when the scan is cancelled during a resolve", func() {
		fixture := useLidarDB()
		config := defaultResource()
		pipeline, _ := persistResource(fixture, config)
		persistedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			config.Type, config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		baselineScope, err := persistedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		baselineLastCheck, err := baselineScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		resolving := make(chan struct{})
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveStub(func(ctx context.Context, _, _ string, _ *imageresolver.BasicAuth) (string, error) {
			close(resolving)
			<-ctx.Done()
			return "", ctx.Err()
		})
		logger := lagertest.NewTestLogger("test")
		ctx, cancel := context.WithCancel(lagerctx.NewContext(context.Background(), logger))
		defer cancel()

		ran := make(chan error, 1)
		go func() {
			ran <- lidar.NewScanner(
				fixture.CheckFactory, atc.NewPlanFactory(0), 10, resolver, fixture.ResourceConfigFactory, &dummy.Secrets{}, nil,
			).Run(ctx)
		}()
		Eventually(resolving).WithTimeout(5 * time.Second).Should(BeClosed())
		cancel()
		Eventually(ran).WithTimeout(5 * time.Second).Should(Receive(BeNil()))

		Eventually(func() bool {
			return loggedAt(logger.Logs(), lager.DEBUG, "resolve-cancelled")
		}).WithTimeout(5 * time.Second).Should(BeTrue())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-resolve-digest")).To(BeFalse())
		scope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		Expect(scope.ID()).To(Equal(baselineScope.ID()))
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck).To(Equal(baselineLastCheck))
	})

	It("does not resolve or attach a persisted native source without a repository", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.Source = atc.Source{"tag": "latest"}
		pipeline, _ := persistResource(fixture, config)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
	})

	It("creates a real in-memory check for a persisted non-native resource", func() {
		fixture := useLidarDB()
		config := atc.ResourceConfig{
			Name: "ordinary-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"uri": "https://github.com/foo/bar"},
		}
		_, resource := persistResource(fixture, config)
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		resolver := new(imageresolvertesting.FakeResolver)

		Expect(runScanner(
			fixture, factory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(resource.ID()))
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.Resource).To(Equal(resource.Name()))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("persists a native digest and creates one ordinary check from a mixed pipeline", func() {
		fixture := useLidarDB()
		nativeConfig := defaultResource()
		nativeConfig.Source = atc.Source{"repository": "my-org/my-image"}
		ordinaryConfig := atc.ResourceConfig{
			Name: "ordinary-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"uri": "https://github.com/foo/bar"},
		}
		_, pipeline := persistLidarPipeline(
			fixture, teamName, pipelineName,
			lidarConfigWithGets(atc.ResourceConfigs{nativeConfig, ordinaryConfig}, nil),
		)
		nativeResource := lidarPipelineResource(pipeline, nativeConfig.Name)
		ordinaryResource := lidarPipelineResource(pipeline, ordinaryConfig.Name)
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:mixed123", nil)

		Expect(runScanner(
			fixture, factory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, _, _ := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("my-org/my-image"))
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(ordinaryResource.ID()))

		freshNative := lidarPipelineResource(pipeline, nativeResource.Name())
		expectedNativeConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", nativeConfig.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshNative.ResourceConfigID()).To(Equal(expectedNativeConfig.ID()))
		scope := resolvedLidarResourceScope(fixture, freshNative)
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:mixed123"})
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(ordinaryResource.ID()))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("increments ChecksEnqueued when the production factory creates a check", func() {
		fixture := useLidarDB()
		config := atc.ResourceConfig{
			Name: "metric-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"some": "source"},
		}
		_, resource := persistResource(fixture, config)
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		resolver := new(imageresolvertesting.FakeResolver)
		metric.Metrics.ChecksEnqueued.Delta()

		Expect(runScanner(
			fixture, factory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ID()).To(Equal(resource.ID()))
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 1))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("does not increment ChecksEnqueued for a production in-flight duplicate", func() {
		fixture := useLidarDB()
		config := atc.ResourceConfig{
			Name: "metric-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"some": "source"},
		}
		pipeline, resource := persistResource(fixture, config)
		scope := attachLidarResourceScope(fixture, resource)
		resource = lidarPipelineResource(pipeline, resource.Name())
		Expect(resource.ResourceConfigScopeID()).To(Equal(scope.ID()))

		_, created, err := fixture.CheckFactory.TryCreateCheck(
			lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("pre-create")),
			resource, nil, nil, false, false, false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		initialBuild := drainLidarCheckBuilds(fixture, 1)[0]
		finished := false
		DeferCleanup(func() {
			if !finished {
				Expect(initialBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			}
		})

		factory := observeLidarCheckFactory(fixture.CheckFactory)
		resolver := new(imageresolvertesting.FakeResolver)
		metric.Metrics.ChecksEnqueued.Delta()
		Expect(runScanner(
			fixture, factory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(factory.Calls()).To(HaveLen(1))
		Expect(factory.Calls()[0].resource().ResourceConfigScopeID()).To(Equal(scope.ID()))
		Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 0))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		Expect(initialBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
		finished = true
	})

	It("gives up on a registry that never answers within the resource's check_timeout", func() {
		fixture := useLidarDB()
		config := defaultResource()
		config.CheckTimeout = "1s"
		config.Source = stalledRegistrySource()
		pipeline, _ := persistResource(fixture, config)
		logger := lagertest.NewTestLogger("test")

		Eventually(runNativeScanner(fixture, logger)).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-resolve-digest")).To(BeTrue())
		expectFailedLidarResolve(resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName)))
	})

	It("gives up on a registry that never answers within the native resolve bound when the resource sets no check_timeout", func() {
		DeferCleanup(lidar.SetNativeResolveTimeout(time.Second))
		fixture := useLidarDB()
		config := defaultResource()
		config.Source = stalledRegistrySource()
		pipeline, _ := persistResource(fixture, config)
		logger := lagertest.NewTestLogger("test")

		Eventually(runNativeScanner(fixture, logger)).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-resolve-digest")).To(BeTrue())
		expectFailedLidarResolve(resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName)))
	})
})

// stalledRegistrySource starts a registry that accepts connections and never
// answers them, and returns a registry-image source that points at it. The
// listener and every connection it held close when the spec ends.
func stalledRegistrySource() atc.Source {
	GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	var held []net.Conn
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			held = append(held, conn)
		}
	}()
	DeferCleanup(func() {
		listener.Close()
		<-accepted
		for _, conn := range held {
			conn.Close()
		}
	})

	return atc.Source{
		"repository": listener.Addr().String() + "/stalled/image",
		"tag":        "latest",
		// Explicit credentials keep the keychain off the network.
		"username": "user",
		"password": "pass",
	}
}

// runNativeScanner runs one scan with the real registry resolver and delivers
// its result on the returned channel.
func runNativeScanner(fixture *lidarDB, logger *lagertest.TestLogger) <-chan error {
	ran := make(chan error, 1)
	go func() {
		ran <- lidar.NewScanner(
			fixture.CheckFactory, atc.NewPlanFactory(0), 10,
			imageresolver.NewResolver(nil), fixture.ResourceConfigFactory, &dummy.Secrets{}, nil,
		).Run(lagerctx.NewContext(context.Background(), logger))
	}()
	return ran
}

var _ = Describe("Scanner native resolution of a ((var)) source", func() {
	const (
		teamName     = "var-team"
		pipelineName = "var-pipeline"
	)

	varSource := atc.Source{
		"repository": "((repo))",
		"tag":        "latest",
		"username":   "((user))",
		"password":   "((secret))",
	}
	evaluatedSource := atc.Source{
		"repository": "registry.example.com/team/app",
		"tag":        "latest",
		"username":   "robot",
		"password":   "s3cret",
	}
	// The global credential manager, keyed under the team and pipeline the
	// check build would look them up under.
	globalSecrets := &dummy.Secrets{StaticVariables: vars.StaticVariables{
		teamName + "/" + pipelineName + "/repo": "registry.example.com/team/app",
		teamName + "/user":                      "robot",
		"secret":                                "s3cret",
	}}

	runScanner := func(fixture *lidarDB, resolver *imageresolvertesting.FakeResolver, pool creds.VarSourcePool, logger *lagertest.TestLogger) {
		GinkgoHelper()
		Expect(lidar.NewScanner(
			fixture.CheckFactory, atc.NewPlanFactory(0), 10, resolver, fixture.ResourceConfigFactory, globalSecrets, pool,
		).Run(lagerctx.NewContext(context.Background(), logger))).To(Succeed())
	}

	expectEvaluatedResolve := func(resolver *imageresolvertesting.FakeResolver) {
		GinkgoHelper()
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		_, repository, tag, auth := resolver.ResolveArgsForCall(0)
		Expect(repository).To(Equal("registry.example.com/team/app"))
		Expect(tag).To(Equal("latest"))
		Expect(auth).NotTo(BeNil())
		Expect(auth.Username).To(Equal("robot"))
		Expect(auth.Password).To(Equal("s3cret"))
	}

	It("resolves a resource with its source evaluated through the credential manager", func() {
		fixture := useLidarDB()
		config := atc.ResourceConfig{Name: "var-image", Type: "registry-image", Source: varSource}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName,
			lidarConfigWithGets(atc.ResourceConfigs{config}, nil))
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:evaluated", nil)

		runScanner(fixture, resolver, nil, lagertest.NewTestLogger("test"))
		expectEvaluatedResolve(resolver)

		// The resource config is the one a check step builds from the
		// evaluated source, not one keyed by the raw ((var)) text.
		resource := lidarPipelineResource(pipeline, config.Name)
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", evaluatedSource, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(resource.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		expectLidarLatestVersion(resolvedLidarResourceScope(fixture, resource), atc.Version{"digest": "sha256:evaluated"})
	})

	It("resolves a resource type with its source evaluated through the pipeline's var_source", func() {
		fixture := useLidarDB()
		pool := creds.NewVarSourcePool(lagertest.NewTestLogger("pool"), creds.CredentialManagementConfig{}, time.Minute, time.Second, clock.NewClock())
		DeferCleanup(pool.Close)
		config := atc.ResourceType{Name: "var-type", Type: "registry-image", Source: atc.Source{
			"repository": "((store:repo))",
			"tag":        "latest",
			"username":   "((store:user))",
			"password":   "((store:secret))",
		}}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, atc.Config{
			VarSources: atc.VarSourceConfigs{{Name: "store", Type: "dummy", Config: map[string]any{
				"vars": map[string]any{"repo": "registry.example.com/team/app", "user": "robot", "secret": "s3cret"},
			}}},
			ResourceTypes: atc.ResourceTypes{config},
		})
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:evaluated", nil)

		runScanner(fixture, resolver, pool, lagertest.NewTestLogger("test"))
		expectEvaluatedResolve(resolver)
		resourceType := lidarPipelineResourceType(pipeline, config.Name)
		expectLidarLatestVersion(resolvedLidarResourceTypeScope(fixture, resourceType), atc.Version{"digest": "sha256:evaluated"})
	})

	It("plans a check and get for a resolved resource type whose repository holds a ((var)), instead of an image ref with the placeholder", func() {
		fixture := useLidarDB()
		config := atc.ResourceType{Name: "var-type", Type: "registry-image", Source: varSource}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, atc.Config{
			ResourceTypes: atc.ResourceTypes{config},
		})
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:evaluated", nil)

		runScanner(fixture, resolver, nil, lagertest.NewTestLogger("test"))
		expectEvaluatedResolve(resolver)

		resourceTypes, err := pipeline.ResourceTypes()
		Expect(err).NotTo(HaveOccurred())
		deserialized := resourceTypes.Deserialize()
		Expect(deserialized).To(HaveLen(1))
		Expect(deserialized[0].Image).To(BeEmpty())
		image := deserialized.ImageForType("plan", config.Name, nil)
		Expect(image.ImageRef).To(BeEmpty())
		Expect(image.GetPlan).NotTo(BeNil())
		Expect(image.GetPlan.Get.Source).To(Equal(varSource))

		// The delegate evaluates the reference's source to the resource config
		// lidar resolved, and finds the digest there.
		evaluatedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", evaluatedSource, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		scope, err := evaluatedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		expectLidarLatestVersion(scope, atc.Version{"digest": "sha256:evaluated"})
	})

	It("checks a resource whose var is undefined instead of sending it to the registry", func() {
		fixture := useLidarDB()
		config := atc.ResourceConfig{Name: "var-image", Type: "registry-image", Source: atc.Source{
			"repository": "((undefined-repo))",
		}}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName,
			lidarConfigWithGets(atc.ResourceConfigs{config}, nil))
		resolver := new(imageresolvertesting.FakeResolver)
		logger := lagertest.NewTestLogger("test")

		runScanner(fixture, resolver, nil, logger)
		Expect(resolver.ResolveCallCount()).To(BeZero())
		Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-evaluate-source")).To(BeTrue())
		resource := lidarPipelineResource(pipeline, config.Name)
		Expect(resource.ResourceConfigScopeID()).To(BeZero())

		// The check build evaluates the source again and reports the failure.
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.Source).To(Equal(config.Source))
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
	})

	It("keeps the check interval for a resource checked once because its var was undefined, once the var evaluates", func() {
		fixture := useLidarDB()
		lateVariables := vars.StaticVariables{}
		secrets := &dummy.Secrets{StaticVariables: lateVariables}
		config := atc.ResourceConfig{
			Name:       "var-image",
			Type:       "registry-image",
			Source:     atc.Source{"repository": "((late-repo))"},
			CheckEvery: &atc.CheckEvery{Interval: time.Hour},
		}
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName,
			lidarConfigWithGets(atc.ResourceConfigs{config}, nil))
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("sha256:evaluated", nil)
		scan := func() {
			GinkgoHelper()
			Expect(lidar.NewScanner(
				fixture.CheckFactory, atc.NewPlanFactory(0), 10, resolver, fixture.ResourceConfigFactory, secrets, nil,
			).Run(lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("test")))).To(Succeed())
		}

		scan()
		Expect(resolver.ResolveCallCount()).To(BeZero())
		// The check build evaluates the source again and errors on the undefined var.
		Expect(drainLidarCheckBuilds(fixture, 1)[0].Finish(db.BuildStatusErrored)).To(Succeed())

		lateVariables[teamName+"/"+pipelineName+"/late-repo"] = "registry.example.com/team/app"
		scan()
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		resource := lidarPipelineResource(pipeline, config.Name)
		lastCheck, err := resolvedLidarResourceScope(fixture, resource).LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(resource.LastCheckEndTime()).To(Equal(lastCheck.EndTime))

		scan()
		Expect(resolver.ResolveCallCount()).To(Equal(1), "a scanner pass within check_every resolved again")
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})
})
