package imageresolver_test

import (
	"regexp"
	"testing"

	"github.com/concourse/concourse/atc/imageresolver"
)

// A base resource type image a node pulls with IfNotPresent must name one
// image, or each node runs whichever one it pulled first, for good.
func TestEveryDefaultResourceTypeImageIsPinnedByDigest(t *testing.T) {
	pinned := regexp.MustCompile(`^concourse/[a-z0-9-]+-resource:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$`)
	for name, image := range imageresolver.DefaultResourceTypeImages {
		if !pinned.MatchString(image) {
			t.Errorf("%s = %q: want concourse/<name>-resource:<release>@sha256:<digest>", name, image)
		}
	}
}

func TestABaseResourceTypeResolvesToItsPinnedImage(t *testing.T) {
	got := imageresolver.Image("", "time", nil)
	if got != imageresolver.DefaultResourceTypeImages["time"] {
		t.Fatalf("Image(time) = %q", got)
	}
}

// A type name no mapping holds is pulled by that name: a deployment can
// pre-load an image as <type>:latest, or resolve it through a registry
// mirror or a short-name search, so an undeclared type such as
// github-release runs wherever a node can resolve it. The runtime refuses
// only a name that stands for an image fetched through another resource
// type (runtime.ImageSpec.FetchedImageType), never a name as such.
func TestAnUnmappedResourceTypeNameIsPulledByName(t *testing.T) {
	if got := imageresolver.Image("", "github-release", nil); got != "github-release" {
		t.Fatalf("Image(github-release) = %q", got)
	}
}
