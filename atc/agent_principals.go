package atc

import "github.com/concourse/concourse/agent/api/principals"

// Agent principal wire types. These are aliased to the canonical types in
// agent/api/principals so the go-concourse client can decode the admin
// principals API responses without maintaining a separate mirror. The
// principals package imports only the standard library, so this import is a
// leaf dependency and introduces no cycle.

// AgentPrincipal is one agent_principals row returned by the admin
// principals API (GET/POST /api/v1/agent/principals).
type AgentPrincipal = principals.Principal

// AgentPrincipalCreateSpec is the POST body for minting a principal.
type AgentPrincipalCreateSpec = principals.CreateSpec

// AgentPrincipalCreated is the POST response: the principal plus the
// one-time token, surfaced exactly once on creation.
type AgentPrincipalCreated = principals.CreatedResponse
