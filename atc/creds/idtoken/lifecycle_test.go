package idtoken_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("IDToken Lifecycle", func() {
	var (
		lifecycler idtoken.SigningKeyLifecycler
		ctx        context.Context
	)

	BeforeEach(func() {
		lifecycler = idtoken.SigningKeyLifecycler{
			Logger:              lager.NewLogger(""),
			DBSigningKeyFactory: signingKeyFactory,
			KeyRotationPeriod:   1 * time.Hour,
			KeyGracePeriod:      10 * time.Minute,
		}

		ctx = context.Background()
	})

	It("makes sure signing keys are created when none exist", func() {
		Expect(allSigningKeys()).To(HaveLen(0))

		Expect(lifecycler.Run(ctx)).To(Succeed())

		Expect(allSigningKeys()).To(HaveLen(2))

		rsaKey, err := signingKeyFactory.GetNewestKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())
		Expect(rsaKey.KeyType()).To(Equal(db.SigningKeyTypeRSA))

		ecKey, err := signingKeyFactory.GetNewestKey(db.SigningKeyTypeEC)
		Expect(err).ToNot(HaveOccurred())
		Expect(ecKey.KeyType()).To(Equal(db.SigningKeyTypeEC))

		// make sure a re-run does not create additional keys
		Expect(lifecycler.Run(ctx)).To(Succeed())
		Expect(allSigningKeys()).To(HaveLen(2))
	})

	It("generates new keys when existing keys are too old", func() {
		oldRSAKey := saveSigningKey(*rsaJWK, 61*time.Minute)
		oldECKey := saveSigningKey(*ecJWK, 61*time.Minute)

		Expect(allSigningKeys()).To(HaveLen(2))

		Expect(lifecycler.Run(ctx)).To(Succeed())

		// old keys are not deleted until after the grace period, so we should now have 4 keys
		Expect(allSigningKeys()).To(HaveLen(4))
		Expect(signingKeyExists(oldRSAKey.ID())).To(BeTrue(), "a rotated key survives its grace period")
		Expect(signingKeyExists(oldECKey.ID())).To(BeTrue(), "a rotated key survives its grace period")

		rsaKey, err := signingKeyFactory.GetNewestKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())
		Expect(rsaKey.KeyType()).To(Equal(db.SigningKeyTypeRSA))
		Expect(rsaKey.ID()).NotTo(Equal(oldRSAKey.ID()))

		ecKey, err := signingKeyFactory.GetNewestKey(db.SigningKeyTypeEC)
		Expect(err).ToNot(HaveOccurred())
		Expect(ecKey.KeyType()).To(Equal(db.SigningKeyTypeEC))
		Expect(ecKey.ID()).NotTo(Equal(oldECKey.ID()))

		// make sure a re-run does not create additional keys
		Expect(lifecycler.Run(ctx)).To(Succeed())
		Expect(allSigningKeys()).To(HaveLen(4))
	})

	It("removes outdated keys after grace period", func() {
		oldRSAKey := saveSigningKey(*rsaJWK, 3*time.Hour)
		oldECKey := saveSigningKey(*ecJWK, 3*time.Hour)
		newRSAKey := saveSigningKey(*rsaJWK, 12*time.Minute)
		newECKey := saveSigningKey(*ecJWK, 12*time.Minute)

		Expect(allSigningKeys()).To(HaveLen(4))

		Expect(lifecycler.Run(ctx)).To(Succeed())

		// The old pair is past rotation and past grace, so those rows are gone.
		// The 12-minute-old pair is past the 10-minute grace period but not past
		// the hour rotation, so it stays.
		Expect(signingKeyExists(oldRSAKey.ID())).To(BeFalse())
		Expect(signingKeyExists(oldECKey.ID())).To(BeFalse())
		Expect(signingKeyExists(newRSAKey.ID())).To(BeTrue())
		Expect(signingKeyExists(newECKey.ID())).To(BeTrue())
	})
})
