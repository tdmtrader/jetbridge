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
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
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
		Expect(plan.Check.TypeImage.CheckPlan).NotTo(BeNil())
		Expect(plan.Check.TypeImage.CheckPlan.Check.Name).To(Equal("custom-type"))
		Expect(plan.Check.TypeImage.CheckPlan.Check.Source).To(Equal(atc.Source{"repository": "custom-image"}))
		Expect(plan.Check.TypeImage.CheckPlan.Check.Tags).To(Equal(atc.Tags{"type-tag"}))
		Expect(plan.Check.TypeImage.GetPlan).NotTo(BeNil())
		Expect(plan.Check.TypeImage.GetPlan.Get.Name).To(Equal("custom-type"))
		Expect(plan.Check.TypeImage.GetPlan.Get.Type).To(Equal(dbtest.BaseResourceType))
		Expect(plan.Check.TypeImage.GetPlan.Get.Source).To(Equal(atc.Source{"repository": "custom-image"}))
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

	It("does not persist a version when the resolver fails", func() {
		fixture := useLidarDB()
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, defaultResourceType())
		resolver := new(imageresolvertesting.FakeResolver)
		resolver.ResolveReturns("", errors.New("registry down"))

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(resolver.ResolveCallCount()).To(Equal(1))
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
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
	var (
		err error

		fakeCheckFactory          *dbfakes.FakeCheckFactory
		fakeResourceConfigFactory *dbfakes.FakeResourceConfigFactory
		fakeResolver              *imageresolvertesting.FakeResolver
		planFactory               atc.PlanFactory

		scanner Scanner

		logger *lagertest.TestLogger

		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		planFactory = atc.NewPlanFactory(0)
		fakeCheckFactory = new(dbfakes.FakeCheckFactory)
		fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
		fakeResolver = new(imageresolvertesting.FakeResolver)

		scanner = lidar.NewScanner(fakeCheckFactory, planFactory, 10, fakeResolver, fakeResourceConfigFactory)
		logger = lagertest.NewTestLogger("test")
		ctx, cancel = context.WithCancel(lagerctx.NewContext(context.Background(), logger))

		// No resource types in these tests.
		fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{}, nil)
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		err = scanner.Run(ctx)
	})

	Context("when a registry-image resource exists", func() {
		var (
			fakeResource            *dbfakes.FakeResource
			fakeResourceConfig      *dbfakes.FakeResourceConfig
			fakeResourceConfigScope *dbfakes.FakeResourceConfigScope
		)

		BeforeEach(func() {
			fakeResource = new(dbfakes.FakeResource)
			fakeResource.IDReturns(10)
			fakeResource.NameReturns("my-image")
			fakeResource.TypeReturns("registry-image")
			fakeResource.TeamNameReturns("main")
			fakeResource.PipelineNameReturns("my-pipeline")
			fakeResource.PipelineIDReturns(1)
			fakeResource.SourceReturns(atc.Source{
				"repository": "us-docker.pkg.dev/my-project/repo/app",
				"tag":        "latest",
			})

			fakeResourceConfig = new(dbfakes.FakeResourceConfig)
			fakeResourceConfig.IDReturns(42)
			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)

			fakeResourceConfigScope = new(dbfakes.FakeResourceConfigScope)
			fakeResourceConfigScope.IDReturns(99)
			fakeResourceConfig.FindOrCreateScopeReturns(fakeResourceConfigScope, nil)

			fakeResolver.ResolveReturns("sha256:nativeresource123", nil)

			fakeCheckFactory.ResourcesReturns([]db.Resource{fakeResource}, nil)
		})

		It("resolves the digest natively and does not create a check pod", func() {
			Expect(err).ToNot(HaveOccurred())

			// Verify resolver was called with correct args.
			Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
			_, repo, tag, auth := fakeResolver.ResolveArgsForCall(0)
			Expect(repo).To(Equal("us-docker.pkg.dev/my-project/repo/app"))
			Expect(tag).To(Equal("latest"))
			Expect(auth).To(BeNil())

			// Verify resource config was created.
			Expect(fakeResourceConfigFactory.FindOrCreateResourceConfigCallCount()).To(Equal(1))
			resourceType, source, cache := fakeResourceConfigFactory.FindOrCreateResourceConfigArgsForCall(0)
			Expect(resourceType).To(Equal("registry-image"))
			Expect(source).To(Equal(atc.Source{
				"repository": "us-docker.pkg.dev/my-project/repo/app",
				"tag":        "latest",
			}))
			Expect(cache).To(BeNil())

			// Verify scope was created and pointed to.
			Expect(fakeResourceConfig.FindOrCreateScopeCallCount()).To(Equal(1))
			Expect(fakeResource.SetResourceConfigScopeCallCount()).To(Equal(1))

			// Verify version was saved.
			Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(1))
			_, versions := fakeResourceConfigScope.SaveVersionsArgsForCall(0)
			Expect(versions).To(Equal([]atc.Version{{"digest": "sha256:nativeresource123"}}))

			// Verify check end time was updated.
			Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(1))

			// Verify no check pod was created.
			Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(0))
		})

		Context("when SaveVersions hits an FK violation (scope deleted by GC)", func() {
			BeforeEach(func() {
				fakeResourceConfigScope.SaveVersionsReturns(fkViolation("save versions"))
			})

			It("does not error the whole scan", func() {
				Expect(err).ToNot(HaveOccurred())
			})

			It("logs the race at debug, not error", func() {
				Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-during-version-save")).To(BeTrue())
				Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-save-versions")).To(BeFalse())
			})
		})

		Context("when SetResourceConfigScope hits an FK violation (scope deleted by GC)", func() {
			BeforeEach(func() {
				fakeResource.SetResourceConfigScopeReturns(fkViolation("set resource scope"))
			})

			It("does not error the whole scan", func() {
				Expect(err).ToNot(HaveOccurred())
			})

			It("logs the race at debug, not error", func() {
				Expect(loggedAt(logger.Logs(), lager.DEBUG, "scope-deleted-before-version-save")).To(BeTrue())
				Expect(loggedAt(logger.Logs(), lager.ERROR, "failed-to-set-resource-config-scope")).To(BeFalse())
			})

			It("does not attempt to save versions", func() {
				Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(0))
			})
		})

		Context("with basic auth credentials in source", func() {
			BeforeEach(func() {
				fakeResource.SourceReturns(atc.Source{
					"repository": "private-registry/app",
					"tag":        "v2",
					"username":   "myuser",
					"password":   "mypass",
				})
			})

			It("passes credentials to the resolver", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, _, _, auth := fakeResolver.ResolveArgsForCall(0)
				Expect(auth).ToNot(BeNil())
				Expect(auth.Username).To(Equal("myuser"))
				Expect(auth.Password).To(Equal("mypass"))
			})
		})

		Context("when check_every is never", func() {
			BeforeEach(func() {
				fakeResource.CheckEveryReturns(&atc.CheckEvery{Never: true})
			})

			It("skips native resolution and does not create a check pod", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
				Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(0))
			})
		})

		Context("when check interval has not elapsed", func() {
			BeforeEach(func() {
				fakeResource.CheckEveryReturns(&atc.CheckEvery{Interval: 1 * time.Hour})
				fakeResource.LastCheckEndTimeReturns(time.Now())
			})

			It("skips resolution", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})

		Context("when the resolver fails", func() {
			BeforeEach(func() {
				fakeResolver.ResolveReturns("", errors.New("registry down"))
			})

			It("does not error the whole scan", func() {
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not save any versions", func() {
				Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(0))
			})
		})

		Context("when source has no repository", func() {
			BeforeEach(func() {
				fakeResource.SourceReturns(atc.Source{"tag": "latest"})
			})

			It("does not call the resolver", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})
	})

	Context("when a non-registry-image resource exists", func() {
		var fakeResource *dbfakes.FakeResource

		BeforeEach(func() {
			fakeResource = new(dbfakes.FakeResource)
			fakeResource.IDReturns(20)
			fakeResource.NameReturns("my-repo")
			fakeResource.TypeReturns("git")
			fakeResource.PipelineIDReturns(1)
			fakeResource.SourceReturns(atc.Source{"uri": "https://github.com/foo/bar"})

			fakeCheckFactory.ResourcesReturns([]db.Resource{fakeResource}, nil)
		})

		It("falls through to the normal check path", func() {
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
		})
	})

	Context("when there is a mix of registry-image and other resources", func() {
		BeforeEach(func() {
			fakeRegistryResource := new(dbfakes.FakeResource)
			fakeRegistryResource.IDReturns(10)
			fakeRegistryResource.NameReturns("my-image")
			fakeRegistryResource.TypeReturns("registry-image")
			fakeRegistryResource.TeamNameReturns("main")
			fakeRegistryResource.PipelineNameReturns("my-pipeline")
			fakeRegistryResource.PipelineIDReturns(1)
			fakeRegistryResource.SourceReturns(atc.Source{
				"repository": "my-org/my-image",
			})

			fakeGitResource := new(dbfakes.FakeResource)
			fakeGitResource.IDReturns(20)
			fakeGitResource.NameReturns("my-repo")
			fakeGitResource.TypeReturns("git")
			fakeGitResource.PipelineIDReturns(1)
			fakeGitResource.SourceReturns(atc.Source{"uri": "https://github.com/foo/bar"})

			fakeResourceConfig := new(dbfakes.FakeResourceConfig)
			fakeResourceConfig.IDReturns(42)
			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)

			fakeResourceConfigScope := new(dbfakes.FakeResourceConfigScope)
			fakeResourceConfigScope.IDReturns(99)
			fakeResourceConfig.FindOrCreateScopeReturns(fakeResourceConfigScope, nil)

			fakeResolver.ResolveReturns("sha256:mixed123", nil)

			fakeCheckFactory.ResourcesReturns([]db.Resource{fakeRegistryResource, fakeGitResource}, nil)
		})

		It("resolves registry-image natively and checks git normally", func() {
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
			Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
		})
	})

	// MO-01: ChecksEnqueued metric incremented on check creation
	Describe("ChecksEnqueued metric", func() {
		var fakeResource *dbfakes.FakeResource

		BeforeEach(func() {
			fakeResource = new(dbfakes.FakeResource)
			fakeResource.NameReturns("metric-resource")
			fakeResource.SourceReturns(atc.Source{"some": "source"})
			fakeResource.TypeReturns("some-type")

			fakeCheckFactory.ResourcesReturns([]db.Resource{fakeResource}, nil)
			fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{}, nil)

			// Drain any leftover metric state
			metric.Metrics.ChecksEnqueued.Delta()
		})

		Context("when a check is created", func() {
			BeforeEach(func() {
				fakeBuild := new(dbfakes.FakeBuild)
				fakeCheckFactory.TryCreateCheckReturns(fakeBuild, true, nil)
			})

			It("increments ChecksEnqueued", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 1))
			})
		})

		Context("when a check already exists (not created)", func() {
			BeforeEach(func() {
				fakeCheckFactory.TryCreateCheckReturns(nil, false, nil)
			})

			It("does not increment ChecksEnqueued", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 0))
			})
		})
	})
})
