package atccmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/concourse/concourse/agent/broker"
)

type agentBrokerCatalogFile struct {
	Profiles []broker.Profile `json:"profiles"`
}

func loadAgentBrokerCatalog(path string) (*broker.Catalog, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent broker catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var configured agentBrokerCatalogFile
	if err := decoder.Decode(&configured); err != nil {
		return nil, fmt.Errorf("parse agent broker catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse agent broker catalog: exactly one JSON value is required")
		}
		return nil, fmt.Errorf("parse agent broker catalog trailing JSON: %w", err)
	}
	catalog, err := broker.NewCatalog(configured.Profiles)
	if err != nil {
		return nil, fmt.Errorf("validate agent broker catalog: %w", err)
	}
	return catalog, nil
}
