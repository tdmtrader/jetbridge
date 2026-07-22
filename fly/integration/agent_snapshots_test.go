package integration_test

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

const agentSnapshotsIntegrationPath = "/api/v1/teams/main/agent/snapshots"

func agentSnapshotFixture(id string, byteSize int64) snapshot.Snapshot {
	parsedID, err := snapshot.ParseSnapshotID(id)
	Expect(err).NotTo(HaveOccurred())
	return snapshot.Snapshot{
		ID:             parsedID,
		Type:           snapshot.TypeRef("review/v1"),
		Digest:         snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		ByteSize:       byteSize,
		FileCount:      1,
		Representation: "application/x-tar",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC),
	}
}

func runAgentSnapshotsCommand(args ...string) *gexec.Session {
	commandArgs := append([]string{"-t", targetName, "agent", "snapshots"}, args...)
	session, err := gexec.Start(exec.Command(flyPath, commandArgs...), GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	<-session.Exited
	return session
}

var _ = Describe("fly agent snapshots", func() {
	It("creates a deterministic raw tar in the selected team and prints human and JSON results", func() {
		source := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(source, "empty"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(source, "review.txt"), []byte("looks good\n"), 0o600)).To(Succeed())
		manifest := agentSnapshotFixture("9007199254740993", 42)

		verifyCreate := func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.URL.Path).To(Equal(agentSnapshotsIntegrationPath))
			Expect(r.URL.Query().Get("type")).To(Equal("review/v1"))
			Expect(r.URL.Query()).To(HaveLen(1))
			Expect(r.Header.Get("Content-Type")).To(Equal("application/x-tar"))
			key := r.Header.Get("Idempotency-Key")
			Expect(key).To(HaveLen(32))
			_, err := hex.DecodeString(key)
			Expect(err).NotTo(HaveOccurred())

			reader := tar.NewReader(r.Body)
			type entry struct {
				name string
				mode int64
				body string
			}
			var entries []entry
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				Expect(err).NotTo(HaveOccurred())
				body, err := io.ReadAll(reader)
				Expect(err).NotTo(HaveOccurred())
				Expect(header.ModTime.Unix()).To(BeZero())
				Expect(header.Uid).To(BeZero())
				Expect(header.Gid).To(BeZero())
				entries = append(entries, entry{header.Name, header.Mode, string(body)})
			}
			Expect(entries).To(Equal([]entry{
				{name: "empty/", mode: 0o755},
				{name: "review.txt", mode: 0o644, body: "looks good\n"},
			}))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			Expect(json.NewEncoder(w).Encode(manifest)).To(Succeed())
		}
		atcServer.AppendHandlers(verifyCreate, infoHandler(), verifyCreate)

		human := runAgentSnapshotsCommand("create", "--type", "review/v1", "--from", source)
		Expect(human.ExitCode()).To(Equal(0))
		Expect(human.Out).To(gbytes.Say(`snapshot 9007199254740993`))

		jsonResult := runAgentSnapshotsCommand("create", "--type", "review/v1", "--from", source, "--json")
		Expect(jsonResult.ExitCode()).To(Equal(0))
		Expect(jsonResult.Out).To(gbytes.Say(`"id": "9007199254740993"`))
	})

	It("rejects an escaping symlink before contacting ATC", func() {
		if runtime.GOOS == "windows" {
			Skip("symlink behavior is Unix-specific")
		}
		source := GinkgoT().TempDir()
		Expect(os.Symlink("../outside", filepath.Join(source, "escape"))).To(Succeed())
		before := len(atcServer.ReceivedRequests())

		session := runAgentSnapshotsCommand("create", "--type", "review/v1", "--from", source)
		Expect(session.ExitCode()).NotTo(Equal(0))
		Expect(session.Err).To(gbytes.Say(`target escapes the source directory`))
		Expect(atcServer.ReceivedRequests()).To(HaveLen(before))
	})

	It("surfaces the server's bounded semantic validation rejection", func() {
		source := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(source, "review.txt"), []byte("bad"), 0o644)).To(Succeed())
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest(http.MethodPost, agentSnapshotsIntegrationPath, "type=review%2Fv1"),
			ghttp.RespondWithJSONEncoded(http.StatusUnprocessableEntity, map[string]string{
				"error": "validation_failed", "message": "review schema rejected",
			}),
		))

		session := runAgentSnapshotsCommand("create", "--type", "review/v1", "--from", source)
		Expect(session.ExitCode()).NotTo(Equal(0))
		Expect(session.Err).To(gbytes.Say(`validation_failed: review schema rejected`))
	})

	It("lists every filter without losing large IDs and preserves quoted IDs in JSON", func() {
		manifests := []snapshot.Snapshot{
			agentSnapshotFixture("9223372036854775807", 99),
			agentSnapshotFixture("9007199254740993", 42),
		}
		query := "content_state=available&created_after=2026-07-01T00%3A00%3A00Z&limit=2&type=review%2Fv1"
		listHandler := ghttp.CombineHandlers(
			ghttp.VerifyRequest(http.MethodGet, agentSnapshotsIntegrationPath, query),
			ghttp.RespondWithJSONEncoded(http.StatusOK, manifests),
		)
		atcServer.AppendHandlers(listHandler, infoHandler(), listHandler)

		args := []string{"list", "--type", "review/v1", "--content-state", "available", "--created-after", "2026-07-01T00:00:00Z", "--limit", "2"}
		human := runAgentSnapshotsCommand(args...)
		Expect(human.ExitCode()).To(Equal(0))
		Expect(human.Out).To(gbytes.Say(`9223372036854775807\s+review/v1\s+available`))
		Expect(human.Out).To(gbytes.Say(`9007199254740993\s+review/v1\s+available`))

		jsonResult := runAgentSnapshotsCommand(append(args, "--json")...)
		Expect(jsonResult.ExitCode()).To(Equal(0))
		Expect(jsonResult.Out).To(gbytes.Say(`"id": "9223372036854775807"`))
		Expect(jsonResult.Out).To(gbytes.Say(`"id": "9007199254740993"`))
	})

	It("shows an exact ID above 2^53", func() {
		id := "9007199254740993"
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest(http.MethodGet, agentSnapshotsIntegrationPath+"/"+id),
			ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
				"manifest": agentSnapshotFixture(id, 42), "replica_count": 2,
				"retention_claims": []any{}, "productions": []any{}, "downstream": []any{},
			}),
		))

		session := runAgentSnapshotsCommand("show", id, "--json")
		Expect(session.ExitCode()).To(Equal(0))
		Expect(session.Out).To(gbytes.Say(`"id": "9007199254740993"`))
		Expect(atcServer.ReceivedRequests()[len(atcServer.ReceivedRequests())-1].URL.Path).To(HaveSuffix("/" + id))
	})

	It("downloads exact verified bytes and never publishes a truncated response", func() {
		id := "9223372036854775807"
		archive := []byte("canonical tar bytes")
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(archive))
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest(http.MethodGet, agentSnapshotsIntegrationPath+"/"+id+"/content"),
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/x-tar")
					w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
					w.Header().Set("ETag", `"`+digest+`"`)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write(archive)
					Expect(err).NotTo(HaveOccurred())
				},
			),
			infoHandler(),
			ghttp.CombineHandlers(
				ghttp.VerifyRequest(http.MethodGet, agentSnapshotsIntegrationPath+"/"+id+"/content"),
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/x-tar")
					w.Header().Set("Content-Length", fmt.Sprint(len(archive)+10))
					w.Header().Set("ETag", `"`+digest+`"`)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write(archive[:4])
					Expect(err).NotTo(HaveOccurred())
				},
			),
		)

		destination := filepath.Join(GinkgoT().TempDir(), "snapshot.tar")
		success := runAgentSnapshotsCommand("download", id, "--to", destination)
		Expect(success.ExitCode()).To(Equal(0))
		Expect(os.ReadFile(destination)).To(Equal(archive))

		Expect(os.WriteFile(destination, []byte("existing"), 0o600)).To(Succeed())
		truncated := runAgentSnapshotsCommand("download", id, "--to", destination)
		Expect(truncated.ExitCode()).NotTo(Equal(0))
		Expect(os.ReadFile(destination)).To(Equal([]byte("existing")))
	})

	It("pins and unpins the exact ID for the selected team", func() {
		id := "9007199254740993"
		createdAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest(http.MethodPut, agentSnapshotsIntegrationPath+"/"+id+"/pin"),
				func(w http.ResponseWriter, r *http.Request) {
					Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
					var body map[string]string
					Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
					Expect(body).To(Equal(map[string]string{"reason": "release audit"}))
					w.Header().Set("Content-Type", "application/json")
					Expect(json.NewEncoder(w).Encode(snapshot.RetentionClaim{
						ID: 7, SnapshotID: snapshot.SnapshotID(9007199254740993),
						Class: snapshot.RetentionClassPin, Actor: "subject:test", Reason: "release audit", CreatedAt: createdAt,
					})).To(Succeed())
				},
			),
			infoHandler(),
			ghttp.CombineHandlers(
				ghttp.VerifyRequest(http.MethodDelete, agentSnapshotsIntegrationPath+"/"+id+"/pin"),
				ghttp.RespondWith(http.StatusNoContent, nil),
			),
		)

		pin := runAgentSnapshotsCommand("pin", id, "--reason", "release audit")
		Expect(pin.ExitCode()).To(Equal(0))
		Expect(pin.Out).To(gbytes.Say(`pinned snapshot 9007199254740993 \(release audit\)`))
		unpin := runAgentSnapshotsCommand("unpin", id)
		Expect(unpin.ExitCode()).To(Equal(0))
		Expect(unpin.Out).To(gbytes.Say(`unpinned snapshot 9007199254740993`))
	})
})
