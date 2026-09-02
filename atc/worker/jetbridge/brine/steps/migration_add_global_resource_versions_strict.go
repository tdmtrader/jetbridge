package steps

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/brine-dev/brine-go/pkg/brine"
)

const (
	addGlobalResourceVersionsPreVersion  = 1537196857
	addGlobalResourceVersionsPostVersion = 1537546150
)

type AddGlobalResourceVersionsMigrationObservation struct {
	Profile string
	Failure string
}

func AddGlobalResourceVersionsMigrationStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AddGlobalResourceVersionsMigrationObservation](
			"the production add-global-resource-versions migration profile {string} is exercised",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AddGlobalResourceVersionsMigrationObservation, error) {
				profile, err := paramAt("the production add-global-resource-versions migration profile {string} is exercised", p, 0)
				if err != nil {
					return AddGlobalResourceVersionsMigrationObservation{}, err
				}
				pm, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return AddGlobalResourceVersionsMigrationObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return AddGlobalResourceVersionsMigrationObservation{
					Profile: profile,
					Failure: observeAddGlobalResourceVersionsMigration(pm, profile),
				}, nil
			},
		),
		brine.DefineCheck[AddGlobalResourceVersionsMigrationObservation](
			"the add-global-resource-versions migration observation exactly matches {string}",
			func(in AddGlobalResourceVersionsMigrationObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the add-global-resource-versions migration observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func observeAddGlobalResourceVersionsMigration(pm *postmaster, profile string) string {
	pm.runner.CreateEmptyTestDB()
	defer pm.runner.DropTestDB()

	switch profile {
	case "up-disabled":
		return observeAddGlobalResourceVersionsDisabled(pm)
	case "up-build-inputs":
		return observeAddGlobalResourceVersionsInputs(pm)
	case "up-build-outputs":
		return observeAddGlobalResourceVersionsOutputs(pm)
	case "down-all-versions":
		return observeAddGlobalResourceVersionsDown(pm)
	default:
		return fmt.Sprintf("unknown add-global-resource-versions migration profile %q", profile)
	}
}

func observeAddGlobalResourceVersionsDisabled(pm *postmaster) string {
	dbConn, err := openAddGlobalResourceVersionsPre(pm, false, false)
	if err != nil {
		return err.Error()
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close pre-migration database: %v", err)
	}

	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPostVersion)
	if err != nil {
		return fmt.Sprintf("run migration: %v", err)
	}
	defer dbConn.Close()
	for i := 1; i <= 3; i++ {
		if _, err := dbConn.Exec("INSERT INTO resource_config_versions(version, version_md5, resource_config_id, check_order) VALUES($1::jsonb, md5($1::text), 1, $2)", fmt.Sprintf(`{"version": "v%d"}`, i), i); err != nil {
			return fmt.Sprintf("insert resource config version %d: %v", i, err)
		}
	}
	rows, err := dbConn.Query(`SELECT d.resource_id, v.version FROM resource_disabled_versions d, resource_config_versions v WHERE d.version_md5 = v.version_md5 ORDER BY d.resource_id, v.version`)
	if err != nil {
		return fmt.Sprintf("query disabled versions: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var resourceID int
		var version string
		if err := rows.Scan(&resourceID, &version); err != nil {
			return fmt.Sprintf("scan disabled version: %v", err)
		}
		got = append(got, fmt.Sprintf("%d:%s", resourceID, version))
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("iterate disabled versions: %v", err)
	}
	if len(got) != 2 {
		return fmt.Sprintf("disabled version count got %d, want 2 (%v)", len(got), got)
	}
	if got[0] != `1:{"version": "v3"}` {
		return fmt.Sprintf("first disabled version got %q, want %q", got[0], `1:{"version": "v3"}`)
	}
	return ""
}

func observeAddGlobalResourceVersionsInputs(pm *postmaster) string {
	dbConn, err := openAddGlobalResourceVersionsPre(pm, true, false)
	if err != nil {
		return err.Error()
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close pre-migration database: %v", err)
	}
	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPostVersion)
	if err != nil {
		return fmt.Sprintf("run migration: %v", err)
	}
	defer dbConn.Close()
	versions, err := addGlobalResourceVersionMD5s(dbConn)
	if err != nil {
		return err.Error()
	}
	want := []string{
		fmt.Sprintf("1:%s:1:build_input1", versions[1]),
		fmt.Sprintf("1:%s:1:build_input2", versions[2]),
		fmt.Sprintf("2:%s:1:build_input3", versions[1]),
		fmt.Sprintf("3:%s:1:build_input4", versions[1]),
		fmt.Sprintf("4:%s:2:build_input5", versions[4]),
	}
	return compareAddGlobalResourceVersionRows(dbConn,
		`SELECT build_id, version_md5, resource_id, name FROM build_resource_config_version_inputs`, want, "build inputs")
}

func observeAddGlobalResourceVersionsOutputs(pm *postmaster) string {
	dbConn, err := openAddGlobalResourceVersionsPre(pm, false, true)
	if err != nil {
		return err.Error()
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close pre-migration database: %v", err)
	}
	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPostVersion)
	if err != nil {
		return fmt.Sprintf("run migration: %v", err)
	}
	defer dbConn.Close()
	versions, err := addGlobalResourceVersionMD5s(dbConn)
	if err != nil {
		return err.Error()
	}
	want := []string{
		fmt.Sprintf("1:%s:1:some-resource", versions[2]),
		fmt.Sprintf("2:%s:1:some-resource", versions[1]),
		fmt.Sprintf("3:%s:1:some-resource", versions[1]),
		fmt.Sprintf("4:%s:2:some-other-resource", versions[4]),
	}
	return compareAddGlobalResourceVersionRows(dbConn,
		`SELECT build_id, version_md5, resource_id, name FROM build_resource_config_version_outputs`, want, "build outputs")
}

