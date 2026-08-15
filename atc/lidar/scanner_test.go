package lidar_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/lidar"
	"github.com/concourse/concourse/atc/metric"
	"github.com/google/go-containerregistry/pkg/authn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type Scanner interface {
	Run(ctx context.Context) error
}

func newLidarRegistry() *imageresolvertesting.Registry {
	GinkgoHelper()
	registry := imageresolvertesting.NewRegistry()
	DeferCleanup(registry.Close)
	return registry
}

func pushLidarImage(registry *imageresolvertesting.Registry, repository, tag string) string {
	GinkgoHelper()
	digest, err := registry.Push(repository, tag)
	Expect(err).NotTo(HaveOccurred())
	return digest
}

func lidarHeadRequests(registry *imageresolvertesting.Registry) []imageresolvertesting.Request {
	GinkgoHelper()
	var heads []imageresolvertesting.Request
	for _, request := range registry.DrainRequests() {
		if request.Method == http.MethodHead {
			heads = append(heads, request)
		}
	}
	return heads
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
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(newScanner(fixture.CheckFactory, 10).Run(ctx)).To(Succeed())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("naturally excludes a persisted check_every never resource", func() {
		fixture := useLidarDB()
		_, pipeline := persistLidarPipeline(fixture, teamName, pipelineName, lidarConfigWithGets(
			atc.ResourceConfigs{{
				Name: "never-resource", Type: dbtest.BaseResourceType,
				Source:     atc.Source{"repository": "never"},
				CheckEvery: &atc.CheckEvery{Never: true},
			}}, nil,
		))
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		freshResource := lidarPipelineResource(pipeline, "never-resource")
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
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
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())

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
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())

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
		Expect(plan.Check.TypeImage.CheckPlan.Check.ResourceType).To(Equal("custom-type"))
		Expect(plan.Check.TypeImage.CheckPlan.Check.Source).To(Equal(atc.Source{"repository": "custom-image"}))
		Expect(plan.Check.TypeImage.CheckPlan.Check.Tags).To(Equal(atc.Tags{"type-tag"}))
		Expect(plan.Check.TypeImage.GetPlan).NotTo(BeNil())
		Expect(plan.Check.TypeImage.GetPlan.Get.Name).To(Equal("custom-type"))
		Expect(plan.Check.TypeImage.GetPlan.Get.Type).To(Equal(dbtest.BaseResourceType))
		Expect(plan.Check.TypeImage.GetPlan.Get.Source).To(Equal(atc.Source{"repository": "custom-image"}))
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
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.FromVersion).To(Equal(version))
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
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.FromVersion).To(BeNil())
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
		Expect(newScanner(fixture.CheckFactory, 10).Run(context.Background())).To(Succeed())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(input.ID()))
		Expect(build.ResourceID()).NotTo(Equal(putOnly.ID()))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
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
		Expect(newScanner(fixture.CheckFactory, 5).Run(context.Background())).To(Succeed())
		builds := drainLidarCheckBuilds(fixture, 20)
		seenIDs := make(map[int]struct{}, 20)
		seenNames := make(map[string]struct{}, 20)
		for _, build := range builds {
			seenIDs[build.ResourceID()] = struct{}{}
			seenNames[build.ResourceName()] = struct{}{}
			Expect(build.PrivatePlan().Check.Resource).To(Equal(build.ResourceName()))
		}
		Expect(seenIDs).To(HaveLen(20))
		Expect(seenNames).To(HaveLen(20))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
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

	defaultResourceType := func(repository string) atc.ResourceType {
		return atc.ResourceType{
			Name: resourceTypeName,
			Type: "registry-image",
			Source: atc.Source{
				"repository": repository,
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
		resolver imageresolver.Resolver,
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
		registry := newLidarRegistry()
		digest := pushLidarImage(registry, "my-image", "latest")
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/my-image")
		pipeline, resourceType := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method: http.MethodHead,
			Path:   "/v2/my-image/manifests/latest",
		}))

		freshType := lidarPipelineResourceType(pipeline, resourceType.Name())
		Expect(freshType.ID()).To(Equal(resourceType.ID()))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshType.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		scope := resolvedLidarResourceTypeScope(fixture, freshType)
		Expect(scope.ResourceID()).To(BeNil())
		expectLidarLatestVersion(scope, atc.Version{"digest": digest})
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeTrue())
		Expect(freshType.LastCheckEndTime()).To(Equal(lastCheck.EndTime))
		Expect(freshType.ResolvedImage()).To(Equal(registry.Host() + "/my-image@" + digest))
	})

	It("passes persisted basic-auth credentials to the resolver", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		digest := pushLidarImage(registry, "image", "v2")
		registry.RequireBasicAuth("user", "pass")
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/image")
		config.Source = atc.Source{
			"repository": registry.Host() + "/image",
			"tag":        "v2",
			"username":   "user",
			"password":   "pass",
		}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method:       http.MethodHead,
			Path:         "/v2/image/manifests/v2",
			HasBasicAuth: true,
		}))
		scope := resolvedLidarResourceTypeScope(fixture, lidarPipelineResourceType(pipeline, resourceTypeName))
		expectLidarLatestVersion(scope, atc.Version{"digest": digest})
	})

	It("skips a persisted direct image reference", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/my-image")
		config.Image = "direct-image:sha256"
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("skips a persisted check_every never resource type", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/my-image")
		config.CheckEvery = &atc.CheckEvery{Never: true}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("skips a persisted resource type whose nonzero interval has not elapsed", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/my-image")
		config.CheckEvery = &atc.CheckEvery{Interval: time.Hour}
		pipeline, resourceType := persistResourceType(fixture, teamName, pipelineName, config)
		scope := attachLidarResourceTypeScope(fixture, resourceType)
		updated, err := scope.UpdateLastCheckEndTime(true)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(BeTrue())
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
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
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/missing-image")
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method: http.MethodHead,
			Path:   "/v2/missing-image/manifests/latest",
		}))
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("does not call the resolver when persisted source has no repository", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResourceType(registry.Host() + "/my-image")
		config.Source = atc.Source{"tag": "latest"}
		pipeline, _ := persistResourceType(fixture, teamName, pipelineName, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		Expect(lidarPipelineResourceType(pipeline, resourceTypeName).ResourceConfigScopeID()).To(BeZero())
	})

	It("resolves persisted resource types across independent pipelines", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		firstDigest := pushLidarImage(registry, "my-image", "latest")
		secondDigest := pushLidarImage(registry, "other-image", "latest")
		Expect(firstDigest).NotTo(Equal(secondDigest))
		registry.DrainRequests()
		firstConfig := defaultResourceType(registry.Host() + "/my-image")
		firstPipeline, _ := persistResourceType(fixture, teamName, pipelineName, firstConfig)
		secondConfig := atc.ResourceType{
			Name: "other-type", Type: "registry-image",
			Source: atc.Source{"repository": registry.Host() + "/other-image"},
		}
		secondPipeline, _ := persistResourceType(
			fixture, "other-team", "other-pipeline", secondConfig,
		)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElements(
			imageresolvertesting.Request{
				Method: http.MethodHead,
				Path:   "/v2/my-image/manifests/latest",
			},
			imageresolvertesting.Request{
				Method: http.MethodHead,
				Path:   "/v2/other-image/manifests/latest",
			},
		))
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
		expectLidarLatestVersion(firstScope, atc.Version{"digest": firstDigest})
		expectLidarLatestVersion(secondScope, atc.Version{"digest": secondDigest})
	})
})

