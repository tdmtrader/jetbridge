package kubernetes

import (
	"code.cloudfoundry.org/lager/v3"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/atc/creds"
)

type kubernetesFactory struct {
	logger lager.Logger

	client          kubernetes.Interface
	namespacePrefix string
}

func NewKubernetesFactory(logger lager.Logger, client kubernetes.Interface, namespacePrefix string) *kubernetesFactory {
	return &kubernetesFactory{
		logger:          logger,
		client:          client,
		namespacePrefix: namespacePrefix,
	}
}

func (factory *kubernetesFactory) NewSecrets() creds.Secrets {
	return &Secrets{
		logger:          factory.logger,
		client:          factory.client,
		namespacePrefix: factory.namespacePrefix,
	}
}
