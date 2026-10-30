package algorithm_test

// versionsDBFixture is the shape of the gzipped JSON under testdata/: a
// pipeline's version history as the old debug versions-db route exported it.
// The route is gone; the fixtures remain the algorithm's regression corpus.
type versionsDBFixture struct {
	Jobs             []fixtureJob
	Resources        []fixtureResource
	ResourceVersions []fixtureResourceVersion
	BuildOutputs     []fixtureBuildOutput
	BuildInputs      []fixtureBuildInput
	BuildReruns      []fixtureBuildRerun

	// pre-6.0 exports carry name->id maps instead of Jobs and Resources
	LegacyJobIDs      map[string]int `json:"JobIDs,omitempty"`
	LegacyResourceIDs map[string]int `json:"ResourceIDs,omitempty"`
}

type fixtureResourceVersion struct {
	VersionID  int
	ResourceID int
	CheckOrder int

	// not present pre-6.0
	ScopeID int
}

type fixtureBuildOutput struct {
	fixtureResourceVersion
	BuildID int
	JobID   int
}

type fixtureBuildInput struct {
	fixtureResourceVersion
	BuildID   int
	JobID     int
	InputName string
}

type fixtureBuildRerun struct {
	BuildID int
	JobID   int
	RerunOf int
}

type fixtureJob struct {
	Name string
	ID   int
}

type fixtureResource struct {
	Name    string
	ID      int
	ScopeID *int
}
