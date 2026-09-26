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
	// Command builds the argument list (excluding the executable) for one
	// non-interactive turn under p.
	Command(p Policy) []string
	// Decode maps one line of the provider's event stream onto the closed
	// Event vocabulary. Anything it does not recognise is an error.
	Decode(line []byte) (Event, error)
}

// Policy is what a workload allows one provider turn to do.
type Policy struct {
	Model string
	// WorkDir is the provider's working directory.
	WorkDir string
	// Edit gives the provider its file-edit tool, confined to WorkDir.
	// Without it the provider's sandbox is read-only. Shell, execution,
	// network and web search are never enabled.
	Edit bool
	// Tools is the only tool server the provider may call.
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
}

// EventKind classifies a provider event.
type EventKind string

const (
	EventStarted   EventKind = "started"   // thread or turn started
	EventCompleted EventKind = "completed" // the turn completed
	EventFailed    EventKind = "failed"    // the provider reported failure
	EventItem      EventKind = "item"      // an item started, updated or completed
)

// ItemKind classifies an item event.
type ItemKind string

const (
	ItemReasoning  ItemKind = "reasoning"
	ItemMessage    ItemKind = "agent_message"
	ItemTodo       ItemKind = "todo_list"
	ItemToolCall   ItemKind = "mcp_tool_call"
	ItemFileChange ItemKind = "file_change"
	ItemError      ItemKind = "error"
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
	// Changes lists the paths of a file change, as the provider reported them.
	Changes []FileChange
}

// FileChange is one path touched by a file-edit item.
type FileChange struct {
	Path string
	// Kind is add, delete or update.
	Kind string
	// MovePath is the destination of a move, if any.
	MovePath string
}
