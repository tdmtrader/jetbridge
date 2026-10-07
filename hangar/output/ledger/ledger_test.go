package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The classifier is what stands between the existing artifact daemon's
// destructive paths and a source some capture is about to seal. Every case here
// is a fact about a file, so the tests are against real directories.

const (
	execA = "33333333-3333-4333-8333-333333333333"
	heldA = execA + ".capture/result"
)

// writeMarker writes a step marker the way the daemon's control store does:
// an envelope whose checksum covers the body bytes.
func writeMarker(t *testing.T, dir, state, execution, output string) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"state": state, "execution": execution, "output": output,
		"node": "node-1", "pod_uid": "pod-1",
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	sum := sha256.Sum256(body)
	wrapper, err := json.Marshal(map[string]any{
		"record_version": recordVersion,
		"checksum":       hex.EncodeToString(sum[:]),
		"body":           json.RawMessage(body),
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	name := stepMarkerPrefix + execution + "." + output + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), wrapper, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
}

func controlDir(t *testing.T) (root, dir string) {
	t.Helper()

	root = t.TempDir()
	dir = filepath.Join(root, ControlDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating: %v", err)
	}

	return root, dir
}

func TestAHeldSourceIsRefusedAndAnUnmanagedOneIsNot(t *testing.T) {
	root, dir := controlDir(t)
	writeMarker(t, dir, "held", execA, "result")

	classifier := New(root)

	// The control FIRST: an ordinary path is unmanaged and the ordinary
	// behaviour is unchanged. "Everything is refused" is what a broken
	// classifier looks like, and it would pass every row below on its own.
	if class := classifier.Classify("some-other-step/output"); !class.Destructive() {
		t.Fatalf("an ordinary path classified %s", class)
	}

	for _, path := range []string{heldA, heldA + "/nested/file.txt"} {
		class := classifier.Classify(path)
		if class != Held {
			t.Errorf("%s classified %s", path, class)
		}
		if class.Destructive() {
			t.Errorf("%s would have been destroyed", path)
		}
		if err := classifier.Reason(path, class); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("the refusal for %s does not name it: %v", path, err)
		}
	}

	// A different output under the same execution is a different step
	// directory.
	if class := classifier.Classify(execA + ".capture/other"); !class.Destructive() {
		t.Errorf("another output classified %s", class)
	}
}

func TestAReleasedSourceBecomesDestructibleAndASealedOneDoesNot(t *testing.T) {
	root, dir := controlDir(t)

	for state, expected := range map[string]Class{
		"held":   Held,
		"sealed": Sealed,
		// A state this reader does not know is HELD. The writer may learn one,
		// and the safe reading of "I do not know what this means" over a
		// marker that exists is that something holds it.
		"quiesced": Held,
	} {
		writeMarker(t, dir, state, execA, "result")

		classifier := New(root)
		if class := classifier.Classify(heldA); class != expected {
			t.Errorf("state %q classified %s, expected %s", state, class, expected)
		}
	}

	// Released is the marker being gone.
	if err := os.Remove(filepath.Join(dir, stepMarkerPrefix+execA+".result.json")); err != nil {
		t.Fatal(err)
	}
	if class := New(root).Classify(heldA); class != Unmanaged {
		t.Errorf("a released step directory classified %s", class)
	}
}

func TestAnUnreadableLedgerRefusesEverythingRatherThanGuessing(t *testing.T) {
	root, dir := controlDir(t)
	writeMarker(t, dir, "held", execA, "result")

	// The control: readable, and an ordinary path proceeds.
	if class := New(root).Classify("ordinary/output"); !class.Destructive() {
		t.Fatalf("a readable ledger refused an ordinary path: %s", class)
	}

	for _, name := range []string{
		"a record that is not JSON",
		"a record from a newer daemon",
		"a torn record",
		"a quarantined record",
		"a directory that cannot be listed",
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := controlDir(t)
			writeMarker(t, dir, "held", execA, "result")
			corruptIn(t, dir, name)

			classifier := New(root)
			// EVERY path, not just the held one. The ledger is the only thing
			// that could have said which paths are held, so with it unreadable
			// nothing is known to be safe.
			for _, path := range []string{heldA, "an-entirely-unrelated-step/output", ""} {
				class := classifier.Classify(path)
				if class != Unavailable {
					t.Errorf("%s classified %s with an unreadable ledger", path, class)
				}
				if class.Destructive() {
					t.Errorf("%s would have been destroyed on a guess", path)
				}
				if err := classifier.Reason(path, class); err == nil ||
					!strings.Contains(err.Error(), "could not be read") {
					t.Errorf("the refusal does not say the ledger was unreadable: %v", err)
				}
			}
		})
	}
}

