package topgun

import (
	"encoding/json"
	"strings"
	"time"

	testotel "github.com/concourse/concourse/testhelpers/otel"
	"github.com/onsi/gomega/gexec"
	"go.opentelemetry.io/otel/attribute"

	. "github.com/onsi/gomega"
)

type FlyCli struct {
	Bin    string
	Target string
	Home   string
}

type Pipeline struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Paused   bool   `json:"paused"`
	Public   bool   `json:"public"`
	TeamName string `json:"team_name"`
}

type Version struct {
	ID      int               `json:"id"`
	Version map[string]string `json:"version"`
	Enabled bool              `json:"enabled"`
}

func (f *FlyCli) Login(user, password, endpoint string, loginArgs ...string) {
	Eventually(func() *gexec.Session {
		sess := f.Start(
			append([]string{"login",
				"-c", endpoint,
				"-u", user,
				"-p", password},
				loginArgs...)...,
		)

		<-sess.Exited
		return sess
	}, 2*time.Minute, 10*time.Second).
		Should(gexec.Exit(0), "Fly should have been able to log in")
}

func (f *FlyCli) Run(argv ...string) {
	cmd := "fly"
	if len(argv) > 0 {
		cmd = "fly." + argv[0]
	}
	end := testotel.StartSpan(cmd, attribute.String("fly.args", strings.Join(argv, " ")))
	defer end()
	Wait(f.Start(argv...))
}

func (f *FlyCli) Start(argv ...string) *gexec.Session {
	return Start([]string{"HOME=" + f.Home}, f.Bin, append([]string{"-t", f.Target}, argv...)...)
}

func (f *FlyCli) GetPipelines() []Pipeline {
	var pipelines = []Pipeline{}

	sess := f.Start("pipelines", "--json")
	<-sess.Exited
	Expect(sess.ExitCode()).To(BeZero())

	err := json.Unmarshal(sess.Out.Contents(), &pipelines)
	Expect(err).ToNot(HaveOccurred())

	return pipelines
}

func (f *FlyCli) GetVersions(pipeline string, resource string) []Version {
	var versions = []Version{}

	sess := f.Start("resource-versions", "-r", pipeline+"/"+resource, "--json")
	<-sess.Exited
	Expect(sess.ExitCode()).To(BeZero())

	err := json.Unmarshal(sess.Out.Contents(), &versions)
	Expect(err).ToNot(HaveOccurred())

	return versions
}

func (f *FlyCli) GetUserRole(teamName string) []string {
	type RoleInfo struct {
		Teams map[string][]string `json:"teams"`
	}
	var teamsInfo RoleInfo = RoleInfo{}

	sess := f.Start("userinfo", "--json")
	<-sess.Exited
	Expect(sess.ExitCode()).To(BeZero())

	err := json.Unmarshal(sess.Out.Contents(), &teamsInfo)
	Expect(err).ToNot(HaveOccurred())

	return teamsInfo.Teams[teamName]
}

func BuildBinary() string {
	flyBinPath, err := gexec.Build("github.com/concourse/concourse/fly")
	Expect(err).ToNot(HaveOccurred())

	return flyBinPath
}
