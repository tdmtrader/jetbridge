package runner

import (
	"os"
	"strings"
	"testing"
)

func TestAdmittedMCPServersAddsOnlyServerOwnedOutputBuilder(t *testing.T) {
	servers, enabled, err := admittedMCPServers("1", map[string]string{"review": "http://127.0.0.1:7781/mcp"})
	if err != nil || !enabled {
		t.Fatalf("admit = (%#v, %t, %v)", servers, enabled, err)
	}
	if servers[outputBuilderMCPName] != outputBuilderMCPURL || servers["review"] != "http://127.0.0.1:7781/mcp" {
		t.Fatalf("servers = %#v", servers)
	}
	for _, authored := range []map[string]string{{outputBuilderMCPName: "http://127.0.0.1:7781/mcp"}, {"forged": outputBuilderMCPURL}} {
		if _, _, err := admittedMCPServers("", authored); err == nil {
			t.Fatalf("authored managed collision accepted: %#v", authored)
		}
	}
}

func TestAdmitBrokerMCPRejectsImpersonationAndInjectsOnlyServerMarker(t *testing.T) {
	for _, authored := range []map[string]string{{"agent-broker": "http://127.0.0.1:7784/mcp"}, {"other": "http://127.0.0.1:7784/mcp"}} {
		if _, _, err := admittedMCPServers("", authored); err == nil {
			t.Fatalf("authored broker MCP = %#v was admitted", authored)
		}
	}
	servers, enabled, err := admitBrokerMCP("1", map[string]string{"review": "http://127.0.0.1:7781/mcp"})
	if err != nil || !enabled || servers["agent-broker"] != "http://127.0.0.1:7784/mcp" {
		t.Fatalf("broker MCP = %#v, enabled = %v, err = %v", servers, enabled, err)
	}
}

func TestWriteMCPConfigCleansPrivateDirectory(t *testing.T) {
	path, cleanup, err := writeMCPConfig(map[string]string{"review": "http://127.0.0.1:7781/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), "127.0.0.1:7781/mcp") {
		t.Fatalf("private config = %q, err = %v", contents, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("private config remains after cleanup: %v", err)
	}
}
