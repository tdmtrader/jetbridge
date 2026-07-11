package commands

// AgentCommand is the shared `fly agent` family. Per the wave-1 contract
// addendum, other workstreams append their own subcommand fields here
// (Workflows, Tickets, ...) — additive merges only.
type AgentCommand struct {
	Auth      AgentAuthCommand      `command:"auth" description:"Vault your Anthropic token for agent workloads"`
	Costs     AgentCostsCommand     `command:"costs" description:"Show agent cost rollups"`
	Workflows AgentWorkflowsCommand `command:"workflows" description:"Manage versioned agent workflow definitions"`
}
