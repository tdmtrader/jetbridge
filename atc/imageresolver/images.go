package imageresolver

import (
	"maps"
	"strings"

	"github.com/concourse/concourse/atc"
)

// DefaultResourceTypeImages maps base Concourse resource type names to their
// Docker image references. These are the official Concourse resource type
// images used when no custom resource type is defined in the pipeline.
//
// Each is pinned by digest, with the release tag beside it for the reader.
// Step pods pull with IfNotPresent, so an unpinned name is whatever a node
// pulled first, kept forever: on 2026-11-06 theborg was still running
// time, git and registry-image images from May to August, carrying 1-4
// critical and 30-72 high CVEs each, while the upstream releases had none to
// three. A new digest here is a new image reference, which every node pulls.
//
// To refresh: for each, `crane digest concourse/<name>-resource:latest`, and
// the release tag whose digest matches (`crane ls`, then `crane digest`).
var DefaultResourceTypeImages = map[string]string{
	"time":           "concourse/time-resource:1.11.6@sha256:aa033888fafc072b9c3f8fc9340236daf7d56a2aff1cb8c78c4a3c80472fd2b2",
	"registry-image": "concourse/registry-image-resource:1.19.1@sha256:35ecd349bacdc9b75d941bdb02022eeb99aa9f37d23d3d18ae708d476e5edee1",
	"git":            "concourse/git-resource:1.24.1@sha256:0b746d5fa996060806b230b64bebb5a7416a800fae1ce3c774fc9bb65b7177c5",
	"s3":             "concourse/s3-resource:2.5.7@sha256:95b2b4e6cce6a4ed6192db08ca0ec0f81a4f54a32f5bf9872812b53d48d9592c",
	"docker-image":   "concourse/docker-image-resource:1.13.4@sha256:ac4bb7b3de4343d124db0e255b98c799569669ce6939a2168e5c85482439f84a",
	"pool":           "concourse/pool-resource:1.8.4@sha256:231c049ff11c1b460c5976ac2d9598f60042b2013cc9122be6f597df44eb25dc",
	"semver":         "concourse/semver-resource:2.0.5@sha256:b88abde30560906c03e1c4139541e0d90c92dec853201fc298bdd00c21d71727",
	"mock":           "concourse/mock-resource:0.14.8@sha256:a0bbd21cb1b11bf49c3f4f127f293e0c87d062ffbc3187defa8de3571bdd7862",
}

// MergeResourceTypeImages returns a new map that starts with a copy of
// DefaultResourceTypeImages and applies operator overrides on top. Each
// override entry is "name=image" (e.g. "git=my-registry/git-resource:v2").
// Entries without an "=" separator are silently skipped.
func MergeResourceTypeImages(overrides []string) map[string]string {
	merged := make(map[string]string, len(DefaultResourceTypeImages))
	maps.Copy(merged, DefaultResourceTypeImages)
	for _, entry := range overrides {
		name, image, ok := strings.Cut(entry, "=")
		if !ok || name == "" || image == "" {
			continue
		}
		merged[name] = image
	}
	return merged
}

// Image extracts a Kubernetes-compatible image reference from an ImageSpec's
// ImageURL and ResourceType. Concourse uses prefixed URLs (docker:///, raw:///)
// while Kubernetes needs bare image references.
//
// For base resource types (time, git, registry-image, etc.), the ImageSpec
// only carries the type name (e.g. "time") and no URL. The resourceTypeImages
// mapping translates these to Docker image references (e.g.
// "concourse/time-resource:1.11.6@sha256:...").
func Image(imageURL, resourceType string, resourceTypeImages map[string]string) string {
	image := atc.StripImageRefPrefix(imageURL)

	if image == "" {
		image = resourceType
	}

	// Map base resource type names to their Docker images.
	// Fall back to DefaultResourceTypeImages if no custom mapping is provided.
	mapping := resourceTypeImages
	if mapping == nil {
		mapping = DefaultResourceTypeImages
	}
	if mapped, ok := mapping[image]; ok {
		image = mapped
	}

	return image
}
