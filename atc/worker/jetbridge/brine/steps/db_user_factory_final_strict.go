package steps

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

type DBUserFactoryFinalObservation struct {
	Profile string
	Failure string
}

func DBUserFactoryFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBUserFactoryFinalObservation](
			"the production user factory evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBUserFactoryFinalObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return DBUserFactoryFinalObservation{}, fmt.Errorf("expected user factory profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBUserFactoryFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBUserFactoryFinalObservation{Profile: profile, Failure: observeDBUserFactoryFinal(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBUserFactoryFinalObservation](
			"the user factory observation exactly matches {string}",
			func(observation DBUserFactoryFinalObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected user factory profile")
				}
				if observation.Profile != profile {
					return fmt.Errorf("profile got %q, want %q", observation.Profile, profile)
				}
				if observation.Failure != "" {
					return fmt.Errorf("user factory observation: %s", observation.Failure)
				}
				return nil
			},
		),
	}
}

func observeDBUserFactoryFinal(database JetbridgeDB, profile string) string {
	if profile != "new-user" && profile != "different-connector" && profile != "same-subject-count" && profile != "same-subject-name" && profile != "same-subject-login" {
		return fmt.Sprintf("unknown profile %q", profile)
	}
	factory := db.NewUserFactory(database.Conn)
	username := "strict-user"
	subject := func(connector string) string {
		return base64.StdEncoding.EncodeToString([]byte(username + connector))
	}

	if profile == "different-connector" {
		if err := factory.CreateOrUpdateUser(username, "basic", subject("basic")); err != nil {
			return err.Error()
		}
	}
	var previous time.Time
	if profile == "same-subject-count" || profile == "same-subject-name" || profile == "same-subject-login" {
		if err := factory.CreateOrUpdateUser(username, "github", subject("github")); err != nil {
			return err.Error()
		}
		users, err := strictCreatedUsers(factory, username)
		if err != nil {
			return err.Error()
		}
		if len(users) != 1 {
			return fmt.Sprintf("precondition user count=%d", len(users))
		}
		previous = users[0].LastLogin()
	}

	before := time.Now()
	if err := factory.CreateOrUpdateUser(username, "github", subject("github")); err != nil {
		return err.Error()
	}
	after := time.Now()
	users, err := strictCreatedUsers(factory, username)
	if err != nil {
		return err.Error()
	}

	switch profile {
	case "new-user":
		if len(users) != 1 || users[0].Name() != username || users[0].LastLogin().Before(before.Add(-time.Second)) || users[0].LastLogin().After(after.Add(time.Second)) {
			return fmt.Sprintf("count=%d name=%q login=%s window=%s..%s", len(users), firstStrictUserName(users), firstStrictUserLogin(users), before, after)
		}
	case "different-connector":
		if len(users) != 2 || users[0].ID() == users[1].ID() {
			return fmt.Sprintf("count=%d ids=%v", len(users), strictUserIDs(users))
		}
	case "same-subject-count":
		if len(users) != 1 {
			return fmt.Sprintf("count=%d, want 1", len(users))
		}
	case "same-subject-name":
		if len(users) == 0 || users[0].Name() != username {
			return fmt.Sprintf("count=%d name=%q", len(users), firstStrictUserName(users))
		}
	case "same-subject-login":
		if len(users) == 0 || users[0].LastLogin().Equal(previous) {
			return fmt.Sprintf("count=%d previous=%s current=%s", len(users), previous, firstStrictUserLogin(users))
		}
	}
	return ""
}

func strictCreatedUsers(factory db.UserFactory, username string) ([]db.User, error) {
	all, err := factory.GetAllUsers()
	if err != nil {
		return nil, err
	}
	users := make([]db.User, 0, len(all))
	for _, user := range all {
		if user.Name() == username || user.Name() == username+"-wrong" {
			users = append(users, user)
		}
	}
	return users, nil
}

func firstStrictUserName(users []db.User) string {
	if len(users) == 0 {
		return ""
	}
	return users[0].Name()
}

func firstStrictUserLogin(users []db.User) time.Time {
	if len(users) == 0 {
		return time.Time{}
	}
	return users[0].LastLogin()
}

func strictUserIDs(users []db.User) []int {
	ids := make([]int, len(users))
	for i, user := range users {
		ids[i] = user.ID()
	}
	return ids
}
