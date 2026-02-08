package atccmd_test

import (
	"fmt"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/acme/autocert"
)

type CommandSuite struct {
	suite.Suite
	*require.Assertions
}

func (s *CommandSuite) TestLetsEncryptDefaultIsUpToDate() {
	cmd := &atccmd.ATCCommand{}

	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	opt := parser.Find("run").FindOptionByLongName("lets-encrypt-acme-url")
	s.NotNil(opt)

	s.Equal(opt.Default, []string{autocert.DefaultACMEDirectory})
}

func (s *CommandSuite) TestInvalidConcurrentRequestLimitAction() {
	cmd := &atccmd.RunCommand{}
	parser := flags.NewParser(cmd, flags.None)
	_, err := parser.ParseArgs([]string{
		"--client-secret",
		"client-secret",
		"--concurrent-request-limit",
		fmt.Sprintf("%s:2", atc.GetInfo),
	})

	s.Contains(
		err.Error(),
		fmt.Sprintf("action '%s' is not supported", atc.GetInfo),
	)
}

func (s *CommandSuite) TestKubernetesFlags() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	nsOpt := runCmd.FindOptionByLongName("kubernetes-namespace")
	s.NotNil(nsOpt, "--kubernetes-namespace flag should exist")
	s.Contains(nsOpt.Description, "Kubernetes namespace")

	kubeconfigOpt := runCmd.FindOptionByLongName("kubernetes-kubeconfig")
	s.NotNil(kubeconfigOpt, "--kubernetes-kubeconfig flag should exist")
	s.Contains(kubeconfigOpt.Description, "kubeconfig")
}

func (s *CommandSuite) TestKubernetesFieldsExistOnRunCommand() {
	cmd := &atccmd.RunCommand{}
	s.Equal("", cmd.Kubernetes.Namespace, "namespace should default to empty string")
	s.Equal("", cmd.Kubernetes.Kubeconfig, "kubeconfig should default to empty string")

	cmd.Kubernetes.Namespace = "ci-workers"
	cmd.Kubernetes.Kubeconfig = "/etc/k8s/config"

	s.Equal("ci-workers", cmd.Kubernetes.Namespace)
	s.Equal("/etc/k8s/config", cmd.Kubernetes.Kubeconfig)
}

func TestSuite(t *testing.T) {
	suite.Run(t, &CommandSuite{
		Assertions: require.New(t),
	})
}
