package runs

import (
	"strings"

	"github.com/concourse/concourse/atc/db"
)

// QueuePrincipal is a landing queue acting for its team (ADR-0009).
//
// The queue is core's own component, so unlike a build it has no row to be
// verified against: what it asserts is the team and queue it runs for, and
// the rule is the build form's, its own team and no other. Only atc/landing
// constructs one; architecture_test.go pins that, because any package that
// could name a team here could admit Runs as that team.
type QueuePrincipal struct {
	TeamName  string
	QueueName string
}

// authorizeQueue decides a landing queue acting for its team.
func authorizeQueue(teams []db.Team, teamName string, queue *QueuePrincipal, customRoles map[string]string, action string) (authorization, error) {
	if queue.TeamName == "" || queue.QueueName == "" || !strings.EqualFold(teamName, queue.TeamName) {
		return authorization{}, ErrUnauthorized
	}
	if !buildHasRequiredRole(customRoles, action) {
		return authorization{}, ErrUnauthorized
	}
	team := findTeam(teams, teamName)
	if team == nil {
		return authorization{}, ErrUnauthorized
	}
	return authorization{
		createdBy: queueCreatedBy(queue),
		isAdmin:   false,
		team:      team,
	}, nil
}

// queueCreatedBy is the display identity recorded as the run's creator when a
// landing queue admitted it. The prefix keeps it from colliding with a person
// or a build.
func queueCreatedBy(queue *QueuePrincipal) string {
	return "landing-queue/" + queue.TeamName + "/" + queue.QueueName
}
