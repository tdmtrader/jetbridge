package lidar_test

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/lidar"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type Scanner interface {
	Run(ctx context.Context) error
}

var _ = Describe("Scanner", func() {
	var (
		err error

		fakeCheckFactory *dbfakes.FakeCheckFactory
		planFactory      atc.PlanFactory

		scanner Scanner

		ctx    context.Context
		cancel context.CancelFunc

		maxConcurrency = 10
	)

	BeforeEach(func() {
		planFactory = atc.NewPlanFactory(0)
		fakeCheckFactory = new(dbfakes.FakeCheckFactory)

		scanner = lidar.NewScanner(fakeCheckFactory, planFactory, maxConcurrency, nil, nil)
		ctx, cancel = context.WithCancel(context.Background())
	})

	JustBeforeEach(func() {
		err = scanner.Run(ctx)
	})

	Describe("Run", func() {
		Context("when fetching resources fails", func() {
			BeforeEach(func() {
				fakeCheckFactory.ResourcesReturns(nil, errors.New("nope"))
			})

			It("errors", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when context is cancelled", func() {
			BeforeEach(func() {
				cancel()
			})

			It("does not check any resources", func() {
				Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(0))
			})
		})

		Context("when fetching resources succeeds", func() {
			var fakeResource *dbfakes.FakeResource

			BeforeEach(func() {
				fakeResource = new(dbfakes.FakeResource)
				fakeResource.NameReturns("some-name")
				fakeResource.TagsReturns([]string{"tag-a", "tag-b"})
				fakeResource.SourceReturns(atc.Source{"some": "source"})

				fakeCheckFactory.ResourcesReturns([]db.Resource{fakeResource}, nil)
			})

			Context("when fetching resource types fails", func() {
				BeforeEach(func() {
					fakeCheckFactory.ResourceTypesByPipelineReturns(nil, errors.New("nope"))
				})

				It("errors", func() {
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when CheckEvery is never", func() {
				BeforeEach(func() {
					fakeResource.CheckEveryReturns(&atc.CheckEvery{Never: true})
					fakeResource.TypeReturns("parent")
					fakeResource.PipelineIDReturns(1)
					fakeResourceType := new(dbfakes.FakeResourceType)
					fakeResourceType.NameReturns("parent")
					fakeResourceType.PipelineIDReturns(1)
					fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{
						1: {fakeResourceType},
					}, nil)
				})

				It("does not check the resource", func() {
					Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(0))
				})
			})

			Context("when fetching resources types succeeds", func() {
				var fakeResourceType *dbfakes.FakeResourceType

				BeforeEach(func() {
					fakeResourceType = new(dbfakes.FakeResourceType)
					fakeResourceType.NameReturns("some-type")
					fakeResourceType.TypeReturns("some-base-type")
					fakeResourceType.TagsReturns([]string{"some-tag"})
					fakeResourceType.SourceReturns(atc.Source{"some": "type-source"})

					fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{1: {fakeResourceType}}, nil)
				})

				Context("when there are more resouces than maxConcurrency", func() {
					BeforeEach(func() {
						maxConcurrency = 5
						var resources []db.Resource
						for range 20 {
							rs := new(dbfakes.FakeResource)
							rs.NameReturns("some-name-")
							rs.SourceReturns(atc.Source{"some": "source"})
							resources = append(resources, rs)
						}
						fakeCheckFactory.ResourcesReturns(resources, nil)
					})

					It("successfully checks all resources", func() {
						Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(20))
					})
				})

				Context("when the resource parent type is a base type", func() {
					BeforeEach(func() {
						fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{}, nil)
						fakeResource.TypeReturns("some-type")
					})

					It("creates a check with empty resource types list", func() {
						_, _, resourceTypes, _, _, _, toDb := fakeCheckFactory.TryCreateCheckArgsForCall(0)
						var nilResourceTypes db.ResourceTypes
						Expect(resourceTypes).To(Equal(nilResourceTypes))
						Expect(toDb).To(BeFalse())
					})

					Context("when the last check end time is past our interval", func() {
						It("creates a check", func() {
							Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
						})

						Context("when try creating a check panics", func() {
							BeforeEach(func() {
								fakeCheckFactory.TryCreateCheckStub = func(context.Context, db.Checkable, db.ResourceTypes, atc.Version, bool, bool, bool) (db.Build, bool, error) {
									panic("something went wrong")
								}
							})

							It("recovers from the panic", func() {
								Expect(err).ToNot(HaveOccurred())
							})
						})
					})

					Context("when the checkable has a pinned version", func() {
						BeforeEach(func() {
							fakeResource.CurrentPinnedVersionReturns(atc.Version{"some": "version"})
						})

						It("creates a check with that pinned version", func() {
							Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
							_, _, _, fromVersion, manuallyTriggered, _, toDb := fakeCheckFactory.TryCreateCheckArgsForCall(0)
							Expect(fromVersion).To(Equal(atc.Version{"some": "version"}))
							Expect(manuallyTriggered).To(BeFalse())
							Expect(toDb).To(BeFalse())
						})
					})

					Context("when the checkable does not have a pinned version", func() {
						BeforeEach(func() {
							fakeResource.CurrentPinnedVersionReturns(nil)
						})

						It("creates a check with a nil pinned version", func() {
							Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1))
							_, _, _, fromVersion, _, _, toDb := fakeCheckFactory.TryCreateCheckArgsForCall(0)
							Expect(fromVersion).To(BeNil())
							Expect(toDb).To(BeFalse())
						})
					})
				})

				Context("when there's a put-only resource", func() {
					BeforeEach(func() {
						By("checkFactory.Resources should not return any put-only resources")
						fakeResourceType.NameReturns("put-only-custom-type")
						fakeResourceType.PipelineIDReturns(1)
					})

					It("does not check the put-only resource", func() {
						Expect(fakeCheckFactory.TryCreateCheckCallCount()).To(Equal(1),
							"one check created for the unrelated fakeResource")
					})
				})
			})
		})
	})
})

