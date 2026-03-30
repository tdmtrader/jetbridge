package native

// Config holds configuration for the native worker backend, which runs task
// steps as local OS processes instead of Kubernetes pods.
type Config struct {
	// WorkDir is the base directory for per-container scratch space. Each
	// container gets a subdirectory under WorkDir/containers/<handle>/.
	WorkDir string

	// CacheDir is a durable directory for task caches that persist across
	// builds. Keyed by jobID-stepName-cachePath. Not cleaned by the reaper.
	CacheDir string

	// Platform is the GOOS of this native worker (e.g. "darwin").
	Platform string

	// WorkerName is the name used for DB registration (e.g. "native-darwin").
	WorkerName string
}
