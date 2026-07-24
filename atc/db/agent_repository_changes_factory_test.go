package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func insertRepositoryChangeProjectionInput(suffix string) (snapshot.SnapshotID, snapshot.SnapshotID) {
	baseMetadata, err := json.Marshal(contracts.RepositoryMetadata{
		RepositoryID: "sha256:" + strings.Repeat("a", 64), ObjectFormat: "sha1",
		HeadSHA: strings.Repeat("b", 40), TreeSHA: strings.Repeat("c", 40), RootCommits: []string{strings.Repeat("d", 40)},
	})
	Expect(err).NotTo(HaveOccurred())
	changeMetadata, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: "sha256:" + strings.Repeat("a", 64), BaseSHA: strings.Repeat("b", 40),
		ResultTreeSHA: strings.Repeat("c", 40), Representation: "patch",
	})
	Expect(err).NotTo(HaveOccurred())
	var baseID snapshot.SnapshotID
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(type_name, type_version, digest, byte_size, file_count, representation, intrinsic_metadata, content_state)
		VALUES ('repository', 1, $1, 2048, 4, 'filesystem-tree-v1', $2, 'available')
		RETURNING id
	`, "sha256:"+strings.Repeat("e", 63)+suffix, baseMetadata).Scan(&baseID)).To(Succeed())

	var changeID snapshot.SnapshotID
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshots
			(type_name, type_version, digest, byte_size, file_count, representation, intrinsic_metadata, content_state)
		VALUES ('repository-change', 1, $1, 1024, 2, 'filesystem-tree-v1', $2, 'available')
		RETURNING id
	`, "sha256:"+strings.Repeat("f", 63)+suffix, changeMetadata).Scan(&changeID)).To(Succeed())

	var productionID int64
	Expect(dbConn.QueryRow(`
		INSERT INTO agent_snapshot_productions
			(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
			 plan_id, attempt, step_kind, step_name, output_port)
		VALUES ($1, 'build', $2, $3, $4, 'repository-projector-test',
		        'plan', '1', 'task', 'change', 'change')
		RETURNING id
	`, int64(changeID), time.Now().UnixNano(), defaultTeam.ID(), defaultTeam.Name()).Scan(&productionID)).To(Succeed())
	_, err = dbConn.Exec(`
		INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
		VALUES ($1, 0, 'base', $2)
	`, productionID, int64(baseID))
	Expect(err).NotTo(HaveOccurred())
	return changeID, baseID
}

