package review

import (
	"encoding/json"
	"strings"
	"testing"
)

const cleanAssessment = `{"summary":"No defects found.","complete":true,"reviewed_files":["parser.go","deleted.txt"],"limitations":[],"findings":[]}`
const locatedAssessment = `{"summary":"Index is outside the intended byte.","complete":true,"reviewed_files":["parser.go","deleted.txt"],"limitations":[],"findings":[{"id":"model-id","severity":"high","dimension":"correctness","title":"Check the length","explanation":"One-byte input panics at index 1.","recommendation":"Validate length before indexing.","location":{"side":"head","path":"parser.go","start_line":2,"end_line":2}}]}`

func reportTest(t *testing.T, b *Bundle, raw string) (*Report, error) {
	t.Helper()
	return BuildReport(b, []byte(raw), Metadata{ModelRequested: "test-model", CodexVersion: "test-cli", Profile: DefaultProfile()})
}

func TestReportVerdictsAndIdentity(t *testing.T) {
	b := captureTest(t)
	for _, tc := range []struct{ raw, verdict string }{
		{cleanAssessment, "no_findings"},
		{locatedAssessment, "findings"},
		{strings.Replace(cleanAssessment, `"complete":true`, `"complete":false`, 1), "incomplete"},
		{strings.Replace(cleanAssessment, `"parser.go","deleted.txt"`, `"parser.go"`, 1), "incomplete"},
	} {
		r, err := reportTest(t, b, tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if r.Verdict != tc.verdict {
			t.Fatalf("got %s want %s", r.Verdict, tc.verdict)
		}
		if r.Provenance.InputDigest != b.Digest || r.Provenance.HeadCommit != b.Manifest.HeadCommit {
			t.Fatal("provenance is not from input")
		}
		if !strings.Contains(strings.Join(r.Assessment.Limitations, " "), "not executed") {
			t.Fatal("missing static-review limitation")
		}
		if len(r.Assessment.Findings) > 0 && r.Assessment.Findings[0].ID != "f-001" {
			t.Fatal("model assigned the identity")
		}
		data, _ := json.Marshal(r)
		roundtrip, err := ParseReport(data, b)
		if err != nil {
			t.Fatal(err)
		}
		if roundtrip.Verdict != tc.verdict {
			t.Fatal("roundtrip changed result")
		}
		md := r.Markdown()
		if !strings.Contains(md, r.Assessment.Summary) {
			t.Fatal("rendering omits summary")
		}
	}
}

func TestRejectInvalidAssessments(t *testing.T) {
	b := captureTest(t)
	for name, raw := range map[string]string{
		"missing": "", "malformed": "{", "unknown": strings.Replace(cleanAssessment, `"complete":true`, `"complete":true,"extra":1`, 1),
		"missing-required": strings.Replace(cleanAssessment, `"complete":true,`, "", 1),
		"null":             strings.Replace(cleanAssessment, `"findings":[]`, `"findings":null`, 1),
		"traversal":        strings.Replace(locatedAssessment, `"path":"parser.go"`, `"path":"../parser.go"`, 1),
		"absolute":         strings.Replace(locatedAssessment, `"path":"parser.go"`, `"path":"/parser.go"`, 1),
		"invalid-line":     strings.Replace(locatedAssessment, `"end_line":2`, `"end_line":999`, 1),
		"reversed-lines":   strings.Replace(locatedAssessment, `"end_line":2`, `"end_line":1`, 1),
		"unknown-side":     strings.Replace(locatedAssessment, `"side":"head"`, `"side":"other"`, 1),
		"unknown-file":     strings.Replace(cleanAssessment, `"deleted.txt"`, `"invented.go"`, 1),
		"unknown-severity": strings.Replace(locatedAssessment, `"severity":"high"`, `"severity":"critical"`, 1),
		"trailing-json":    cleanAssessment + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := reportTest(t, b, raw); err == nil {
				t.Fatal("accepted invalid assessment")
			}
		})
	}
}

func TestDeletedLocationAndForgedProvenance(t *testing.T) {
	b := captureTest(t)
	raw := strings.ReplaceAll(locatedAssessment, `"side":"head","path":"parser.go","start_line":2,"end_line":2`, `"side":"base","path":"deleted.txt","start_line":1,"end_line":1`)
	r, err := reportTest(t, b, raw)
	if err != nil {
		t.Fatal(err)
	}
	r.Provenance.InputDigest = strings.Repeat("0", 64)
	data, _ := json.Marshal(r)
	if _, err := ParseReport(data, b); err == nil {
		t.Fatal("accepted forged provenance")
	}
	r, err = reportTest(t, b, raw)
	if err != nil {
		t.Fatal(err)
	}
	r.Verdict = "no_findings"
	data, _ = json.Marshal(r)
	if _, err := ParseReport(data, b); err == nil {
		t.Fatal("accepted false clean verdict")
	}
}
