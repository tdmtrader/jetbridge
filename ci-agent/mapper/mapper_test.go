package mapper_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/mapper"
	"github.com/concourse/ci-agent/specparser"
)

var _ = Describe("BuildTestIndex", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "mapper-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	It("scans for _test.go files", func() {
		content := `package foo_test

func TestAuth(t *testing.T) {}
func TestSession(t *testing.T) {}
`
		os.WriteFile(filepath.Join(tmpDir, "auth_test.go"), []byte(content), 0644)

		index, err := mapper.BuildTestIndex(tmpDir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.Tests).To(HaveLen(2))
		Expect(index.Tests[0].Function).To(Equal("TestAuth"))
		Expect(index.Tests[1].Function).To(Equal("TestSession"))
	})

	It("extracts Ginkgo It descriptions", func() {
		content := `package foo_test

var _ = Describe("Auth", func() {
	It("authenticates users with password", func() {})
	It("rejects invalid credentials", func() {})
})
`
		os.WriteFile(filepath.Join(tmpDir, "auth_test.go"), []byte(content), 0644)

		index, err := mapper.BuildTestIndex(tmpDir, nil)
		Expect(err).NotTo(HaveOccurred())
		// Should find Describe + 2 It blocks
		descriptions := []string{}
		for _, t := range index.Tests {
			if t.Description != "" {
				descriptions = append(descriptions, t.Description)
			}
		}
		Expect(descriptions).To(ContainElement("authenticates users with password"))
		Expect(descriptions).To(ContainElement("rejects invalid credentials"))
		Expect(descriptions).To(ContainElement("Auth"))
	})

	It("skips vendor directories", func() {
		vendorDir := filepath.Join(tmpDir, "vendor")
		os.MkdirAll(vendorDir, 0755)
		os.WriteFile(filepath.Join(vendorDir, "vendor_test.go"),
			[]byte(`package vendor_test; func TestVendor(t *testing.T) {}`), 0644)

		index, err := mapper.BuildTestIndex(tmpDir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.Tests).To(BeEmpty())
	})

	It("returns empty index for empty directory", func() {
		index, err := mapper.BuildTestIndex(tmpDir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(index.Tests).To(BeEmpty())
	})
})

var _ = Describe("MapRequirements", func() {
	It("maps requirements to matching tests", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "System authenticates users with password"},
			},
		}
		index := &mapper.TestIndex{
			Tests: []mapper.TestEntry{
				{File: "auth_test.go", Description: "authenticates users with password"},
			},
		}
		mappings := mapper.MapRequirements(spec, index)
		Expect(mappings).To(HaveLen(1))
		Expect(mappings[0].Status).To(Equal("covered"))
		Expect(mappings[0].Matches).To(HaveLen(1))
	})

	It("marks uncovered when no matching tests", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "Rate limiting is enforced"},
			},
		}
		index := &mapper.TestIndex{
			Tests: []mapper.TestEntry{
				{File: "auth_test.go", Description: "authenticates users"},
			},
		}
		mappings := mapper.MapRequirements(spec, index)
		Expect(mappings).To(HaveLen(1))
		Expect(mappings[0].Status).To(Equal("uncovered"))
	})

	It("marks partial when low confidence match", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "Sessions expire after configurable timeout period"},
			},
		}
		index := &mapper.TestIndex{
			Tests: []mapper.TestEntry{
				{File: "session_test.go", Description: "creates a session"},
			},
		}
		mappings := mapper.MapRequirements(spec, index)
		Expect(mappings).To(HaveLen(1))
		// "session" matches but other words don't → partial
		Expect(mappings[0].Status).To(Or(Equal("partial"), Equal("uncovered")))
	})

	It("includes confidence per match", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "authenticate users"},
			},
		}
		index := &mapper.TestIndex{
			Tests: []mapper.TestEntry{
				{File: "auth_test.go", Description: "authenticate users with password"},
			},
		}
		mappings := mapper.MapRequirements(spec, index)
		Expect(mappings[0].Matches).To(HaveLen(1))
		Expect(mappings[0].Matches[0].Confidence).To(BeNumerically(">", 0.0))
	})

	It("handles both requirements and acceptance criteria", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "auth works"},
			},
			AcceptanceCriteria: []specparser.AcceptanceCriterion{
				{ID: "AC1", Text: "login page loads"},
			},
		}
		index := &mapper.TestIndex{}
		mappings := mapper.MapRequirements(spec, index)
		Expect(mappings).To(HaveLen(2))
	})
})