var _ = Describe("Scanner Native Resource Resolution", func() {
	const (
		teamName     = "native-resource-team"
		pipelineName = "native-resource-pipeline"
		resourceName = "native-image"
	)

	defaultResource := func(repository string) atc.ResourceConfig {
		return atc.ResourceConfig{
			Name: resourceName,
			Type: "registry-image",
			Source: atc.Source{
				"repository": repository,
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
		resolver imageresolver.Resolver,
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
		registry := newLidarRegistry()
		digest := pushLidarImage(registry, "my-project/repo/app", "latest")
		registry.DrainRequests()
		config := defaultResource(registry.Host() + "/my-project/repo/app")
		pipeline, resource := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method: http.MethodHead,
			Path:   "/v2/my-project/repo/app/manifests/latest",
		}))

		freshResource := lidarPipelineResource(pipeline, resource.Name())
		Expect(freshResource.ID()).To(Equal(resource.ID()))
		expectedConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", config.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshResource.ResourceConfigID()).To(Equal(expectedConfig.ID()))
		scope := resolvedLidarResourceScope(fixture, freshResource)
		Expect(scope.ResourceID()).To(BeNil())
		expectLidarLatestVersion(scope, atc.Version{"digest": digest})
		lastCheck, err := scope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).NotTo(BeZero())
		Expect(lastCheck.Succeeded).To(BeTrue())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("passes persisted native basic-auth credentials to the resolver", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		digest := pushLidarImage(registry, "app", "v2")
		registry.RequireBasicAuth("myuser", "mypass")
		registry.DrainRequests()
		config := defaultResource(registry.Host() + "/app")
		config.Source = atc.Source{
			"repository": registry.Host() + "/app",
			"tag":        "v2",
			"username":   "myuser",
			"password":   "mypass",
		}
		pipeline, _ := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method:       http.MethodHead,
			Path:         "/v2/app/manifests/v2",
			HasBasicAuth: true,
		}))
		scope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		expectLidarLatestVersion(scope, atc.Version{"digest": digest})
	})

	It("skips a persisted native resource with check_every never", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResource(registry.Host() + "/app")
		config.CheckEvery = &atc.CheckEvery{Never: true}
		pipeline, _ := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("skips a persisted native resource whose nonzero interval has not elapsed", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResource(registry.Host() + "/app")
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
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		freshScope := resolvedLidarResourceScope(fixture, lidarPipelineResource(pipeline, resourceName))
		_, found, err := freshScope.LatestVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		lastCheck, err := freshScope.LastCheck()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastCheck.EndTime).To(Equal(baselineLastCheck.EndTime))
	})

	It("does not persist native scope or version when the resolver fails", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		pipeline, _ := persistResource(fixture, defaultResource(registry.Host()+"/missing-app"))
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method: http.MethodHead,
			Path:   "/v2/missing-app/manifests/latest",
		}))
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
	})

	It("does not resolve or attach a persisted native source without a repository", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := defaultResource(registry.Host() + "/app")
		config.Source = atc.Source{"tag": "latest"}
		pipeline, _ := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		freshResource := lidarPipelineResource(pipeline, resourceName)
		Expect(freshResource.ResourceConfigID()).To(BeZero())
		Expect(freshResource.ResourceConfigScopeID()).To(BeZero())
	})

	It("creates a real in-memory check for a persisted non-native resource", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := atc.ResourceConfig{
			Name: "ordinary-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"uri": "https://github.com/foo/bar"},
		}
		_, resource := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(build.PrivatePlan().Check.Resource).To(Equal(resource.Name()))
	})

	It("persists a native digest and creates one ordinary check from a mixed pipeline", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		digest := pushLidarImage(registry, "my-org/my-image", "latest")
		registry.DrainRequests()
		nativeConfig := defaultResource(registry.Host() + "/my-org/my-image")
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
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
			Method: http.MethodHead,
			Path:   "/v2/my-org/my-image/manifests/latest",
		}))

		freshNative := lidarPipelineResource(pipeline, nativeResource.Name())
		expectedNativeConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"registry-image", nativeConfig.Source, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(freshNative.ResourceConfigID()).To(Equal(expectedNativeConfig.ID()))
		scope := resolvedLidarResourceScope(fixture, freshNative)
		expectLidarLatestVersion(scope, atc.Version{"digest": digest})
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(ordinaryResource.ID()))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
	})

	It("increments ChecksEnqueued when the production factory creates a check", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
		config := atc.ResourceConfig{
			Name: "metric-resource", Type: dbtest.BaseResourceType,
			Source: atc.Source{"some": "source"},
		}
		_, resource := persistResource(fixture, config)
		resolver := imageresolver.NewResolver(authn.DefaultKeychain)
		metric.Metrics.ChecksEnqueued.Delta()

		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		build := drainLidarCheckBuilds(fixture, 1)[0]
		Expect(build.ResourceID()).To(Equal(resource.ID()))
		Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 1))
	})

	It("does not increment ChecksEnqueued for a production in-flight duplicate", func() {
		fixture := useLidarDB()
		registry := newLidarRegistry()
		registry.DrainRequests()
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
		initialBuild := receiveLidarCheckBuild(fixture)
		finished := false
		DeferCleanup(func() {
			if !finished {
				Expect(initialBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			}
		})

		resolver := imageresolver.NewResolver(authn.DefaultKeychain)
		metric.Metrics.ChecksEnqueued.Delta()
		Expect(runScanner(
			fixture, fixture.CheckFactory, resolver, fixture.ResourceConfigFactory,
			lagertest.NewTestLogger("test"),
		)).To(Succeed())
		Expect(lidarHeadRequests(registry)).To(BeEmpty())
		Expect(initialBuild.ResourceID()).To(Equal(resource.ID()))
		Expect(initialBuild.PrivatePlan().Check.Resource).To(Equal(resource.Name()))
		Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 0))
		Consistently(fixture.CheckBuilds).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		Expect(initialBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
		finished = true
	})
})
