package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	beforeGlobalScopeFoldVersion = 1789793166
	globalScopeFoldVersion       = 1789793167
)

// A deployment that ran with global resources off kept each resource's
// versions in a scope of its own. The fold keeps that history under the
// config's global scope instead of leaving the web to start an empty one.
var _ = Describe("1789793167 folds per-resource scopes into global ones", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(beforeGlobalScopeFoldVersion)
		DeferCleanup(func() {
			if database != nil {
				Expect(database.Close()).To(Succeed())
			}
		})

		seedBypassingConstraints(database, `
			INSERT INTO base_resource_types (id, name, unique_version_history) VALUES
				(1, 'git', false),
				(2, 'time', true);
			INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES
				(10, 1, 'promoted'),
				(20, 1, 'shared'),
				(30, 1, 'upgraded'),
				(40, 2, 'unique');
			INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_scope_id) VALUES
				(1, 'alone',     1, 'git',  '{}', true, 100),
				(2, 'twin-a',    1, 'git',  '{}', true, 200),
				(3, 'twin-b',    1, 'git',  '{}', true, 201),
				(4, 'upgraded',  1, 'git',  '{}', true, 301),
				(5, 'ticker',    1, 'time', '{}', true, 400);
			INSERT INTO resource_config_scopes (id, resource_config_id, resource_id, last_check_end_time) VALUES
				(100, 10, 1,    '2026-01-01'),
				(200, 20, 2,    '2026-01-01'),
				(201, 20, 3,    '2026-02-01'),
				(300, 30, 4,    '2026-01-01'),
				(301, 30, NULL, '2026-03-01'),
				(400, 40, 5,    '2026-01-01');
			INSERT INTO resource_config_versions (resource_config_scope_id, version, version_md5, version_sha256, check_order) VALUES
				(100, '{"ref":"a1"}', 'a1', 'a1', 1),
				(100, '{"ref":"a2"}', 'a2', 'a2', 2),
				(200, '{"ref":"s1"}', 's1', 's1', 49),
				(200, '{"ref":"s2"}', 's2', 's2', 50),
				(201, '{"ref":"s2"}', 's2', 's2', 1),
				(201, '{"ref":"s3"}', 's3', 's3', 2),
				(300, '{"ref":"u1"}', 'u1', 'u1', 1),
				(300, '{"ref":"u2"}', 'u2', 'u2', 2),
				(301, '{"ref":"u2"}', 'u2', 'u2', 1),
				(301, '{"ref":"u3"}', 'u3', 'u3', 2),
				(400, '{"time":"t1"}', 't1', 't1', 1);
		`)

		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(globalScopeFoldVersion)
	})

	scopeOf := func(resourceID int) (int, *int) {
		GinkgoHelper()
		var scopeID int
		var owner sql.NullInt64
		Expect(database.QueryRow(`
			SELECT s.id, s.resource_id
			FROM resources r JOIN resource_config_scopes s ON s.id = r.resource_config_scope_id
			WHERE r.id = $1`, resourceID).Scan(&scopeID, &owner)).To(Succeed())
		if !owner.Valid {
			return scopeID, nil
		}
		id := int(owner.Int64)
		return scopeID, &id
	}

	versionsNewestFirst := func(scopeID int) []string {
		GinkgoHelper()
		rows, err := database.Query(`
			SELECT version_md5 FROM resource_config_versions
			WHERE resource_config_scope_id = $1
			ORDER BY check_order DESC, version_md5 DESC`, scopeID)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var versions []string
		for rows.Next() {
			var v string
			Expect(rows.Scan(&v)).To(Succeed())
			versions = append(versions, v)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return versions
	}

	It("makes a resource's own scope its config's global scope, keeping its versions", func() {
		scopeID, owner := scopeOf(1)
		Expect(scopeID).To(Equal(100))
		Expect(owner).To(BeNil())
		Expect(versionsNewestFirst(100)).To(Equal([]string{"a2", "a1"}))
	})

	It("merges resources that shared a config into the most recently checked scope", func() {
		scopeA, owner := scopeOf(2)
		scopeB, _ := scopeOf(3)
		Expect(scopeA).To(Equal(201))
		Expect(scopeB).To(Equal(201))
		Expect(owner).To(BeNil())
		// 200 was checked for longer, so its numbers run higher; its old s1
		// must not outrank s3, the newest version the kept scope found.
		Expect(versionsNewestFirst(201)).To(Equal([]string{"s3", "s2", "s1"}))

		var remaining int
		Expect(database.QueryRow(`SELECT count(*) FROM resource_config_scopes WHERE id = 200`).Scan(&remaining)).To(Succeed())
		Expect(remaining).To(Equal(0))
	})

	It("folds the old history under a global scope 0.4.0 already started, keeping the newer versions newest", func() {
		scopeID, owner := scopeOf(4)
		Expect(scopeID).To(Equal(301))
		Expect(owner).To(BeNil())
		Expect(versionsNewestFirst(301)).To(Equal([]string{"u3", "u2", "u1"}))
	})

	It("leaves a base type that asks for a unique history alone", func() {
		scopeID, owner := scopeOf(5)
		Expect(scopeID).To(Equal(400))
		Expect(owner).NotTo(BeNil())
		Expect(*owner).To(Equal(5))
		Expect(versionsNewestFirst(400)).To(Equal([]string{"t1"}))
	})

	It("leaves one live global scope per config", func() {
		var duplicates int
		Expect(database.QueryRow(`
			SELECT count(*) FROM (
				SELECT resource_config_id FROM resource_config_scopes
				WHERE resource_id IS NULL AND deprecated_at IS NULL
				GROUP BY resource_config_id HAVING count(*) > 1
			) d`).Scan(&duplicates)).To(Succeed())
		Expect(duplicates).To(BeZero())
	})
})
