package atc

import "strings"

// ImageRefPrefixes are the Concourse-internal URL schemes an image reference
// may carry in a plan or an artifact repository. A container runtime and a
// pinned image's identity want the bare reference.
var ImageRefPrefixes = []string{"docker:///", "docker://", "raw:///"}

// StripImageRefPrefix returns the bare image reference.
func StripImageRefPrefix(image string) string {
	for _, prefix := range ImageRefPrefixes {
		if strings.HasPrefix(image, prefix) {
			return strings.TrimPrefix(image, prefix)
		}
	}
	return image
}
