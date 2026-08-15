package lidar_test

import (
	"database/sql"
	"errors"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func init() {
	util.PanicSink = GinkgoWriter
}

func TestLidar(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Lidar Suite")
}

var lidarPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&lidarPostgresRunner)

type lidarDB struct {
	Conn                  db.DbConn
	LockFactory           lock.LockFactory
	Builder               dbtest.Builder
	TeamFactory           db.TeamFactory
	BuildFactory          db.BuildFactory
	ResourceConfigFactory db.ResourceConfigFactory
	CheckFactory          db.CheckFactory
	CheckBuilds           chan db.Build
}

func useLidarDB() *lidarDB {
	GinkgoHelper()

	lidarPostgresRunner.CreateTestDBFromTemplate()
	// DeferCleanup is LIFO: register drop first so every ordinary and advisory
	// lock connection closes before the unique clone is removed.
	DeferCleanup(lidarPostgresRunner.DropTestDB)

	conn := lidarPostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	lockFactory, closeLocks := openLidarLockFactory()
	DeferCleanup(func() { Expect(closeLocks()).To(Succeed()) })

	checkBuilds := make(chan db.Build, 64)
	checkFactory := db.NewCheckFactory(
		conn,
		lockFactory,
		checkBuilds,
		util.NewSequenceGenerator(1),
	)

	return &lidarDB{
		Conn:                  conn,
		LockFactory:           lockFactory,
		Builder:               dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:           db.NewTeamFactory(conn, lockFactory),
		BuildFactory:          db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
		ResourceConfigFactory: db.NewResourceConfigFactory(conn, lockFactory),
		CheckFactory:          checkFactory,
		CheckBuilds:           checkBuilds,
	}
}

func openLidarLockFactory() (lock.LockFactory, func() error) {
	GinkgoHelper()

	var conns [lock.FactoryCount]*sql.DB
	for i := range conns {
		conns[i] = lidarPostgresRunner.OpenSingleton()
	}

	closeLocks := func() error {
		var closeErrs []error
		for _, conn := range conns {
			if err := conn.Close(); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		return errors.Join(closeErrs...)
	}
	ignore := func(lager.Logger, lock.LockID) {}
	return lock.NewLockFactory(conns, ignore, ignore), closeLocks
}

func persistLidarPipeline(
	fixture *lidarDB,
	teamName string,
	pipelineName string,
	config atc.Config,
) (db.Team, db.Pipeline) {
	GinkgoHelper()

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName}, config, db.ConfigVersion(0), false,
	)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline
}

func updateLidarPipeline(team db.Team, pipeline db.Pipeline, config atc.Config) db.Pipeline {
	GinkgoHelper()

	updated, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipeline.Name(), InstanceVars: pipeline.InstanceVars()},
		config,
		pipeline.ConfigVersion(),
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	return updated
}

func lidarPipelineResource(pipeline db.Pipeline, name string) db.Resource {
	GinkgoHelper()
	resource, found, err := pipeline.Resource(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "resource %q not found", name)
	return resource
}

func lidarPipelineResourceType(pipeline db.Pipeline, name string) db.ResourceType {
	GinkgoHelper()
	resourceType, found, err := pipeline.ResourceType(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue(), "resource type %q not found", name)
	return resourceType
}

func attachLidarResourceScope(fixture *lidarDB, resource db.Resource) db.ResourceConfigScope {
	GinkgoHelper()
	config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
		resource.Type(), resource.Source(), nil,
	)
	Expect(err).NotTo(HaveOccurred())
	resourceID := resource.ID()
	scope, err := config.FindOrCreateScope(&resourceID)
	Expect(err).NotTo(HaveOccurred())
	Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
	return scope
}

func attachLidarNativeResourceScope(fixture *lidarDB, resource db.Resource) db.ResourceConfigScope {
	GinkgoHelper()
	config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
		resource.Type(), resource.Source(), nil,
	)
	Expect(err).NotTo(HaveOccurred())
	scope, err := config.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(resource.SetResourceConfigScope(scope)).To(Succeed())
	return scope
}

