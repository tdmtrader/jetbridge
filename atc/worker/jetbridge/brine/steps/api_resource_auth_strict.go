package steps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/workerserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

func observeStrictPipelineAuthorization(
	database JetbridgeDB,
	logger lager.Logger,
	accessFactory accessor.AccessFactory,
	aud auditor.Auditor,
	profile string,
) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	requestedPipeline := "some-pipeline"
	authorization := ""

	switch profile {
	case "team-missing-status":
		if err := team.Delete(); err != nil {
			return "", err
		}
	case "public-context", "public-status":
		pipeline, err := saveAuthPipeline(team, requestedPipeline)
		if err != nil {
			return "", err
		}
		if err := pipeline.Expose(); err != nil {
			return "", err
		}
	case "private-authorized-context", "private-authorized-status":
		pipeline, err := saveAuthPipeline(team, requestedPipeline)
		if err != nil {
			return "", err
		}
		if err := pipeline.Hide(); err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-pipeline-viewer", "brine-subject", time.Now().Add(time.Hour))
	case "private-other-status":
		pipeline, err := saveAuthPipeline(team, requestedPipeline)
		if err != nil {
			return "", err
		}
		if err := pipeline.Hide(); err != nil {
			return "", err
		}
		other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(other, accessor.ViewerRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-other-pipeline-viewer", "brine-subject", time.Now().Add(time.Hour))
	case "private-anonymous-status":
		pipeline, err := saveAuthPipeline(team, requestedPipeline)
		if err != nil {
			return "", err
		}
		if err := pipeline.Hide(); err != nil {
			return "", err
		}
	case "missing-status", "missing-downstream":
		if _, err := saveAuthPipeline(team, "other-pipeline"); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown strict pipeline authorization profile %q", profile)
	}
	if err != nil {
		return "", err
	}

	pipelineServer := pipelineserver.NewServer(
		logger, database.TeamFactory,
		db.NewPipelineFactory(database.Conn, database.LockFactory),
		"http://127.0.0.1",
	)
	downstream := http.Handler(pipelineserver.NewScopedHandlerFactory(database.TeamFactory).HandlerFor(pipelineServer.GetPipeline))
	extraQuery := url.Values{}
	if profile == "missing-downstream" {
		workerFactory, err := strictResourceWorkerFactory(database, logger)
		if err != nil {
			return "", err
		}
		if _, err := workerFactory.SaveWorker(strictResourceATCWorker("guard-worker"), 0); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-pipeline-system", "brine-system", time.Now().Add(time.Hour))
		if err != nil {
			return "", err
		}
		downstream = http.HandlerFunc(workerserver.NewServer(logger, database.TeamFactory, workerFactory).DeleteWorker)
		extraQuery.Set(":worker_name", "guard-worker")
	}

	inner := auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory).
		HandlerFor(downstream, auth.UnauthorizedRejector{})
	response, err := runStrictResourceAuthorizationHTTP(
		logger, accessFactory, aud, atc.GetPipeline, inner, authorization,
		rata.Params{"team_name": team.Name(), "pipeline_name": requestedPipeline}, extraQuery,
	)
	if err != nil {
		return "", err
	}

	switch profile {
	case "public-context", "private-authorized-context":
		if response.Status != http.StatusOK {
			return fmt.Sprintf("status=%d", response.Status), nil
		}
		var presented atc.Pipeline
		if err := json.Unmarshal([]byte(response.Body), &presented); err != nil {
			return "", fmt.Errorf("decode production pipeline response: %w", err)
		}
		return fmt.Sprintf("status=%d;pipeline=%s;team=%s", response.Status, presented.Name, presented.TeamName), nil
	case "missing-downstream":
		present, err := strictResourceWorkerPresent(database, "guard-worker")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d;guard-worker-present=%t", response.Status, present), nil
	default:
		return fmt.Sprintf("status=%d", response.Status), nil
	}
}

