package concourse

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Which operation kinds are woken by a NOTIFY, and which are content with their
// periodic tick, is a decision -- and a decision with no record is
// indistinguishable from an implementation that stopped halfway.
//
// Phase 7 shipped one producer of nine kinds. That is the right answer and the
// reason is real, but it was nowhere: a reviewer had to derive it, and the next
// person would have had to derive it again or, worse, "finish" it. The reasons
// live at hangar/output/operations.go, beside NotifyChannel, one line per kind.
// This is what stops them being prose: it reads the producers out of atc/db and
// fails if the tree and the record disagree in either direction.
//
// It also fails if a kind has no recorded line at all, so a tenth kind arrives
// with the question already asked.
const (
	operationsFile     = "hangar/output/operations.go"
	accelerationMarker = "//\tAcceleration: "
)

// acceleratedKinds is the record, restated where the check can read it.
var acceleratedKinds = map[string]bool{"reclaim_delete": true}

func TestOnlyTheRecordedOperationKindsAreAccelerated(t *testing.T) {
	root := repositoryRoot()

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(operationsFile)))
	if err != nil {
		t.Fatalf("reading %s: %v", operationsFile, err)
	}
	kinds := declaredOperationKinds(t, string(source))
	if len(kinds) < 9 {
		t.Fatalf("found only %d operation kinds in %s, which is fewer than the schema's nine; "+
			"the discovery failed and this rule would pass vacuously", len(kinds), operationsFile)
	}

	// Every kind carries its line, accelerated or not.
	for _, kind := range kinds {
		if !strings.Contains(string(source), accelerationMarker+kind+" ") {
			t.Errorf("%s has no `Acceleration: %s` line in %s.\n\nEvery kind states whether a "+
				"notification wakes it and why. A kind with no line is a question nobody asked, "+
				"which is how one producer of nine came to look unfinished.", kind, kind,
				operationsFile)
		}
	}

	// And the record matches the tree.
	found := acceleratedKindsInSource(t, root)
	for _, kind := range kinds {
		recorded, produced := acceleratedKinds[kind], found[kind]
		switch {
		case recorded && !produced:
			t.Errorf("%s is recorded as accelerated and nothing under atc/db issues a pg_notify "+
				"on its channel. A producer that went away silently costs latency and says "+
				"nothing: the work is still found by the tick, later.", kind)
		case !recorded && produced:
			t.Errorf("%s is accelerated by a producer under atc/db and the record in %s says it "+
				"is not. Add the `Acceleration: %s` line saying what the notification is worth, "+
				"and add the kind to acceleratedKinds here.", kind, operationsFile, kind)
		}
	}
	for kind := range acceleratedKinds {
		if !containsString(kinds, kind) {
			t.Errorf("acceleratedKinds names %q, which is not an operation kind; the line is "+
				"stale", kind)
		}
	}
}

var operationKindDeclaration = regexp.MustCompile(`Operation\w+\s+OperationKind\s*=\s*"([a-z_]+)"`)

func declaredOperationKinds(t *testing.T, source string) []string {
	t.Helper()

	var kinds []string
	for _, match := range operationKindDeclaration.FindAllStringSubmatch(source, -1) {
		kinds = append(kinds, match[1])
	}
	sort.Strings(kinds)

	return kinds
}

// acceleratedKindsInSource finds every kind some non-test file under atc/db
// notifies on.
//
// It reads the ARGUMENT to pg_notify rather than a channel string, because the
// channel name is derived from the kind and a literal would be the drift the
// derivation exists to prevent.
func acceleratedKindsInSource(t *testing.T, root string) map[string]bool {
	t.Helper()

	notify := regexp.MustCompile(`NotifyChannel\(output\.Operation(\w+)\)`)
	pgNotify := regexp.MustCompile(`pg_notify`)

	found := map[string]bool{}
	scanned := 0
	err := filepath.Walk(filepath.Join(root, "atc", "db"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		text := string(body)
		if !pgNotify.MatchString(text) {
			return nil
		}
		for _, match := range notify.FindAllStringSubmatch(text, -1) {
			found[operationKindOf(match[1])] = true
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking atc/db: %v", err)
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d files under atc/db; the walk failed and this rule would pass "+
			"vacuously", scanned)
	}

	return found
}

// operationKindOf turns the Go constant's suffix into the schema's member, so
// OperationReclaimDelete becomes reclaim_delete. The two spellings are the
// thing being compared, so the translation is here and stated rather than a
// second literal list.
func operationKindOf(suffix string) string {
	var out []rune
	for index, letter := range suffix {
		if index > 0 && letter >= 'A' && letter <= 'Z' {
			out = append(out, '_')
		}
		out = append(out, letter|0x20)
	}

	return string(out)
}
