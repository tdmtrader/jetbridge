package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const sampleLog = `created owned live namespace brine-runtime-abc (not a record)
{"type":"run_start","timestamp":"t"}
{"type":"feature_start","timestamp":"t","name":"Volumes","source":"/repo/features/volume.feature"}
{"type":"step_end","timestamp":"t","scenario":"quick one","keyword":"Given","text":"a worker","status":"passed","duration_ms":60}
{"type":"scenario_end","timestamp":"t","name":"quick one","status":"passed","duration_ms":100,"manifest":"/repo/.brine"}
{"type":"step_end","timestamp":"t","scenario":"slow one","keyword":"Given","text":"a worker","status":"passed","duration_ms":8000}
{"type":"scenario_end","timestamp":"t","name":"slow one","status":"passed","duration_ms":9000,"manifest":"/repo/.brine"}
{"type":"feature_start","timestamp":"t","name":"Live volumes","source":"/repo/features/live/volume.feature"}
{"type":"scenario_end","timestamp":"t","name":"real pod","status":"failed","duration_ms":12000,"manifest":"/repo/live/.brine"}
{"type":"run_end","timestamp":"t","failed":1}
`

func TestScenariosAreAttributedToTheirFeatureAndManifest(t *testing.T) {
	timings, steps, err := collect(strings.NewReader(sampleLog))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1].phrase != "Given a worker" || steps[1].duration != 8*time.Second {
		t.Errorf("steps: %+v", steps)
	}
	if len(timings) != 3 {
		t.Fatalf("collected %d timings, want 3: %+v", len(timings), timings)
	}
	if timings[1].feature != "/repo/features/volume.feature" || timings[1].duration != 9*time.Second {
		t.Errorf("slow one: %+v", timings[1])
	}
	if timings[2].feature != "/repo/features/live/volume.feature" || timings[2].manifest != "/repo/live/.brine" {
		t.Errorf("real pod: %+v", timings[2])
	}
}

func TestReportLeadsWithWhereTheTimeWent(t *testing.T) {
	timings, steps, err := collect(strings.NewReader(sampleLog))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	report(&out, timings, steps, 2)
	text := out.String()

	for _, want := range []string{
		"3 scenarios, 21s summed scenario time",
		"/repo/live/.brine",
		"features/live/volume.feature",
		"12s  56.9%  features/live/volume.feature: real pod [failed]",
		"9s  42.7%  features/volume.feature: slow one",
		"By step phrase, slowest first (8s in steps, 13s outside steps):",
		"8s  38.2%     2 scenarios     4.03s mean  Given a worker",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "quick one") {
		t.Errorf("-top 2 still listed the third scenario:\n%s", text)
	}
}

func TestAnEmptyLogSaysSo(t *testing.T) {
	var out bytes.Buffer
	report(&out, nil, nil, 5)
	if !strings.Contains(out.String(), "no scenario_end records") {
		t.Errorf("empty report: %q", out.String())
	}
}
