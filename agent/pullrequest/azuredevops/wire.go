package azuredevops

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/pullrequest"
)

var _ pullrequest.Observer = (*Observer)(nil)

type azurePullRequest struct {
	Repository                azureRepository `json:"repository"`
	PullRequestID             int64           `json:"pullRequestId"`
	Status                    string          `json:"status"`
	MergeStatus               string          `json:"mergeStatus"`
	SourceRefName             string          `json:"sourceRefName"`
	TargetRefName             string          `json:"targetRefName"`
	LastMergeSourceCommit     azureCommit     `json:"lastMergeSourceCommit"`
	LastMergeTargetCommit     azureCommit     `json:"lastMergeTargetCommit"`
	SupportsIterations        bool            `json:"supportsIterations"`
	ForkSource                json.RawMessage `json:"forkSource"`
	ProviderInternalURL       string          `json:"url"`
	ProviderInternalRemoteURL string          `json:"remoteUrl"`
}

type azureRepository struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	IsFork  bool         `json:"isFork"`
	Project azureProject `json:"project"`
}

type azureProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type azureCommit struct {
	CommitID string `json:"commitId"`
}

type azureIteration struct {
	ID              int64       `json:"id"`
	SourceRefCommit azureCommit `json:"sourceRefCommit"`
	TargetRefCommit azureCommit `json:"targetRefCommit"`
}

type azureReviewer struct {
	ID          string `json:"id"`
	Vote        int    `json:"vote"`
	IsContainer bool   `json:"isContainer"`
	Inactive    bool   `json:"inactive"`
}

type azureThread struct {
	ID                       int64                    `json:"id"`
	PublishedDate            string                   `json:"publishedDate"`
	LastUpdatedDate          string                   `json:"lastUpdatedDate"`
	Comments                 []azureComment           `json:"comments"`
	Status                   string                   `json:"status"`
	ThreadContext            *azureThreadContext      `json:"threadContext"`
	PullRequestThreadContext *azurePullThreadContext  `json:"pullRequestThreadContext"`
	Properties               map[string]azureProperty `json:"properties"`
	IsDeleted                bool                     `json:"isDeleted"`
}

type azureComment struct {
	ID                     int64         `json:"id"`
	ParentCommentID        int64         `json:"parentCommentId"`
	Author                 azureIdentity `json:"author"`
	Content                string        `json:"content"`
	PublishedDate          string        `json:"publishedDate"`
	LastUpdatedDate        string        `json:"lastUpdatedDate"`
	LastContentUpdatedDate string        `json:"lastContentUpdatedDate"`
	CommentType            string        `json:"commentType"`
	IsDeleted              bool          `json:"isDeleted"`
}

type azureIdentity struct {
	ID string `json:"id"`
}

type azureThreadContext struct {
	FilePath       string         `json:"filePath"`
	LeftFileStart  *azurePosition `json:"leftFileStart"`
	LeftFileEnd    *azurePosition `json:"leftFileEnd"`
	RightFileStart *azurePosition `json:"rightFileStart"`
	RightFileEnd   *azurePosition `json:"rightFileEnd"`
}

type azurePosition struct {
	Line   int `json:"line"`
	Offset int `json:"offset"`
}

type azurePullThreadContext struct {
	IterationContext *azureIterationContext `json:"iterationContext"`
	ChangeTrackingID int64                  `json:"changeTrackingId"`
}

type azureIterationContext struct {
	FirstComparingIteration  int64 `json:"firstComparingIteration"`
	SecondComparingIteration int64 `json:"secondComparingIteration"`
}

type azureProperty struct {
	Type  string          `json:"$type"`
	Value json.RawMessage `json:"$value"`
}

type collectionPage[T any] struct {
	Count *int `json:"count"`
	Value *[]T `json:"value"`
}
