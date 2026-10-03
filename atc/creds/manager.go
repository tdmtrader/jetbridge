package creds

import (
	"encoding/json"

	"code.cloudfoundry.org/lager/v3"
	"github.com/jessevdk/go-flags"
)

type Manager interface {
	IsConfigured() bool
	Validate() error
	Health() (*HealthResponse, error)
	Init(lager.Logger) error
	Close(logger lager.Logger)

	NewSecretsFactory(lager.Logger) (SecretsFactory, error)
}

type ManagerFactory interface {
	AddConfig(*flags.Group) Manager
	NewInstance(any) (Manager, error)
}

type Managers map[string]Manager

type CredentialManagementConfig struct {
	RetryConfig SecretRetryConfig
	CacheConfig SecretCacheConfig
}

// NewSecrets creates a Secrets object from secretsFactory based on configs.
func (c CredentialManagementConfig) NewSecrets(secretsFactory SecretsFactory) Secrets {
	result := secretsFactory.NewSecrets()
	result = NewRetryableSecrets(result, c.RetryConfig)
	if c.CacheConfig.Enabled {
		result = NewCachedSecrets(result, c.CacheConfig)
	}
	return result
}

type HealthResponse struct {
	Response any    `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Method   string `json:"method,omitempty"`
}

// MarshalHealth is the JSON form of a manager whose only state worth showing
// is its health: {"health": ...}, or Health's error unchanged.
func MarshalHealth(manager Manager) ([]byte, error) {
	health, err := manager.Health()
	if err != nil {
		return nil, err
	}

	return json.Marshal(&map[string]any{
		"health": health,
	})
}

var managerFactories = map[string]ManagerFactory{}

func Register(name string, managerFactory ManagerFactory) {
	managerFactories[name] = managerFactory
}

func ManagerFactories() map[string]ManagerFactory {
	return managerFactories
}
