// Module boundary keeps this fixture-only probe out of production package
// discovery: main.go imports the separate Brine module. The documented runner builds main.go from
// the existing Brine module, whose dependency graph owns this auth fixture.
module github.com/concourse/concourse/hack/mcp-oauth-probe

go 1.25.6
