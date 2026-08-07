package lidar_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds/credsfakes"
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

type lidarCheckCall struct {
	checkable               db.Checkable
	resourceTypes           db.ResourceTypes
	from                    atc.Version
	manuallyTriggered       bool
	skipIntervalRecursively bool
	toDB                    bool
}

func (call lidarCheckCall) resource() db.Resource {
	GinkgoHelper()
	resource, ok := call.checkable.(db.Resource)
	Expect(ok).To(BeTrue(), "expected a resource check, got %T", call.checkable)
	return resource
}

// lidarCheckFactory observes calls while keeping the production CheckFactory
// on every successful path. Its overrides are limited to fetch failures,
// panic recovery, and post-fetch wrappers for the two GC-race boundaries.
type lidarCheckFactory struct {
	db.CheckFactory

	mu                    sync.Mutex
	calls                 []lidarCheckCall
	resourcesErr          error
	resourceTypesErr      error
	panicTryCreate        bool
	resourceScopeErrs     map[int]error
	resourceTypeScopeErrs map[int]error
}

func observeLidarCheckFactory(factory db.CheckFactory) *lidarCheckFactory {
	return &lidarCheckFactory{
		CheckFactory:          factory,
		resourceScopeErrs:     make(map[int]error),
		resourceTypeScopeErrs: make(map[int]error),
	}
}

func (factory *lidarCheckFactory) TryCreateCheck(
	ctx context.Context,
	checkable db.Checkable,
	resourceTypes db.ResourceTypes,
	from atc.Version,
	manuallyTriggered bool,
	skipIntervalRecursively bool,
	toDB bool,
) (db.Build, bool, error) {
	factory.mu.Lock()
	factory.calls = append(factory.calls, lidarCheckCall{
		checkable:               checkable,
		resourceTypes:           resourceTypes,
		from:                    from,
		manuallyTriggered:       manuallyTriggered,
		skipIntervalRecursively: skipIntervalRecursively,
		toDB:                    toDB,
	})
	panicTryCreate := factory.panicTryCreate
	factory.mu.Unlock()

	if panicTryCreate {
		panic("configured TryCreateCheck panic")
	}
	return factory.CheckFactory.TryCreateCheck(
		ctx, checkable, resourceTypes, from,
		manuallyTriggered, skipIntervalRecursively, toDB,
	)
}

func (factory *lidarCheckFactory) Resources() ([]db.Resource, error) {
	factory.mu.Lock()
	configuredErr := factory.resourcesErr
	factory.mu.Unlock()
	if configuredErr != nil {
		return nil, configuredErr
	}

	resources, err := factory.CheckFactory.Resources()
	if err != nil {
		return nil, err
	}
	for i, resource := range resources {
		factory.mu.Lock()
		scopeErr := factory.resourceScopeErrs[resource.ID()]
		factory.mu.Unlock()
		if scopeErr != nil {
			resources[i] = lidarResourceWithScopeError{Resource: resource, err: scopeErr}
		}
	}
	return resources, nil
}

func (factory *lidarCheckFactory) ResourceTypesByPipeline() (map[int]db.ResourceTypes, error) {
	factory.mu.Lock()
	configuredErr := factory.resourceTypesErr
	factory.mu.Unlock()
	if configuredErr != nil {
		return nil, configuredErr
	}

	byPipeline, err := factory.CheckFactory.ResourceTypesByPipeline()
	if err != nil {
		return nil, err
	}
	for pipelineID, resourceTypes := range byPipeline {
		wrapped := append(db.ResourceTypes(nil), resourceTypes...)
		for i, resourceType := range wrapped {
			factory.mu.Lock()
			scopeErr := factory.resourceTypeScopeErrs[resourceType.ID()]
			factory.mu.Unlock()
			if scopeErr != nil {
				wrapped[i] = lidarResourceTypeWithScopeError{ResourceType: resourceType, err: scopeErr}
			}
		}
		byPipeline[pipelineID] = wrapped
	}
	return byPipeline, nil
}

func (factory *lidarCheckFactory) Calls() []lidarCheckCall {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]lidarCheckCall(nil), factory.calls...)
}

func (factory *lidarCheckFactory) FailResources(err error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.resourcesErr = err
}

func (factory *lidarCheckFactory) FailResourceTypes(err error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.resourceTypesErr = err
}

func (factory *lidarCheckFactory) PanicOnTryCreate() {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.panicTryCreate = true
}

func (factory *lidarCheckFactory) FailResourceScope(resourceID int, err error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.resourceScopeErrs[resourceID] = err
}

func (factory *lidarCheckFactory) FailResourceTypeScope(resourceTypeID int, err error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.resourceTypeScopeErrs[resourceTypeID] = err
}

type lidarResourceWithScopeError struct {
	db.Resource
	err error
}

func (resource lidarResourceWithScopeError) SetResourceConfigScope(db.ResourceConfigScope) error {
	return resource.err
}

type lidarResourceTypeWithScopeError struct {
	db.ResourceType
	err error
}

func (resourceType lidarResourceTypeWithScopeError) SetResourceConfigScope(db.ResourceConfigScope) error {
	return resourceType.err
}

// lidarResourceConfigFactory delegates real config/scope lookup and can only
// replace SaveVersions with the post-lookup failure under test.
type lidarResourceConfigFactory struct {
	db.ResourceConfigFactory
	saveVersionsErr error
}

func (factory lidarResourceConfigFactory) FindOrCreateResourceConfig(
	resourceType string,
	source atc.Source,
	customTypeResourceCache db.ResourceCache,
) (db.ResourceConfig, error) {
	config, err := factory.ResourceConfigFactory.FindOrCreateResourceConfig(
		resourceType, source, customTypeResourceCache,
	)
	if err != nil {
		return nil, err
	}
	return lidarResourceConfig{ResourceConfig: config, saveVersionsErr: factory.saveVersionsErr}, nil
}

type lidarResourceConfig struct {
	db.ResourceConfig
	saveVersionsErr error
}

func (config lidarResourceConfig) FindOrCreateScope(resourceID *int) (db.ResourceConfigScope, error) {
	scope, err := config.ResourceConfig.FindOrCreateScope(resourceID)
	if err != nil {
		return nil, err
	}
	return lidarResourceConfigScope{ResourceConfigScope: scope, saveVersionsErr: config.saveVersionsErr}, nil
}

type lidarResourceConfigScope struct {
	db.ResourceConfigScope
	saveVersionsErr error
}

func (scope lidarResourceConfigScope) SaveVersions(span db.SpanContext, versions []atc.Version) error {
	if scope.saveVersionsErr != nil {
		return scope.saveVersionsErr
	}
	return scope.ResourceConfigScope.SaveVersions(span, versions)
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
		new(credsfakes.FakeSecrets),
		new(credsfakes.FakeVarSourcePool),
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

func drainLidarCheckBuilds(fixture *lidarDB, count int) []db.Build {
	GinkgoHelper()
	builds := make([]db.Build, 0, count)
	for range count {
		var build db.Build
		Eventually(fixture.CheckBuilds).WithTimeout(5 * time.Second).Should(Receive(&build))
		builds = append(builds, build)
	}
	return builds
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
