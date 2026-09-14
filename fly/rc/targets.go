package rc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/concourse/concourse/atc"

	"sigs.k8s.io/yaml"
)

var (
	ErrNoTargetSpecified = errors.New("no target specified")
	ErrNoTargetFromURL   = errors.New("no target matching url")
)

type UnknownTargetError struct {
	TargetName TargetName
}

func (err UnknownTargetError) Error() string {
	return fmt.Sprintf("unknown target: %s", err.TargetName)
}

type Targets map[TargetName]TargetProps

type RC struct {
	Targets Targets `json:"targets"`
}

type TargetProps struct {
	API            string       `json:"api"`
	TeamName       string       `json:"team"`
	Insecure       bool         `json:"insecure,omitempty"`
	Token          *TargetToken `json:"token,omitempty"`
	CACert         string       `json:"ca_cert,omitempty"`
	ClientCertPath string       `json:"client_cert_path,omitempty"`
	ClientKeyPath  string       `json:"client_key_path,omitempty"`
}

type TargetToken struct {
	Type          string `json:"type"`
	Value         string `json:"value"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	OAuthClientID string `json:"oauth_client_id,omitempty"`
}

func flyrcPath() string {
	return filepath.Join(userHomeDir(), ".flyrc")
}

func LogoutTarget(targetName TargetName) error {
	return updateTargets(func(flyTargets Targets) error {

		if target, ok := flyTargets[targetName]; ok {
			if target.Token != nil {
				*target.Token = TargetToken{}
			}
		}

		return nil
	})
}

func DeleteTarget(targetName TargetName) error {
	return updateTargets(func(flyTargets Targets) error {
		delete(flyTargets, targetName)
		return nil
	})
}

func DeleteAllTargets() error {
	return updateTargets(func(targets Targets) error { clear(targets); return nil })
}

func UpdateTargetProps(targetName TargetName, targetProps TargetProps) error {
	return updateTargets(func(flyTargets Targets) error {
		target := flyTargets[targetName]

		if targetProps.API != "" {
			target.API = targetProps.API
		}

		if targetProps.TeamName != "" {
			target.TeamName = targetProps.TeamName
		}

		flyTargets[targetName] = target

		return nil
	})
}

func UpdateTargetName(targetName TargetName, newTargetName TargetName) error {
	return updateTargets(func(flyTargets Targets) error {
		if newTargetName != "" {
			flyTargets[newTargetName] = flyTargets[targetName]
			delete(flyTargets, targetName)
		}

		return nil
	})
}

func SaveTarget(
	targetName TargetName,
	api string,
	insecure bool,
	teamName string,
	token *TargetToken,
	caCert string,
	clientCertPath string,
	clientKeyPath string,
) error {
	return updateTargets(func(flyTargets Targets) error {
		newInfo := flyTargets[targetName]
		newInfo.API = api
		newInfo.Insecure = insecure
		newInfo.Token = token
		newInfo.TeamName = teamName
		newInfo.CACert = caCert
		newInfo.ClientCertPath = clientCertPath
		newInfo.ClientKeyPath = clientKeyPath

		flyTargets[targetName] = newInfo
		return nil
	})
}

func selectTarget(selectedTarget TargetName) (TargetProps, error) {
	if selectedTarget == "" {
		return TargetProps{}, ErrNoTargetSpecified
	}
	flyTargets, err := LoadTargets()
	if err != nil {
		return TargetProps{}, err
	}

	target, ok := flyTargets[selectedTarget]
	if !ok {
		return TargetProps{}, UnknownTargetError{selectedTarget}
	}
	return target, nil
}

func userHomeDir() string {
	home := os.Getenv("FLY_HOME")
	if home != "" {
		return home
	}

	home = os.Getenv("HOME")
	if home != "" {
		return home
	}

	if runtime.GOOS == "windows" {
		home = os.Getenv("USERPROFILE")
		if home != "" {
			return home
		}

		home = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home != "" {
			return home
		}
	}

	panic("could not detect home directory for .flyrc")
}

func LoadTargets() (Targets, error) {
	var rc RC

	flyrc := flyrcPath()
	flyTargetsBytes, err := os.ReadFile(flyrc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		err = yaml.Unmarshal(flyTargetsBytes, &rc)
		if err != nil {
			return nil, fmt.Errorf("in the file '%s': %s", flyrc, err)
		}
	}

	targets := rc.Targets
	if targets == nil {
		targets = map[TargetName]TargetProps{}
	}

	for name, targetProps := range targets {
		if targetProps.TeamName == "" {
			targetProps.TeamName = atc.DefaultTeamName
			targets[name] = targetProps
		}
	}

	return targets, nil
}

func writeTargets(configFileLocation string, targetsToWrite Targets) error {
	// Keep extension fields written by newer clients while updating known fields.
	var raw map[string]json.RawMessage
	old, err := os.ReadFile(configFileLocation)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		data, err := yaml.YAMLToJSON(old)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	previous := map[TargetName]map[string]json.RawMessage{}
	if data := raw["targets"]; len(data) != 0 {
		if err := json.Unmarshal(data, &previous); err != nil {
			return err
		}
	}
	merged := map[TargetName]map[string]json.RawMessage{}
	for name, props := range targetsToWrite {
		fields := previous[name]
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		for _, key := range []string{"api", "team", "insecure", "token", "ca_cert", "client_cert_path", "client_key_path"} {
			delete(fields, key)
		}
		data, err := json.Marshal(props)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		merged[name] = fields
	}
	raw["targets"], err = json.Marshal(merged)
	if err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	yamlBytes, err := yaml.JSONToYAML(data)
	if err != nil {
		return err
	}

	return atomicWriteTargets(configFileLocation, yamlBytes)
}
