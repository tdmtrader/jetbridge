package contracts

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestPullRequestRecordValidatorsFailClosedForUnknownNonemptySchema(t *testing.T) {
	unknownSchema := snapshot.Digest("sha256:" + strings.Repeat("f", 64))
	responseSubjects := []Subject{{
		ID: "primary", Role: SubjectRolePrimary, Input: "pr", Type: pullRequestType,
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	for _, tc := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "pull request",
			validate: func() error {
				return pullRequestBody(Record[PullRequestBody]{
					Schema: unknownSchema,
					Body: PullRequestBody{
						Provider: "github", Repository: "example/widget", ExternalID: "42",
						URL: "https://example.invalid/widget/pull/42", State: PullRequestActive,
						Mergeability: PullRequestMergeable, SourceRef: "refs/heads/change",
						SourceSHA: strings.Repeat("a", 40), TargetRef: "refs/heads/main",
						TargetSHA: strings.Repeat("b", 40), Iteration: "iteration-1",
						Trigger: PullRequestFreshnessTrigger,
					},
				})
			},
		},
		{
			name: "pull request response",
			validate: func() error {
				return pullRequestResponseBody(Record[PullRequestResponseBody]{
					Schema: unknownSchema, Subjects: responseSubjects,
					Body: PullRequestResponseBody{BatchID: "batch-1", Summary: "Done."},
				})
			},
		},
		{
			name: "publish impact",
			validate: func() error {
				return publishImpactBody(Record[PublishImpactBody]{
					Schema: unknownSchema,
					Body: PublishImpactBody{
						BaselineDigest:  "sha256:" + strings.Repeat("a", 64),
						CandidateDigest: "sha256:" + strings.Repeat("b", 64),
					},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(); err == nil {
				t.Fatal("record validator accepted an unknown nonempty schema")
			}
		})
	}
}
