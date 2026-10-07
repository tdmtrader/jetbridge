package output

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar"
)

// The artifact daemon's durable cache tier cannot spell an output object's key.
//
// INTERIM GUARD, to be replaced by Track 1. The separation between the
// fail-open cache and the output plane is now a NAMESPACE: the cache is its own
// bucket or disk namespace, and objectstore.Namespaces refuses any two of
// cache, input and output being equal. The artifact daemon checks cache against
// strict input today, but it is not yet handed the output bucket -- that
// arrives when Track 1 merges the output daemon into it -- so it validates with
// output = "". Until then this keeps the older, belt-and-braces property: a key
// the cache accepts (ValidateKey: at most maxKeySegments segments, with no
// deployment prefix any more) is far too shallow to be an output object key,
// so even a cache pointed at the output bucket by mistake could not name, read
// or expire an output tree. When the daemon validates all three names, delete
// this file.
//
// It reads the tier's bound out of its source rather than importing it: the
// durable tier and hangar/output are separated by an architecture rule that
// counts test edges, and an import here to prove a separation would violate it.
const durableSourceFile = "cmd/artifact-daemon/durable/durable.go"

func TestNoCacheKeyCanSpellAnOutputObjectKey(t *testing.T) {
	composable := durableSegmentBound(t, "maxKeySegments")

	digest := hangar.Digest("sha256:ab12cd34" + strings.Repeat("0", 56))
	if err := digest.Validate(); err != nil {
		t.Fatalf("the probe digest is not a digest: %v", err)
	}

	// Every deployment prefix shape this plane supports, shallowest first. The
	// shallowest is the one that matters: if the empty prefix is out of reach,
	// so is every deeper one.
	for _, prefix := range []string{"", "deployments", "deployments/blue"} {
		config := validNamespaceConfig()
		config.DeploymentPrefix = prefix

		namespace, err := DeriveNamespace(config)
		if err != nil {
			t.Fatalf("deriving with prefix %q: %v", prefix, err)
		}
		key, err := namespace.ObjectKey(digest)
		if err != nil {
			t.Fatalf("object key with prefix %q: %v", prefix, err)
		}

		segments := len(strings.Split(key, "/"))
		if segments < 5 {
			t.Fatalf("the output key %q is %d segments; the scheme collapsed and this "+
				"comparison means nothing", key, segments)
		}
		if segments <= composable {
			t.Errorf("an output object key is %d segments (%q) and the durable cache tier "+
				"accepts keys of up to %d (maxKeySegments in %s).\n\nThe cache holds a delete "+
				"over its namespace, and until the artifact daemon validates the cache bucket "+
				"against the output bucket too, the key depth is what keeps a misconfigured "+
				"cache from naming an output object.", segments, key, composable, durableSourceFile)
		}
	}
}

// durableSegmentBound reads one integer constant out of the durable tier's
// source. A missing constant is a fatal error rather than a default: a default
// would let this rule keep passing over a file that no longer says anything.
func durableSegmentBound(t *testing.T, name string) int {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", filepath.FromSlash(durableSourceFile))

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", durableSourceFile, err)
	}

	value := -1
	ast.Inspect(file, func(node ast.Node) bool {
		spec, isValue := node.(*ast.ValueSpec)
		if !isValue {
			return true
		}
		for i, ident := range spec.Names {
			if ident.Name != name || i >= len(spec.Values) {
				continue
			}
			literal, isLiteral := spec.Values[i].(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.INT {
				continue
			}
			parsed, parseErr := strconv.Atoi(literal.Value)
			if parseErr != nil {
				continue
			}
			value = parsed
		}

		return true
	})

	if value < 1 {
		t.Fatalf("%s declares no integer constant %s.\n\nThis rule reads the durable tier's "+
			"key bound out of its source because an import would violate the separation it is "+
			"about. If the constant was renamed or inlined, point durableSegmentBound at where it "+
			"lives now; delete this test only once the daemon validates cache, input and output "+
			"together.", durableSourceFile, name)
	}

	return value
}
