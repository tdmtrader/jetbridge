package atccmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the composition root can be asked without a database.
//
// constructChildRunAdmitter assembles the port, the composition service and
// the adapter, and touches no connection while doing it -- runs.NewAdmitter
// and db.NewPipelineRunFactory only store their collaborators. So the two
// things this function decides can be asserted with nil factories: that a web
// node gets a real admitter rather than nothing, and that a role mapping the
// operator got wrong stops the boot instead of quietly producing an admitter
// that would refuse every admission later, at a build's expense.
//
// The adapter's other half -- the field-for-field translation in
// AdmitChildRun -- is not asserted here. Exercising it needs a real service
// over a real port over a real database, which atc/integration already boots:
// see run_pipeline_step_test.go, where a real build's step admits a real run
// through this adapter and the run's created_by names the build. A stand-in
// admitter written for this file would assert that the translation was called,
// which is not the claim.

func TestConstructChildRunAdmitterNeedsNoDatabase(t *testing.T) {
	cmd := &RunCommand{}

	admitter, err := cmd.constructChildRunAdmitter(nil, nil, nil)
	if err != nil {
		t.Fatalf("constructing the run_pipeline admitter failed: %v", err)
	}

	// Not nil: atc/engine substitutes a refusing fallback for a nil admitter,
	// so a composition root that returned one would wire a web node that
	// errors every run_pipeline step and says it is unwired -- which it is
	// not.
	if admitter == nil {
		t.Fatal("the composition root produced a nil admitter; every run_pipeline step " +
			"on this web node would report the port as unwired")
	}
}

// The role mapping is the same one the API's authorization is built from, and
// the port refuses to admit anything at all under a mapping it will not
// honour. Surfacing that here means the operator learns at boot rather than
// from a build that failed hours later.
func TestConstructChildRunAdmitterRejectsABadRoleMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rbac.yml")

	// Making run creation a viewer's capability is the inversion the port
	// exists to refuse: it would make creating a run weaker than setting a
	// pipeline config.
	if err := os.WriteFile(path, []byte("viewer:\n- CreatePipelineRunV2\n"), 0o600); err != nil {
		t.Fatalf("writing the role mapping: %v", err)
	}

	cmd := &RunCommand{}
	if err := cmd.ConfigRBAC.UnmarshalFlag(path); err != nil {
		t.Fatalf("pointing the command at the role mapping: %v", err)
	}

	_, err := cmd.constructChildRunAdmitter(nil, nil, nil)
	if err == nil {
		t.Fatal("expected an operator role mapping the port will not honour to stop " +
			"the admitter being constructed, got no error")
	}

	// Named, so that a typo in the fixture cannot pass this test by failing to
	// parse. The mapping is well-formed; it is the assignment that is refused.
	if !strings.Contains(err.Error(), "failed to customize roles") {
		t.Errorf("expected the role assignment to be refused, got: %v", err)
	}
}
