package concourse

import (
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/credentials"
	agentschema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . Client
type Client interface {
	URL() string
	HTTPClient() *http.Client
	Builds(Page) ([]atc.Build, Pagination, error)
	Build(buildID string) (atc.Build, bool, error)
	BuildEvents(buildID string) (Events, error)
	BuildResources(buildID int) (atc.BuildInputsOutputs, bool, error)
	ListBuildArtifacts(buildID string) ([]atc.WorkerArtifact, error)
	AbortBuild(buildID string) error
	BuildPlan(buildID int) (atc.PublicBuildPlan, bool, error)
	SaveWorker(atc.Worker, *time.Duration) (*atc.Worker, error)
	ListWorkers() ([]atc.Worker, error)
	GetInfo() (atc.Info, error)
	GetCLIReader(arch, platform string) (io.ReadCloser, http.Header, error)
	ListPipelines() ([]atc.Pipeline, error)
	ListAllJobs() ([]atc.Job, error)
	ListTeams() ([]atc.Team, error)
	FindTeam(teamName string) (Team, error)
	Team(teamName string) Team
	UserInfo() (atc.UserInfo, error)
	ListActiveUsersSince(since time.Time) ([]atc.User, error)
	GetWall() (atc.Wall, error)
	SetWall(atc.Wall) error
	ClearWall() error
	SetAgentUserCredential(req credentials.PutRequest) error
	AgentUserCredentialStatus() ([]credentials.Credential, error)
	// platform=true targets the §1.13 service user's credential (admin only).
	DeleteAgentUserCredential(kind string, platform bool) error
	AgentCostRollup(groupBy, since, until string) (costs.RollupResponse, error)
	AgentRunMetrics(limit int) ([]agentschema.RunMetrics, error)
	ListAgentTickets(filter tickets.ListFilter) ([]tickets.Ticket, error)
	CreateAgentTicket(req tickets.CreateRequest) (tickets.Ticket, error)
	GetAgentTicket(id int) (tickets.TicketDetail, bool, error)
	TransitionAgentTicket(id int, req tickets.TransitionRequest) (tickets.Ticket, error)
	DispatchAgentTicket(id int) (tickets.DispatchResponse, error)
	// Agent principals are admin-only: minted, listed, and revoked by admins.
	ListAgentPrincipals() ([]atc.AgentPrincipal, error)
	CreateAgentPrincipal(spec atc.AgentPrincipalCreateSpec) (atc.AgentPrincipalCreated, error)
	RevokeAgentPrincipal(id int) error
}

type client struct {
	connection internal.Connection //Deprecated
	httpAgent  internal.HTTPAgent
}

func NewClient(apiURL string, httpClient *http.Client, tracing bool) Client {
	return &client{
		connection: internal.NewConnection(apiURL, httpClient, tracing),
		httpAgent:  internal.NewHTTPAgent(apiURL, httpClient, tracing),
	}
}

func (client *client) URL() string {
	return client.connection.URL()
}

func (client *client) HTTPClient() *http.Client {
	return client.connection.HTTPClient()
}
