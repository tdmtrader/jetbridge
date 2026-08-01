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

func TestOutputBuilderPromptRetainsSealedRecordAuthorityFallback(t *testing.T) {
	prompt := decorateOutputContractPrompt(
		"do it",
		true,
		map[string]SnapshotAuthority{
			"AGENT_INPUT_LOGS": {Type: "log-bundle/v1", Digest: "sha256:" + strings.Repeat("a", 64)},
		},
		map[string]RecordAuthority{
			"AGENT_OUTPUT_DIAGNOSIS": {Type: "diagnosis/v1", Schema: "sha256:" + strings.Repeat("b", 64)},
		},
	)
	for _, want := range []string{
		"# Structured output builder (platform-managed MCP)",
		"# Sealed record authority (platform-resolved)",
		"$AGENT_INPUT_LOGS_SNAPSHOT_DIGEST = sha256:" + strings.Repeat("a", 64),
		"$AGENT_OUTPUT_DIAGNOSIS_RECORD_SCHEMA = sha256:" + strings.Repeat("b", 64),
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
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
	path, cleanup, err := writeMCPConfig(map[string]string{
		brokerMCPName: brokerMCPURL,
	}, "parent-access-token")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), brokerMCPURL) ||
		!strings.Contains(string(contents), `"Authorization":"Bearer parent-access-token"`) {
		t.Fatalf("private config = %q, err = %v", contents, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("private config remains after cleanup: %v", err)
	}
}
