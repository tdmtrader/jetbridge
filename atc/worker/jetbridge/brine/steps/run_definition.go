package steps

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/encryption"
	"github.com/concourse/concourse/atc/db/migration"
)

type RetainedRunDefinition struct {
	DB          JetbridgeDB
	Team        db.Team
	Template    db.Pipeline
	Creation    db.RunCreation
	Original    atc.RunDefinition
	Read        *atc.RunDefinition
	MutationErr error
	RolledBack  bool
}

func RunDefinitionDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("the encrypted database rolls back the empty definition migration", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			key, err := definitionEncryptionKey("AES256Key-32Characters1234567890")
			if err != nil {
				return in, err
			}
			conn := in.DB.runner.OpenSingleton()
			conn.SetMaxOpenConns(4)
			defer conn.Close()
			migrator := migration.NewMigrator(conn, in.DB.LockFactory)
			migrations, err := migrator.Migrations()
			if err != nil {
				return in, err
			}
			head, err := migrator.SupportedVersion()
			if err != nil {
				return in, err
			}
			target, previous := 0, 0
			for _, migration := range migrations {
				if strings.HasSuffix(migration.Name, "_retain_run_definitions.up.sql") {
					target = migration.Version
				}
			}
			for _, migration := range migrations {
				if migration.Direction == "up" && migration.Version < target && migration.Version > previous {
					previous = migration.Version
				}
			}
			if target == 0 || previous == 0 {
				return in, fmt.Errorf("could not locate the definition migration and its predecessor")
			}
			if err := migrator.Migrate(key, nil, head); err != nil {
				return in, err
			}
			in.MutationErr = migrator.Migrate(key, nil, previous)
			if in.MutationErr == nil {
				version, err := migrator.CurrentVersion()
				if err != nil {
					return in, err
				}
				if version != previous {
					return in, fmt.Errorf("migration remained at %d instead of %d", version, previous)
				}
			}
			return in, nil
		}),
		CheckThat[RetainedRunDefinition]("the schema rollback completes without losing encryption support", func(in RetainedRunDefinition) error { return in.MutationErr }),
		brine.DefineMapUsing[brine.Empty, RetainedRunDefinition]("a Run admitted from a parameterized template", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RetainedRunDefinition, error) {
			return retainedDefinitionFixture(res, false)
		}),
		brine.DefineMapUsing[brine.Empty, RetainedRunDefinition]("an admission transaction that rejects its new Run before commit", []string{"jetbridge-db"}, func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RetainedRunDefinition, error) {
			return retainedDefinitionFixture(res, true)
		}),
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("the template command and default parameter change", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			payload, err := json.Marshal(in.Original.Template)
			if err != nil {
				return in, err
			}
			var changed atc.Config
			if err := json.Unmarshal(payload, &changed); err != nil {
				return in, err
			}
			changed.Params[0].Default = "new-default"
			changed.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Run.Args = []string{"changed"}
			_, _, err = in.Team.SavePipeline(in.Template.PipelineRef(), changed, in.Template.ConfigVersion(), false)
			return in, err
		}),
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("its completed payload is reclaimed", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			for _, query := range []string{
				`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1`,
				`UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() - interval '2 days' WHERE id = $1`,
			} {
				if _, err := in.DB.Conn.Exec(query, in.Creation.Run.ID()); err != nil {
					return in, err
				}
			}
			destroyed, err := db.NewPipelineRunReclaimLifecycle(in.DB.Conn).DestroyReclaimableRun(in.Creation.Run.ID())
			if err != nil {
				return in, err
			}
			if !destroyed {
				return in, fmt.Errorf("the completed payload was not reclaimed")
			}
			return in, nil
		}),
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("the database encrypts its data and rotates the encryption key", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			first, err := definitionEncryptionKey("AES256Key-32Characters1234567890")
			if err != nil {
				return in, err
			}
			second, err := definitionEncryptionKey("AES256Key-32Characters9564567123")
			if err != nil {
				return in, err
			}
			sqlConn := in.DB.runner.OpenSingleton()
			sqlConn.SetMaxOpenConns(4)
			defer sqlConn.Close()
			migrator := migration.NewMigrator(sqlConn, in.DB.LockFactory)
			version, err := migrator.SupportedVersion()
			if err != nil {
				return in, err
			}
			if err := migrator.Migrate(first, nil, version); err != nil {
				return in, err
			}
			if err := migrator.Migrate(second, first, version); err != nil {
				return in, err
			}
			keyed, err := db.NewConn("brine-definition", in.DB.runner.OpenSingleton(), in.DB.runner.DataSourceName(), first, second)
			if err != nil {
				return in, err
			}
			defer keyed.Close()
			var nonce *string
			if err := keyed.QueryRow(`SELECT nonce FROM pipeline_run_definitions WHERE run_id = $1`, in.Creation.Run.ID()).Scan(&nonce); err != nil {
				return in, err
			}
			if nonce == nil {
				return in, fmt.Errorf("retained definition was left unencrypted")
			}
			definition, err := readDefinition(keyed, in)
			in.Read = &definition
			return in, err
		}),
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("the stored definition bytes are corrupted", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			_, err := in.DB.Conn.Exec(`UPDATE pipeline_run_definitions SET config = replace(config, 'hello', 'corrupted') WHERE run_id = $1`, in.Creation.Run.ID())
			return in, err
		}),
		brine.DefineMap[RetainedRunDefinition, RetainedRunDefinition]("deletion of the retained definition is attempted", func(in RetainedRunDefinition, _ brine.Params, _ *brine.Recorder) (RetainedRunDefinition, error) {
			_, in.MutationErr = in.DB.Conn.Exec(`DELETE FROM pipeline_run_definitions WHERE run_id = $1`, in.Creation.Run.ID())
			return in, nil
		}),
		CheckThat[RetainedRunDefinition]("the Run still returns its original template and materialized definition", checkOriginalDefinition),
		CheckThat[RetainedRunDefinition]("deletion is refused and the original definition remains", func(in RetainedRunDefinition) error {
			if in.MutationErr == nil || !strings.Contains(in.MutationErr.Error(), "retained") {
				return fmt.Errorf("expected retained definition deletion refusal, got %v", in.MutationErr)
			}
			return checkOriginalDefinition(in)
		}),
		CheckThat[RetainedRunDefinition]("the definition reader refuses the corrupted revision", func(in RetainedRunDefinition) error {
			_, err := readDefinition(in.DB.Conn, in)
			if err == nil || !strings.Contains(err.Error(), "attestation") {
				return fmt.Errorf("expected attestation refusal, got %v", err)
			}
			return nil
		}),
		CheckThat[RetainedRunDefinition]("neither the Run nor its retained definition exists", func(in RetainedRunDefinition) error {
			if !in.RolledBack {
				return fmt.Errorf("admission was not rolled back")
			}
			var runs, definitions int
			if err := in.DB.Conn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_runs), (SELECT count(*) FROM pipeline_run_definitions)`).Scan(&runs, &definitions); err != nil {
				return err
			}
			if runs != 0 || definitions != 0 {
				return fmt.Errorf("rejected admission left %d Runs and %d definitions", runs, definitions)
			}
			return nil
		}),
	}
}

func readDefinition(conn db.DbConn, in RetainedRunDefinition) (atc.RunDefinition, error) {
	reader := db.NewPipelineRunFactory(conn, in.DB.LockFactory)
	definition, found, err := reader.Definition(in.Creation.Run.ID())
	if err != nil {
		return atc.RunDefinition{}, err
	}
	if !found {
		return atc.RunDefinition{}, fmt.Errorf("Run lost its admitted definition")
	}
	return definition, nil
}

func checkOriginalDefinition(in RetainedRunDefinition) error {
	definition := in.Read
	if definition == nil {
		read, err := readDefinition(in.DB.Conn, in)
		if err != nil {
			return err
		}
		definition = &read
	}
	actual, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	want, err := json.Marshal(in.Original)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, want) {
		return fmt.Errorf("admitted definition changed after template or payload mutation")
	}
	return nil
}

func retainedDefinitionFixture(res brine.Resources, rollback bool) (RetainedRunDefinition, error) {
	jdb, err := jetbridgeDBFrom(res)
	if err != nil {
		return RetainedRunDefinition{}, err
	}
	team, err := jdb.TeamFactory.CreateTeam(atc.Team{Name: "definition-history"})
	if err != nil {
		return RetainedRunDefinition{}, err
	}
	ttl := 1
	config := atc.Config{
		Template:     true,
		Params:       []atc.ParamSchema{{Name: "message", Type: atc.ParamTypeString, Default: "hello"}},
		RunRetention: &atc.RunRetentionConfig{TTLDays: &ttl},
		Jobs: atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{
			{Config: &atc.TaskStep{Name: "task-((run))", Config: &atc.TaskConfig{
				Platform: "linux", Run: atc.TaskRunConfig{Path: "echo", Args: []string{"((message))"}},
			}}},
		}}},
	}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "review"}, config, 0, false)
	if err != nil {
		return RetainedRunDefinition{}, err
	}
	factory := db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory)
	in := RetainedRunDefinition{DB: jdb, Team: team, Template: template}
	if rollback {
		tx, err := jdb.Conn.Begin()
		if err != nil {
			return in, err
		}
		defer db.Rollback(tx)
		refused := errors.New("owner rejected admission")
		_, err = factory.CreateRunInTx(context.Background(), tx, template, db.RunParams{}, "brine", db.RunCreationOpts{BeforeCommit: func(db.Tx, db.RunCreation) error { return refused }})
		if !errors.Is(err, refused) {
			return in, fmt.Errorf("expected owner refusal, got %v", err)
		}
		if err := tx.Rollback(); err != nil {
			return in, err
		}
		in.RolledBack = true
		return in, nil
	}
	creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "brine")
	if err != nil {
		return in, err
	}
	in.Creation = creation
	in.Original = atc.RunDefinition{Template: config, Materialized: creation.Config}
	return in, nil
}

func definitionEncryptionKey(value string) (*encryption.Key, error) {
	block, err := aes.NewCipher([]byte(value))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return encryption.NewKey(aead), nil
}
