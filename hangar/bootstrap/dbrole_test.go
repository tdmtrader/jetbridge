package bootstrap_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/hangar/bootstrap"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("The activation database role", func() {
	const secretName = "activation-dsn"

	var (
		ctx    context.Context
		admin  *sql.DB
		store  *memoryStore
		role   string
		logged []string
	)

	step := func(adminDB *sql.DB) bootstrap.DatabaseRole {
		return bootstrap.DatabaseRole{
			Admin:  adminDB,
			Store:  store,
			Secret: secretName,
			DSN:    postgresRunner.ActivationRoleDataSourceName,
			Log: func(event string, fields map[string]string) {
				logged = append(logged, fmt.Sprintf("%s %v", event, fields))
			},
		}
	}

	logsIn := func(dsn string) error {
		conn, err := sql.Open("pgx", dsn)
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close()
		return conn.PingContext(ctx)
	}

	roleExists := func() bool {
		var exists bool
		Expect(admin.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists)).To(Succeed())
		return exists
	}

	BeforeEach(func() {
		ctx = context.Background()
		postgresRunner.CreateTestDBFromTemplate()
		DeferCleanup(postgresRunner.DropTestDB)
		admin = postgresRunner.OpenDB()
		DeferCleanup(admin.Close)
		store = newMemoryStore()
		logged = nil
		role = postgresRunner.ActivationRoleName()
		var exists bool
		Expect(admin.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists)).To(Succeed())
		if exists {
			_, err := admin.Exec(fmt.Sprintf(`DROP OWNED BY %q; DROP ROLE %q`, role, role))
			Expect(err).NotTo(HaveOccurred())
		}
		// A wrong password must be refused, not waved through by the runner's
		// trust lines, or every login check below would pass vacuously.
		postgresRunner.RequireSCRAM(role)
	})

	It("creates the role, then its Secret, and the Secret's credential logs in where a wrong one does not", func() {
		Expect(bootstrap.EnsureActivationRole(ctx, step(admin))).To(Succeed())

		dsn := string(store.secrets[secretName].Data["dsn"])
		Expect(logsIn(dsn)).To(Succeed())
		Expect(logsIn(postgresRunner.ActivationRoleDataSourceName(role, "not-the-password"))).NotTo(Succeed())
	})

	It("completes a run interrupted after the role and before the Secret", func() {
		failing := &failingStore{memoryStore: store}
		interrupted := step(admin)
		interrupted.Store = failing
		Expect(bootstrap.EnsureActivationRole(ctx, interrupted)).NotTo(Succeed())
		Expect(roleExists()).To(BeTrue())
		Expect(store.secrets).NotTo(HaveKey(secretName))

		Expect(bootstrap.EnsureActivationRole(ctx, step(admin))).To(Succeed())
		Expect(logsIn(string(store.secrets[secretName].Data["dsn"]))).To(Succeed())
	})

	It("re-aligns the role to an existing Secret it does not accept", func() {
		Expect(bootstrap.EnsureActivationRole(ctx, step(admin))).To(Succeed())
		dsn := postgresRunner.ActivationRoleDataSourceName(role, "the-secret-is-the-authority")
		store.secrets[secretName] = bootstrap.Secret{Name: secretName, Data: map[string][]byte{"dsn": []byte(dsn)}}
		Expect(logsIn(dsn)).NotTo(Succeed())

		Expect(bootstrap.EnsureActivationRole(ctx, step(admin))).To(Succeed())
		Expect(logsIn(dsn)).To(Succeed())
		Expect(strings.Join(logged, "\n")).To(ContainSubstring("realigned"))
	})

	It("refuses, creating nothing, with a credential that cannot create roles", func() {
		_, err := admin.Exec(`DROP ROLE IF EXISTS bootstrap_weak; CREATE ROLE bootstrap_weak LOGIN`)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _, _ = admin.Exec(`DROP ROLE IF EXISTS bootstrap_weak`) })
		weak, err := sql.Open("pgx", fmt.Sprintf("host=/tmp user=bootstrap_weak dbname=testdb sslmode=disable port=%d", postgresRunner.Port))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(weak.Close)

		err = bootstrap.EnsureActivationRole(ctx, step(weak))
		Expect(errors.Is(err, bootstrap.ErrRefused)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("cannot create roles"))
		Expect(roleExists()).To(BeFalse())
		Expect(store.secrets).To(BeEmpty())
	})

	It("logs no password", func() {
		Expect(bootstrap.EnsureActivationRole(ctx, step(admin))).To(Succeed())
		dsn := string(store.secrets[secretName].Data["dsn"])
		password := dsn[strings.Index(dsn, "password=")+len("password="):]
		password = strings.Fields(password)[0]
		Expect(strings.Join(logged, "\n")).NotTo(ContainSubstring(password))
		Expect(strings.Join(logged, "\n")).NotTo(ContainSubstring("password="))
	})
})

// failingStore refuses every create, as an API server lost mid-run does.
type failingStore struct{ *memoryStore }

func (store *failingStore) Create(context.Context, bootstrap.Secret) error {
	return errors.New("the API server went away")
}
