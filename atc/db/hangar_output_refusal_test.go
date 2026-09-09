package db

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Every refusal the output plane's schema raises, mapped to the one sentinel a
// caller will see for it.
//
// This is an internal test on purpose: the mapping under test is
// hangarConflict, and asserting it through an exported path would be asserting
// whichever caller happened to be reachable rather than the map itself.
//
// The refusals are read out of the migration rather than listed here. A list
// would be a second copy of the schema, and the failure mode this guards
// against -- a RAISE added with no class, matched by whatever substring
// happened to be nearby -- is exactly the one a hand-maintained list misses.
const hangarOutputMigration = "atc/db/migration/migrations/1788936403_add_hangar_output_tables.up.sql"

// hangarRaise is one RAISE EXCEPTION statement, with the class its own USING
// clause gives it.
type hangarRaise struct {
	Line    int
	Message string
	Code    string
}

// scanHangarRaises reads the migration and returns every RAISE EXCEPTION in it.
//
// The scan is quote-aware because the messages themselves contain semicolons,
// which is precisely why the statement terminator cannot be found by looking
// for the next `;`.
func scanHangarRaises(t *testing.T) []hangarRaise {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", hangarOutputMigration)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", hangarOutputMigration, err)
	}
	text := string(source)

	var raises []hangarRaise
	for offset := 0; ; {
		index := strings.Index(text[offset:], "RAISE EXCEPTION")
		if index < 0 {
			break
		}
		start := offset + index
		end := start
		quoted := false
		for end < len(text) {
			switch {
			case text[end] == '\'' && quoted && end+1 < len(text) && text[end+1] == '\'':
				end++
			case text[end] == '\'':
				quoted = !quoted
			case text[end] == ';' && !quoted:
				goto found
			}
			end++
		}
		t.Fatalf("unterminated RAISE at offset %d", start)
	found:
		statement := text[start:end]
		raises = append(raises, hangarRaise{
			Line:    strings.Count(text[:start], "\n") + 1,
			Message: firstQuoted(statement),
			Code:    usingErrcode(statement),
		})
		offset = end
	}

	return raises
}

func firstQuoted(statement string) string {
	open := strings.Index(statement, "'")
	if open < 0 {
		return ""
	}
	closed := strings.Index(statement[open+1:], "'")
	if closed < 0 {
		return ""
	}

	return statement[open+1 : open+1+closed]
}

func usingErrcode(statement string) string {
	marker := strings.Index(statement, "USING ERRCODE = '")
	if marker < 0 {
		return ""
	}
	rest := statement[marker+len("USING ERRCODE = '"):]
	closed := strings.Index(rest, "'")
	if closed < 0 {
		return ""
	}

	return rest[:closed]
}

// hangarSentinelsInPlay is every typed outcome a Hangar caller distinguishes.
//
// "Exactly one" is checked against this whole set rather than against the four
// the map produces, because the defect this test exists for was a refusal that
// satisfied errors.Is for a sentinel nobody meant it to -- a stale owner told
// to retry.
var hangarSentinelsInPlay = []error{
	output.ErrConflict,
	output.ErrAtRisk,
	executioncontrol.ErrStaleFence,
	output.ErrIncomplete,
	output.ErrNotFound,
	output.ErrCorrupt,
	output.ErrInfrastructure,
	ErrHangarLockRetry,
}