func corruptIn(t *testing.T, dir, how string) {
	t.Helper()

	path := filepath.Join(dir, stepMarkerPrefix+execA+".result.json")
	switch how {
	case "a record that is not JSON":
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
	case "a record from a newer daemon":
		if err := os.WriteFile(path,
			[]byte(`{"record_version":"hangar-output-control-record-v2","body":{}}`), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
	case "a torn record":
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		// The same envelope with its body changed under the checksum: what a
		// crash, a truncated restore or a flipped bit leaves.
		torn := strings.Replace(string(raw), `"held"`, `"sealed"`, 1)
		if err := os.WriteFile(path, []byte(torn), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
	case "a quarantined record":
		if err := os.MkdirAll(filepath.Join(dir, quarantineDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, filepath.Join(dir, quarantineDirName, filepath.Base(path))); err != nil {
			t.Fatal(err)
		}
	case "a directory that cannot be listed":
		// Mode bits do not apply to uid 0, and CI runs as root: chmod 000 there
		// leaves the directory perfectly readable and this arm would assert
		// nothing. The other two arms cover the unavailable path on every
		// platform, so skipping is honest rather than a hole.
		if os.Geteuid() == 0 {
			t.Skip("running as root, which ignores the mode bits this arm depends on")
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	default:
		t.Fatalf("no corruption named %q", how)
	}
}

// A node with no output plane at all is not a node whose every delete is
// refused. The absence of the directory is a real answer; being unable to read
// it is not.
func TestANodeWithNoOutputPlaneIsUnchanged(t *testing.T) {
	classifier := New(t.TempDir())

	if class := classifier.Classify("any/path"); !class.Destructive() {
		t.Errorf("a node with no output plane classified %s", class)
	}
}

// The read-only half, structurally. A writer here would be the two authorities
// becoming one.
func TestThisPackageOffersNoWayToChangeALedger(t *testing.T) {
	source, err := os.ReadFile("ledger.go")
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	body := string(source)

	for _, forbidden := range []string{
		"os.WriteFile", "os.Create", "os.Remove", "os.RemoveAll", "os.Rename",
		"os.OpenFile", "os.Mkdir", "os.MkdirAll", "os.Chmod",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("this package calls %s. It is the READER of a ledger another process owns; "+
				"one authority writes it and this one respects it, and a mutator here is those "+
				"two becoming one", forbidden)
		}
	}
	if !strings.Contains(body, "os.ReadFile") {
		t.Error("this package reads no file, so this guard is asserting nothing")
	}
}

// A hold established a moment before a delete arrives is seen.
//
// This is the race the classifier exists for, and it is the row CI failed on
// when the answers were cached: the first version keyed its cache on the
// control directory's mtime, whose granularity on Linux is coarse enough that a
// hold written in the same tick as the previous read looks like no change at
// all. It answered "unmanaged" for a hold that already existed. There is no
// cache now, and this row is what keeps one from coming back.
func TestAHoldEstablishedAfterTheLastReadIsSeenImmediately(t *testing.T) {
	root, dir := controlDir(t)
	classifier := New(root)

	if class := classifier.Classify(heldA); !class.Destructive() {
		t.Fatalf("an empty ledger classified %s", class)
	}

	writeMarker(t, dir, "held", execA, "result")

	if class := classifier.Classify(heldA); class != Held {
		t.Errorf("a hold established after the previous read classified %s. The cache is keyed "+
			"on the control directory's modification time for exactly this case", class)
	}
}

// Destroying the PARENT of a held incarnation destroys the held incarnation.
//
// This is the direction the first version of this package did not answer, and
// the caller that asks it is the one that matters most: the Reaper deletes
// `steps/<handle>` -- the step directory -- while a hold names
// `<execution>.capture/<output>` BENEATH it. A classifier that only
// answered "is this path at or under a held incarnation" told the Reaper that
// the directory containing a held source was unmanaged, and the source went
// with it.
//
// The sweeper asks the same question about the same path, on a timer, with
// nobody watching.
func TestDestroyingAnAncestorOfAHeldIncarnationIsRefused(t *testing.T) {
	root, dir := controlDir(t)
	writeMarker(t, dir, "held", "exec-1", "result")

	classifier := New(root)

	// The incarnation itself, and everything beneath it: the rows that already
	// held. They are the control -- if these stop working the ancestor rule was
	// bought with the descendant one.
	for _, held := range []string{"exec-1.capture/result", "exec-1.capture/result/inner/file"} {
		if class := classifier.Classify(held); class != Held {
			t.Errorf("%s classified %s, expected held", held, class)
		}
	}

	// The step directory the Reaper and the sweeper name.
	if class := classifier.Classify("exec-1.capture"); class != Held {
		t.Errorf("the step directory containing a held source classified %s; deleting it "+
			"destroys the held source just as surely as deleting the source", class)
	}

	// And the refusal has to say something, because a caller that cannot
	// explain why it did not delete is a stuck sweep nobody can diagnose.
	if err := classifier.Reason("exec-1.capture", Held); err == nil {
		t.Error("an ancestor refusal carried no reason")
	}

	// A SIBLING that merely shares a prefix is not held. This is the row that
	// keeps the ancestor rule from becoming "anything that looks similar":
	// exec-1.capture and exec-1.captured are different directories.
	for _, free := range []string{"exec-1.captured", "exec-1.capturex", "exec-2.capture", "exec-1"} {
		if class := classifier.Classify(free); class != Unmanaged {
			t.Errorf("%s classified %s, expected unmanaged: an unrelated step that shares "+
				"characters with a held one is not held", free, class)
		}
	}
}
