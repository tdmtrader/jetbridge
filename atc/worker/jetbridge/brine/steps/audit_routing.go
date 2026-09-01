package steps

import (
	"fmt"
	"net/http"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

type AuditRoutingObservation struct{ Value string }

func AuditRoutingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, AuditRoutingObservation](
			"the production auditor evaluates category {string} with case {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (AuditRoutingObservation, error) {
				category, profile, err := twoParams("the production auditor evaluates category {string} with case {string}", p)
				if err != nil {
					return AuditRoutingObservation{}, err
				}
				value, err := observeAuditRouting(category, profile)
				return AuditRoutingObservation{Value: value}, err
			},
		),
		CheckString[AuditRoutingObservation]("the audit routing result is {string}", "audit routing result", func(in AuditRoutingObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeAuditRouting(category, profile string) (string, error) {
	logger := lagertest.NewTestLogger("brine-auditor")
	request, err := http.NewRequest(http.MethodGet, "http://localhost:8080", nil)
	if err != nil {
		return "", err
	}
	if category == "all" && profile == "all-routes" {
		audit := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)
		for _, route := range atc.Routes {
			audit.Audit(route.Name, "brine-user", request)
		}
		return fmt.Sprintf("logged=%t;action=all-routes", len(logger.Logs()) > 0), nil
	}

	actions := map[string]string{
		"build": "GetBuildPlan", "container": "GetContainer", "job": "GetJob",
		"pipeline": "GetPipeline", "resource": "GetResource", "system": "SaveConfig",
		"team": "ListTeams", "worker": "ListWorkers", "volume": "ListVolumes",
	}
	matchedAction, ok := actions[category]
	if !ok {
		return "", fmt.Errorf("unknown audit category %q", category)
	}
	enabled := strings.HasPrefix(profile, "enabled-")
	action := matchedAction
	if strings.HasSuffix(profile, "-other") {
		action = "SaveConfig"
		if category == "system" {
			action = "GetBuild"
		}
	}
	flags := make([]bool, 9)
	indexes := map[string]int{"build": 0, "container": 1, "job": 2, "pipeline": 3, "resource": 4, "system": 5, "team": 6, "worker": 7, "volume": 8}
	flags[indexes[category]] = enabled
	audit := auditor.NewAuditor(flags[0], flags[1], flags[2], flags[3], flags[4], flags[5], flags[6], flags[7], flags[8], logger)
	audit.Audit(action, "brine-user", request)
	logs := logger.Logs()
	logged := len(logs) > 0
	if logged {
		if got, _ := logs[0].Data["action"].(string); got != action {
			return "", fmt.Errorf("audit action = %q, want %q", got, action)
		}
	}
	return fmt.Sprintf("logged=%t;action=%s", logged, action), nil
}
