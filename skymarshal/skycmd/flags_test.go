package skycmd_test

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/skymarshal/skycmd"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("skyDisplayUserIdGenerator", func() {
	var displayUserIdGenerator atc.DisplayUserIdGenerator

	Context("NewSkyDisplayUserIdGenerator", func() {
		Context("when connector is invalid", func() {
			It("should return an error", func() {
				var err error
				displayUserIdGenerator, err = skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"dummy": "user"})
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(errors.New("invalid connector: dummy")))
				Expect(displayUserIdGenerator).To(BeNil())
			})
		})

		Context("when connector field is invalid", func() {
			It("should return an error", func() {
				var err error
				displayUserIdGenerator, err = skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user"})
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(errors.New("invalid user field user of connector local")))
				Expect(displayUserIdGenerator).To(BeNil())
			})
		})

		DescribeTable("local display fields", func(field, expected string) {
			generator, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": field})
			Expect(err).NotTo(HaveOccurred())
			Expect(generator.DisplayUserId("local", "userid", "username", "preferredUsername", "email")).To(Equal(expected))
		},
			Entry("user ID", "user_id", "userid"),
			Entry("name", "name", "username"),
			Entry("username", "username", "preferredUsername"),
			Entry("email", "email", "email"),
		)
		It("uses the default local display ID", func() {
			generator, err := skycmd.NewSkyDisplayUserIdGenerator(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(generator.DisplayUserId("local", "id", "name", "preferred", "email")).To(Equal("preferred"))
			Expect(generator.DisplayUserId("local", "id", "name", "", "email")).To(Equal("id"))
			Expect(generator.DisplayUserId("local", "", "name", "", "email")).To(Equal("name"))
		})
	})
})

var _ = Describe("team file authentication", func() {
	DescribeTable("rejects unsupported membership even alongside local users", func(connector string) {
		path := filepath.Join(GinkgoT().TempDir(), "team.yml")
		content := "roles:\n- name: owner\n  local:\n    users: [admin]\n  " + connector + ":\n    users: [external]\n"
		Expect(os.WriteFile(path, []byte(content), 0600)).To(Succeed())
		flags := skycmd.AuthTeamFlags{}
		Expect(flags.Config.UnmarshalFlag(path)).To(Succeed())
		auth, err := flags.Format()
		Expect(err).To(MatchError("unsupported connector \"" + connector + "\" in team role \"owner\": only local authentication is supported"))
		Expect(auth).To(BeNil())
	},
		Entry("GitHub", "github"),
		Entry("GitLab", "gitlab"),
		Entry("LDAP", "ldap"),
		Entry("SAML", "saml"),
		Entry("OIDC", "oidc"),
		Entry("OAuth", "oauth"),
		Entry("CloudFoundry", "cf"),
		Entry("Bitbucket Cloud", "bitbucket-cloud"),
		Entry("Microsoft", "microsoft"),
		Entry("unknown connector", "unknown"),
	)
})
