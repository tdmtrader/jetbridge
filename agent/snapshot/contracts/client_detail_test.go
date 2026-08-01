package contracts_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// Each of these is a mistake a first user makes with `fly agent snapshots
// create`, and each must come back with a message naming the actual problem.
func TestContractFailuresCarryClientDetail(t *testing.T) {
	tests := map[string]struct {
		typeRef string
		files   map[string]string
		want    string
		// mustNotContain catches a mark that discloses more than it should —
		// e.g. a %v of the wrapped dependency error smuggled into the detail.
		// A `want` substring check alone cannot catch this: the leaked text
		// often contains `want` as a substring too (a decoder error naming
		// the field it choked on still contains the file name), so the
		// mutation that leaks it can pass a want-only assertion.
		mustNotContain string
	}{
		"missing declared document": {
			typeRef: "work-item/v1",
			files:   map[string]string{"task.md": "# not the declared document"},
			want:    `required regular file "work-item.json" is missing`,
		},
		"document fails its own rules": {
			typeRef: "work-item/v1",
			files: map[string]string{"work-item.json": `{
				"schema_version":"1.0.0","adapter":"","external_id":"x","revision":"1",
				"captured_at":"2026-08-01T12:00:00Z","title":"t","body":"b"}`},
			want: "adapter is required",
		},
		// The marked detail names the document that failed to decode, not the
		// decoder's own message — the dependency's text stays out of the
		// disclosable channel.
		"unknown field": {
			typeRef: "work-item/v1",
			files: map[string]string{"work-item.json": `{
				"schema_version":"1.0.0","adapter":"a","external_id":"x","revision":"1",
				"captured_at":"2026-08-01T12:00:00Z","title":"t","body":"b","extra":1}`},
			want: "decode work-item.json",
			// encoding/json's own text for this failure is `json: unknown
			// field "extra"` — it names a Go struct field, which is not one
			// of the three disclosable sources (see client_detail.go). If a
			// future edit restates err with %v instead of wrapping it with
			// WrapClientDetailf, this is what must catch it.
			mustNotContain: "unknown field",
		},
		// documentValidator[T] is the generic validator behind four types
		// (upgrade-request/v1, upgrade-report/v1, question/v1,
		// human-answer/v1). This proves the mark on its shared Validate
		// rather than re-testing workItemValidator's hand-written twin.
		"generic document validator's own rule fails": {
			typeRef: "question/v1",
			files: map[string]string{"question.json": `{
				"schema_version":"1.0.0","prompt":"","context":"c"}`},
			want: "prompt is required",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			registry, err := contracts.NewRegistry()
			if err != nil {
				t.Fatal(err)
			}
			ref, err := snapshot.ParseTypeRef(test.typeRef)
			if err != nil {
				t.Fatal(err)
			}
			validator, err := registry.Lookup(ref)
			if err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			for name, contents := range test.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()

			validationContext, err := snapshot.NewValidationContext(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, validationErr := validator.AdmitForSeal(context.Background(), root, validationContext)
			if validationErr == nil {
				t.Fatal("expected validation to fail")
			}
			detail, ok := snapshot.ClientDetail(validationErr)
			if !ok {
				t.Fatalf("no client detail on %v", validationErr)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.want)
			}
			if test.mustNotContain != "" && strings.Contains(detail, test.mustNotContain) {
				t.Fatalf("detail = %q leaked %q — a dependency's own text reached the disclosable channel", detail, test.mustNotContain)
			}
			// This is a tripwire for future marking sites, not a live check
			// today: os.Root errors carry only the relative entry name, and
			// ClientDetail returns only the mark's own detail text, so `dir`
			// (an absolute host scratch path) cannot appear in any of the
			// cases above no matter what they do. It stays here so a future
			// site that DOES have a route to a host path — e.g. one built
			// from ValidateTempDir's %q tempDir — trips it.
			if strings.Contains(detail, dir) {
				t.Fatalf("detail leaked the host scratch path: %q", detail)
			}
		})
	}
}
