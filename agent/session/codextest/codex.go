// Package codextest runs the pinned Codex CLI, unmodified, against a scripted
// model. The model is the one collaborator a test cannot run for real: a real
// model call needs the owner's paid subscription and is nondeterministic. So
// Model scripts it, and everything between it and a published result (Codex's
// tool dispatch and sandbox, its --json events, the worker's decoder and
// policy, credential staging and destruction) is the real thing.
//
// Binary provisions the release pinned by agent/session (codex-version,
// codex-checksums.txt) once per cache, verified against the pinned digest.
// There is no fallback: a test that cannot get the pinned Codex fails and
// says why, because a skipped test here would report containment nobody
// checked.
package codextest

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
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
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
)

// CacheVariable names a directory holding (or to hold) the pinned release.
// It defaults to a jetbridge directory in the user cache. Placing the
// release's tarball there beforehand provisions Codex without network access.
const CacheVariable = "JB_CODEX_CACHE"

// executableVariable names an installed pinned Codex to run instead of
// provisioning one: the worker image's own /usr/local/bin/codex, when these
// tests run inside that image. It must report the pinned version.
const executableVariable = "JB_CODEX_EXECUTABLE"

// ReleaseURL is where the worker image and this harness fetch the pinned
// release (deploy/Dockerfile.review-worker).
const ReleaseURL = "https://github.com/openai/codex/releases/download/rust-v"

var provisioned struct {
	once sync.Once
	path string
	err  error
}

// Binary returns the absolute path of the pinned Codex executable for this
// platform. The release tarball is downloaded at most once per cache and its
// SHA-256 checked against the pin every time a test process first asks.
func Binary(t testing.TB) string {
	t.Helper()
	provisioned.once.Do(func() { provisioned.path, provisioned.err = provision() })
	if provisioned.err != nil {
		t.Fatalf("cannot provision the pinned Codex %s: %v\n"+
			"These tests run the real provider. Allow access to github.com, or put the release tarball in $%s.",
			session.CodexVersion(), provisioned.err, CacheVariable)
	}
	return provisioned.path
}

