package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// DurableKey exists so a copy of a cache's bytes can outlive the cache's row.
// Every test here is about that one word, "outlive": the id cannot do the job
// because it is a sequence, so the key has to be derived from what the cache
// holds.
var _ = Describe("ResourceCache DurableKey", func() {
	var build db.Build

	find := func(typeName string, version atc.Version, source atc.Source, params atc.Params, parent db.ResourceCache) db.ResourceCache {
		GinkgoHelper()

		cache, err := resourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(build.ID()), typeName, version, source, params, parent,
		)
		Expect(err).ToNot(HaveOccurred())

		return cache
	}

	BeforeEach(func() {
		var err error
		build, err = defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())

		setupTx, err := dbConn.Begin()
		Expect(err).ToNot(HaveOccurred())
		_, err = db.BaseResourceType{Name: "some-base-type"}.FindOrCreate(setupTx, false)
		Expect(err).ToNot(HaveOccurred())
		Expect(setupTx.Commit()).To(Succeed())
	})

	It("survives the row being deleted and recreated", func() {
		// The property the whole feature rests on. CleanUpInvalidCaches drops a
		// cache the moment nothing references it, which is exactly when a
		// long-term copy is still wanted; the next build re-inserts the same
		// tuple and gets a fresh id. Keyed on the id, the durable copy would be
		// orphaned and the lookup would miss forever.
		first := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)
		Expect(first.DurableKey()).ToNot(BeEmpty())

		_, err := dbConn.Exec("DELETE FROM resource_cache_uses WHERE resource_cache_id = $1", first.ID())
		Expect(err).ToNot(HaveOccurred())
		_, err = dbConn.Exec("DELETE FROM resource_caches WHERE id = $1", first.ID())
		Expect(err).ToNot(HaveOccurred())

		second := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)

		Expect(second.ID()).ToNot(Equal(first.ID()), "the row should genuinely be a new one")
		Expect(second.DurableKey()).To(Equal(first.DurableKey()))
	})

	DescribeTable("distinguishes caches that hold different bytes",
		func(typeName string, version atc.Version, source atc.Source, params atc.Params) {
			base := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)
			other := find(typeName, version, source, params, nil)

			Expect(other.DurableKey()).ToNot(Equal(base.DurableKey()))
		},
		Entry("a different version", "some-base-type", atc.Version{"v": "2"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}),
		Entry("a different source", "some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "z"}, atc.Params{"p": "y"}),
		Entry("different params", "some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "z"}),
	)

	It("distinguishes two versions of the same custom type", func() {
		// The trap this key was written to avoid. Deriving the parent component
		// from BaseResourceType() flattens the custom-type chain to its base, so
		// these two — identical source, identical params, fetched with different
		// builds of the same custom type — would collide and serve each other's
		// bytes. The parent has to contribute its own key.
		typeV1 := find("some-base-type", atc.Version{"type": "1"}, atc.Source{"t": "s"}, nil, nil)
		typeV2 := find("some-base-type", atc.Version{"type": "2"}, atc.Source{"t": "s"}, nil, nil)
		Expect(typeV1.DurableKey()).ToNot(Equal(typeV2.DurableKey()))

		viaV1 := find("custom", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, typeV1)
		viaV2 := find("custom", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, typeV2)

		Expect(viaV1.DurableKey()).ToNot(Equal(viaV2.DurableKey()))
	})

	It("is readable on a cache loaded by id", func() {
		// FindResourceCacheByID has neither source nor params in scope, which is
		// why the key is a stored column rather than something recomputed on
		// demand. If it were not persisted this path would silently return "".
		created := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)

		loaded, found, err := resourceCacheFactory.FindResourceCacheByID(created.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		Expect(loaded.DurableKey()).To(Equal(created.DurableKey()))
	})

	It("backfills a row that predates the column", func() {
		// The migration cannot compute the key: the source lives in
		// resource_configs and the custom type chain has to be walked in Go. So
		// existing rows start NULL and the next find fills them in. Without
		// this, every cache in an upgraded cluster stays permanently ineligible.
		created := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)

		_, err := dbConn.Exec("UPDATE resource_caches SET durable_key = NULL WHERE id = $1", created.ID())
		Expect(err).ToNot(HaveOccurred())

		refound := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)
		Expect(refound.ID()).To(Equal(created.ID()), "expected to find the same row, not create a new one")
		Expect(refound.DurableKey()).To(Equal(created.DurableKey()))

		var stored string
		err = dbConn.QueryRow("SELECT durable_key FROM resource_caches WHERE id = $1", created.ID()).Scan(&stored)
		Expect(err).ToNot(HaveOccurred())
		Expect(stored).To(Equal(created.DurableKey()), "the backfill must be persisted, not just returned")
	})

	It("is a key the artifact daemon will accept", func() {
		// The daemon takes the key as an opaque string but does validate it,
		// since it becomes a path component in the durable store. A key it
		// rejects would disable the feature silently.
		cache := find("some-base-type", atc.Version{"v": "1"}, atc.Source{"s": "x"}, atc.Params{"p": "y"}, nil)

		Expect(cache.DurableKey()).To(MatchRegexp(`^rc-[0-9a-f]{64}$`))
	})
})
