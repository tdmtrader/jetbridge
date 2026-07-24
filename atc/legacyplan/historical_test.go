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

func TestDecodeCompletedPublicPreservesObjectValuedOpaqueTaskData(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","task":{"name":"ordinary","config":{"image_resource":{"params":{"payload":{"id":"customer-id","harvest":{"mode":"keep"}}}}},"vars":{"payload":{"id":"vars-id","harvest":{"mode":"also-keep"}}}}}`)

	decoded, err := DecodeCompletedPublic(&raw)
	if err != nil {
		t.Fatal(err)
	}

	if string(*decoded) != `{"id":"0","task":{"config":{"image_resource":{"params":{"payload":{"harvest":{"mode":"keep"},"id":"customer-id"}}}},"name":"ordinary","vars":{"payload":{"harvest":{"mode":"also-keep"},"id":"vars-id"}}}}` {
		t.Fatalf("decoded = %s", *decoded)
	}
}

func TestDecodeCompletedPublicRewritesHarvestInAcrossTemplate(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","across":{"vars":[],"substep_template":"{\"id\":\"1\",\"harvest\":{\"name\":\"push\"}}"}}`)

	decoded, err := DecodeCompletedPublic(&raw)
	if err != nil {
		t.Fatal(err)
	}

	if string(*decoded) != `{"across":{"substep_template":"{\"id\":\"1\",\"retired_step\":{\"kind\":\"harvest\",\"name\":\"push\"}}","vars":[]},"id":"0"}` {
		t.Fatalf("decoded = %s", *decoded)
	}
}

func TestDecodeCompletedPublicRejectsMalformedAcrossTemplate(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","across":{"vars":[],"substep_template":"{\"id\":"}}`)

	_, err := DecodeCompletedPublic(&raw)
	if err == nil {
		t.Fatal("expected malformed across template error")
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

func TestContainsHarvestDoesNotTreatObjectValuedOpaqueTaskDataAsSteps(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":"0","task":{"name":"ordinary","config":{"image_resource":{"params":{"payload":{"id":"customer-id","harvest":{"mode":"keep"}}}}},"vars":{"payload":{"id":"vars-id","harvest":{"mode":"also-keep"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("object-valued opaque harvest data was treated as a plan node")
	}
}

func TestContainsHarvestDetectsAcrossTemplatePlan(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":"0","across":{"vars":[],"substep_template":"{\"id\":\"1\",\"harvest\":{\"name\":\"push\"}}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected harvest plan in across template")
	}
}

func TestContainsHarvestTraversesEveryDirectPlanChildArm(t *testing.T) {
	const harvest = `{"id":"h","harvest":{"name":"push"}}`
	cases := map[string]string{
		"do":                        `{"id":"0","do":[` + harvest + `]}`,
		"retry":                     `{"id":"0","retry":[` + harvest + `]}`,
		"in_parallel steps":         `{"id":"0","in_parallel":{"steps":[` + harvest + `]}}`,
		"on_success step":           `{"id":"0","on_success":{"step":` + harvest + `,"on_success":{"id":"next"}}}`,
		"on_success next":           `{"id":"0","on_success":{"step":{"id":"step"},"on_success":` + harvest + `}}`,
		"on_failure step":           `{"id":"0","on_failure":{"step":` + harvest + `,"on_failure":{"id":"next"}}}`,
		"on_failure next":           `{"id":"0","on_failure":{"step":{"id":"step"},"on_failure":` + harvest + `}}`,
		"on_abort step":             `{"id":"0","on_abort":{"step":` + harvest + `,"on_abort":{"id":"next"}}}`,
		"on_abort next":             `{"id":"0","on_abort":{"step":{"id":"step"},"on_abort":` + harvest + `}}`,
		"on_error step":             `{"id":"0","on_error":{"step":` + harvest + `,"on_error":{"id":"next"}}}`,
		"on_error next":             `{"id":"0","on_error":{"step":{"id":"step"},"on_error":` + harvest + `}}`,
		"ensure step":               `{"id":"0","ensure":{"step":` + harvest + `,"ensure":{"id":"next"}}}`,
		"ensure next":               `{"id":"0","ensure":{"step":{"id":"step"},"ensure":` + harvest + `}}`,
		"try step":                  `{"id":"0","try":{"step":` + harvest + `}}`,
		"timeout step":              `{"id":"0","timeout":{"step":` + harvest + `}}`,
		"get public image get":      `{"id":"0","get":{"image_get_plan":` + harvest + `}}`,
		"get public image check":    `{"id":"0","get":{"image_check_plan":` + harvest + `}}`,
		"put public image get":      `{"id":"0","put":{"image_get_plan":` + harvest + `}}`,
		"put public image check":    `{"id":"0","put":{"image_check_plan":` + harvest + `}}`,
		"check public image get":    `{"id":"0","check":{"image_get_plan":` + harvest + `}}`,
		"check public image check":  `{"id":"0","check":{"image_check_plan":` + harvest + `}}`,
		"get private image get":     `{"id":"0","get":{"image":{"get_plan":` + harvest + `}}}`,
		"get private image check":   `{"id":"0","get":{"image":{"check_plan":` + harvest + `}}}`,
		"put private image get":     `{"id":"0","put":{"image":{"get_plan":` + harvest + `}}}`,
		"put private image check":   `{"id":"0","put":{"image":{"check_plan":` + harvest + `}}}`,
		"check private image get":   `{"id":"0","check":{"image":{"get_plan":` + harvest + `}}}`,
		"check private image check": `{"id":"0","check":{"image":{"check_plan":` + harvest + `}}}`,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			found, err := ContainsHarvest([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatal("expected harvest in direct plan child")
			}
		})
	}
}

func TestContainsHarvestRejectsMalformedAcrossTemplate(t *testing.T) {
	found, err := ContainsHarvest([]byte(`{"id":"0","across":{"vars":[],"substep_template":"{\"id\":"}}`))
	if err == nil {
		t.Fatal("expected malformed across template error")
	}
	if found {
		t.Fatal("malformed across template reported harvest")
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
