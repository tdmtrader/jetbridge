package legacyplan

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeCompletedPublicRewritesNestedHarvestOnly(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","do":[{"id":"1","harvest":{"name":"push-branch","repo":"private/repo"}},{"id":"2","agent":{"name":"implement"}}]}`)

	decoded, err := DecodeCompletedPublic(&raw)
	if err != nil {
		t.Fatal(err)
	}

	if string(*decoded) != `{"do":[{"id":"1","retired_step":{"kind":"harvest","name":"push-branch"}},{"agent":{"name":"implement"},"id":"2"}],"id":"0"}` {
		t.Fatalf("decoded = %s", *decoded)
	}
}

func TestDecodeCompletedPublicPreservesOrdinaryHarvestFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","task":{"name":"ordinary","config":{"params":{"harvest":"keep"}}}}`)

	decoded, err := DecodeCompletedPublic(&raw)
	if err != nil {
		t.Fatal(err)
	}

	if string(*decoded) != `{"id":"0","task":{"config":{"params":{"harvest":"keep"}},"name":"ordinary"}}` {
		t.Fatalf("decoded = %s", *decoded)
	}
}

func TestDecodeCompletedPublicRejectsInvalidHarvestNode(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","harvest":"push-branch"}`)

	_, err := DecodeCompletedPublic(&raw)
	if err == nil {
		t.Fatal("expected invalid harvest node error")
	}
}

func TestDecodeCompletedPublicRejectsMalformedJSON(t *testing.T) {
	raw := json.RawMessage(`{"id":`)

	_, err := DecodeCompletedPublic(&raw)
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestDecodeCompletedPublicAcceptsNil(t *testing.T) {
	decoded, err := DecodeCompletedPublic(nil)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != nil {
		t.Fatalf("decoded = %s; want nil", *decoded)
	}
}

func TestContainsHarvestDetectsNestedPlanNode(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":"0","do":[{"id":"1","harvest":{"name":"push"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected nested harvest plan node")
	}
}

func TestContainsHarvestDoesNotTreatOrdinaryStringsAsSteps(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":"0","task":{"name":"ordinary","config":{"params":{"harvest":"keep"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("ordinary harvest field was treated as a plan node")
	}
}

func TestContainsHarvestRequiresStringIDAndObjectHarvest(t *testing.T) {
	cases := []string{
		`{"harvest":{"name":"push"}}`,
		`{"id":1,"harvest":{"name":"push"}}`,
		`{"id":"0","harvest":"push"}`,
	}
	for _, raw := range cases {
		found, err := ContainsHarvest([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Fatalf("ContainsHarvest(%s) = true", raw)
		}
	}
}

func TestContainsHarvestRejectsMalformedJSON(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":`))
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
	if found {
		t.Fatal("malformed JSON reported harvest")
	}
}

func TestErrActiveHarvestPlanContract(t *testing.T) {
	if !errors.Is(ErrActiveHarvestPlan, ErrActiveHarvestPlan) {
		t.Fatal("active harvest sentinel must support errors.Is")
	}
	if ErrActiveHarvestPlan.Error() != "legacy plan: harvest is retired and cannot execute" {
		t.Fatalf("unexpected sentinel: %q", ErrActiveHarvestPlan)
	}
}
