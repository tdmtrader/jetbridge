package deploy

import (
	"os"
	"strings"
	"testing"
)

// TestDevCapabilityImageShipsBothFacades checks the image assembly boundary:
// the interactive entrypoint remains dev-mcp while the deterministic CLI from
// the same ci-agent source stage is available to the later hermetic task.
func TestDevCapabilityImageShipsBothFacades(t *testing.T) {
	dockerfile, err := os.ReadFile("Dockerfile.mcp-dev-concourse")
	if err != nil {
		t.Fatal(err)
	}
	source := string(dockerfile)
	for _, want := range []string{
		"go build -o /dev-mcp ./cmd/dev-mcp",
		"go build -o /dev-capability ./cmd/dev-capability",
		"COPY --from=builder /dev-mcp /usr/local/bin/dev-mcp",
		"COPY --from=builder /dev-capability /usr/local/bin/dev-capability",
		"go build -o /function-runner ./cmd/function-runner",
		"COPY --from=function-runner /function-runner /usr/local/bin/function-runner",
		"ENTRYPOINT [\"/usr/local/bin/dev-mcp\"]",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("mcp image does not contain %q", want)
		}
	}
}
