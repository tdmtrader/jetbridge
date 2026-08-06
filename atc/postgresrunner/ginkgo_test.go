package postgresrunner

import (
	"encoding/json"
	"testing"
)

func TestRunnerSuiteConfigRoundTrips(t *testing.T) {
	original := SuiteConfig{
		AdminDSN:     DefaultAdminDSN,
		RunID:        "t1786000000_p42_aabbccdd",
		TemplateName: "cc_tpl_t1786000000_p42_aabbccdd",
		CreatedUnix:  1786000000,
	}

	encoded, err := marshalSuiteConfig(original)
	if err != nil {
		t.Fatalf("marshal suite config: %v", err)
	}
	decoded, err := unmarshalSuiteConfig(encoded)
	if err != nil {
		t.Fatalf("unmarshal suite config: %v", err)
	}
	if decoded != original {
		t.Fatalf("round trip = %#v, want %#v", decoded, original)
	}

	if _, err := unmarshalSuiteConfig([]byte(`{"run_id":`)); err == nil {
		t.Fatal("malformed JSON was accepted")
	}

	invalid := original
	invalid.TemplateName = "cc_tpl_t1786000000_p42_eeeeeeee"
	encodedInvalid, err := json.Marshal(invalid)
	if err != nil {
		t.Fatalf("marshal invalid fixture: %v", err)
	}
	if _, err := unmarshalSuiteConfig(encodedInvalid); err == nil {
		t.Fatal("mismatched template/run pair was accepted")
	}
}
