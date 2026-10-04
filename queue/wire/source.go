package wire

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Source is the queue resource's source, the same in both resources but for
// mode; queue drain reads it too.
type Source struct {
	Mode       string `json:"mode"` // candidate or verdict
	URI        string `json:"uri"`
	PrivateKey string `json:"private_key"`
	KnownHosts string `json:"known_hosts"` // required with private_key
	Main       string `json:"main"`
	Config     string `json:"config"`      // the queue config, inline
	ConfigFile string `json:"config_file"` // or a path to it
	Owner      string `json:"owner"`       // the fixed lease owner, so each check renews the lease
	WaitCap    string `json:"wait_cap"`    // how long a run waits for a verdict; default 1h
}

// LoadConfig reads the queue config, inline or from a file; source.uri and source.main, if set, replace the config's.
func LoadConfig(s Source) (config.Config, error) {
	data := []byte(s.Config)
	if (s.Config == "") == (s.ConfigFile == "") {
		return config.Config{}, errors.New("give one of source.config or source.config_file")
	}
	if s.ConfigFile != "" {
		b, err := os.ReadFile(s.ConfigFile)
		if err != nil {
			return config.Config{}, errors.New("source.config_file cannot be read")
		}
		data = b
	}
	if err := config.URL("source.uri", s.URI); err != nil {
		return config.Config{}, err
	}
	return config.ParseWith(data, func(c *config.Config) {
		c.Repository.URI, c.Repository.Main = cmp.Or(s.URI, c.Repository.URI), cmp.Or(s.Main, c.Repository.Main)
	})
}

// SSHKey writes the private key and known hosts to 0600 files git's ssh uses, removed by cleanup.
func SSHKey(s Source) (cleanup func(), err error) {
	if s.PrivateKey == "" {
		return func() {}, nil
	}
	for line := range strings.SplitSeq(s.PrivateKey, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "-----") {
			core.Secrets.Add(l)
		}
	}
	if s.KnownHosts == "" {
		return nil, errors.New("source.known_hosts is required with source.private_key")
	}
	dir, err := os.MkdirTemp("", "queue-resource-ssh-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	key, hosts := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	if err := errors.Join(os.WriteFile(key, []byte(strings.TrimSpace(s.PrivateKey)+"\n"), 0o600),
		os.WriteFile(hosts, []byte(s.KnownHosts+"\n"), 0o600)); err != nil {
		cleanup()
		return nil, err
	}
	os.Setenv("GIT_SSH_COMMAND", "ssh -i "+key+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile="+hosts)
	return cleanup, nil
}
