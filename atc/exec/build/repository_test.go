package build_test

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/compression"
	. "github.com/concourse/concourse/atc/exec/build"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type Artifact string

func (a Artifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	panic("unimplemented")
}

func (a Artifact) Handle() string {
	panic("unimplemented")
}

func (a Artifact) Source() string {
	panic("unimplemented")
}

func snapshotRef(id int64) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID:     snapshot.SnapshotID(id),
		Type:   snapshot.TypeRef("repository/v1"),
		Digest: snapshot.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}
}

var _ = Describe("ArtifactRepository", func() {
	var (
		repo *Repository
	)

	BeforeEach(func() {
		repo = NewRepository()
	})

	It("initially does not contain any artifacts", func() {
		_, _, found := repo.ArtifactFor("first-artifact")
		Expect(found).To(BeFalse())
	})

	Context("when a artifact is registered", func() {
		BeforeEach(func() {
			repo.RegisterArtifact("first-artifact", Artifact("first"), false)
		})

		Describe("ArtifactFor", func() {
			It("yields the artifact by the given name", func() {
				artifact, fromCache, found := repo.ArtifactFor("first-artifact")
				Expect(found).To(BeTrue())
				Expect(fromCache).To(BeFalse())
				Expect(artifact).To(Equal(Artifact("first")))
			})

			It("yields nothing for unregistered names", func() {
				_, _, found := repo.ArtifactFor("bogus-artifact")
				Expect(found).To(BeFalse())
			})
		})

		Describe("NewLocalScope", func() {
			var child *Repository

			BeforeEach(func() {
				child = repo.NewLocalScope()
			})

			It("contains the same artifacts as the parent", func() {
				Expect(child.AsMap()).To(Equal(repo.AsMap()))
			})

			It("maintains a reference to the parent", func() {
				Expect(child.Parent()).To(Equal(repo))
			})

			Context("when an artifact is registered", func() {
				BeforeEach(func() {
					child.RegisterArtifact("second-artifact", Artifact("second"), false)
				})

				It("is present in the child but not the parent", func() {
					Expect(child.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
						"first-artifact": {
							Artifact:  Artifact("first"),
							FromCache: false,
						},
						"second-artifact": {
							Artifact:  Artifact("second"),
							FromCache: false,
						},
					}))

					Expect(repo.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
						"first-artifact": {
							Artifact:  Artifact("first"),
							FromCache: false,
						},
					}))
				})
			})

			Context("when an artifact is overridden", func() {
				BeforeEach(func() {
					child.RegisterArtifact("first-artifact", Artifact("modified-first"), false)
				})

				It("is overridden in the child but not the parent", func() {
					Expect(child.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
						"first-artifact": {
							Artifact:  Artifact("modified-first"),
							FromCache: false,
						},
					}))

					Expect(repo.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
						"first-artifact": {
							Artifact:  Artifact("first"),
							FromCache: false,
						},
					}))
				})
			})

			Context("with multiple levels of nesting", func() {
				var grandchild *Repository

				BeforeEach(func() {
					child.RegisterArtifact("child-artifact", Artifact("child"), false)
					grandchild = child.NewLocalScope()
					grandchild.RegisterArtifact("grandchild-artifact", Artifact("grandchild"), false)
				})

				It("correctly merges all ancestors in AsMap", func() {
					grandchildMap := grandchild.AsMap()

					// Check the length first
					Expect(len(grandchildMap)).To(Equal(3), "Grandchild map should have 3 entries")

					// Access values directly to verify they exist
					firstValue, firstExists := grandchildMap["first-artifact"]
					Expect(firstExists).To(BeTrue(), "should contain first-artifact")
					Expect(firstValue.Artifact).To(Equal(Artifact("first")))

					childValue, childExists := grandchildMap["child-artifact"]
					Expect(childExists).To(BeTrue(), "should contain child-artifact")
					Expect(childValue.Artifact).To(Equal(Artifact("child")))

					grandchildValue, grandchildExists := grandchildMap["grandchild-artifact"]
					Expect(grandchildExists).To(BeTrue(), "should contain grandchild-artifact")
					Expect(grandchildValue.Artifact).To(Equal(Artifact("grandchild")))
				})
			})
		})

		Describe("RegisterImageRef / ImageRefFor", func() {
			It("stores and retrieves an image ref by name", func() {
				repo.RegisterImageRef("my-image", "docker:///myrepo/myimage@sha256:abc123")
				imageRef, found := repo.ImageRefFor("my-image")
				Expect(found).To(BeTrue())
				Expect(imageRef).To(Equal("docker:///myrepo/myimage@sha256:abc123"))
			})

			It("returns not found for unregistered names", func() {
				_, found := repo.ImageRefFor("no-such-image")
				Expect(found).To(BeFalse())
			})

			Context("with local scopes", func() {
				It("looks up parent image refs from child scope", func() {
					repo.RegisterImageRef("parent-image", "docker:///parent@sha256:111")
					child := repo.NewLocalScope()
					imageRef, found := child.ImageRefFor("parent-image")
					Expect(found).To(BeTrue())
					Expect(imageRef).To(Equal("docker:///parent@sha256:111"))
				})

				It("allows child to override parent image ref", func() {
					repo.RegisterImageRef("img", "docker:///old@sha256:000")
					child := repo.NewLocalScope()
					child.RegisterImageRef("img", "docker:///new@sha256:999")
					imageRef, found := child.ImageRefFor("img")
					Expect(found).To(BeTrue())
					Expect(imageRef).To(Equal("docker:///new@sha256:999"))
				})
			})
		})

		Context("when a second artifact is registered", func() {
			BeforeEach(func() {
				repo.RegisterArtifact("second-artifact", Artifact("second"), false)
			})

			Describe("AsMap", func() {
				It("returns all artifacts", func() {
					Expect(repo.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
						"first-artifact": {
							Artifact:  Artifact("first"),
							FromCache: false,
						},
						"second-artifact": {
							Artifact:  Artifact("second"),
							FromCache: false,
						},
					}))
				})
			})

			Describe("ArtifactFor", func() {
				It("yields the first artifact by the given name", func() {
					actualArtifact, fromCache, found := repo.ArtifactFor("first-artifact")
					Expect(found).To(BeTrue())
					Expect(fromCache).To(BeFalse())
					Expect(actualArtifact).To(Equal(Artifact("first")))
				})

				It("yields the second artifact by the given name", func() {
					actualArtifact, fromCache, found := repo.ArtifactFor("second-artifact")
					Expect(found).To(BeTrue())
					Expect(fromCache).To(BeFalse())
					Expect(actualArtifact).To(Equal(Artifact("second")))
				})

				It("yields nothing for unregistered names", func() {
					_, _, found := repo.ArtifactFor("bogus-artifact")
					Expect(found).To(BeFalse())
				})
			})
		})
	})

	Describe("atomic artifact entries", func() {
		It("publishes a checked batch all at once", func() {
			firstRef := snapshotRef(1)
			secondRef := snapshotRef(2)

			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"first":  {Artifact: Artifact("first"), Snapshot: &firstRef},
				"second": {Artifact: Artifact("second"), FromCache: true, Snapshot: &secondRef},
			})).To(Succeed())

			Expect(repo.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
				"first":  {Artifact: Artifact("first"), Snapshot: &firstRef},
				"second": {Artifact: Artifact("second"), FromCache: true, Snapshot: &secondRef},
			}))
		})

		It("rejects an invalid snapshot reference without publishing any entry", func() {
			valid := snapshotRef(1)
			invalid := snapshotRef(2)
			invalid.Digest = "not-a-digest"

			err := repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"valid":   {Artifact: Artifact("valid"), Snapshot: &valid},
				"invalid": {Artifact: Artifact("invalid"), Snapshot: &invalid},
			})

			Expect(err).To(MatchError(ContainSubstring("invalid")))
			Expect(repo.AsMap()).To(BeEmpty())
		})

		It("returns a full entry from one repository generation", func() {
			ref := snapshotRef(42)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("generation-42"), FromCache: true, Snapshot: &ref},
			})).To(Succeed())

			entry, found := repo.ArtifactEntryFor("subject")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(Equal(Artifact("generation-42")))
			Expect(entry.FromCache).To(BeTrue())
			Expect(entry.Snapshot).To(Equal(&ref))
		})

		It("returns snapshot values without aliasing input or repository state", func() {
			ref := snapshotRef(7)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("subject"), Snapshot: &ref},
			})).To(Succeed())

			ref.ID = 99
			entry, found := repo.ArtifactEntryFor("subject")
			Expect(found).To(BeTrue())
			Expect(entry.Snapshot.ID).To(Equal(snapshot.SnapshotID(7)))

			entry.Snapshot.ID = 101
			entryAgain, found := repo.ArtifactEntryFor("subject")
			Expect(found).To(BeTrue())
			Expect(entryAgain.Snapshot.ID).To(Equal(snapshot.SnapshotID(7)))

			asMap := repo.AsMap()
			asMap["subject"].Snapshot.ID = 102
			refValue, found := repo.SnapshotFor("subject")
			Expect(found).To(BeTrue())
			Expect(refValue.ID).To(Equal(snapshot.SnapshotID(7)))
			refValue.ID = 103

			stored, found := repo.SnapshotFor("subject")
			Expect(found).To(BeTrue())
			Expect(stored.ID).To(Equal(snapshot.SnapshotID(7)))
		})

		It("never exposes a mixed generation to concurrent readers", func() {
			initial := snapshotRef(1)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"left":  {Artifact: Artifact("1"), Snapshot: &initial},
				"right": {Artifact: Artifact("1"), Snapshot: &initial},
			})).To(Succeed())

			start := make(chan struct{})
			errCh := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				for generation := int64(2); generation <= 500; generation++ {
					ref := snapshotRef(generation)
					if err := repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
						"left":  {Artifact: Artifact(strconv.FormatInt(generation, 10)), Snapshot: &ref},
						"right": {Artifact: Artifact(strconv.FormatInt(generation, 10)), Snapshot: &ref},
					}); err != nil {
						errCh <- err
						return
					}
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				for range 1000 {
					entries := repo.AsMap()
					left := entries["left"]
					right := entries["right"]
					if left.Artifact != right.Artifact || left.Snapshot.ID != right.Snapshot.ID {
						errCh <- fmt.Errorf("mixed generation: left=%#v right=%#v", left, right)
						return
					}
					artifactGeneration, err := strconv.ParseInt(string(left.Artifact.(Artifact)), 10, 64)
					if err != nil || snapshot.SnapshotID(artifactGeneration) != left.Snapshot.ID {
						errCh <- fmt.Errorf("mixed artifact/snapshot entry: %#v", left)
						return
					}
				}
			}()

			close(start)
			wg.Wait()
			close(errCh)
			for err := range errCh {
				Expect(err).NotTo(HaveOccurred())
			}
		})
	})

	Describe("copy-on-write local scopes", func() {
		It("captures a stable parent generation", func() {
			original := snapshotRef(1)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("original"), Snapshot: &original},
			})).To(Succeed())
			repo.RegisterImageRef("image", "docker:///original@sha256:111")
			child := repo.NewLocalScope()

			replacement := snapshotRef(2)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("replacement"), Snapshot: &replacement},
				"later":   {Artifact: Artifact("later")},
			})).To(Succeed())
			repo.RegisterImageRef("image", "docker:///replacement@sha256:222")

			entry, found := child.ArtifactEntryFor("subject")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(Equal(Artifact("original")))
			Expect(entry.Snapshot.ID).To(Equal(snapshot.SnapshotID(1)))
			_, _, found = child.ArtifactFor("later")
			Expect(found).To(BeFalse())
			imageRef, found := child.ImageRefFor("image")
			Expect(found).To(BeTrue())
			Expect(imageRef).To(Equal("docker:///original@sha256:111"))
		})

		It("discards child artifacts and image refs unless committed", func() {
			child := repo.NewLocalScope()
			ref := snapshotRef(1)
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"output": {Artifact: Artifact("candidate"), Snapshot: &ref},
			})).To(Succeed())
			child.RegisterImageRef("output", "docker:///candidate@sha256:111")

			Expect(child.AsMap()).To(HaveKey(ArtifactName("output")))
			Expect(repo.AsMap()).NotTo(HaveKey(ArtifactName("output")))
			_, found := repo.ImageRefFor("output")
			Expect(found).To(BeFalse())
		})

		It("commits every dirty artifact and image ref atomically", func() {
			child := repo.NewLocalScope()
			first := snapshotRef(1)
			second := snapshotRef(2)
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"first":  {Artifact: Artifact("first"), Snapshot: &first},
				"second": {Artifact: Artifact("second"), Snapshot: &second},
			})).To(Succeed())
			child.RegisterImageRef("first", "docker:///first@sha256:111")

			Expect(child.CommitToParent()).To(Succeed())

			Expect(repo.AsMap()).To(HaveLen(2))
			imageRef, found := repo.ImageRefFor("first")
			Expect(found).To(BeTrue())
			Expect(imageRef).To(Equal("docker:///first@sha256:111"))
		})

		It("allows one inherited replacement but rejects a second registration in the same scope atomically", func() {
			original := snapshotRef(1)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("original"), Snapshot: &original},
			})).To(Succeed())
			child := repo.NewLocalScope()
			first := snapshotRef(2)
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("first replacement"), Snapshot: &first},
			})).To(Succeed())

			second := snapshotRef(3)
			err := child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("second replacement"), Snapshot: &second},
				"leak":    {Artifact: Artifact("must not publish")},
			})
			Expect(err).To(MatchError(ContainSubstring("subject")))
			Expect(child.AsMap()).NotTo(HaveKey(ArtifactName("leak")))
			entry, found := child.ArtifactEntryFor("subject")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(Equal(Artifact("first replacement")))

			Expect(child.CommitToParent()).To(Succeed())
			freshAttempt := repo.NewLocalScope()
			Expect(freshAttempt.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"subject": {Artifact: Artifact("fresh replacement"), Snapshot: &second},
			})).To(Succeed())
		})

		It("preserves the first local value when the void legacy wrapper cannot return a duplicate error", func() {
			child := repo.NewLocalScope()
			child.RegisterArtifact("subject", Artifact("first"), false)
			child.RegisterArtifact("subject", Artifact("second"), true)

			artifact, fromCache, found := child.ArtifactFor("subject")
			Expect(found).To(BeTrue())
			Expect(artifact).To(Equal(Artifact("first")))
			Expect(fromCache).To(BeFalse())
		})

		It("rejects a same-name parent race without partially merging the batch", func() {
			base := snapshotRef(1)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"contended": {Artifact: Artifact("base"), Snapshot: &base},
			})).To(Succeed())
			child := repo.NewLocalScope()
			candidate := snapshotRef(2)
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"contended": {Artifact: Artifact("candidate"), Snapshot: &candidate},
				"other":     {Artifact: Artifact("must not leak")},
			})).To(Succeed())

			raced := snapshotRef(3)
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"contended": {Artifact: Artifact("raced"), Snapshot: &raced},
			})).To(Succeed())

			Expect(child.CommitToParent()).To(MatchError(ContainSubstring("contended")))
			entry, found := repo.ArtifactEntryFor("contended")
			Expect(found).To(BeTrue())
			Expect(entry.Artifact).To(Equal(Artifact("raced")))
			Expect(repo.AsMap()).NotTo(HaveKey(ArtifactName("other")))
		})

		It("preserves unrelated parent writes when committing", func() {
			child := repo.NewLocalScope()
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"child": {Artifact: Artifact("child")},
			})).To(Succeed())
			Expect(repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"concurrent": {Artifact: Artifact("concurrent")},
			})).To(Succeed())

			Expect(child.CommitToParent()).To(Succeed())
			Expect(repo.AsMap()).To(Equal(map[ArtifactName]ArtifactEntry{
				"child":      {Artifact: Artifact("child")},
				"concurrent": {Artifact: Artifact("concurrent")},
			}))
		})

		It("detects image-ref conflicts and rolls back artifact changes", func() {
			repo.RegisterImageRef("image", "docker:///base@sha256:111")
			child := repo.NewLocalScope()
			child.RegisterImageRef("image", "docker:///candidate@sha256:222")
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"artifact": {Artifact: Artifact("must not leak")},
			})).To(Succeed())

			repo.RegisterImageRef("image", "docker:///raced@sha256:333")
			Expect(child.CommitToParent()).To(MatchError(ContainSubstring("image")))
			imageRef, found := repo.ImageRefFor("image")
			Expect(found).To(BeTrue())
			Expect(imageRef).To(Equal("docker:///raced@sha256:333"))
			Expect(repo.AsMap()).NotTo(HaveKey(ArtifactName("artifact")))
		})

		It("has single-use commit semantics", func() {
			child := repo.NewLocalScope()
			Expect(child.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
				"output": {Artifact: Artifact("output")},
			})).To(Succeed())

			Expect(child.CommitToParent()).To(Succeed())
			Expect(child.CommitToParent()).To(MatchError(ContainSubstring("already committed")))
		})
	})

	It("preserves legacy root last-writer-wins registration", func() {
		repo.RegisterArtifact("subject", Artifact("first"), false)
		repo.RegisterArtifact("subject", Artifact("second"), true)

		artifact, fromCache, found := repo.ArtifactFor("subject")
		Expect(found).To(BeTrue())
		Expect(artifact).To(Equal(Artifact("second")))
		Expect(fromCache).To(BeTrue())
	})

	// [AR-02] Repository must be safe for concurrent access.
	// If the implementation lacks proper locking, concurrent map writes will
	// panic immediately in Go (without requiring the race detector).
	Context("[AR-02] concurrent access", func() {
		It("is safe for concurrent RegisterArtifact, ArtifactFor, and AsMap calls", func() {
			concurrentRepo := NewRepository()

			const goroutines = 20
			var wg sync.WaitGroup
			wg.Add(goroutines)

			for i := 0; i < goroutines; i++ {
				go func() {
					defer wg.Done()
					concurrentRepo.RegisterArtifact("shared-artifact", Artifact("value"), false)
					concurrentRepo.ArtifactFor("shared-artifact")
					concurrentRepo.AsMap()
				}()
			}

			wg.Wait()

			// After all goroutines finish, the artifact should be registered.
			_, _, found := concurrentRepo.ArtifactFor("shared-artifact")
			Expect(found).To(BeTrue())
		})

		It("is safe for concurrent reads across parent and child scopes", func() {
			parent := NewRepository()
			parent.RegisterArtifact("parent-artifact", Artifact("parent-value"), false)
			child := parent.NewLocalScope()

			var wg sync.WaitGroup
			wg.Add(20)

			for i := 0; i < 10; i++ {
				go func() {
					defer wg.Done()
					parent.RegisterArtifact("parent-artifact", Artifact("updated"), false)
					parent.AsMap()
				}()
			}

			for i := 0; i < 10; i++ {
				go func() {
					defer wg.Done()
					child.RegisterArtifact("child-artifact", Artifact("child-value"), false)
					child.ArtifactFor("parent-artifact")
					child.AsMap()
				}()
			}

			wg.Wait()
		})
	})
})
