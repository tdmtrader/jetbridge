package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// hangarSource is atccmd's status source, over the suite's real database.
type hangarSource struct {
	conn db.DbConn
}

type hangarSourceTransactor struct{ conn db.DbConn }

func (transactor hangarSourceTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}
	return db.HangarOutputTx{Tx: tx}, nil
}

func (source hangarSource) repository() *db.HangarOutputRepository {
	return db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
}

func (source hangarSource) Read(ctx context.Context) (hangaroutput.Status, error) {
	reader := &hangaroutput.StatusReader{Transactor: hangarSourceTransactor{conn: source.conn}, Repository: source.repository()}
	return reader.Read(ctx)
}

func (source hangarSource) Resolve(ctx context.Context, id int64) error {
	return hangaroutput.ResolveFinding(ctx, hangarSourceTransactor{conn: source.conn}, source.repository(), id)
}

var _ = Describe("Hangar status API", func() {
	var (
		realdb *realDB
		server *httptest.Server
	)

	BeforeEach(func() {
		realdb = useRealDB()
		realdb.Deps.hangarStatus = hangarSource{conn: realdb.Conn}
		server = realdb.Serve()
		fakeAccess.IsAuthenticatedReturns(true)
	})

	get := func() *http.Response {
		GinkgoHelper()
		response, err := client.Get(server.URL + "/api/v1/hangar/status")
		Expect(err).NotTo(HaveOccurred())
		return response
	}

	resolve := func(id string) *http.Response {
		GinkgoHelper()
		request, err := http.NewRequest("PUT", server.URL+"/api/v1/hangar/findings/"+id+"/resolve", nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		return response
	}

	It("is admin-only", func() {
		fakeAccess.IsAdminReturns(false)
		Expect(get().StatusCode).To(Equal(http.StatusForbidden))
		Expect(resolve("1").StatusCode).To(Equal(http.StatusForbidden))
	})

	Context("as an admin", func() {
		BeforeEach(func() {
			fakeAccess.IsAdminReturns(true)
		})

		It("reports the in-service flag, the residue and the open findings, and resolves one by id", func() {
			_, err := db.SetHangarEnabled(context.Background(), realdb.Conn, false)
			Expect(err).NotTo(HaveOccurred())

			tx, err := realdb.Conn.Begin()
			Expect(err).NotTo(HaveOccurred())
			Expect(db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()).RecordRuntimeAtRisk(
				context.Background(), db.HangarOutputTx{Tx: tx}, output.IntegrityFindingRecord{
					Violation: output.ViolationOutOfBandAbsence, Subject: "scope/digest/1", Detail: "gone",
				})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			response := get()
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var status atc.HangarStatus
			Expect(json.NewDecoder(response.Body).Decode(&status)).To(Succeed())
			Expect(status.Enabled).To(BeFalse())
			Expect(status.Drained).To(BeTrue())
			Expect(status.AtRisk).To(BeTrue())
			Expect(status.Residue.Total).To(BeZero())
			Expect(status.Findings).To(HaveLen(1))
			Expect(status.Findings[0].Violation).To(Equal("out_of_band_absence"))
			Expect(status.Findings[0].BlocksAdmission).To(BeTrue())

			id := strconv.FormatInt(status.Findings[0].ID, 10)
			Expect(resolve(id).StatusCode).To(Equal(http.StatusNoContent))
			Expect(resolve(id).StatusCode).To(Equal(http.StatusNoContent), "resolving again is idempotent")
			Expect(resolve("999999").StatusCode).To(Equal(http.StatusNotFound), "no such finding")
			Expect(resolve("not-a-number").StatusCode).To(Equal(http.StatusBadRequest))

			response = get()
			Expect(json.NewDecoder(response.Body).Decode(&status)).To(Succeed())
			Expect(status.Findings).To(BeEmpty())
			Expect(status.AtRisk).To(BeFalse())
		})

		It("says so on a web node with no output plane", func() {
			realdb.Deps.hangarStatus = nil
			unconfigured := realdb.Serve()
			response, err := client.Get(unconfigured.URL + "/api/v1/hangar/status")
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		})
	})
})