func TestEveryHangarSchemaRefusalMapsToExactlyOneSentinel(t *testing.T) {
	raises := scanHangarRaises(t)

	// A scan that found nothing would pass forever.
	if len(raises) < 40 {
		t.Fatalf("the scan found %d RAISE statements in %s, which is too few to be the whole "+
			"schema; either the file moved or the scan stopped recognising them, and in both "+
			"cases this test is guarding nothing", len(raises), hangarOutputMigration)
	}

	expected := map[string]error{
		hangarRefusalConflict:   output.ErrConflict,
		hangarRefusalAtRisk:     output.ErrAtRisk,
		hangarRefusalStaleFence: executioncontrol.ErrStaleFence,
		hangarRefusalIncomplete: output.ErrIncomplete,
	}
	used := map[string]int{}

	for _, raise := range raises {
		if raise.Code == "" {
			t.Errorf("%s:%d raises %q with no class ERRCODE.\n\nEvery refusal this schema makes "+
				"names its class on the RAISE itself, because the messages are shared: "+
				"\"backwards\" appears in six trigger functions and \"exclude one another "+
				"permanently\" in two, so a substring never named one refusal. Add USING "+
				"ERRCODE = '%s' (conflict), '%s' (at risk), '%s' (stale fence or epoch) or "+
				"'%s' (incomplete or immutable).",
				hangarOutputMigration, raise.Line, raise.Message,
				hangarRefusalConflict, hangarRefusalAtRisk, hangarRefusalStaleFence,
				hangarRefusalIncomplete)

			continue
		}

		want, known := expected[raise.Code]
		if !known {
			t.Errorf("%s:%d raises class %q, which hangarConflict does not map: %q",
				hangarOutputMigration, raise.Line, raise.Code, raise.Message)

			continue
		}
		used[raise.Code]++

		mapped := hangarConflict(&pgconn.PgError{Code: raise.Code, Message: raise.Message})

		var matched []error
		for _, sentinel := range hangarSentinelsInPlay {
			if errors.Is(mapped, sentinel) {
				matched = append(matched, sentinel)
			}
		}
		if len(matched) != 1 {
			t.Errorf("%s:%d (%s) maps to %d sentinels, not one: %v\n\nmessage: %q",
				hangarOutputMigration, raise.Line, raise.Code, len(matched), matched, raise.Message)

			continue
		}
		if matched[0] != want {
			t.Errorf("%s:%d (%s) maps to %v, not %v\n\nmessage: %q",
				hangarOutputMigration, raise.Line, raise.Code, matched[0], want, raise.Message)
		}
	}

	// Every class is carried by at least one refusal. A class nothing raises is
	// a branch in the map that no schema statement can reach.
	for code := range expected {
		if used[code] == 0 {
			t.Errorf("no RAISE in %s carries class %s, so that branch of the map is unreachable",
				hangarOutputMigration, code)
		}
	}
}

// TestHangarRetryIsReservedForTheDatabasesOwnRetries is the other half of
// finding 3: the retry sentinel means "PostgreSQL asked you to run this
// again", and a schema refusal never does.
func TestHangarRetryIsReservedForTheDatabasesOwnRetries(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		mapped := hangarConflict(&pgconn.PgError{Code: code, Message: "could not serialize access"})
		if !errors.Is(mapped, ErrHangarLockRetry) {
			t.Errorf("SQLSTATE %s did not map to ErrHangarLockRetry; it is a serialization "+
				"failure or a deadlock, which is the one thing a caller should re-run", code)
		}
	}

	for _, raise := range scanHangarRaises(t) {
		if raise.Code == "" {
			continue
		}
		mapped := hangarConflict(&pgconn.PgError{Code: raise.Code, Message: raise.Message})
		if errors.Is(mapped, ErrHangarLockRetry) {
			t.Errorf("%s:%d is told to retry: %q.\n\nA stale owner, a superseded fence and a "+
				"re-enabled disabled epoch are all refusals; re-running the same transaction "+
				"produces the same refusal, and a retry loop around one is a spin.",
				hangarOutputMigration, raise.Line, raise.Message)
		}
	}
}

// TestAnUnclassifiedSchemaRefusalIsNotARetry covers the shape a new RAISE has
// for the moment before someone gives it a class: it must be a refusal, and it
// must not be a retry or a conflict it never claimed to be.
func TestAnUnclassifiedSchemaRefusalIsNotARetry(t *testing.T) {
	mapped := hangarConflict(&pgconn.PgError{
		Code:    "P0001",
		Message: "hangar: something new refused, and nobody classified it",
	})
	if !errors.Is(mapped, output.ErrIncomplete) {
		t.Errorf("an unclassified schema refusal mapped to %v, not ErrIncomplete", mapped)
	}
	if errors.Is(mapped, ErrHangarLockRetry) || errors.Is(mapped, output.ErrConflict) {
		t.Errorf("an unclassified schema refusal mapped to %v", mapped)
	}
}
