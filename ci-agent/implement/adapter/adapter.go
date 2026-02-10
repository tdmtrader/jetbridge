package adapter

import "context"

// CodeGenRequest is the input for both test and implementation generation.
type CodeGenRequest struct {
	TaskDescription string   `json:"task_description"`
	SpecContext     string   `json:"spec_context"`
	RepoDir         string   `json:"repo_dir"`
	TargetFiles     []string `json:"target_files,omitempty"`
	PriorContext    string   `json:"prior_context,omitempty"`
}

// TestGenResponse is the output from test generation.
type TestGenResponse struct {
	TestFilePath string `json:"test_file_path"`
	TestContent  string `json:"test_content"`
	PackageName  string `json:"package_name"`
}

// ImplGenResponse is the output from implementation generation.
type ImplGenResponse struct {
	Patches []FilePatch `json:"patches"`
}

// FilePatch describes a file to write or overwrite.
type FilePatch struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Adapter generates test code and implementation code via an AI agent.
type Adapter interface {
	GenerateTest(ctx context.Context, req CodeGenRequest) (*TestGenResponse, error)
	GenerateImpl(ctx context.Context, req CodeGenRequest, testCode string) (*ImplGenResponse, error)
}