func observeAddGlobalResourceVersionsDown(pm *postmaster) string {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPostVersion)
	if err != nil {
		return fmt.Sprintf("open post-migration database: %v", err)
	}
	if err := setupAddGlobalResourceVersionsBase(dbConn); err != nil {
		return closeAddGlobalResourceVersionsDB(dbConn, err.Error())
	}
	if err := setupAddGlobalResourceVersionsResources(dbConn); err != nil {
		return closeAddGlobalResourceVersionsDB(dbConn, err.Error())
	}
	if err := setupAddGlobalResourceVersionsLegacyVersions(dbConn); err != nil {
		return closeAddGlobalResourceVersionsDB(dbConn, err.Error())
	}
	for i := 1; i <= 3; i++ {
		if _, err := dbConn.Exec("INSERT INTO resource_config_versions(version, version_md5, resource_config_id, check_order) VALUES($1::jsonb, md5($1::text), 1, $2)", fmt.Sprintf(`{"version": "v%d"}`, i), i); err != nil {
			return closeAddGlobalResourceVersionsDB(dbConn, fmt.Sprintf("insert resource config version %d: %v", i, err))
		}
	}
	for _, version := range []int{2, 3} {
		if _, err := dbConn.Exec("INSERT INTO resource_disabled_versions(version_md5, resource_id) VALUES(md5($1::text), 1)", fmt.Sprintf(`{"version": "v%d"}`, version)); err != nil {
			return closeAddGlobalResourceVersionsDB(dbConn, fmt.Sprintf("disable resource version %d: %v", version, err))
		}
	}
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("close post-migration database: %v", err)
	}

	dbConn, err = pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPreVersion)
	if err != nil {
		return fmt.Sprintf("run downgrade: %v", err)
	}
	defer dbConn.Close()
	rows, err := dbConn.Query(`SELECT resource_id, version, type, enabled FROM versioned_resources`)
	if err != nil {
		return fmt.Sprintf("query downgraded versions: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var resourceID int
		var version string
		var resourceType string
		var enabled bool
		if err := rows.Scan(&resourceID, &version, &resourceType, &enabled); err != nil {
			return fmt.Sprintf("scan downgraded version: %v", err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%s:%t", resourceID, version, resourceType, enabled))
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("iterate downgraded versions: %v", err)
	}
	want := []string{
		`1:{"version": "v1"}:some-type:true`,
		`1:{"version": "v2"}:some-type:false`,
		`1:{"version": "v3"}:some-type:false`,
		`2:{"version": "v1"}:some-type:true`,
		`2:{"version": "v2"}:some-type:true`,
		`2:{"version": "v3"}:some-type:true`,
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != 6 {
		return fmt.Sprintf("downgraded version count got %d, want 6 (%v)", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Sprintf("downgraded versions got %v, want %v", got, want)
		}
	}
	return ""
}

func openAddGlobalResourceVersionsPre(pm *postmaster, inputs, outputs bool) (*sql.DB, error) {
	dbConn, err := pm.runner.TryOpenDBAtVersion(addGlobalResourceVersionsPreVersion)
	if err != nil {
		return nil, fmt.Errorf("open pre-migration database: %v", err)
	}
	if err := setupAddGlobalResourceVersionsBase(dbConn); err != nil {
		return dbConn, fmt.Errorf("setup base rows: %v", err)
	}
	if err := setupAddGlobalResourceVersionsResources(dbConn); err != nil {
		return dbConn, fmt.Errorf("setup resources: %v", err)
	}
	if err := setupAddGlobalResourceVersionsLegacyVersions(dbConn); err != nil {
		return dbConn, fmt.Errorf("setup versioned resources: %v", err)
	}
	if inputs || outputs {
		if err := setupAddGlobalResourceVersionsBuilds(dbConn); err != nil {
			return dbConn, fmt.Errorf("setup builds: %v", err)
		}
	}
	if inputs {
		if _, err := dbConn.Exec(`INSERT INTO build_inputs(build_id, versioned_resource_id, name) VALUES
			(1, 1, 'build_input1'), (1, 2, 'build_input2'), (2, 1, 'build_input3'),
			(3, 1, 'build_input4'), (3, 1, 'build_input4'), (4, 4, 'build_input5')`); err != nil {
			return dbConn, fmt.Errorf("setup build inputs: %v", err)
		}
	}
	if outputs {
		if _, err := dbConn.Exec(`INSERT INTO build_outputs(build_id, versioned_resource_id) VALUES
			(1, 2), (2, 1), (3, 1), (3, 1), (4, 4)`); err != nil {
			return dbConn, fmt.Errorf("setup build outputs: %v", err)
		}
	}
	return dbConn, nil
}

func setupAddGlobalResourceVersionsBase(dbConn *sql.DB) error {
	if _, err := dbConn.Exec(`INSERT INTO teams(id, name) VALUES (1, 'some-team')`); err != nil {
		return fmt.Errorf("insert team: %v", err)
	}
	if _, err := dbConn.Exec(`INSERT INTO pipelines(id, team_id, name, groups) VALUES
		(1, 1, 'pipeline1', '[{"name":"group1","jobs":["job1","job2"]},{"name":"group2","jobs":["job2","job3"]}]'),
		(2, 1, 'pipeline2', '[{"name":"group2","jobs":["job1"]}]')`); err != nil {
		return fmt.Errorf("insert pipelines: %v", err)
	}
	if _, err := dbConn.Exec(`INSERT INTO jobs(id, pipeline_id, name, config) VALUES
		(1, 1, 'job1', '{"name":"job1"}'), (2, 1, 'job2', '{"name":"job2"}'),
		(3, 1, 'job3', '{"name":"job3"}'), (4, 2, 'job1', '{"name":"job1"}')`); err != nil {
		return fmt.Errorf("insert jobs: %v", err)
	}
	return nil
}

func setupAddGlobalResourceVersionsResources(dbConn *sql.DB) error {
	if _, err := dbConn.Exec(`INSERT INTO base_resource_types(id, name) VALUES(1, 'some-type')`); err != nil {
		return fmt.Errorf("insert base resource type: %v", err)
	}
	if _, err := dbConn.Exec(`INSERT INTO resource_configs(id, source_hash, base_resource_type_id) VALUES(1, 'some-source', 1)`); err != nil {
		return fmt.Errorf("insert resource config: %v", err)
	}
	if _, err := dbConn.Exec(`INSERT INTO resources(id, name, pipeline_id, config, active, resource_config_id) VALUES
		(1, 'some-resource', 1, '{"type":"some-type"}', true, 1),
		(2, 'some-other-resource', 2, '{"type":"some-type"}', true, 1)`); err != nil {
		return fmt.Errorf("insert resources: %v", err)
	}
	return nil
}

func setupAddGlobalResourceVersionsLegacyVersions(dbConn *sql.DB) error {
	if _, err := dbConn.Exec(`INSERT INTO versioned_resources(version, metadata, type, enabled, resource_id, check_order) VALUES
		('{"version": "v1"}', 'some-metadata', 'some-type', true, 1, 1),
		('{"version": "v2"}', 'some-metadata', 'some-type', true, 1, 2),
		('{"version": "v3"}', 'some-metadata', 'some-type', false, 1, 3),
		('{"version": "v1"}', 'some-metadata', 'some-type', false, 2, 1)`); err != nil {
		return fmt.Errorf("insert versioned resources: %v", err)
	}
	return nil
}

func setupAddGlobalResourceVersionsBuilds(dbConn *sql.DB) error {
	if _, err := dbConn.Exec(`INSERT INTO builds(id, name, status, job_id, team_id, pipeline_id) VALUES
		(1, 'build1', 'succeeded', 1, 1, 1), (2, 'build2', 'succeeded', 1, 1, 1),
		(3, 'build3', 'started', 2, 1, 1), (4, 'build4', 'pending', 4, 1, 2)`); err != nil {
		return fmt.Errorf("insert builds: %v", err)
	}
	return nil
}

func addGlobalResourceVersionMD5s(dbConn *sql.DB) (map[int]string, error) {
	rows, err := dbConn.Query(`SELECT id, md5(version) FROM versioned_resources`)
	if err != nil {
		return nil, fmt.Errorf("query legacy version hashes: %v", err)
	}
	defer rows.Close()
	versions := map[int]string{}
	for rows.Next() {
		var id int
		var version string
		if err := rows.Scan(&id, &version); err != nil {
			return nil, fmt.Errorf("scan legacy version hash: %v", err)
		}
		versions[id] = version
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy version hashes: %v", err)
	}
	return versions, nil
}

func compareAddGlobalResourceVersionRows(dbConn *sql.DB, query string, want []string, label string) string {
	rows, err := dbConn.Query(query)
	if err != nil {
		return fmt.Sprintf("query %s: %v", label, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var buildID, resourceID int
		var versionMD5, name string
		if err := rows.Scan(&buildID, &versionMD5, &resourceID, &name); err != nil {
			return fmt.Sprintf("scan %s: %v", label, err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%d:%s", buildID, versionMD5, resourceID, name))
	}
	if err := rows.Err(); err != nil {
		return fmt.Sprintf("iterate %s: %v", label, err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return fmt.Sprintf("%s count got %d, want %d (%v)", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Sprintf("%s got %v, want %v", label, got, want)
		}
	}
	return ""
}

func closeAddGlobalResourceVersionsDB(dbConn *sql.DB, failure string) string {
	if err := dbConn.Close(); err != nil {
		return fmt.Sprintf("%s; close database: %v", failure, err)
	}
	return failure
}
