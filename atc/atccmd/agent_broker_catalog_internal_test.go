package atccmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
)

func TestLoadAgentBrokerCatalogIsOptionalAndStrict(t *testing.T) {
	catalog, err := loadAgentBrokerCatalog("")
	if err != nil || catalog != nil {
		t.Fatalf("disabled catalog = (%v, %v), want (nil, nil)", catalog, err)
	}

	dir := t.TempDir()
	valid := filepath.Join(dir, "catalog.json")
	content := `{"profiles":[{
		"id":"balanced-review","revision":1,
		"selector":{"tier":"balanced","effort":"high"},
		"tools":["request_review"],
		"worker_image":"registry.example/broker@sha256:` + strings.Repeat("a", 64) + `",
		"adapter":{"name":"codex","version":"1.2.3"},
		"provider":{"name":"operator-provider","model":"exact-model"},
		"native_effort":"high",
		"instructions_digest":"sha256:` + strings.Repeat("b", 64) + `",
		"credential_slot":"shared",
		"limits":{"timeout":60000000000,"max_input_bytes":1024,"max_output_bytes":1024},
		"controls":{"read_only_workspace":true,"no_broker_recursion":true,"tests_unavailable":true,"native_output_schema":true,"ignores_user_config":true}
	}]}`
	if err := os.WriteFile(valid, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err = loadAgentBrokerCatalog(valid)
	if err != nil {
		t.Fatalf("load valid catalog: %v", err)
	}
	resolved, err := catalog.Resolve(
		broker.ToolRequestReview,
		broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
	)
	if err != nil || resolved.ID != "balanced-review" || resolved.Provider.Model != "exact-model" {
		t.Fatalf("resolved profile = (%#v, %v)", resolved, err)
	}

	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(strings.Replace(content, `{"profiles":`, `{"unknown":true,"profiles":`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentBrokerCatalog(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}
