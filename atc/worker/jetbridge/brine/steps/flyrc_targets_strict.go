package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/rc"
)

type FlyRCTargetsObservation struct {
	Profile string
	Value   string
}

var flyRCTargetsEnvironment sync.Mutex

const flyRCTargetsCAPEM = `-----BEGIN CERTIFICATE-----
MIIB0zCCAX2gAwIBAgIJAI/M7BYjwB+uMA0GCSqGSIb3DQEBBQUAMEUxCzAJBgNV
BAYTAkFVMRMwEQYDVQQIDApTb21lLVN0YXRlMSEwHwYDVQQKDBhJbnRlcm5ldCBX
aWRnaXRzIFB0eSBMdGQwHhcNMTIwOTEyMjE1MjAyWhcNMTUwOTEyMjE1MjAyWjBF
MQswCQYDVQQGEwJBVTETMBEGA1UECAwKU29tZS1TdGF0ZTEhMB8GA1UECgwYSW50
ZXJuZXQgV2lkZ2l0cyBQdHkgTHRkMFwwDQYJKoZIhvcNAQEBBQADSwAwSAJBANLJ
hPHhITqQbPklG3ibCVxwGMRfp/v4XqhfdQHdcVfHap6NQ5Wok/4xIA+ui35/MmNa
rtNuC+BdZ1tMuVCPFZcCAwEAAaNQME4wHQYDVR0OBBYEFJvKs8RfJaXTH08W+SGv
zQyKn0H8MB8GA1UdIwQYMBaAFJvKs8RfJaXTH08W+SGvzQyKn0H8MAwGA1UdEwQF
MAMBAf8wDQYJKoZIhvcNAQEFBQADQQBJlffJHybjDGxRMqaRmDhX0+6v02TUKZsW
r5QuVbpQhH6u+0UgcW0jp9QwpxoPTLTWGXEWBBBurxFwiCBhkQ+V
-----END CERTIFICATE-----`

func FlyRCTargetsStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, FlyRCTargetsObservation](
			"the production flyrc target profile {string} is exercised",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (FlyRCTargetsObservation, error) {
				profile, err := paramAt("the production flyrc target profile {string} is exercised", p, 0)
				if err != nil {
					return FlyRCTargetsObservation{}, err
				}
				value, err := observeFlyRCTargets(profile)
				return FlyRCTargetsObservation{Profile: profile, Value: value}, err
			},
		),
		brine.DefineCheck[FlyRCTargetsObservation](
			"the flyrc target observation exactly matches {string}",
			func(in FlyRCTargetsObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the flyrc target observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
					return fmt.Errorf("flyrc profile got %q, want %q", in.Profile, profile)
				}
				if in.Value != "ok" {
					return fmt.Errorf("%s: %s", profile, in.Value)
				}
				return nil
			},
		),
	}
}

