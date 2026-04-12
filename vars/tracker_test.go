package vars_test

import (
	. "github.com/concourse/concourse/vars"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Tracker SecretRef tracking", func() {
	var tracker *Tracker

	BeforeEach(func() {
		tracker = NewTracker()
	})

	Describe("TrackSecretRef", func() {
		It("records a secret ref for a var path", func() {
			ref := SecretRef{Namespace: "concourse-main", Name: "db-password", Key: "value"}
			tracker.TrackSecretRef(Reference{Path: "db-password"}, ref)

			refs := collectSecretRefs(tracker)
			Expect(refs).To(HaveKeyWithValue("db-password", ref))
		})

		It("overwrites a previous ref for the same path", func() {
			ref1 := SecretRef{Namespace: "ns1", Name: "secret1", Key: "value"}
			ref2 := SecretRef{Namespace: "ns2", Name: "secret2", Key: "value"}
			tracker.TrackSecretRef(Reference{Path: "my-var"}, ref1)
			tracker.TrackSecretRef(Reference{Path: "my-var"}, ref2)

			refs := collectSecretRefs(tracker)
			Expect(refs).To(HaveKeyWithValue("my-var", ref2))
		})

		It("tracks multiple different refs", func() {
			ref1 := SecretRef{Namespace: "ns", Name: "s1", Key: "value"}
			ref2 := SecretRef{Namespace: "ns", Name: "s2", Key: "value"}
			tracker.TrackSecretRef(Reference{Path: "var1"}, ref1)
			tracker.TrackSecretRef(Reference{Path: "var2"}, ref2)

			refs := collectSecretRefs(tracker)
			Expect(refs).To(HaveLen(2))
			Expect(refs).To(HaveKeyWithValue("var1", ref1))
			Expect(refs).To(HaveKeyWithValue("var2", ref2))
		})
	})

	Describe("IterateSecretRefs", func() {
		It("yields nothing when no refs are tracked", func() {
			refs := collectSecretRefs(tracker)
			Expect(refs).To(BeEmpty())
		})
	})
})

var _ = Describe("CredVarsTracker secret ref integration", func() {
	It("tracks secret refs when CredVars implements SecretRefResolver", func() {
		fakeVars := &fakeSecretRefVars{
			vals: map[string]any{"db-password": "s3cret"},
			refs: map[string]*SecretRef{
				"db-password": {Namespace: "concourse-main", Name: "db-password", Key: "value"},
			},
		}
		tracker := &CredVarsTracker{
			CredVars: fakeVars,
			Tracker:  NewTracker(),
		}

		val, found, err := tracker.Get(Reference{Path: "db-password"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal("s3cret"))

		refs := collectSecretRefs(tracker.Tracker)
		Expect(refs).To(HaveKeyWithValue("db-password", SecretRef{
			Namespace: "concourse-main",
			Name:      "db-password",
			Key:       "value",
		}))
	})

	It("does not track secret refs when CredVars does not implement SecretRefResolver", func() {
		fakeVars := &FakeVariables{
			GetFunc: func(ref Reference) (any, bool, error) {
				return "plain-value", true, nil
			},
		}
		tracker := &CredVarsTracker{
			CredVars: fakeVars,
			Tracker:  NewTracker(),
		}

		_, found, err := tracker.Get(Reference{Path: "some-var"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		refs := collectSecretRefs(tracker.Tracker)
		Expect(refs).To(BeEmpty())
	})

	It("does not track secret refs when the var is not found", func() {
		fakeVars := &fakeSecretRefVars{
			vals: map[string]any{},
			refs: map[string]*SecretRef{
				"db-password": {Namespace: "ns", Name: "s", Key: "value"},
			},
		}
		tracker := &CredVarsTracker{
			CredVars: fakeVars,
			Tracker:  NewTracker(),
		}

		_, found, err := tracker.Get(Reference{Path: "db-password"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		refs := collectSecretRefs(tracker.Tracker)
		Expect(refs).To(BeEmpty())
	})

	It("does not track secret ref when resolver returns false", func() {
		fakeVars := &fakeSecretRefVars{
			vals: map[string]any{"some-var": "val"},
			refs: map[string]*SecretRef{}, // no ref for this var
		}
		tracker := &CredVarsTracker{
			CredVars: fakeVars,
			Tracker:  NewTracker(),
		}

		_, found, err := tracker.Get(Reference{Path: "some-var"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		refs := collectSecretRefs(tracker.Tracker)
		Expect(refs).To(BeEmpty())
	})
})

// fakeSecretRefVars implements both Variables and SecretRefResolver.
type fakeSecretRefVars struct {
	vals map[string]any
	refs map[string]*SecretRef
}

func (f *fakeSecretRefVars) Get(ref Reference) (any, bool, error) {
	val, ok := f.vals[ref.Path]
	return val, ok, nil
}

func (f *fakeSecretRefVars) List() ([]Reference, error) {
	return nil, nil
}

func (f *fakeSecretRefVars) GetSecretRef(ref Reference) (*SecretRef, bool) {
	r, ok := f.refs[ref.Path]
	return r, ok
}

type secretRefCollector map[string]SecretRef

func (c secretRefCollector) YieldSecretRef(varPath string, ref SecretRef) {
	c[varPath] = ref
}

func collectSecretRefs(tracker *Tracker) map[string]SecretRef {
	result := secretRefCollector{}
	tracker.IterateSecretRefs(result)
	return result
}
