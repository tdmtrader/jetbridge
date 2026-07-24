package commands

// AgentCommand is the shared `fly agent` family. Per the wave-1 contract
// addendum, other workstreams append their own subcommand fields here
// (Workflows, Tickets, ...) — additive merges only.
type AgentCommand struct {
	Auth        AgentAuthCommand        `command:"auth" description:"Vault your Anthropic token for agent workloads"`
	Costs       AgentCostsCommand       `command:"costs" description:"Show agent cost rollups"`
	Runs        AgentRunsCommand        `command:"runs" description:"List recent agent run metrics (cost, tokens, status)"`
	Workflows   AgentWorkflowsCommand   `command:"workflows" description:"Manage versioned agent workflow definitions"`
	Principals  AgentPrincipalsCommand  `command:"principals" description:"Mint, list, and revoke agent principals (admin)"`
	Tickets     AgentTicketsCommand     `command:"tickets" description:"File and track agent tickets"`
	Dispatcher  AgentDispatcherCommand  `command:"dispatcher" description:"Show or set the autonomous dispatcher mode (off|paused|active)"`
	Snapshots   AgentSnapshotsCommand   `command:"snapshots" description:"Create, inspect, retain, and download typed snapshots"`
	Experiments AgentExperimentsCommand `command:"experiments" description:"Create, run, and inspect controlled workflow experiments"`

	CleanupLegacyPipelines AgentCleanupLegacyPipelinesCommand `command:"cleanup-legacy-pipelines" description:"One-time: archive retired agent-ticket-<id> pipelines (main team only)"`
}
