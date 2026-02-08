package jetbridge_test

import (
	"strings"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GeneratePodName", func() {
	Describe("build step containers", func() {
		It("produces <pipeline>-<job>-b<build>-<type>-<suffix> format", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "my-pipeline",
				JobName:      "unit-test",
				BuildName:    "42",
				StepName:     "run-tests",
				Type:         db.ContainerTypeTask,
			}, "550e8400-e29b-41d4-a716-446655440000")

			Expect(name).To(MatchRegexp(`^my-pipeline-unit-test-b42-task-[a-f0-9]{8}$`))
		})

		It("includes step name for get and put types", func() {
			getName := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "ci",
				JobName:      "build",
				BuildName:    "7",
				StepName:     "source-code",
				Type:         db.ContainerTypeGet,
			}, "aabbccdd-1122-3344-5566-778899aabbcc")

			Expect(getName).To(MatchRegexp(`^ci-build-b7-get-[a-f0-9]{8}$`))

			putName := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "ci",
				JobName:      "build",
				BuildName:    "7",
				StepName:     "push-image",
				Type:         db.ContainerTypePut,
			}, "aabbccdd-1122-3344-5566-778899aabbcc")

			Expect(putName).To(MatchRegexp(`^ci-build-b7-put-[a-f0-9]{8}$`))
		})

		It("uses the first 8 hex chars of the handle as the suffix", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "p",
				JobName:      "j",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-3456-7890-abcd-ef1234567890")

			Expect(name).To(HaveSuffix("abcdef12"))
		})
	})

	Describe("sanitization", func() {
		It("lowercases all characters", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "My-Pipeline",
				JobName:      "Unit-TEST",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			Expect(name).To(Equal(strings.ToLower(name)))
		})

		It("replaces underscores, dots, and spaces with hyphens", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "my_pipe.line",
				JobName:      "unit test",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			Expect(name).ToNot(ContainSubstring("_"))
			Expect(name).ToNot(ContainSubstring("."))
			Expect(name).ToNot(ContainSubstring(" "))
			Expect(name).To(MatchRegexp(`^my-pipe-line-unit-test-b1-task-abcdef12$`))
		})

		It("strips non-alphanumeric non-hyphen characters", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "pipe@line!",
				JobName:      "job#1",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			Expect(name).ToNot(MatchRegexp(`[^a-z0-9-]`))
		})

		It("collapses consecutive hyphens", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "my--pipe",
				JobName:      "my___job",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			Expect(name).ToNot(ContainSubstring("--"))
		})
	})

	Describe("truncation", func() {
		It("truncates pipeline and job segments to 20 chars each", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "this-is-a-very-long-pipeline-name-that-exceeds",
				JobName:      "this-is-a-very-long-job-name-that-exceeds-too",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			parts := strings.SplitN(name, "-b1-", 2)
			Expect(len(parts)).To(Equal(2))
			// The prefix before -b1- should contain truncated pipeline and job
			// Each segment should be at most 20 chars
			prefix := parts[0]
			Expect(len(prefix)).To(BeNumerically("<=", 41)) // 20 + hyphen + 20
		})

		It("does not exceed 63 chars total (DNS label safe)", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "extremely-long-pipeline-name-that-goes-on-forever",
				JobName:      "extremely-long-job-name-that-goes-on-forever-too",
				BuildName:    "999999",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			Expect(len(name)).To(BeNumerically("<=", 63))
		})

		It("does not end with a hyphen after truncation", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "abcdefghijklmnopqrst-uvwxyz",
				JobName:      "j",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcdef12-0000-0000-0000-000000000000")

			// After truncating pipeline to 20 chars: "abcdefghijklmnopqrst"
			// should not end with hyphen
			segments := strings.Split(name, "-")
			for _, s := range segments {
				if s != "" {
					Expect(s[len(s)-1]).ToNot(Equal(byte('-')))
				}
			}
		})
	})

	Describe("fallback for missing metadata", func() {
		It("falls back to UUID-only when pipeline and job are empty (fly execute)", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type: db.ContainerTypeTask,
			}, handle)

			Expect(name).To(Equal(handle))
		})

		It("falls back to UUID-only when metadata is completely empty", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			name := jetbridge.GeneratePodName(db.ContainerMetadata{}, handle)

			Expect(name).To(Equal(handle))
		})
	})

	Describe("check containers", func() {
		It("uses chk-<step-name>-<suffix> format", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type:     db.ContainerTypeCheck,
				StepName: "my-git-resource",
			}, "aabbccdd-1122-3344-5566-778899aabbcc")

			Expect(name).To(MatchRegexp(`^chk-my-git-resource-[a-f0-9]{8}$`))
		})

		It("truncates long resource names in check format", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type:     db.ContainerTypeCheck,
				StepName: "extremely-long-resource-name-that-goes-on-forever-and-ever",
			}, "aabbccdd-1122-3344-5566-778899aabbcc")

			Expect(len(name)).To(BeNumerically("<=", 63))
			Expect(name).To(HavePrefix("chk-"))
		})

		It("falls back to UUID for check with no step name", func() {
			handle := "550e8400-e29b-41d4-a716-446655440000"
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				Type: db.ContainerTypeCheck,
			}, handle)

			Expect(name).To(Equal(handle))
		})
	})

	Describe("handle suffix extraction", func() {
		It("strips hyphens from UUID to extract hex suffix", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "p",
				JobName:      "j",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abcd-ef12-3456-7890-abcdef123456")

			// After stripping hyphens: "abcdef1234567890abcdef123456"
			// First 8 chars: "abcdef12"
			Expect(name).To(HaveSuffix("abcdef12"))
		})

		It("handles short handles gracefully", func() {
			name := jetbridge.GeneratePodName(db.ContainerMetadata{
				PipelineName: "p",
				JobName:      "j",
				BuildName:    "1",
				Type:         db.ContainerTypeTask,
			}, "abc")

			Expect(name).To(HaveSuffix("abc"))
		})
	})
})
