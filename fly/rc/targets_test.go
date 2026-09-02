package rc_test

import (
	"os"
	"path/filepath"

	"github.com/concourse/concourse/fly/rc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Targets", func() {
	var tmpDir string
	var flyrc string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "fly-test")
		Expect(err).ToNot(HaveOccurred())

		os.Setenv("HOME", tmpDir)

		flyrc = filepath.Join(userHomeDir(), ".flyrc")
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("LoadTargets", func() {
		Context("hierarchy of loading config file", func() {
			BeforeEach(func() {
				flyrcContents := `targets:
  some-target:
    api: http://concourse.com
    team: main`
				os.WriteFile(flyrc, []byte(flyrcContents), 0777)
			})

			AfterEach(func() {
				os.Unsetenv("FLY_HOME")
				os.Unsetenv("HOME")
			})

			Context("when FLY_HOME is unset and HOME is set", func() {
				BeforeEach(func() {
					os.Setenv("FLY_HOME", "")
					os.Setenv("HOME", tmpDir)
				})
				It("loads from the path set in HOME", func() {
					targets, err := rc.LoadTargets()
					Expect(err).ToNot(HaveOccurred())
					Expect(targets).To(HaveLen(1))
				})
			})
		})
	})

	Describe("SaveTarget", func() {
		Describe("CA Cert Flag", func() {
			Describe("when 'ca_cert' is not set in the flyrc", func() {
				var targetName rc.TargetName
				BeforeEach(func() {
					targetName = "foo"
					err := rc.SaveTarget(
						targetName,
						"some api url",
						false,
						"main",
						nil,
						"",
						"",
						"",
					)
					Expect(err).ToNot(HaveOccurred())
				})

				It("returns the rc empty ca-cert", func() {
					returnedTarget, err := rc.LoadTarget(targetName, false)
					Expect(err).ToNot(HaveOccurred())
					Expect(returnedTarget.CACert()).To(BeEmpty())
				})
			})
		})
	})
})
