package session

import "context"

// Provider holds only what differs between agent providers: how their
// subscription credential is recognised, how their pinned version is checked,
// how a policy becomes a command line, and how their event stream is decoded.
// Session lifetime, tmpfs staging, cleanup and the credential handoff are
// shared and live in Session.
type Provider interface {
	// Name identifies the provider in provenance and names its private home
	// directory inside the session.
	Name() string
	// CredentialFile is the file, inside the provider home, holding the
	// subscription credential.
	CredentialFile() string
	// Environment returns provider-specific variables pointing it at home.
	Environment(home string) []string
	// CheckAuth accepts only subscription session credentials, never API keys.
	CheckAuth(auth []byte) error
	// Version runs the provider's version query through run and returns its
	// self-reported version, refusing anything but the pinned release.
	Version(ctx context.Context, run func(args ...string) ([]byte, error)) (string, error)
	// Prepare writes any configuration the provider's command line names
	// into home, using run to query the pinned executable. It runs once,
	// after Version and before the credential handoff is acknowledged.
	Prepare(ctx context.Context, run func(args ...string) ([]byte, error), home string) error
	// Command builds the argument list (excluding the executable) for one
	// non-interactive turn under p, with the provider's home at home.
	Command(p Policy, home string) []string
	// Decode maps one line of the provider's event stream onto the closed
	// Event vocabulary. Anything it does not recognise is an error.
	Decode(line []byte) (Event, error)
}

// Policy is what a workload allows one provider turn to do.
type Policy struct {
	Model string
	// WorkDir is the provider's working directory.
	WorkDir string
	// Tools is the only tool server the provider may call. The provider
	// itself can neither write nor execute anything: its sandbox is
	// read-only, and a workload that edits does so through this server.
	Tools ToolServer
	// OutputSchema constrains the final message, written to LastMessage.
	OutputSchema, LastMessage string
}

// ToolServer is a private stdio MCP server started by the provider.
type ToolServer struct {
	Name    string
	Command string
	Args    []string
	Tools   []string
	// Writes are the tools in Tools that change something. With approvals
	// off, the provider runs a tool that is not read-only only when the
	// policy names it here; the server's handler is what confines it.
	Writes []string
}

// EventKind classifies a provider event.
type EventKind string

const (
	eventStarted   EventKind = "started"   // thread or turn started
	EventCompleted EventKind = "completed" // the turn completed
	EventFailed    EventKind = "failed"    // the provider reported failure
	EventItem      EventKind = "item"      // an item started, updated or completed
)

// ItemKind classifies an item event.
type ItemKind string

const (
	ItemReasoning ItemKind = "reasoning"
	ItemMessage   ItemKind = "agent_message"
	ItemTodo      ItemKind = "todo_list"
	ItemToolCall  ItemKind = "mcp_tool_call"
	ItemError     ItemKind = "error"
	// ItemOther is any item the provider emitted that no policy has reviewed,
	// such as command execution or web search. Every policy refuses it.
	ItemOther ItemKind = "other"
)

// Event is one decoded provider event. Raw events are checked and discarded,
// never saved or logged.
type Event struct {
	Kind EventKind
	Item ItemKind
	// Server and Tool name a tool call.
	Server, Tool string
}
