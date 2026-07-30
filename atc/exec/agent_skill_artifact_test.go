package exec

import (
	"archive/tar"
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/concourse/concourse/atc/compression"
)

func TestFrozenAgentSkillArtifactStreamsCanonicalReadOnlyTree(t *testing.T) {
	artifact, err := newFrozenAgentSkillArtifact(map[string]string{
		"skills/review/refs/rules.md": "rules",
		"skills/review/SKILL.md":      "instructions",
	})
	if err != nil {
		t.Fatalf("newFrozenAgentSkillArtifact: %v", err)
	}

	stream, err := artifact.StreamOut(context.Background(), ".", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	decoded, err := compression.NewGzipCompression().NewReader(stream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	defer decoded.Close()
	reader := tar.NewReader(decoded)
	var names []string
	var modes []int64
	var contents []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next tar entry: %v", err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %q: %v", header.Name, err)
		}
		names = append(names, header.Name)
		modes = append(modes, header.Mode)
		contents = append(contents, string(body))
	}
	if want := []string{"review/SKILL.md", "review/refs/rules.md"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tar names = %v, want %v", names, want)
	}
	if want := []int64{0o444, 0o444}; !reflect.DeepEqual(modes, want) {
		t.Fatalf("tar modes = %v, want %v", modes, want)
	}
	if want := []string{"instructions", "rules"}; !reflect.DeepEqual(contents, want) {
		t.Fatalf("tar contents = %v, want %v", contents, want)
	}
	if artifact.Handle() == "" || artifact.Source() != "frozen-agent-skills" {
		t.Fatalf("identity = handle %q source %q", artifact.Handle(), artifact.Source())
	}
}

func TestFrozenAgentSkillArtifactRejectsUnsafePath(t *testing.T) {
	if _, err := newFrozenAgentSkillArtifact(map[string]string{"skills/review/../secret": "no"}); err == nil {
		t.Fatal("unsafe skill path was accepted")
	}
}
