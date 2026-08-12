package accessor_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

type realTeamFixture struct {
	Conn               db.DbConn
	TeamFactory        db.TeamFactory
	AccessTokenFactory db.AccessTokenFactory

	closeOnce sync.Once
}

// useRealTeamFixture gives a database-backed spec its own migrated clone, and
// retains the clone-local connection for specs that need to persist team
// attributes which are not exposed through TeamFactory. Route-map specs
// deliberately do not call this helper and stay database-free.
func useRealTeamFixture() *realTeamFixture {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	dbConn := postgresRunner.OpenConn()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
		lockConn := lockConns[i]
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}

	ignore := func(lager.Logger, lock.LockID) {}
	lockFactory := lock.NewLockFactory(lockConns, ignore, ignore)
	fixture := &realTeamFixture{
		Conn:               dbConn,
		TeamFactory:        db.NewTeamFactory(dbConn, lockFactory),
		AccessTokenFactory: db.NewAccessTokenFactory(dbConn),
	}
	DeferCleanup(fixture.disconnect)
	return fixture
}

// disconnect drops the connection this fixture handed out, which is how a spec
// reaches the error paths that only a database going away can produce. Cleanup
// calls it too, so it must survive being called twice.
func (fixture *realTeamFixture) disconnect() {
	GinkgoHelper()

	fixture.closeOnce.Do(func() {
		Expect(fixture.Conn.Close()).To(Succeed())
	})
}

// persistAccessToken stores a token and hands back the claims as PostgreSQL
// gives them up again. The claims column only ever holds the raw claims map --
// db.Claims marshals nothing else -- so every typed field the verifier reads is
// reconstructed by the round trip rather than carried over from the input.
func (fixture *realTeamFixture) persistAccessToken(token string, rawClaims map[string]any) db.Claims {
	GinkgoHelper()

	payload, err := json.Marshal(rawClaims)
	Expect(err).NotTo(HaveOccurred())

	var claims db.Claims
	Expect(json.Unmarshal(payload, &claims)).To(Succeed())
	Expect(fixture.AccessTokenFactory.CreateAccessToken(token, claims)).To(Succeed())

	stored, found, err := fixture.AccessTokenFactory.GetAccessToken(token)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return stored.Claims
}

// persistTeam always reloads the row so callers observe values scanned from
// PostgreSQL. Administrator status has no public setter, so set it by the
// freshly allocated row ID within this spec's isolated clone.
func (fixture *realTeamFixture) persistTeam(team atc.Team, admin bool) db.Team {
	GinkgoHelper()

	createdTeam, err := fixture.TeamFactory.CreateTeam(team)
	Expect(err).NotTo(HaveOccurred())

	if admin {
		result, err := fixture.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, createdTeam.ID())
		Expect(err).NotTo(HaveOccurred())

		rowsAffected, err := result.RowsAffected()
		Expect(err).NotTo(HaveOccurred())
		Expect(rowsAffected).To(Equal(int64(1)))
	}

	persistedTeam, found, err := fixture.TeamFactory.FindTeam(team.Name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return persistedTeam
}

// persistRoleTeam reloads the newly created row so authorization observes the
// JSON auth stored in PostgreSQL rather than the atc.Team input value.
func persistRoleTeam(teamFactory db.TeamFactory, name, role, userID string) db.Team {
	GinkgoHelper()

	auth := atc.TeamAuth{
		role: {"users": {fmt.Sprintf("test:%s", userID)}},
	}
	_, err := teamFactory.CreateTeam(atc.Team{Name: name, Auth: auth})
	Expect(err).NotTo(HaveOccurred())

	team, found, err := teamFactory.FindTeam(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return team
}

func accessForPersistedRole(teamFactory db.TeamFactory, requiredRole, teamName, actualRole, userID string) accessor.Access {
	GinkgoHelper()

	team := persistRoleTeam(teamFactory, teamName, actualRole, userID)
	return accessor.NewAccessor(verificationForUser(userID), requiredRole, "sub", []string{"system"}, []db.Team{team}, nil)
}

func verificationForUser(userID string) accessor.Verification {
	return accessor.Verification{
		HasToken:     true,
		IsTokenValid: true,
		RawClaims: map[string]any{
			"federated_claims": map[string]any{
				"connector_id": "test",
				"user_id":      userID,
			},
		},
	}
}

func TestAccessor(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Accessor Suite")
}
