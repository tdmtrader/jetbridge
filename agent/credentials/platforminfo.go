package credentials

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// PlatformInfo is the GET /api/v1/agent/platform-info body: the
// agent-platform facts rendered alongside the /agent page's
// credentials/platform section. Ticket #45 (runner-image skew
// visibility): the agent step image once drifted ~28 releases behind the
// web binary with zero surfacing, silently disabling the flight recorder
// on every run. These fields make the skew visible; nothing auto-fixes it.
type PlatformInfo struct {
	// AgentStepImage is the configured --agent-step-image value
	// (CONCOURSE_AGENT_STEP_IMAGE); empty when unset.
	AgentStepImage string `json:"agent_step_image"`
	// WebVersion is the web binary's release version (concourse.Version).
	WebVersion string `json:"web_version"`
	// ImageVersionKnown is false when no vX.Y.Z tag could be parsed from
	// the image ref (:latest, digest pins, garbage) — skew is then
	// unknowable and reported false rather than erroring.
	ImageVersionKnown bool `json:"image_version_known"`
	// ImageVersionSkew is true when the image's vX.Y.Z tag lags WebVersion.
	ImageVersionSkew bool `json:"image_version_skew"`
}

// NewPlatformInfo derives the skew fields from the configured agent step
// image ref and the web binary's version. It never errors: unparseable
// inputs yield ImageVersionKnown=false, ImageVersionSkew=false.
func NewPlatformInfo(agentStepImage, webVersion string) PlatformInfo {
	skew, known := ImageVersionSkew(agentStepImage, webVersion)
	return PlatformInfo{
		AgentStepImage:    agentStepImage,
		WebVersion:        webVersion,
		ImageVersionKnown: known,
		ImageVersionSkew:  skew,
	}
}

// PlatformInfoHandler serves GET /api/v1/agent/platform-info
// (GetAgentPlatformInfo; authenticated via the wrappa). The payload is
// fixed at construction — both inputs are process-lifetime constants.
func PlatformInfoHandler(info PlatformInfo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
}

// ImageVersionSkew parses a vX.Y.Z tag suffix from imageRef and compares
// it against the web binary's X.Y.Z version. skew is true only when the
// image version is strictly older than webVersion (a newer image never
// "lags"). known is false — and skew forced false — when either side is
// unparseable: :latest, digest pins (any "@"), a missing tag, or garbage.
func ImageVersionSkew(imageRef, webVersion string) (skew, known bool) {
	img, ok := parseImageTagVersion(imageRef)
	if !ok {
		return false, false
	}
	web, ok := parseVersionTriple(webVersion)
	if !ok {
		return false, false
	}
	return versionLess(img, web), true
}

type versionTriple struct {
	major, minor, patch int
}

// parseImageTagVersion extracts the tag from an image ref and parses it
// as a version. Digest pins ("@...") are unknown — the digest, not the
// tag, decides what runs. A ":" before the last "/" is a registry port,
// not a tag separator.
func parseImageTagVersion(ref string) (versionTriple, bool) {
	if ref == "" || strings.Contains(ref, "@") {
		return versionTriple{}, false
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return versionTriple{}, false // no tag
	}
	return parseVersionTriple(ref[colon+1:])
}

// parseVersionTriple parses "X.Y.Z" with an optional leading "v" and an
// ignored pre-release/build suffix ("0.0.0-dev", "v1.2.3-rc.1").
func parseVersionTriple(v string) (versionTriple, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return versionTriple{}, false
	}
	var t versionTriple
	for i, dst := range []*int{&t.major, &t.minor, &t.patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return versionTriple{}, false
		}
		*dst = n
	}
	return t, true
}

func versionLess(a, b versionTriple) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}
