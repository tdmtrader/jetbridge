package accessor_test

import (
	"database/sql"
	"fmt"
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

// useRealTeamFactory gives a database-backed spec its own migrated clone.
// Route-map specs deliberately do not call this helper and stay database-free.
func useRealTeamFactory() db.TeamFactory {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	dbConn := postgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(dbConn.Close()).To(Succeed())
	})

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
	return db.NewTeamFactory(dbConn, lockFactory)
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
