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

// The artifact daemon's durable cache tier cannot address an output object, and
// the reason is one segment of arithmetic between two files that do not mention
// each other.
//
// The tier exports NewGCS over a real cloud client and Delete(ctx, key) over
// whatever bucket its config names, so an output root that linked it could
// remove any object it could NAME. Architecture guards say which binaries may
// link it, and the answer is one. This says something else and narrower: even
// holding the tier and pointed at the output bucket, the set of object names it
// can compose does not contain an output object key.
//
// The margin is exactly one segment. ValidatePrefix admits at most four,
// ValidateKey at most two, and every backend's objectName concatenates them, so
// the deepest name the tier can build is six. An output key is
//
//	hangar/v1/scopes/<scope>/trees/sha256/<digest>.tar.zst
//
// which is seven, and eight or nine with a deployment prefix. Raise
// MaxPrefixSegments to five, or shorten the key scheme by one segment, and route
// H becomes a live reach into the output bucket stopped only by IAM. That was a
// coincidence until this test; it is an invariant now, and the constants it
// reads carry a comment pointing back here.
//
// It reads the tier's bounds out of its source rather than importing it: the
// durable tier and hangar are separated by an architecture rule that counts test
// edges, and an import here to prove a separation would violate one.
const durableSourceFile = "cmd/artifact-daemon/durable/durable.go"

func TestNoDurableTierObjectNameCanAddressAnOutputObject(t *testing.T) {
	prefixMax := durableSegmentBound(t, "MaxPrefixSegments")
	keyMax := durableSegmentBound(t, "MaxKeySegments")
	composable := prefixMax + keyMax

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
			t.Errorf("an output object key is %d segments (%q) and the durable cache tier can "+
				"compose an object name of up to %d (%s=%d + %s=%d).\n\nThat tier's Delete "+
				"takes a key and its bucket comes from a caller-supplied config field, so a "+
				"process holding it and pointed at the output bucket could now NAME an output "+
				"object. The gap was never a design, only an arithmetic margin of one segment "+
				"between %s and hangar.TreeKey; restoring it means lowering the tier's bound or "+
				"leaving the output key scheme at its current depth.",
				segments, key, composable,
				"MaxPrefixSegments", prefixMax, "MaxKeySegments", keyMax, durableSourceFile)
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
			"object-name bound out of its source because an import would violate the "+
			"separation it is about. If the constant was renamed or inlined, point "+
			"durableSegmentBound at where it lives now -- do not delete this test, because the "+
			"bound it checks is one segment wide.", durableSourceFile, name)
	}

	return value
}
