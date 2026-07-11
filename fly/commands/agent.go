package commands

// AgentCommand groups agentic-platform subcommands (`fly agent ...`).
type AgentCommand struct {
	Workflows AgentWorkflowsCommand `command:"workflows" description:"Manage versioned agent workflow definitions"`
}
