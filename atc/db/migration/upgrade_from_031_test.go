package migration_test

import (
	"database/sql"
	"time"

	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// jb031HeadVersion is the last migration jb-0.3.1 shipped. Deployments other
// than concourse.home upgrade from there straight to the current release.
const jb031HeadVersion = 1773105509

// 0.4.0's migrations dropped the idtoken signing keys and every pipeline's
// var_sources, and no test noticed: each migration spec here starts from the
// version before its own. This one starts where another deployment starts --
// a keyless jb-0.3.1 database holding the data of the capabilities 0.4.1
// restored -- and runs every migration since, so a later one that drops or
// rewrites that data fails here by name.
var _ = Describe("upgrade from 0.3.1", func() {
	const (
		varSources    = `[{"name":"vault","type":"vault","config":{"url":"https://vault.example:8200"}},{"name":"cloud","type":"idtoken","config":{"audience":["sts.amazonaws.com"]}}]`
		signingJWK    = `{"kty":"RSA","kid":"key-1","n":"0vx7","e":"AQAB"}`
		teamAuth      = `{"owner":{"users":["oidc:alice"],"groups":["oidc:platform-admins"]}}`
		dockerImageRT = `{"name":"custom","type":"docker-image","source":{"repository":"registry.example/custom-resource","insecure_registries":["registry.example"]}}`
	)

	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(jb031HeadVersion)
		DeferCleanup(func() {
			if database != nil {
				Expect(database.Close()).To(Succeed())
			}
		})

		seedBypassingConstraints(database, `INSERT INTO teams (id, name, admin, auth) VALUES (1, 'main', true, $1)`, teamAuth)
		seedBypassingConstraints(database, `INSERT INTO pipelines (id, name, team_id, secondary_ordering, var_sources) VALUES (1, 'deploy', 1, 1, $1)`, varSources)
		seedBypassingConstraints(database, `INSERT INTO signing_keys (kid, kty, jwk, created_at) VALUES ('key-1', 'RSA', $1, '2026-08-01T00:00:00Z')`, signingJWK)
		seedBypassingConstraints(database, `INSERT INTO resource_types (id, pipeline_id, name, type, config, active) VALUES (1, 1, 'custom', 'docker-image', $1, true)`, dockerImageRT)
		seedBypassingConstraints(database, `
			INSERT INTO base_resource_types (id, name, unique_version_history) VALUES (1, 'git', false);
			INSERT INTO resource_configs (id, base_resource_type_id, source_hash) VALUES (10, 1, 'shared');
			INSERT INTO resources (id, name, pipeline_id, type, config, active, resource_config_scope_id) VALUES
				(1, 'repo-a', 1, 'git', '{"name":"repo-a"}', true, 100),
				(2, 'repo-b', 1, 'git', '{"name":"repo-b"}', true, 101);
			INSERT INTO resource_config_scopes (id, resource_config_id, resource_id, last_check_end_time) VALUES
				(100, 10, 1, '2026-08-01'),
				(101, 10, 2, '2026-09-01');
			INSERT INTO resource_config_versions (resource_config_scope_id, version, version_md5, version_sha256, check_order) VALUES
				(100, '{"ref":"older"}', 'older', 'older', 39),
				(100, '{"ref":"old"}', 'old', 'old', 40),
				(101, '{"ref":"old"}', 'old', 'old', 1),
				(101, '{"ref":"new"}', 'new', 'new', 2);
		`)

		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDB()
	})

	decrypted := func(query string) string {
		GinkgoHelper()
		var text string
		var nonce sql.NullString
		Expect(database.QueryRow(query).Scan(&text, &nonce)).To(Succeed())
		Expect(nonce.Valid).To(BeTrue(), "the upgrade left the row in plaintext")
		plain, err := postgresrunner.EncryptionKey().Decrypt(text, &nonce.String)
		Expect(err).NotTo(HaveOccurred())
		return string(plain)
	}

	It("keeps a pipeline's var_sources, encrypted under the key", func() {
		Expect(decrypted(`SELECT var_sources, nonce FROM pipelines WHERE id = 1`)).To(MatchJSON(varSources))
	})

	It("keeps the idtoken signing keys", func() {
		var kty, jwk string
		var created time.Time
		Expect(database.QueryRow(`SELECT kty, jwk, created_at FROM signing_keys WHERE kid = 'key-1'`).Scan(&kty, &jwk, &created)).To(Succeed())
		Expect(kty).To(Equal("RSA"))
		Expect(jwk).To(MatchJSON(signingJWK))
		Expect(created).To(BeTemporally("==", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))
	})

	It("keeps a team owned through an OIDC group", func() {
		var auth string
		Expect(database.QueryRow(`SELECT auth FROM teams WHERE id = 1`).Scan(&auth)).To(Succeed())
		Expect(auth).To(MatchJSON(teamAuth))
	})

	It("keeps a resource type whose image comes from another type", func() {
		var name, typ string
		var pipelineID int
		var active bool
		Expect(database.QueryRow(`SELECT name, type, pipeline_id, active FROM resource_types WHERE id = 1`).Scan(&name, &typ, &pipelineID, &active)).To(Succeed())
		Expect([]any{name, typ, pipelineID, active}).To(Equal([]any{"custom", "docker-image", 1, true}))
		Expect(decrypted(`SELECT config, nonce FROM resource_types WHERE id = 1`)).To(MatchJSON(dockerImageRT))
	})

	// The older scope numbered its versions higher, so a merge that kept raw
	// check_order would make its history outrank the newest version found.
	It("keeps each resource's history, newest version latest", func() {
		var scopes []int
		rows, err := database.Query(`SELECT DISTINCT resource_config_scope_id FROM resources WHERE id IN (1, 2)`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for rows.Next() {
			var id int
			Expect(rows.Scan(&id)).To(Succeed())
			scopes = append(scopes, id)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(scopes).To(HaveLen(1), "resources sharing a config share one scope")

		var newestFirst string
		Expect(database.QueryRow(`
			SELECT string_agg(version_md5, ',' ORDER BY check_order DESC)
			FROM resource_config_versions WHERE resource_config_scope_id = $1`,
			scopes[0]).Scan(&newestFirst)).To(Succeed())
		Expect(newestFirst).To(Equal("new,old,older"))
	})
})
