package exec

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	"github.com/concourse/concourse/atc/compression"
)

const maxFrozenAgentSkillBytes = 512 << 10

// frozenAgentSkillArtifact is a deterministic, immutable implementation of a
// compiled agent skill tree. It deliberately has no repository entry: skills
// are execution authority, never an authored DAG input or a visible artifact.
type frozenAgentSkillArtifact struct {
	tar    []byte
	handle string
}

func newFrozenAgentSkillArtifact(files map[string]string) (*frozenAgentSkillArtifact, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("frozen agent skills are required when skills are selected")
	}
	names := make([]string, 0, len(files))
	bytesTotal := 0
	for name, content := range files {
		if err := validateFrozenAgentSkillPath(name); err != nil {
			return nil, err
		}
		if len(content) > maxFrozenAgentSkillBytes-bytesTotal {
			return nil, fmt.Errorf("frozen agent skills exceed %d bytes", maxFrozenAgentSkillBytes)
		}
		bytesTotal += len(content)
		names = append(names, name)
	}
	sort.Strings(names)

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, name := range names {
		content := files[name]
		header := &tar.Header{
			// The input itself mounts at .../skills, so tar paths are relative
			// to that root. Keeping the logical skills/ prefix here would create
			// an unintended .../skills/skills subtree.
			Name: strings.TrimPrefix(name, "skills/"), Mode: 0o444, Size: int64(len(content)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Unix(0, 0).UTC(), ChangeTime: time.Unix(0, 0).UTC(),
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write frozen agent skill header %q: %w", name, err)
		}
		if _, err := io.WriteString(writer, content); err != nil {
			return nil, fmt.Errorf("write frozen agent skill %q: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finalize frozen agent skills: %w", err)
	}
	raw := archive.Bytes()
	digest := sha256.Sum256(raw)
	return &frozenAgentSkillArtifact{tar: append([]byte(nil), raw...), handle: "frozen-agent-skills:" + hex.EncodeToString(digest[:])}, nil
}

func (artifact *frozenAgentSkillArtifact) Handle() string { return artifact.handle }

func (artifact *frozenAgentSkillArtifact) Source() string { return "frozen-agent-skills" }

func (artifact *frozenAgentSkillArtifact) StreamOut(_ context.Context, subpath string, requested compression.Compression) (io.ReadCloser, error) {
	if subpath != "" && subpath != "." {
		return nil, fmt.Errorf("frozen agent skills: subpath is not supported")
	}
	var output bytes.Buffer
	if requested == nil || requested.Encoding() == compression.RawEncoding {
		output.Write(artifact.tar)
		return io.NopCloser(bytes.NewReader(output.Bytes())), nil
	}
	var writer io.WriteCloser
	switch requested.Encoding() {
	case compression.GzipEncoding:
		writer = gzip.NewWriter(&output)
	case compression.ZstdEncoding:
		compressed, err := zstd.NewWriter(&output)
		if err != nil {
			return nil, err
		}
		writer = compressed
	case compression.S2Encoding:
		writer = s2.NewWriter(&output)
	default:
		return nil, fmt.Errorf("frozen agent skills: unsupported compression %q", requested.Encoding())
	}
	if _, err := writer.Write(artifact.tar); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}

func validateFrozenAgentSkillPath(name string) error {
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) {
		return fmt.Errorf("frozen agent skill path %q is unsafe", name)
	}
	parts := strings.Split(name, "/")
	if len(parts) < 3 || parts[0] != "skills" || parts[1] == "" {
		return fmt.Errorf("frozen agent skill path %q must be below skills/<name>", name)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return fmt.Errorf("frozen agent skill path %q is unsafe", name)
		}
	}
	return nil
}
