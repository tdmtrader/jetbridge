package atc

type Info struct {
	Version          string          `json:"version"`
	WorkerVersion    string          `json:"worker_version"`
	ExternalURL      string          `json:"external_url,omitempty"`
	ClusterName      string          `json:"cluster_name,omitempty"`
	JetBridgeVersion string          `json:"jetbridge_version,omitempty"`
	ConcourseVersion string          `json:"concourse_version,omitempty"`
}
