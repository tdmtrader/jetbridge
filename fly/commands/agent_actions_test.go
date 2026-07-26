package commands

import "testing"

func TestActionsActionToMode(t *testing.T) {
	for _, test := range []struct {
		action string
		mode   string
		ok     bool
	}{
		{action: "suppress", mode: "suppressed", ok: true},
		{action: "resume", mode: "active", ok: true},
		{action: "active", mode: "active", ok: true},
		{action: "off", ok: false},
		{action: "pause", ok: false},
		{action: "", ok: false},
	} {
		mode, ok := actionsActionToMode(test.action)
		if ok != test.ok || mode != test.mode {
			t.Errorf("actionsActionToMode(%q) = (%q, %t), want (%q, %t)", test.action, mode, ok, test.mode, test.ok)
		}
	}
}