func observeFlyRCTargets(profile string) (value string, resultErr error) {
	flyRCTargetsEnvironment.Lock()
	defer flyRCTargetsEnvironment.Unlock()

	tempDir, err := os.MkdirTemp("", "brine-flyrc-targets-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	originalFlyHome, hadFlyHome := os.LookupEnv("FLY_HOME")
	originalHome, hadHome := os.LookupEnv("HOME")
	defer func() {
		if hadFlyHome {
			_ = os.Setenv("FLY_HOME", originalFlyHome)
		} else {
			_ = os.Unsetenv("FLY_HOME")
		}
		if hadHome {
			_ = os.Setenv("HOME", originalHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}()
	if err := os.Setenv("FLY_HOME", tempDir); err != nil {
		return "", err
	}
	if err := os.Setenv("HOME", tempDir); err != nil {
		return "", err
	}
	flyrc := filepath.Join(tempDir, ".flyrc")

	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	write := func(contents string, mode os.FileMode) error {
		return os.WriteFile(flyrc, []byte(contents), mode)
	}
	load := func() (rc.Targets, error) { return rc.LoadTargets() }
	initial := `targets:
  target-name:
    api: http://concourse.example
    team: some-team
    token:
      type: Bearer
      value: some-token
  new-target:
    api: another-api
    team: another-team
    token:
      type: Bearer
      value: another-token
`

	switch profile {
	case "default-team":
		if err := write("targets:\n  some-target:\n    api: http://concourse.example\n", 0600); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		if targets["some-target"].TeamName != atc.DefaultTeamName {
			return fail("team=%q", targets["some-target"].TeamName), nil
		}

	case "fly-home-precedence":
		if err := write("targets:\n  selected:\n    api: preferred\n    team: main\n", 0600); err != nil {
			return "", err
		}
		if err := os.Setenv("HOME", filepath.Join(tempDir, "missing")); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		if len(targets) != 1 || targets["selected"].API != "preferred" {
			return fail("targets=%v", targets), nil
		}

	case "delete-one":
		if err := write(initial, 0600); err != nil {
			return "", err
		}
		if err := rc.DeleteTarget("target-name"); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		if len(targets) != 1 || targets["new-target"].API != "another-api" {
			return fail("targets=%v", targets), nil
		}

	case "delete-all":
		if err := write(initial, 0600); err != nil {
			return "", err
		}
		if err := rc.DeleteAllTargets(); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		if len(targets) != 0 {
			return fail("target-count=%d", len(targets)), nil
		}

	case "update-properties":
		if err := write("targets:\n  some-target:\n    api: old-api\n    team: old-team\n    token:\n      type: Bearer\n      value: token\n", 0600); err != nil {
			return "", err
		}
		if err := rc.UpdateTargetProps("some-target", rc.TargetProps{API: "new-api", TeamName: "new-team"}); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		got := targets["some-target"]
		if got.API != "new-api" || got.TeamName != "new-team" || got.Token == nil || got.Token.Value != "token" {
			return fail("target=%+v", got), nil
		}

	case "rename-target":
		if err := write("targets:\n  old:\n    api: retained\n    team: main\n", 0600); err != nil {
			return "", err
		}
		if err := rc.UpdateTargetName("old", "new"); err != nil {
			return "", err
		}
		targets, err := load()
		if err != nil {
			return "", err
		}
		if len(targets) != 1 || targets["new"].API != "retained" {
			return fail("targets=%v", targets), nil
		}

	case "new-file-permissions":
		if err := rc.SaveTarget("foo", "url", false, "main", nil, "", "", ""); err != nil {
			return "", err
		}
		info, err := os.Stat(flyrc)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm() != 0600 {
			return fail("mode=%#o", info.Mode().Perm()), nil
		}

	case "existing-file-permissions":
		if err := write("", 0755); err != nil {
			return "", err
		}
		if err := os.Chmod(flyrc, 0755); err != nil {
			return "", err
		}
		if err := rc.SaveTarget("foo", "url", false, "main", nil, "", "", ""); err != nil {
			return "", err
		}
		info, err := os.Stat(flyrc)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm() != 0755 {
			return fail("mode=%#o", info.Mode().Perm()), nil
		}

	case "ca-set":
		certificate := flyRCTargetsCAPEM
		if err := rc.SaveTarget("foo", "url", false, "main", nil, certificate, "", ""); err != nil {
			return "", err
		}
		target, err := rc.LoadTarget("foo", false)
		if err != nil {
			return "", err
		}
		if target.CACert() != certificate {
			return fail("ca=%q want=%q", target.CACert(), certificate), nil
		}

	case "insecure-false", "insecure-true":
		insecure := profile == "insecure-true"
		if err := rc.SaveTarget("foo", "url", insecure, "main", nil, "", "", ""); err != nil {
			return "", err
		}
		target, err := rc.LoadTarget("foo", false)
		if err != nil {
			return "", err
		}
		if target.TLSConfig().InsecureSkipVerify != insecure {
			return fail("insecure=%t want=%t", target.TLSConfig().InsecureSkipVerify, insecure), nil
		}

	case "unknown-target":
		_, err := rc.LoadTarget("bogus", false)
		if err != (rc.UnknownTargetError{TargetName: "bogus"}) {
			return fail("error=%v", err), nil
		}

	case "no-target":
		_, err := rc.LoadTarget("", false)
		if err != rc.ErrNoTargetSpecified {
			return fail("error=%v", err), nil
		}

	default:
		return "", fmt.Errorf("unknown flyrc target profile %q", profile)
	}

	return "ok", nil
}
