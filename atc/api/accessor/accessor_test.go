package accessor_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

var _ = Describe("Accessor", func() {
	Describe("UserInfo", func() {
		Context("when there is a valid token", func() {
			DescribeTable("DisplayUserId for the field configured on the connector",
				func(fieldName string, expected string) {
					generator, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"oidc": fieldName})
					Expect(err).NotTo(HaveOccurred())

					access := accessor.NewAccessor(
						accessor.Verification{
							HasToken:     true,
							IsTokenValid: true,
							RawClaims: map[string]any{
								"sub":                "some-sub",
								"name":               "some-name",
								"preferred_username": "some-user-name",
								"email":               "some-email",
								"federated_claims": map[string]any{
									"user_id":      "some-id",
									"connector_id": "oidc",
								},
							},
						},
						"", "sub", []string{"system"}, nil, generator,
					)
					Expect(access.UserInfo().DisplayUserId).To(Equal(expected))
				},

				Entry("user_id", "user_id", "some-id"),
				Entry("name", "name", "some-name"),
				Entry("username", "username", "some-user-name"),
				Entry("email", "email", "some-email"),
			)
		})
	})
})
