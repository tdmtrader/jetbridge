package bootstrap

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/pbkdf2"
)

// DatabaseRole is the bootstrap's database step: it gives the activation
// database role a login and its grants, and writes the Secret holding its
// connection string.
//
// deferred: run by `concourse hangar-bootstrap --database`
// (hangar_activation_db_role T9).
type DatabaseRole struct {
	// Admin is a connection whose role may create roles: web's own.
	Admin *sql.DB
	// Store is where the role's connection-string Secret goes.
	Store SecretStore
	// Secret names that Secret; its data key is `dsn`.
	Secret string
	Labels map[string]string
	// DSN builds the keyword-form connection string the role logs in with.
	DSN func(role, password string) string
	// Login opens a connection with a DSN and reports whether it
	// authenticated. Nil uses the pgx driver.
	Login func(ctx context.Context, dsn string) error
	Log   Logger
}

// scramIterations is PostgreSQL's default SCRAM-SHA-256 iteration count.
const scramIterations = 4096

// EnsureActivationRole makes the activation database role able to log in
// with the credential its Secret holds, with exactly its grants.
//
// It refuses, creating nothing, if the admin credential cannot create roles.
// Otherwise it writes the role before the Secret, so an interrupted run leaves
// a role and no Secret, and the next run gives the role a new password and
// writes the Secret. An existing Secret is the authority: a role that does not
// accept its credential is re-aligned to it. The password reaches the server
// only as a SCRAM verifier computed here, never in the clear.
func EnsureActivationRole(ctx context.Context, step DatabaseRole) error {
	log := step.Log
	if log == nil {
		log = func(string, map[string]string) {}
	}
	login := step.Login
	if login == nil {
		login = pgxLogin
	}

	var role string
	if err := step.Admin.QueryRowContext(ctx, "SELECT hangar_activation_role_name()").Scan(&role); err != nil {
		return fmt.Errorf("%w: the database has not run the migration that defines the activation database role: %v", ErrRefused, err)
	}
	var mayCreate bool
	if err := step.Admin.QueryRowContext(ctx,
		"SELECT rolcreaterole OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&mayCreate); err != nil {
		return fmt.Errorf("check the database credential: %w", err)
	}
	if !mayCreate {
		return fmt.Errorf("%w: the database credential cannot create roles (it has neither CREATEROLE nor SUPERUSER), so it cannot create the activation database role %s", ErrRefused, role)
	}

	secret, found, err := step.Store.Get(ctx, step.Secret)
	if err != nil {
		return fmt.Errorf("get Secret %q: %w", step.Secret, err)
	}
	if found {
		dsn := string(secret.Data["dsn"])
		password, ok := dsnPassword(dsn)
		if !ok {
			return fmt.Errorf("%w: Secret %q holds no keyword-form connection string with a password", ErrRefused, step.Secret)
		}
		event := "kept"
		if login(ctx, dsn) != nil {
			// The Secret is the authority; the role is brought back to it.
			if err := setRoleLogin(ctx, step.Admin, role, password); err != nil {
				return err
			}
			if err := login(ctx, dsn); err != nil {
				return fmt.Errorf("the activation database role %s does not log in with its Secret's credential after re-aligning: %w", role, err)
			}
			event = "realigned"
		}
		if err := grant(ctx, step.Admin); err != nil {
			return err
		}
		log(event, map[string]string{"name": step.Secret, "kind": string(KindDatabaseCredential), "role": role})
		return nil
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	password := hex.EncodeToString(raw)
	if err := setRoleLogin(ctx, step.Admin, role, password); err != nil {
		return err
	}
	if err := grant(ctx, step.Admin); err != nil {
		return err
	}
	dsn := step.DSN(role, password)
	if err := login(ctx, dsn); err != nil {
		return fmt.Errorf("the activation database role %s does not log in with its new credential: %w", role, err)
	}
	if err := step.Store.Create(ctx, Secret{
		Name: step.Secret, Type: secretTypeOpaque, Labels: step.Labels,
		Data: map[string][]byte{"dsn": []byte(dsn)},
	}); err != nil {
		return fmt.Errorf("create Secret %q: %w", step.Secret, err)
	}
	log("created", map[string]string{"name": step.Secret, "kind": string(KindDatabaseCredential), "role": role})
	return nil
}

// setRoleLogin creates the role, or alters it, to log in with password. It
// sends a SCRAM verifier, so the clear password is in no statement.
func setRoleLogin(ctx context.Context, admin *sql.DB, role, password string) error {
	verifier, err := scramVerifier(password)
	if err != nil {
		return err
	}
	var exists bool
	if err := admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	statement := fmt.Sprintf("%s ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD %s",
		verb, quoteIdentifier(role), quoteLiteral(verifier))
	if _, err := admin.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("%s the activation database role %s: %w", strings.ToLower(verb), role, err)
	}
	return nil
}

func grant(ctx context.Context, admin *sql.DB) error {
	if _, err := admin.ExecContext(ctx, "SELECT hangar_activation_grants()"); err != nil {
		return fmt.Errorf("grant the activation database role its privileges: %w", err)
	}
	return nil
}

// scramVerifier computes PostgreSQL's SCRAM-SHA-256 verifier for password
// (RFC 5802, RFC 7677): SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func scramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	salted := pbkdf2.Key([]byte(password), salt, scramIterations, sha256.Size, sha256.New)
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// dsnPassword reads the password field of a keyword-form connection string.
func dsnPassword(dsn string) (string, bool) {
	for _, field := range strings.Fields(dsn) {
		if value, ok := strings.CutPrefix(field, "password="); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

func pgxLogin(ctx context.Context, dsn string) error {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.PingContext(ctx)
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