var _ = Describe("Scanner Resource Type Resolution", func() {
	var (
		err error

		fakeCheckFactory        *dbfakes.FakeCheckFactory
		fakeResourceConfigFactory *dbfakes.FakeResourceConfigFactory
		fakeResolver            *imageresolvertesting.FakeResolver
		planFactory             atc.PlanFactory

		scanner Scanner

		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		planFactory = atc.NewPlanFactory(0)
		fakeCheckFactory = new(dbfakes.FakeCheckFactory)
		fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
		fakeResolver = new(imageresolvertesting.FakeResolver)

		scanner = lidar.NewScanner(fakeCheckFactory, planFactory, 10, fakeResolver, fakeResourceConfigFactory)
		ctx, cancel = context.WithCancel(context.Background())

		fakeCheckFactory.ResourcesReturns(nil, nil)
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		err = scanner.Run(ctx)
	})

	Context("when there are resource types to resolve", func() {
		var (
			fakeResourceType       *dbfakes.FakeResourceType
			fakeResourceConfig     *dbfakes.FakeResourceConfig
			fakeResourceConfigScope *dbfakes.FakeResourceConfigScope
		)

		BeforeEach(func() {
			fakeResourceType = new(dbfakes.FakeResourceType)
			fakeResourceType.IDReturns(1)
			fakeResourceType.NameReturns("my-custom-type")
			fakeResourceType.TypeReturns("registry-image")
			fakeResourceType.TeamNameReturns("main")
			fakeResourceType.PipelineNameReturns("my-pipeline")
			fakeResourceType.PipelineIDReturns(1)
			fakeResourceType.SourceReturns(atc.Source{
				"repository": "my-registry/my-image",
				"tag":        "latest",
			})

			fakeResourceConfig = new(dbfakes.FakeResourceConfig)
			fakeResourceConfig.IDReturns(42)
			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)

			fakeResourceConfigScope = new(dbfakes.FakeResourceConfigScope)
			fakeResourceConfigScope.IDReturns(99)
			fakeResourceConfig.FindOrCreateScopeReturns(fakeResourceConfigScope, nil)

			fakeResolver.ResolveReturns("sha256:abc123", nil)

			fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{
				1: {fakeResourceType},
			}, nil)
		})

		It("resolves the digest and saves it as a version", func() {
			Expect(err).ToNot(HaveOccurred())

			// Verify resolver was called with correct args.
			Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
			_, repo, tag, auth := fakeResolver.ResolveArgsForCall(0)
			Expect(repo).To(Equal("my-registry/my-image"))
			Expect(tag).To(Equal("latest"))
			Expect(auth).To(BeNil())

			// Verify resource config was created.
			Expect(fakeResourceConfigFactory.FindOrCreateResourceConfigCallCount()).To(Equal(1))
			resourceType, source, cache := fakeResourceConfigFactory.FindOrCreateResourceConfigArgsForCall(0)
			Expect(resourceType).To(Equal("registry-image"))
			Expect(source).To(Equal(atc.Source{
				"repository": "my-registry/my-image",
				"tag":        "latest",
			}))
			Expect(cache).To(BeNil())

			// Verify scope was created and pointed to.
			Expect(fakeResourceConfig.FindOrCreateScopeCallCount()).To(Equal(1))
			Expect(fakeResourceType.SetResourceConfigScopeCallCount()).To(Equal(1))

			// Verify version was saved.
			Expect(fakeResourceConfigScope.SaveVersionsCallCount()).To(Equal(1))
			_, versions := fakeResourceConfigScope.SaveVersionsArgsForCall(0)
			Expect(versions).To(Equal([]atc.Version{{"digest": "sha256:abc123"}}))

			// Verify check end time was updated.
			Expect(fakeResourceConfigScope.UpdateLastCheckEndTimeCallCount()).To(Equal(1))
			succeeded := fakeResourceConfigScope.UpdateLastCheckEndTimeArgsForCall(0)
			Expect(succeeded).To(BeTrue())
		})

		Context("with basic auth credentials in source", func() {
			BeforeEach(func() {
				fakeResourceType.SourceReturns(atc.Source{
					"repository": "private-registry/image",
					"tag":        "v2",
					"username":   "user",
					"password":   "pass",
				})
			})

			It("passes credentials to the resolver", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, _, _, auth := fakeResolver.ResolveArgsForCall(0)
				Expect(auth).ToNot(BeNil())
				Expect(auth.Username).To(Equal("user"))
				Expect(auth.Password).To(Equal("pass"))
			})
		})

		Context("when the resource type has a direct image field", func() {
			BeforeEach(func() {
				fakeResourceType.ImageReturns("direct-image:sha256")
			})

			It("skips resolution", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})

		Context("when check_every is never", func() {
			BeforeEach(func() {
				fakeResourceType.CheckEveryReturns(&atc.CheckEvery{Never: true})
			})

			It("skips resolution", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})

		Context("when check interval has not elapsed", func() {
			BeforeEach(func() {
				atc.DefaultResourceTypeInterval = 1 * time.Hour
				fakeResourceType.LastCheckEndTimeReturns(time.Now())
			})

			AfterEach(func() {
				atc.DefaultResourceTypeInterval = 0
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
				fakeResourceType.SourceReturns(atc.Source{"tag": "latest"})
			})

			It("does not call the resolver", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))
			})
		})

		Context("when there are multiple resource types across pipelines", func() {
			BeforeEach(func() {
				fakeResourceType2 := new(dbfakes.FakeResourceType)
				fakeResourceType2.IDReturns(2)
				fakeResourceType2.NameReturns("other-type")
				fakeResourceType2.TypeReturns("registry-image")
				fakeResourceType2.TeamNameReturns("other-team")
				fakeResourceType2.PipelineNameReturns("other-pipeline")
				fakeResourceType2.PipelineIDReturns(2)
				fakeResourceType2.SourceReturns(atc.Source{
					"repository": "other-registry/other-image",
				})

				fakeCheckFactory.ResourceTypesByPipelineReturns(map[int]db.ResourceTypes{
					1: {fakeResourceType},
					2: {fakeResourceType2},
				}, nil)

				fakeResolver.ResolveReturns("sha256:def456", nil)
			})

			It("resolves all resource types", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(fakeResolver.ResolveCallCount()).To(Equal(2))
			})
		})
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

		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		planFactory = atc.NewPlanFactory(0)
		fakeCheckFactory = new(dbfakes.FakeCheckFactory)
		fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
		fakeResolver = new(imageresolvertesting.FakeResolver)

		scanner = lidar.NewScanner(fakeCheckFactory, planFactory, 10, fakeResolver, fakeResourceConfigFactory)
		ctx, cancel = context.WithCancel(context.Background())

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
})
