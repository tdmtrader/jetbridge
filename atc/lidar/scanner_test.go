package lidar_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/lidar"
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
		return lidar.NewScanner(factory, atc.NewPlanFactory(0), maxConcurrency, nil, nil)
	}

	It("does not schedule a check for an already-cancelled empty enumeration", func() {
		fixture := useLidarDB()
		factory := observeLidarCheckFactory(fixture.CheckFactory)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(newScanner(factory, 10).Run(ctx)).To(Succeed())
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
			factory, atc.NewPlanFactory(0), 10, resolver, resourceConfigFactory,
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
			factory, atc.NewPlanFactory(0), 10, resolver, resourceConfigFactory,
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
})