func attachLidarResourceTypeScope(fixture *lidarDB, resourceType db.ResourceType) db.ResourceConfigScope {
	GinkgoHelper()
	config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
		resourceType.Type(), resourceType.Source(), nil,
	)
	Expect(err).NotTo(HaveOccurred())
	scope, err := config.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(resourceType.SetResourceConfigScope(scope)).To(Succeed())
	return scope
}

func reloadLidarScope(fixture *lidarDB, scope db.ResourceConfigScope) db.ResourceConfigScope {
	GinkgoHelper()
	config, found, err := fixture.ResourceConfigFactory.FindResourceConfigByID(scope.ResourceConfig().ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	freshScope, err := config.FindOrCreateScope(scope.ResourceID())
	Expect(err).NotTo(HaveOccurred())
	Expect(freshScope.ID()).To(Equal(scope.ID()))
	return freshScope
}

func resolvedLidarResourceTypeScope(fixture *lidarDB, resourceType db.ResourceType) db.ResourceConfigScope {
	GinkgoHelper()
	Expect(resourceType.ResourceConfigID()).NotTo(BeZero())
	Expect(resourceType.ResourceConfigScopeID()).NotTo(BeZero())
	config, found, err := fixture.ResourceConfigFactory.FindResourceConfigByID(resourceType.ResourceConfigID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(config.CreatedByResourceCache()).To(BeNil())
	Expect(config.CreatedByBaseResourceType()).NotTo(BeNil())
	Expect(config.CreatedByBaseResourceType().Name).To(Equal("registry-image"))
	scope, err := config.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(scope.ID()).To(Equal(resourceType.ResourceConfigScopeID()))
	return scope
}

func resolvedLidarResourceScope(fixture *lidarDB, resource db.Resource) db.ResourceConfigScope {
	GinkgoHelper()
	Expect(resource.ResourceConfigID()).NotTo(BeZero())
	Expect(resource.ResourceConfigScopeID()).NotTo(BeZero())
	config, found, err := fixture.ResourceConfigFactory.FindResourceConfigByID(resource.ResourceConfigID())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(config.CreatedByResourceCache()).To(BeNil())
	Expect(config.CreatedByBaseResourceType()).NotTo(BeNil())
	Expect(config.CreatedByBaseResourceType().Name).To(Equal("registry-image"))
	scope, err := config.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	Expect(scope.ID()).To(Equal(resource.ResourceConfigScopeID()))
	return scope
}

func expectLidarLatestVersion(scope db.ResourceConfigScope, expected atc.Version) {
	GinkgoHelper()
	latest, found, err := scope.LatestVersion()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(atc.Version(latest.Version())).To(Equal(expected))
}

func drainLidarCheckBuilds(fixture *lidarDB, count int) []db.Build {
	GinkgoHelper()
	builds := make([]db.Build, 0, count)
	for range count {
		build := receiveLidarCheckBuild(fixture)
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		builds = append(builds, build)
	}
	return builds
}

func receiveLidarCheckBuild(fixture *lidarDB) db.Build {
	GinkgoHelper()
	var build db.Build
	Eventually(fixture.CheckBuilds).WithTimeout(5 * time.Second).Should(Receive(&build))
	return build
}

func lidarConfigWithGets(resources atc.ResourceConfigs, resourceTypes atc.ResourceTypes) atc.Config {
	steps := make([]atc.Step, 0, len(resources))
	for _, resource := range resources {
		steps = append(steps, atc.Step{Config: &atc.GetStep{Name: resource.Name}})
	}
	return atc.Config{
		Resources:     resources,
		ResourceTypes: resourceTypes,
		Jobs: atc.JobConfigs{{
			Name:         "scan-job",
			PlanSequence: steps,
		}},
	}
}