var _ = Describe("AgentRepositoryChangesFactory", func() {
	var factory db.AgentRepositoryChangesFactory

	BeforeEach(func() {
		factory = db.NewAgentRepositoryChangesFactoryForTeam(dbConn, defaultTeam.Name())
	})

	It("discovers missing and retryable projections with their exact lineage", func() {
		changeID, baseID := insertRepositoryChangeProjectionInput("1")
		candidates, err := factory.ListUnprojectedRepositoryChanges(context.Background(), 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(ContainElement(HaveField("ID", changeID)))

		input, found, err := factory.FindRepositoryChangeInput(context.Background(), changeID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(input.Snapshot.ID).To(Equal(changeID))
		Expect(input.Inputs).To(HaveLen(1))
		Expect(input.Inputs[0].Port).To(Equal("base"))
		Expect(input.Inputs[0].Snapshot.ID).To(Equal(baseID))

		Expect(factory.SetRepositoryChangeProjectionStatus(context.Background(), changeID, projection.RepositoryChangeProjectionUnavailable, "object store offline")).To(Succeed())
		candidates, err = factory.ListUnprojectedRepositoryChanges(context.Background(), 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(ContainElement(HaveField("ID", changeID)))

		Expect(factory.SetRepositoryChangeProjectionStatus(context.Background(), changeID, projection.RepositoryChangeProjectionInvalid, "sealed lineage is invalid")).To(Succeed())
		candidates, err = factory.ListUnprojectedRepositoryChanges(context.Background(), 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).NotTo(ContainElement(HaveField("ID", changeID)))
	})

	It("chooses one semantically matching production occurrence for a value snapshot", func() {
		changeID, firstBaseID := insertRepositoryChangeProjectionInput("9")
		var secondBaseID snapshot.SnapshotID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation,
				 intrinsic_metadata, content_state)
			SELECT type_name, type_version, $2, byte_size, file_count, representation,
			       intrinsic_metadata, content_state
			FROM agent_snapshots WHERE id = $1
			RETURNING id
		`, int64(firstBaseID), "sha256:"+strings.Repeat("0", 63)+"9").Scan(&secondBaseID)).To(Succeed())

		var secondProductionID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
				 plan_id, attempt, step_kind, step_name, output_port)
			VALUES ($1, 'build', $2, $3, $4, 'repository-projector-test',
			        'second-plan', '1', 'task', 'change', 'change')
			RETURNING id
		`, int64(changeID), time.Now().UnixNano(), defaultTeam.ID(), defaultTeam.Name()).Scan(&secondProductionID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_snapshot_lineage (production_id, position, input_port, input_snapshot_id)
			VALUES ($1, 0, 'base', $2)
		`, secondProductionID, int64(secondBaseID))
		Expect(err).NotTo(HaveOccurred())

		input, found, err := factory.FindRepositoryChangeInput(context.Background(), changeID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(input.Inputs).To(HaveLen(1))
		Expect(input.Inputs[0].Port).To(Equal("base"))
		Expect(input.Inputs[0].Snapshot.ID).To(Equal(firstBaseID))
	})

	It("idempotently persists a bounded ready projection by snapshot_id", func() {
		changeID, _ := insertRepositoryChangeProjectionInput("2")
		value := projection.RepositoryChange{
			SnapshotID: changeID, Status: projection.RepositoryChangeProjectionReady,
			RepositoryID: "sha256:" + strings.Repeat("a", 64), BaseSHA: strings.Repeat("b", 40),
			ResultTreeSHA: strings.Repeat("c", 40), Representation: "patch",
			Files:     []gitcheck.ChangedFile{{Path: "README.md", Status: gitcheck.ChangeModified, LinesAdded: 2, LinesDeleted: 1, Patch: "patch"}},
			FileCount: 1, LinesAdded: 2, LinesDeleted: 1, UnifiedDiff: "diff --git a/README.md b/README.md\n",
		}
		Expect(factory.UpsertRepositoryChangeProjection(context.Background(), value)).To(Succeed())
		value.LinesAdded = 3
		value.Files[0].LinesAdded = 3
		Expect(factory.UpsertRepositoryChangeProjection(context.Background(), value)).To(Succeed())

		got, found, err := factory.GetRepositoryChangeProjection(context.Background(), changeID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Status).To(Equal(projection.RepositoryChangeProjectionReady))
		Expect(got.LinesAdded).To(Equal(3))
		Expect(got.Files).To(Equal(value.Files))
		Expect(got.UnifiedDiff).To(Equal(value.UnifiedDiff))

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_repository_change_projections WHERE snapshot_id = $1`, int64(changeID)).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("returns persisted unavailable status without fabricating diff data", func() {
		changeID, _ := insertRepositoryChangeProjectionInput("3")
		Expect(factory.SetRepositoryChangeProjectionStatus(context.Background(), changeID, projection.RepositoryChangeProjectionUnavailable, "temporarily missing")).To(Succeed())
		got, found, err := factory.GetRepositoryChangeProjection(context.Background(), changeID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Status).To(Equal(projection.RepositoryChangeProjectionUnavailable))
		Expect(got.ProjectionError).To(Equal("temporarily missing"))
		Expect(got.Files).To(BeEmpty())
	})

	It("rejects an oversized durable diff before writing it", func() {
		changeID, _ := insertRepositoryChangeProjectionInput("4")
		value := projection.RepositoryChange{
			SnapshotID: changeID, Status: projection.RepositoryChangeProjectionReady,
			RepositoryID: "sha256:" + strings.Repeat("a", 64), BaseSHA: strings.Repeat("b", 40),
			ResultTreeSHA: strings.Repeat("c", 40), Representation: "patch",
			Files: []gitcheck.ChangedFile{}, UnifiedDiff: strings.Repeat("x", gitcheck.BoundedUnifiedDiffBytes+1),
		}
		Expect(factory.UpsertRepositoryChangeProjection(context.Background(), value)).To(MatchError(ContainSubstring("65536")))
	})
})
