package atc

type PipelineRun struct {
	ID          int            `json:"id"`
	Number      int            `json:"number"`
	Status      string         `json:"status"`
	Params      map[string]any `json:"params,omitempty"`
	CreatedBy   string         `json:"created_by,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	CompletedAt int64          `json:"completed_at,omitempty"`
	Archived    bool           `json:"archived,omitempty"`
}

type CreatePipelineRunRequest struct {
	Params map[string]any `json:"params,omitempty"`
}