func observeStrictBuildAuthorization(
	database JetbridgeDB,
	logger lager.Logger,
	accessFactory accessor.AccessFactory,
	aud auditor.Auditor,
	profile string,
) (string, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	build, err := saveAuthBuild(team)
	if err != nil {
		return "", err
	}
	requestedID := build.ID()
	authorization := ""

	switch profile {
	case "same-team-status", "same-team-context":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-build-operator", "brine-subject", time.Now().Add(time.Hour))
	case "missing-status":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-missing-build-operator", "brine-subject", time.Now().Add(time.Hour))
		requestedID += 1000000
	case "other-team-status":
		other, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if createErr != nil {
			return "", createErr
		}
		if err := grantAPIAuthRole(other, accessor.OperatorRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-other-build-operator", "brine-subject", time.Now().Add(time.Hour))
	case "weak-role-status":
		if err := grantAPIAuthRole(team, accessor.ViewerRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-build-viewer", "brine-subject", time.Now().Add(time.Hour))
	case "anonymous-status":
		if err := grantAPIAuthRole(team, accessor.OperatorRole); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown strict build authorization profile %q", profile)
	}
	if err != nil {
		return "", err
	}

	buildServer := buildserver.NewServer(logger, "http://127.0.0.1", database.TeamFactory, database.BuildFactory, nil)
	downstream := buildserver.NewScopedHandlerFactory(logger).HandlerFor(buildServer.AbortBuild)
	inner := auth.NewCheckBuildWriteAccessHandlerFactory(database.BuildFactory).
		HandlerFor(downstream, auth.UnauthorizedRejector{})
	response, err := runStrictResourceAuthorizationHTTP(
		logger, accessFactory, aud, atc.AbortBuild, inner, authorization,
		rata.Params{"build_id": strconv.Itoa(requestedID)}, nil,
	)
	if err != nil {
		return "", err
	}
	if profile == "same-team-context" {
		var aborted bool
		if err := database.Conn.QueryRow("SELECT aborted FROM builds WHERE id = $1", build.ID()).Scan(&aborted); err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d;build-aborted=%t", response.Status, aborted), nil
	}
	return fmt.Sprintf("status=%d", response.Status), nil
}

func observeStrictWorkerAuthorization(
	database JetbridgeDB,
	logger lager.Logger,
	accessFactory accessor.AccessFactory,
	aud auditor.Auditor,
	profile string,
) (string, error) {
	workerFactory, err := strictResourceWorkerFactory(database, logger)
	if err != nil {
		return "", err
	}
	authorization := ""
	workerName := "some-worker"

	saveTeamOwned := func() (db.Team, error) {
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
		if err != nil {
			return nil, err
		}
		_, err = team.SaveWorker(strictResourceATCWorker(workerName), 5*time.Minute)
		return team, err
	}
	saveGlobal := func() error {
		_, err := workerFactory.SaveWorker(strictResourceATCWorker(workerName), 5*time.Minute)
		return err
	}

	switch profile {
	case "anonymous-status", "anonymous-downstream":
		if err := saveGlobal(); err != nil {
			return "", err
		}
	case "team-admin-downstream", "team-admin-status":
		team, err := saveTeamOwned()
		if err != nil {
			return "", err
		}
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-worker-admin", "brine-subject", time.Now().Add(time.Hour))
	case "system-downstream":
		if _, err := saveTeamOwned(); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-worker-system", "brine-system", time.Now().Add(time.Hour))
	case "team-match-downstream":
		team, err := saveTeamOwned()
		if err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-worker-member", "brine-subject", time.Now().Add(time.Hour))
	case "team-other-downstream", "team-other-status":
		if _, err := saveTeamOwned(); err != nil {
			return "", err
		}
		other, err := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
		if err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(other, accessor.MemberRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-other-worker-member", "brine-subject", time.Now().Add(time.Hour))
	case "global-admin-downstream", "global-admin-status":
		if err := saveGlobal(); err != nil {
			return "", err
		}
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
		if err != nil {
			return "", err
		}
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-global-worker-admin", "brine-subject", time.Now().Add(time.Hour))
	case "global-member-downstream", "global-member-status":
		if err := saveGlobal(); err != nil {
			return "", err
		}
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
		if err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-global-worker-member", "brine-subject", time.Now().Add(time.Hour))
	case "missing-downstream", "missing-status":
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
		if err != nil {
			return "", err
		}
		if err := grantAPIAuthRole(team, accessor.MemberRole); err != nil {
			return "", err
		}
		authorization, err = persistAPIAuthToken(database, "strict-missing-worker-member", "brine-subject", time.Now().Add(time.Hour))
	default:
		return "", fmt.Errorf("unknown strict worker authorization profile %q", profile)
	}
	if err != nil {
		return "", err
	}

	workerServer := workerserver.NewServer(logger, database.TeamFactory, workerFactory)
	downstream := http.HandlerFunc(workerServer.DeleteWorker)
	inner := auth.NewCheckWorkerTeamAccessHandlerFactory(workerFactory).
		HandlerFor(downstream, auth.UnauthorizedRejector{})
	response, err := runStrictResourceAuthorizationHTTP(
		logger, accessFactory, aud, atc.DeleteWorker, inner, authorization,
		rata.Params{"worker_name": workerName}, nil,
	)
	if err != nil {
		return "", err
	}
	present, err := strictResourceWorkerPresent(database, workerName)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(profile, "-downstream") {
		return fmt.Sprintf("status=%d;worker-present=%t", response.Status, present), nil
	}
	return fmt.Sprintf("status=%d", response.Status), nil
}
