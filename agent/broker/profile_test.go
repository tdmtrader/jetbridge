package broker_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
)

func TestCatalogResolvesNeutralSelectorsDeterministically(t *testing.T) {
	profile := validProfile()
	catalog, err := broker.NewCatalog([]broker.Profile{profile})
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	got, err := catalog.Resolve(broker.ToolConsultAgent, broker.Selector{
		Tier: broker.TierBalanced, Effort: broker.EffortHigh,
	})
	if err != nil {
		t.Fatalf("Resolve(): %v", err)
	}
	if got.ID != profile.ID || got.Provider.Model != profile.Provider.Model {
		t.Fatalf("Resolve() = %#v, want %#v", got, profile)
	}
	if got.Digest == "" {
		t.Fatal("resolved profile has no immutable digest")
	}
	visible := catalog.Visible()
	if len(visible) != 1 || visible[0].Tier != broker.TierBalanced ||
		visible[0].Effort != broker.EffortHigh {
		t.Fatalf("Visible() = %#v", visible)
	}
}

func TestCatalogRejectsInvalidOrAmbiguousProfiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*broker.Profile)
		want  string
	}{
		{"provider tier", func(p *broker.Profile) { p.Selector.Tier = "sol" }, "tier"},
		{"low effort", func(p *broker.Profile) { p.Selector.Effort = "low" }, "effort"},
		{"unknown tool", func(p *broker.Profile) { p.Tools = []broker.Tool{"delegate_task"} }, "tool"},
		{"mutable image", func(p *broker.Profile) { p.WorkerImage = "example/broker:latest" }, "pinned"},
		{"missing instruction digest", func(p *broker.Profile) { p.InstructionsDigest = "" }, "instructions"},
		{"missing credential slot", func(p *broker.Profile) { p.CredentialSlot = "" }, "credential"},
		{"no deadline", func(p *broker.Profile) { p.Limits.Timeout = 0 }, "timeout"},
		{"cursor claims schema control", func(p *broker.Profile) {
			p.Adapter.Name = broker.AdapterCursor
			p.Controls.NativeOutputSchema = true
		}, "output schema"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profile := validProfile()
			tc.setup(&profile)
			_, err := broker.NewCatalog([]broker.Profile{profile})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("NewCatalog() error = %v, want %q", err, tc.want)
			}
		})
	}

	duplicate := validProfile()
	duplicate.ID = "other"
	if _, err := broker.NewCatalog([]broker.Profile{validProfile(), duplicate}); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate selector error = %v", err)
	}
}

func TestProfileDigestChangesWithAuthorityButNotPurpose(t *testing.T) {
	first := validProfile()
	second := first
	second.Purpose = "operator copy change"
	a, err := broker.NewCatalog([]broker.Profile{first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := broker.NewCatalog([]broker.Profile{second})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := a.Resolve(broker.ToolConsultAgent, first.Selector)
	two, _ := b.Resolve(broker.ToolConsultAgent, second.Selector)
	if one.Digest != two.Digest {
		t.Fatal("advisory purpose changed the authority digest")
	}
	second.Provider.Model = "exact-model-2"
	c, err := broker.NewCatalog([]broker.Profile{second})
	if err != nil {
		t.Fatal(err)
	}
	three, _ := c.Resolve(broker.ToolConsultAgent, second.Selector)
	if one.Digest == three.Digest {
		t.Fatal("exact model did not change the authority digest")
	}
}

func validProfile() broker.Profile {
	return broker.Profile{
		ID:       "balanced-high-v1",
		Revision: 1,
		Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:    []broker.Tool{broker.ToolRequestReview, broker.ToolConsultAgent},
		Purpose:  "careful general review",
		WorkerImage: "registry.example/broker@sha256:" +
			strings.Repeat("a", 64),
		Adapter:            broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:           broker.ProviderSpec{Name: "openai", Model: "exact-model"},
		NativeEffort:       "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot:     "shared-openai",
		Limits: broker.Limits{
			Timeout: 15 * time.Minute, MaxInputBytes: 8 << 20, MaxOutputBytes: 1 << 20,
		},
		Controls: broker.Controls{
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeOutputSchema: true, IgnoresUserConfig: true,
		},
	}
}
