package token_test

import (
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

type realTokenDB struct {
	Conn         db.DbConn
	AccessTokens db.AccessTokenFactory
	Users        db.UserFactory
}

func useRealTokenDB() *realTokenDB {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	conn := postgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})

	return &realTokenDB{
		Conn:         conn,
		AccessTokens: db.NewAccessTokenFactory(conn),
		Users:        db.NewUserFactory(conn),
	}
}

func TestToken(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Token Suite")
}
