package artifactwire

import "net/http"

// DefaultPort is the port an artifact daemon listens on when nothing says
// otherwise. It is the one number both a chart value and an init container's
// shell prelude agree on.
const DefaultPort = 7780

// Path prefixes that a key or a handle is appended to. They are the parts of
// a route that do not fit a mux pattern, kept beside the routes that use them
// so that a URL builder and the daemon's pattern come from the same text.
const (
	// ArtifactsPrefix fronts a key for stream out, delete and probe by key.
	ArtifactsPrefix = "/artifacts/"
	// StreamInPrefix fronts a key for a tar stream in. It is the same key
	// space as ArtifactsPrefix; only the verb and the body differ.
	StreamInPrefix = "/stream-in/"
	// ResourceCachesPrefix fronts a resource cache key.
	ResourceCachesPrefix = "/resource-caches/"
	// CaptureHeldStepsPrefix fronts a step handle for the capture class.
	CaptureHeldStepsPrefix = "/capture-held/steps/"
	// StepsPrefix, under a key, names the daemon's own steps/ directory
	// rather than a registered alias. Peers hold mirrored bytes only there.
	StepsPrefix = "steps/"
)

// Route is one method and path the daemon serves, and whether a caller must
// present a client certificate to reach it.
type Route struct {
	Method string
	// Path is the mux pattern's path: a prefix route ends in "/", a wildcard
	// route carries the "{name...}" segment.
	Path string
	// MTLSExempt says a caller with no client certificate is admitted. An
	// exempt route is one that a kubelet probe, a Prometheus scraper or an
	// init container in a pod on this node has to reach, none of which holds
	// a certificate; what protects each one is stated at the daemon's handler.
	MTLSExempt bool
}

// Pattern is the route as net/http's ServeMux registers it.
func (r Route) Pattern() string {
	return r.Method + " " + r.Path
}

// The route table. Seventeen routes, the same seventeen the daemon's mux
// registers, because it registers them from here.
var (
	Healthz      = Route{Method: http.MethodGet, Path: "/healthz", MTLSExempt: true}
	Resolve      = Route{Method: http.MethodPost, Path: "/resolve", MTLSExempt: true}
	ResolveBatch = Route{Method: http.MethodPost, Path: "/resolve-batch", MTLSExempt: true}
	CaptureHeld  = Route{Method: http.MethodGet, Path: CaptureHeldStepsPrefix + "{handle...}", MTLSExempt: true}
	Metrics      = Route{Method: http.MethodGet, Path: "/metrics", MTLSExempt: true}

	GetArtifact    = Route{Method: http.MethodGet, Path: ArtifactsPrefix}
	PutArtifact    = Route{Method: http.MethodPut, Path: ArtifactsPrefix}
	DeleteArtifact = Route{Method: http.MethodDelete, Path: ArtifactsPrefix}
	HeadArtifact   = Route{Method: http.MethodHead, Path: ArtifactsPrefix}
	Register       = Route{Method: http.MethodPost, Path: "/register"}
	Mirror         = Route{Method: http.MethodPost, Path: "/mirror"}
	StreamIn       = Route{Method: http.MethodPut, Path: StreamInPrefix}
	DurableRestore = Route{Method: http.MethodPost, Path: "/durable/restore"}

	HeadResourceCache = Route{Method: http.MethodHead, Path: ResourceCachesPrefix}
	GetResourceCache  = Route{Method: http.MethodGet, Path: ResourceCachesPrefix}

	HangarPublish = Route{Method: http.MethodPost, Path: "/hangar/v1/scopes/{scope}/trees"}
	// HangarMaterializations is exempt because its caller is an init
	// container; each item carries its own signed warrant instead.
	HangarMaterializations = Route{Method: http.MethodPost, Path: "/hangar/v1/materializations", MTLSExempt: true}
)

// Routes is every route, in the order the daemon registers them.
func Routes() []Route {
	return []Route{
		Healthz, Resolve, ResolveBatch, CaptureHeld, Metrics,
		GetArtifact, PutArtifact, DeleteArtifact, HeadArtifact,
		Register, Mirror, StreamIn, DurableRestore,
		HeadResourceCache, GetResourceCache,
		HangarPublish, HangarMaterializations,
	}
}