// Asset is the pinned release's tarball for this platform.
func Asset() (string, error) {
	triple := map[string]string{
		"darwin/arm64": "aarch64-apple-darwin", "darwin/amd64": "x86_64-apple-darwin",
		"linux/arm64": "aarch64-unknown-linux-musl", "linux/amd64": "x86_64-unknown-linux-musl",
	}[runtime.GOOS+"/"+runtime.GOARCH]
	if triple == "" {
		return "", fmt.Errorf("the pinned Codex has no release for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return "codex-" + triple + ".tar.gz", nil
}

func provision() (string, error) {
	if installed := os.Getenv(executableVariable); installed != "" {
		if !filepath.IsAbs(installed) {
			return "", fmt.Errorf("$%s must be an absolute path", executableVariable)
		}
		out, err := exec.Command(installed, "--version").Output()
		if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+session.CodexVersion() {
			return "", fmt.Errorf("$%s is not codex-cli %s: %q %v", executableVariable, session.CodexVersion(), out, err)
		}
		return installed, nil
	}
	asset, err := Asset()
	if err != nil {
		return "", err
	}
	want, ok := session.CodexChecksum(asset)
	if !ok {
		return "", fmt.Errorf("agent/session/codex-checksums.txt pins no digest for %s", asset)
	}
	dir := os.Getenv(CacheVariable)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		dir = filepath.Join(base, "jetbridge", "codex", session.CodexVersion())
	}
	// The launcher runs Codex from the session's private runtime, so the
	// executable's path must not depend on this process's directory.
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Parallel test processes provision one cache: one downloads, the rest wait.
	unlock, err := lock(filepath.Join(dir, ".lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	tarball := filepath.Join(dir, asset)
	if sum, err := digest(tarball); err != nil || sum != want {
		if err := download(ReleaseURL+session.CodexVersion()+"/"+asset, tarball); err != nil {
			return "", err
		}
	}
	if sum, err := digest(tarball); err != nil || sum != want {
		os.Remove(tarball)
		return "", fmt.Errorf("%s does not match its pinned digest %s (got %s, %v)", asset, want, sum, err)
	}
	// The executable is extracted again, by every test process, from the
	// tarball that just matched the pin: an executable left in the cache is
	// never trusted on its own.
	binary := filepath.Join(dir, want[:16], "codex")
	return binary, extract(tarball, strings.TrimSuffix(asset, ".tar.gz"), binary)
}

func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(url, path string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("GET %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func extract(tarball, member, path string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	archive := tar.NewReader(gz)
	for {
		h, err := archive.Next()
		if err != nil {
			return fmt.Errorf("%s has no %s: %v", filepath.Base(tarball), member, err)
		}
		if h.Typeflag != tar.TypeReg || h.Name != member {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		out, err := os.CreateTemp(filepath.Dir(path), ".extract-")
		if err != nil {
			return err
		}
		defer os.Remove(out.Name())
		if _, err := io.Copy(out, archive); err != nil {
			out.Close()
			return err
		}
		if err := out.Chmod(0o755); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(out.Name(), path)
	}
}

// Launcher returns an executable that runs the pinned Codex with the exact
// arguments it is given, after three configuration overrides that point
// every backend Codex calls at model: the Responses API (openai_base_url),
// the ChatGPT backend for its model catalog, settings and analytics
// (chatgpt_base_url), and its metrics exporter. None of the overrides grants
// or removes a capability; they only say where the backend is.
//
// It decorates the executable rather than the Provider because the workers
// construct session.Codex themselves: through Launcher a test runs a worker's
// own code path and its production argv, and so do the Brine steps, which
// can only name an executable. Without the overrides Codex would present the
// staged test credential to chatgpt.com, and on rejection try to refresh it
// at auth.openai.com.
func Launcher(t testing.TB, model *Model) string {
	t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	args := []string{
		`openai_base_url="` + model.URL + `/backend-api/codex"`,
		`chatgpt_base_url="` + model.URL + `/backend-api/"`,
		`otel.metrics_exporter={otlp-http={endpoint="` + model.URL + `/otlp/v1/metrics",protocol="json"}}`,
	}
	// The overrides must follow the exec subcommand: Codex does not carry
	// root-level -c into exec, and would silently reach the real backend.
	// Anything but the three invocations a session makes is refused.
	codex := quote(Binary(t))
	overrides := ""
	for _, a := range args {
		overrides += " -c " + quote(a)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"--version) exec " + codex + " --version ;;\n" +
		"exec) shift; exec " + codex + " exec" + overrides + " \"$@\" ;;\n" +
		"debug) [ \"$*\" = \"debug models --bundled\" ] && exec " + codex + " debug models --bundled ;;\n" +
		"*) echo \"codextest: unexpected Codex invocation: $1\" >&2; exit 64 ;;\nesac\n"
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Secret marks every token in Auth, so a test can prove the credential never
// reached an output, a log or a published result.
const Secret = "synthetic-"

// Auth is a ChatGPT subscription auth.json that the pinned Codex loads as
// its own: unsigned but well-formed tokens that expire in a day, so Codex
// never tries to refresh them, and a last_refresh, without which Codex
// withholds the token from every request. The model server checks that Codex
// presents AccessToken.
func Auth() []byte { return append([]byte(nil), auth...) }

var auth, _ = json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "last_refresh": time.Now().UTC().Format(time.RFC3339), "tokens": map[string]string{
	"id_token": token("id", map[string]any{"email": "owner@example.test", "https://api.openai.com/auth": map[string]string{
		"chatgpt_plan_type": "pro", "chatgpt_account_id": "synthetic-account", "chatgpt_user_id": "synthetic-user"}}),
	"access_token":  AccessToken,
	"refresh_token": Secret + "refresh",
	"account_id":    "synthetic-account",
}})

// AccessToken is the bearer token Codex presents from Auth.
var AccessToken = token("access", map[string]any{"sub": "synthetic-user"})

// token is an unsigned JWT. Codex decodes only its claims; the signature
// segment is the literal Secret, so the whole token is recognisable in any
// output it leaks into.
func token(name string, claims map[string]any) string {
	claims["exp"] = time.Now().Add(24 * time.Hour).Unix()
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(b) + "." + Secret + name
}
