// Package jetbridge is a core.Runner that tests each candidate with one job of
// a JetBridge pipeline, pinning the candidate's version so the job tests it.
package jetbridge

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/concourse/concourse/queue/config"
)

// DefaultFailedPattern matches a log line `FAILED: <test name>`.
const DefaultFailedPattern = `^FAILED: (.+?)\s*$`

// Config is the runner section of a queue's config file. Credential names where
// the bearer token is (env:NAME or file:PATH), never the token itself. A build
// not done WaitCap after Start is no verdict.
type Config struct {
	Kind, URL, Team, Pipeline, Job, Resource, Credential string
	WaitCap                                              time.Duration `yaml:"wait_cap"`
	FailedPattern                                        string        `yaml:"failed_pattern"`
}

var known = map[string][]string{
	"runner": {"kind", "url", "team", "pipeline", "job", "resource", "credential", "wait_cap", "failed_pattern"},
}

// Parse reads the runner section strictly, filling the defaults.
func Parse(n *yaml.Node) (Config, error) {
	if err := config.Strict(n, "runner", known); err != nil {
		return Config{}, err
	}
	c := Config{Team: "main", WaitCap: time.Hour, FailedPattern: DefaultFailedPattern}
	if err := n.Decode(&c); err != nil {
		return Config{}, errors.New("runner: a value has the wrong type") // a yaml error may quote a value
	}
	switch {
	case c.Kind != "jetbridge":
		return Config{}, errors.New("runner.kind: unsupported value; use one of: jetbridge")
	case c.URL == "" || c.Pipeline == "" || c.Job == "" || c.Resource == "":
		return Config{}, errors.New("runner.url, runner.pipeline, runner.job and runner.resource are required")
	case !strings.HasPrefix(c.Credential, "env:") && !strings.HasPrefix(c.Credential, "file:"):
		return Config{}, errors.New("runner.credential must name an env var (env:NAME) or a file (file:PATH)")
	case c.WaitCap <= 0:
		return Config{}, errors.New("runner.wait_cap must be more than zero")
	}
	if re, err := regexp.Compile(c.FailedPattern); err != nil || re.NumSubexp() != 1 {
		return Config{}, errors.New("runner.failed_pattern: must be a regular expression with exactly one group")
	}
	if err := config.URL("runner.url", c.URL); err != nil { // as decoded, whoever loaded the section
		return Config{}, err
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

// Secret is the bearer token Credential names, read afresh.
func (c Config) Secret() (string, error) {
	b, err := []byte(os.Getenv(strings.TrimPrefix(c.Credential, "env:"))), error(nil)
	if path, ok := strings.CutPrefix(c.Credential, "file:"); ok {
		b, err = os.ReadFile(path)
	}
	if s := strings.TrimSpace(string(b)); s != "" || err != nil {
		return s, err
	}
	return "", errors.New("runner.credential is empty")
}
