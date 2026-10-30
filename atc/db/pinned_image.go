package db

import (
	"strings"

	"github.com/concourse/concourse/atc"
)

// PinnedImage is the resource cache a resource type declared with a direct
// image: is fetched with: the type, source and version of a registry-image
// get of that exact reference. Every resource config and resource cache of
// such a type is created under this cache as its parent, the way a check/get
// custom type's are created under the cache of its fetched image, so two
// types of the same name pinned to different images share no config, cache
// or version history, and the cache's base type is registry-image: the type
// every worker that pulls images registers, which ties the cache to a worker.
//
// A reference with a digest is pinned by it. Without one, the tag is all
// there is to pin; a moving tag stays one cache, as a moving tag always has.
func PinnedImage(imageRef string) (resourceType string, source atc.Source, version atc.Version) {
	ref := atc.StripImageRefPrefix(imageRef)

	if repository, digest, pinned := strings.Cut(ref, "@"); pinned {
		return "registry-image", atc.Source{"repository": repository}, atc.Version{"digest": digest}
	}

	repository, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repository, tag = ref[:i], ref[i+1:]
	}
	return "registry-image", atc.Source{"repository": repository, "tag": tag}, atc.Version{"tag": tag}
}
