package imageresolver

import (
	"maps"
	"strings"

	"github.com/concourse/concourse/atc"
)

// DefaultResourceTypeImages maps base Concourse resource type names to their
// Docker image references. These are the official Concourse resource type
// images used when no custom resource type is defined in the pipeline.
var DefaultResourceTypeImages = map[string]string{
	"time":           "concourse/time-resource",
	"registry-image": "concourse/registry-image-resource",
	"git":            "concourse/git-resource",
	"s3":             "concourse/s3-resource",
	"docker-image":   "concourse/docker-image-resource",
	"pool":           "concourse/pool-resource",
	"semver":         "concourse/semver-resource",
	"mock":           "concourse/mock-resource",
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
// "concourse/time-resource").
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
