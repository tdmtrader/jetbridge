package atc

type ClearTaskCacheResponse struct {
	CachesRemoved int64 `json:"caches_removed"`
}

type SaveConfigResponse struct {
	Code     string          `json:"code,omitempty"`
	Errors   []string        `json:"errors,omitempty"`
	Warnings []ConfigWarning `json:"warnings,omitempty"`
}

type ConfigResponse struct {
	Config Config `json:"config"`
}

type ClearResourceCacheResponse struct {
	CachesRemoved int64 `json:"caches_removed"`
}

type ClearVersionsResponse struct {
	VersionsRemoved int64 `json:"versions_removed"`
}

type CopyVersionsResponse struct {
	VersionsCopied int `json:"versions_copied"`
}

type DeprecatedScope struct {
	ID           int    `json:"id"`
	DeprecatedAt string `json:"deprecated_at"`
	ConfigID     int    `json:"config_id"`
}

// PipelinePage and BuildPage are the bounded format=page views. Legacy list
// arrays and their ordering stay unchanged when the format is not requested.
type PipelinePage struct {
	Items      []Pipeline `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}
type BuildPage struct {
	Items      []Build `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
