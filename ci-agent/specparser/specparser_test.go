package specparser_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/specparser"
)

var _ = Describe("ParseSpec", func() {
	It("extracts numbered requirements from ## Requirements section", func() {
		md := []byte(`# Feature Spec

## Requirements

1. The system must authenticate users
2. Sessions expire after 24 hours
3. Rate limiting is enforced

## Other Section

Some text.
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements).To(HaveLen(3))
		Expect(spec.Requirements[0].ID).To(Equal("R1"))
		Expect(spec.Requirements[0].Text).To(Equal("The system must authenticate users"))
		Expect(spec.Requirements[1].ID).To(Equal("R2"))
		Expect(spec.Requirements[2].ID).To(Equal("R3"))
	})

	It("extracts acceptance criteria from ## Acceptance Criteria section", func() {
		md := []byte(`# Spec

## Acceptance Criteria

- [ ] Users can log in with email and password
- [ ] JWT tokens are issued on successful login
- [ ] Invalid credentials return 401
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.AcceptanceCriteria).To(HaveLen(3))
		Expect(spec.AcceptanceCriteria[0].ID).To(Equal("AC1"))
		Expect(spec.AcceptanceCriteria[0].Text).To(Equal("Users can log in with email and password"))
		Expect(spec.AcceptanceCriteria[1].ID).To(Equal("AC2"))
		Expect(spec.AcceptanceCriteria[2].ID).To(Equal("AC3"))
	})

	It("handles both requirements and acceptance criteria", func() {
		md := []byte(`# Full Spec

## Requirements

1. Auth required
2. Sessions managed

## Acceptance Criteria

- [ ] Login works
- [ ] Logout works
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements).To(HaveLen(2))
		Expect(spec.AcceptanceCriteria).To(HaveLen(2))
	})

	It("returns empty lists when sections are missing", func() {
		md := []byte(`# Minimal Spec

Just some description text.
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements).To(BeEmpty())
		Expect(spec.AcceptanceCriteria).To(BeEmpty())
	})

	It("handles checked acceptance criteria checkboxes", func() {
		md := []byte(`## Acceptance Criteria

- [x] Already done criterion
- [ ] Not done criterion
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.AcceptanceCriteria).To(HaveLen(2))
		Expect(spec.AcceptanceCriteria[0].Text).To(Equal("Already done criterion"))
	})

	It("handles mixed numbering styles", func() {
		md := []byte(`## Requirements

1. First req
2. Second req
3. Third req
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements).To(HaveLen(3))
	})

	It("provides AllItems for unified iteration", func() {
		md := []byte(`## Requirements

1. Req one

## Acceptance Criteria

- [ ] AC one
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		items := spec.AllItems()
		Expect(items).To(HaveLen(2))
		Expect(items[0].ID).To(Equal("R1"))
		Expect(items[1].ID).To(Equal("AC1"))
	})

	It("trims whitespace from requirement text", func() {
		md := []byte(`## Requirements

1.   Lots of spaces
2. Normal text
`)
		spec, err := specparser.ParseSpec(md)
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements[0].Text).To(Equal("Lots of spaces"))
	})

	It("handles empty input", func() {
		spec, err := specparser.ParseSpec([]byte(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Requirements).To(BeEmpty())
		Expect(spec.AcceptanceCriteria).To(BeEmpty())
	})
})
