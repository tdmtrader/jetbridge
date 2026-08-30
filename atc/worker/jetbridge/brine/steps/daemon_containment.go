package steps

// Containment steps: the executable half of ../features/daemon-containment.feature.
//
// What the artifact daemon REFUSES — traversing keys, structural names, paths
// outside its storage root, and archives that reach out of the artifact they
// are being extracted into.
//
// EVERY SCENARIO DRIVES THE REAL BINARY. There is no double here and there
// could not honestly be one: a double of a guard is a guard you wrote, and
// asserting that it refuses proves only that you remembered to make it
// refuse. ../steps/realdaemon.go builds cmd/artifact-daemon once per process
// and startRealDaemon runs one with its own storage root on a free port.
//
// The daemon is started IN THE GIVEN, never as a brine resource. brine
// acquires every ScopeScenario resource before EVERY scenario in the suite —
// RequireAllForScope iterates the definitions at that scope, not the ones a
// scenario's steps declare — so a daemon registered that way is built,
// started and killed once per scenario in the whole corpus to be used by the
// seventeen here. That was measured at +70 seconds when the first real-daemon
// scenarios were wired up. The kill goes on the Recorder, which drains LIFO
// at scenario end on pass, on failure, and on SIGTERM.
//
// --node-name is never passed. It makes the daemon build a Kubernetes client
// and os.Exit(1) outside a cluster.
//
// TWO THINGS EVERY ASSERTION HERE DOES, both because their absence is how a
// containment test lies:
//
//   - it reads the state OUTSIDE the boundary, not just the status. A refusal
//     that arrives after the RemoveAll is not a refusal, and only the victim
//     file can tell you which one happened.
//   - it reads WHY the refusal came. A check that accepts any non-2xx passes
//     when the daemon 500s for an unrelated reason, and "refused for the
//     wrong reason" is exactly the failure mode this family produces.
//
// The refusal texts asserted in the feature file are SUBSTRINGS of what the
// daemon actually says, chosen to name the rule that fired rather than the
// whole sentence — "has an empty or relative segment", "targets an absolute
// path", "names a structural path". They were read off a running daemon, not
// guessed from the source: several of them are not the message the code
// reads as if it would produce. A traversing key never reaches
// validateRequestKey's "escapes the storage root" branch, for instance,
// because the segment loop above it rejects the ".." component first.

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// containmentProbe is one request in a sweep across verbs or keys.
type containmentProbe struct {
	Label  string
	Status int
	Body   string
}

// ContainedDaemon is a running daemon, a directory outside its storage root,
// and the last answer it gave.
type ContainedDaemon struct {
	Daemon *realDaemon

	// Outside is a directory the daemon's storage root does not contain, and
	// OutsideFile a file inside it. Both are the "victim" every escape
	// scenario checks after the refusal.
	Outside     string
	OutsideFile string

	// OutsideDest is the last destination outside the root a step asked the
	// daemon to write to. Kept so a check can assert nothing was created
	// there — the status alone cannot say that.
	OutsideDest string

	// Registered maps a registered key to the absolute path it was registered
	// at, so a later step can swap that path for a link out of the root. A
	// map rather than a field because brine passes the state by value between
	// steps and the swap has to be visible to the check that follows.
	Registered map[string]string

	Status int
	Body   string
	Err    error

	// Probes holds the answers to a sweep. Reset by every sweeping step, so
	// the checks that read it must follow their When immediately.
	Probes []containmentProbe
}

func (s ContainedDaemon) root() string { return s.Daemon.Root }

// storePath resolves a path named relative to the storage root.
func (s ContainedDaemon) storePath(rel string) string {
	return filepath.Join(s.root(), filepath.FromSlash(rel))
}

// do performs one request and returns the status and body. HEAD answers carry
// no body by definition, which is why the sweeping checks over HEAD assert
// only the status.
func (s ContainedDaemon) do(method, path string, body []byte, contentType string) (int, string, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, s.Daemon.URL+path, r)
	if err != nil {
		return 0, "", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), err
}

// answered records a single request as the state's current answer.
func (s ContainedDaemon) answered(method, path string, body []byte, contentType string) ContainedDaemon {
	status, text, err := s.do(method, path, body, contentType)
	s.Status, s.Body, s.Err = status, text, err
	s.Probes = nil
	return s
}

// encodeTraversal percent-encodes "." and "/" so path cleaning at the mux
// cannot collapse a traversal before the handler sees it.
//
// This is the whole vector. Go's ServeMux cleans the UNESCAPED path, so
// "%2e%2e%2f" survives routing and arrives in r.URL.Path already decoded to
// "../"; a literal "../" never reaches a handler, because the mux 301s it. A
// scenario that sent the literal form would be testing the mux.
func encodeTraversal(rel string) string {
	var b strings.Builder
	for _, c := range rel {
		switch c {
		case '.':
			b.WriteString("%2e")
		case '/':
			b.WriteString("%2f")
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// The sweeping steps read their comma-separated key lists with splitList,
// which container_pod.go already defines for the same purpose.

// containmentArchive builds the archive a scenario names.
//
// The shapes are named for what they DO rather than for the tar type they
// use, because that is what the feature file has to read as. Two of them need
// a path outside the storage root, and say so rather than silently producing
// a harmless archive if the scenario forgot the Given.
func containmentArchive(kind string, in ContainedDaemon) ([]byte, error) {
	needOutside := func() (string, error) {
		if in.OutsideFile == "" {
			return "", fmt.Errorf("the %q archive needs a file outside the storage root; "+
				"the scenario is missing its \"a directory outside that root\" Given", kind)
		}
		return in.OutsideFile, nil
	}

	switch kind {
	case "ordinary":
		return tarBytes(tarItem{Name: "new.txt", Type: tar.TypeReg, Body: "NEW", Mode: 0o644})

	case "internal links":
		// The case the containment rule exists to PERMIT: two trees in one
		// artifact sharing a dependency directory by relative link, plus a
		// hard link, which an extractor that switched on Dir/Reg/Symlink only
		// used to drop on the floor while reporting success.
		return tarBytes(
			tarItem{Name: "shared/pkg.txt", Type: tar.TypeReg, Body: "deps", Mode: 0o644},
			tarItem{Name: "app/node_modules", Type: tar.TypeSymlink, Link: "../shared", Mode: 0o777},
			tarItem{Name: "target.txt", Type: tar.TypeReg, Body: "payload", Mode: 0o644},
			tarItem{Name: "a/b/hard", Type: tar.TypeLink, Link: "target.txt", Mode: 0o644},
		)

	case "traversing entry name":
		return tarBytes(tarItem{Name: "../escape.txt", Type: tar.TypeReg, Body: "PWNED", Mode: 0o644})

	case "absolute symlink":
		return tarBytes(tarItem{Name: "hatch", Type: tar.TypeSymlink, Link: "/etc", Mode: 0o777})

	case "absolute symlink to the file outside":
		target, err := needOutside()
		if err != nil {
			return nil, err
		}
		return tarBytes(tarItem{Name: "pwn", Type: tar.TypeSymlink, Link: target, Mode: 0o777})

	case "symlink out of the artifact":
		// Relative, and each hop looks local. Only the composition escapes,
		// which is the case a HasPrefix check on the linkname misses.
		//
		// The SECOND entry is what makes "the file outside is unchanged"
		// load-bearing, and it was missing at first. An escaping link on its
		// own cannot alter anything: it is a link. The source case pairs it
		// with a write THROUGH the link, and only then does a daemon that
		// creates the link overwrite the victim. Without this entry an audit
		// measured the outside file reading "original" even with the guard
		// removed — the assertion was inert padding on every row.
		return tarBytes(
			tarItem{Name: "hatch", Type: tar.TypeSymlink, Link: "../../../outside", Mode: 0o777},
			tarItem{Name: "hatch/victim.txt", Type: tar.TypeReg, Body: "overwritten", Mode: 0o644},
		)

	case "hard link out of the artifact":
		target, err := needOutside()
		if err != nil {
			return nil, err
		}
		return tarBytes(tarItem{Name: "hard.txt", Type: tar.TypeLink, Link: target, Mode: 0o644})

	case "unsupported entry type":
		return tarBytes(tarItem{Name: "dev", Type: tar.TypeChar, Mode: 0o666})

	case "good entries then a bad one":
		// Entries land before the refusal, so the destination and the
		// extraction temp directory both have content to leave behind.
		return tarBytes(
			tarItem{Name: "good.txt", Type: tar.TypeReg, Body: "legit", Mode: 0o644},
			tarItem{Name: "sub/also-good.txt", Type: tar.TypeReg, Body: "legit", Mode: 0o644},
			tarItem{Name: "bad", Type: tar.TypeSymlink, Link: "/etc", Mode: 0o777},
		)

	case "malformed":
		return []byte("not a tar at all"), nil

	case "truncated":
		// The likeliest corrupt input in production, and it surfaces from the
		// body copy rather than from a header read — a different path out of
		// the extractor than "malformed" takes.
		full, err := tarBytes(tarItem{
			Name: "f", Type: tar.TypeReg, Body: strings.Repeat("x", 4096), Mode: 0o644,
		})
		if err != nil {
			return nil, err
		}
		return full[:1500], nil
	}

	return nil, fmt.Errorf("no archive shape named %q; add it to containmentArchive", kind)
}

// tarItem is one header plus its body.
type tarItem struct {
	Name string
	Type byte
	Link string
	Body string
	Mode int64
}

func tarBytes(items ...tarItem) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, it := range items {
		hdr := &tar.Header{Name: it.Name, Typeflag: it.Type, Linkname: it.Link, Mode: it.Mode}
		if it.Type == tar.TypeReg {
			hdr.Size = int64(len(it.Body))
		}
		if it.Type == tar.TypeChar {
			hdr.Devmajor, hdr.Devminor = 1, 3
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write header %q: %w", it.Name, err)
		}
		if it.Body != "" {
			if _, err := io.WriteString(tw, it.Body); err != nil {
				return nil, fmt.Errorf("write body %q: %w", it.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return buf.Bytes(), nil
}

// aliasStoreContents reads aliases.json off disk.
//
// Off DISK, not out of the in-memory registry, because the defect this
// guards is that a refused registration SURVIVES A RESTART. A missing file is
// reported as such rather than as an empty string, so "the alias store names
// X" cannot pass against a daemon that stopped persisting altogether.
func (s ContainedDaemon) aliasStoreContents() (string, error) {
	data, err := os.ReadFile(s.storePath("aliases.json"))
	if err != nil {
		return "", fmt.Errorf("read the alias store: %w", err)
	}
	return string(data), nil
}

// DaemonContainmentDefinitions drives an actual artifact-daemon process
// against the inputs it must refuse.
func DaemonContainmentDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// -------------------------------------------------------------------
		// Givens
		// -------------------------------------------------------------------

		brine.DefineMap[brine.Empty, ContainedDaemon](
			"a real artifact daemon guarding its storage root",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (ContainedDaemon, error) {
				d, err := startRealDaemon()
				if err != nil {
					return ContainedDaemon{}, err
				}
				rec.RegisterDisposer(func() { _ = d.stop() })
				return ContainedDaemon{Daemon: d, Registered: map[string]string{}}, nil
			},
		),

		// A sibling of the storage root, so filepath.Rel between them yields a
		// key beginning "..". On macOS both live under /var/folders, which is
		// itself a symlink to /private/var — the daemon's own validator
		// resolves both sides for exactly that reason, and this Given must
		// not resolve either, or the traversal it builds stops being one.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"a directory outside that root holding a file that reads {string}",
			func(in ContainedDaemon, p brine.Params, rec *brine.Recorder) (ContainedDaemon, error) {
				content, err := paramAt("a directory outside that root holding a file that reads {string}", p, 0)
				if err != nil {
					return in, err
				}
				dir, err := os.MkdirTemp("", "brine-outside-root-*")
				if err != nil {
					return in, fmt.Errorf("make a directory outside the storage root: %w", err)
				}
				rec.RegisterDisposer(func() { _ = os.RemoveAll(dir) })
				file := filepath.Join(dir, "victim.txt")
				if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
					return in, err
				}
				in.Outside, in.OutsideFile = dir, file
				return in, nil
			},
		),

		// A step's output is a DIRECTORY, so an artifact is made by naming a
		// file inside one. Nothing tells the daemon it is there — the
		// filesystem is the record.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the store holds a file {string} reading {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				pattern := "the store holds a file {string} reading {string}"
				rel, content, err := twoParams(pattern, p)
				if err != nil {
					return in, err
				}
				full := in.storePath(rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return in, err
				}
				return in, os.WriteFile(full, []byte(content), 0o644)
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the store holds an alias file naming {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				name, err := paramAt("the store holds an alias file naming {string}", p, 0)
				if err != nil {
					return in, err
				}
				body := fmt.Sprintf("{%q:%q}", name, "/somewhere/on/this/node")
				return in, os.WriteFile(in.storePath("aliases.json"), []byte(body), 0o644)
			},
		),

		// Planted on disk rather than through the API on purpose. An absolute
		// symlink is refused at ingress now, so this link can no longer be
		// created through stream-in — but one may predate the rule or arrive
		// by another path, and a boundary that only holds for links it made
		// itself is not a boundary.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"a symlink is planted on disk at {string} pointing at the storage root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				rel, err := paramAt("a symlink is planted on disk at {string} pointing at the storage root", p, 0)
				if err != nil {
					return in, err
				}
				full := in.storePath(rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return in, err
				}
				return in, os.Symlink(in.root(), full)
			},
		),

		// -------------------------------------------------------------------
		// Requests against /artifacts/
		// -------------------------------------------------------------------

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC sends {string} for the artifact key {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				method, key, err := twoParams("the ATC sends {string} for the artifact key {string}", p)
				if err != nil {
					return in, err
				}
				return in.answered(method, "/artifacts/"+key, []byte("x"), ""), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC sends {string} for an artifact key that climbs out to the directory outside the root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				method, err := paramAt(
					"the ATC sends {string} for an artifact key that climbs out to the directory outside the root", p, 0)
				if err != nil {
					return in, err
				}
				if in.Outside == "" {
					return in, fmt.Errorf("no directory outside the storage root was set up")
				}
				rel, err := filepath.Rel(in.root(), in.Outside)
				if err != nil {
					return in, fmt.Errorf("relate %q to the storage root: %w", in.Outside, err)
				}
				if !strings.HasPrefix(rel, "..") {
					return in, fmt.Errorf(
						"the directory %q is INSIDE the storage root (relative %q), so this step would not be a traversal at all",
						in.Outside, rel)
				}
				return in.answered(method, "/artifacts/"+encodeTraversal(rel), nil, ""), nil
			},
		),

		// Sweeps the four per-artifact verbs. os.Root refuses "." for Remove
		// and RemoveAll ONLY — Root.Stat(".") and Root.Open(".") succeed and
		// enumerate the whole store — so a check on DELETE alone would stay
		// green against a daemon that had lost the rule everywhere else.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC sends every per-artifact verb for the percent-encoded artifact key {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt(
					"the ATC sends every per-artifact verb for the percent-encoded artifact key {string}", p, 0)
				if err != nil {
					return in, err
				}
				in.Probes = nil
				for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
					status, body, err := in.do(method, "/artifacts/"+encodeTraversal(key), []byte("x"), "")
					if err != nil {
						return in, fmt.Errorf("%s %q: %w", method, key, err)
					}
					in.Probes = append(in.Probes,
						containmentProbe{Label: method + " /artifacts/" + key, Status: status, Body: body})
				}
				return in, nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC sends {string} for each of the artifact keys {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				method, list, err := twoParams("the ATC sends {string} for each of the artifact keys {string}", p)
				if err != nil {
					return in, err
				}
				in.Probes = nil
				for _, key := range splitList(list) {
					status, body, err := in.do(method, "/artifacts/"+key, nil, "")
					if err != nil {
						return in, fmt.Errorf("%s %q: %w", method, key, err)
					}
					in.Probes = append(in.Probes,
						containmentProbe{Label: method + " /artifacts/" + key, Status: status, Body: body})
				}
				return in, nil
			},
		),

		// -------------------------------------------------------------------
		// Requests against /stream-in/
		// -------------------------------------------------------------------

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC streams the {string} archive in under the key {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				kind, key, err := twoParams("the ATC streams the {string} archive in under the key {string}", p)
				if err != nil {
					return in, err
				}
				body, err := containmentArchive(kind, in)
				if err != nil {
					return in, err
				}
				return in.answered(http.MethodPut, "/stream-in/"+key, body, "application/x-tar"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC streams the {string} archive in under the percent-encoded key {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				kind, key, err := twoParams(
					"the ATC streams the {string} archive in under the percent-encoded key {string}", p)
				if err != nil {
					return in, err
				}
				body, err := containmentArchive(kind, in)
				if err != nil {
					return in, err
				}
				return in.answered(http.MethodPut, "/stream-in/"+encodeTraversal(key), body, "application/x-tar"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC streams the {string} archive in under each of the keys {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				kind, list, err := twoParams(
					"the ATC streams the {string} archive in under each of the keys {string}", p)
				if err != nil {
					return in, err
				}
				in.Probes = nil
				for _, key := range splitList(list) {
					body, err := containmentArchive(kind, in)
					if err != nil {
						return in, err
					}
					status, text, err := in.do(http.MethodPut, "/stream-in/"+key, body, "application/x-tar")
					if err != nil {
						return in, fmt.Errorf("PUT /stream-in/%s: %w", key, err)
					}
					in.Probes = append(in.Probes,
						containmentProbe{Label: "PUT /stream-in/" + key, Status: status, Body: text})
				}
				return in, nil
			},
		),

		// -------------------------------------------------------------------
		// Requests against /register, /mirror, /resolve, /resolve-batch
		// -------------------------------------------------------------------

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC registers {string} as living at the stored path {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, rel, err := twoParams("the ATC registers {string} as living at the stored path {string}", p)
				if err != nil {
					return in, err
				}
				full := in.storePath(rel)
				in.Registered[key] = full
				body := fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, full)
				return in.answered(http.MethodPost, "/register", []byte(body), "application/json"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC registers {string} as living at the file outside the storage root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt("the ATC registers {string} as living at the file outside the storage root", p, 0)
				if err != nil {
					return in, err
				}
				if in.OutsideFile == "" {
					return in, fmt.Errorf("no file outside the storage root was set up")
				}
				body := fmt.Sprintf(`{"key":%q,"local_path":%q}`, key, in.OutsideFile)
				return in.answered(http.MethodPost, "/register", []byte(body), "application/json"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks the daemon to mirror the key {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt("the ATC asks the daemon to mirror the key {string}", p, 0)
				if err != nil {
					return in, err
				}
				body := fmt.Sprintf(`{"key":%q}`, key)
				return in.answered(http.MethodPost, "/mirror", []byte(body), "application/json"), nil
			},
		),

		// copyArtifact makes its temp directory as a SIBLING of dest, so a
		// contained destination still needs its parent to exist. Production
		// gets that from the caller; a scenario naming a destination under the
		// storage root has to create it, and doing so here keeps the
		// arrangement out of the feature file where it would read as part of
		// the claim.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks it to resolve {string} into the stored destination {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, rel, err := twoParams(
					"the ATC asks it to resolve {string} into the stored destination {string}", p)
				if err != nil {
					return in, err
				}
				dest := in.storePath(rel)
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return in, err
				}
				body := fmt.Sprintf(`{"key":%q,"dest":%q}`, key, dest)
				return in.answered(http.MethodPost, "/resolve", []byte(body), "application/json"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks it to resolve {string} into a destination outside the storage root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt(
					"the ATC asks it to resolve {string} into a destination outside the storage root", p, 0)
				if err != nil {
					return in, err
				}
				if in.Outside == "" {
					return in, fmt.Errorf("no directory outside the storage root was set up")
				}
				in.OutsideDest = filepath.Join(in.Outside, "attacker-chosen")
				body := fmt.Sprintf(`{"key":%q,"dest":%q}`, key, in.OutsideDest)
				return in.answered(http.MethodPost, "/resolve", []byte(body), "application/json"), nil
			},
		),

		// dest == the root is its own case, and not a pedantic one:
		// copyArtifact does os.MkdirTemp(filepath.Dir(dest)) and
		// os.RemoveAll(dest), so accepting it writes into the root's parent —
		// a host directory in production — and then removes the whole store.
		// filepath.Rel reports "." for this, and the first cut of the
		// validator accepted it.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks it to resolve {string} into the storage root itself",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt("the ATC asks it to resolve {string} into the storage root itself", p, 0)
				if err != nil {
					return in, err
				}
				body := fmt.Sprintf(`{"key":%q,"dest":%q}`, key, in.root())
				return in.answered(http.MethodPost, "/resolve", []byte(body), "application/json"), nil
			},
		),

		// The refused item is deliberately SECOND. Validating each item as its
		// goroutine starts would refuse it correctly and still let item 0
		// copy, and only the order shows that up.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks it to resolve {string} into the stored destination {string} and into a destination outside the storage root",
			func(in ContainedDaemon, p brine.Params, rec *brine.Recorder) (ContainedDaemon, error) {
				key, rel, err := twoParams(
					"the ATC asks it to resolve {string} into the stored destination {string} and into a destination outside the storage root", p)
				if err != nil {
					return in, err
				}
				if in.Outside == "" {
					// This fallback fires whenever the scenario has no "a
					// directory outside that root" Given, and it used to leak:
					// the handler took no Recorder, so nothing removed the
					// directory. One empty brine-outside-root-* per run, for
					// the life of the temp dir.
					in.Outside, err = os.MkdirTemp("", "brine-outside-root-*")
					if err != nil {
						return in, err
					}
					outside := in.Outside
					rec.RegisterDisposer(func() { _ = os.RemoveAll(outside) })
				}
				first := in.storePath(rel)
				if err := os.MkdirAll(filepath.Dir(first), 0o755); err != nil {
					return in, err
				}
				in.OutsideDest = filepath.Join(in.Outside, "escaped")
				body := fmt.Sprintf(`{"items":[{"key":%q,"dest":%q},{"key":%q,"dest":%q}]}`,
					key, first, key, in.OutsideDest)
				return in.answered(http.MethodPost, "/resolve-batch", []byte(body), "application/json"), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks it to resolve {string} into the stored destinations {string} and {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				pattern := "the ATC asks it to resolve {string} into the stored destinations {string} and {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				firstRel, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				secondRel, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				first, second := in.storePath(firstRel), in.storePath(secondRel)
				for _, d := range []string{first, second} {
					if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
						return in, err
					}
				}
				body := fmt.Sprintf(`{"items":[{"key":%q,"dest":%q},{"key":%q,"dest":%q}]}`,
					key, first, key, second)
				return in.answered(http.MethodPost, "/resolve-batch", []byte(body), "application/json"), nil
			},
		),

		// -------------------------------------------------------------------
		// The resource-cache routes, which read the same registry
		// -------------------------------------------------------------------

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks for the resource cache {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt("the ATC asks for the resource cache {string}", p, 0)
				if err != nil {
					return in, err
				}
				return in.answered(http.MethodGet, "/resource-caches/"+key, nil, ""), nil
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the ATC asks whether the daemon holds the resource cache {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt("the ATC asks whether the daemon holds the resource cache {string}", p, 0)
				if err != nil {
					return in, err
				}
				return in.answered(http.MethodHead, "/resource-caches/"+key, nil, ""), nil
			},
		),

		// The swap is the whole point of validating at USE rather than at
		// registration. Two shapes because the two routes hold two shapes: an
		// alias to a directory that resolve copies, and an alias to a file the
		// resource-cache route streams.
		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the path behind {string} is swapped for a link to the directory outside the root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt(
					"the path behind {string} is swapped for a link to the directory outside the root", p, 0)
				if err != nil {
					return in, err
				}
				return in, swapForLink(in, key, in.Outside)
			},
		),

		brine.DefineMap[ContainedDaemon, ContainedDaemon](
			"the path behind {string} is swapped for a link to the file outside the root",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) (ContainedDaemon, error) {
				key, err := paramAt(
					"the path behind {string} is swapped for a link to the file outside the root", p, 0)
				if err != nil {
					return in, err
				}
				return in, swapForLink(in, key, in.OutsideFile)
			},
		),

		// -------------------------------------------------------------------
		// Checks
		// -------------------------------------------------------------------

		CheckInt[ContainedDaemon]("the daemon replies with {int}",
			"the daemon's status",
			func(in ContainedDaemon) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Status, nil
			},
			func(in ContainedDaemon) string { return "body: " + abbrev(in.Body) }),

		// Reports the 2xx case rather than comparing against it, so a
		// scenario cannot assert a reason against an answer that was not a
		// refusal — which is how "not 200" checks quietly stop meaning
		// anything.
		CheckContains[ContainedDaemon]("the refusal says {string}",
			"the daemon's refusal",
			func(in ContainedDaemon) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("no answer at all: %v", in.Err)
				}
				if in.Status >= 200 && in.Status < 300 {
					return "", fmt.Errorf("expected a refusal, the daemon answered %d", in.Status)
				}
				return in.Body, nil
			}),

		CheckString[ContainedDaemon]("the answer's bytes are {string}",
			"the bytes the daemon served",
			func(in ContainedDaemon) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Body, nil
			},
			func(in ContainedDaemon) string { return fmt.Sprintf("status %d", in.Status) }),

		// The arbitrary-read assertion. A status alone does not carry it: a
		// daemon that served the file with a 404 would still have leaked it.
		brine.DefineCheck[ContainedDaemon](
			"the answer does not contain {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) error {
				unwanted, err := paramAt("the answer does not contain {string}", p, 0)
				if err != nil {
					return err
				}
				if strings.Contains(in.Body, unwanted) {
					return fmt.Errorf(
						"ARBITRARY READ: the answer (status %d) carries %q, which lives outside the storage root: %s",
						in.Status, unwanted, abbrev(in.Body))
				}
				return nil
			},
		),

		CheckString[ContainedDaemon]("the file outside the storage root still reads {string}",
			"the file outside the storage root",
			func(in ContainedDaemon) (string, error) {
				if in.OutsideFile == "" {
					return "", fmt.Errorf("no file outside the storage root was set up")
				}
				data, err := os.ReadFile(in.OutsideFile)
				if err != nil {
					return "", fmt.Errorf("ESCAPE: the file outside the storage root is gone: %w", err)
				}
				return string(data), nil
			}),

		CheckStringFor[ContainedDaemon]("the store's file {string} reads {string}",
			"the stored file",
			func(in ContainedDaemon, rel string) (string, error) {
				data, err := os.ReadFile(in.storePath(rel))
				if err != nil {
					return "", fmt.Errorf("read %q from the store: %w", rel, err)
				}
				return string(data), nil
			}),

		// Lstat, not Stat: a dangling symlink left behind by a refused
		// extraction is exactly the landmine this asserts is absent, and Stat
		// would not see it.
		brine.DefineCheck[ContainedDaemon](
			"the store has nothing at {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) error {
				rel, err := paramAt("the store has nothing at {string}", p, 0)
				if err != nil {
					return err
				}
				full := in.storePath(rel)
				if _, err := os.Lstat(full); err == nil {
					return fmt.Errorf("expected the store to hold nothing at %q, but %s exists (%s)",
						rel, full, describeEntry(full))
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("stat %q: %w", rel, err)
				}
				return nil
			},
		),

		CheckThat[ContainedDaemon]("nothing was created at that destination outside the root",
			func(in ContainedDaemon) error {
				if in.OutsideDest == "" {
					return fmt.Errorf("no step named a destination outside the storage root")
				}
				if _, err := os.Lstat(in.OutsideDest); err == nil {
					return fmt.Errorf("ESCAPE: the daemon created %s, outside its storage root", in.OutsideDest)
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("stat %q: %w", in.OutsideDest, err)
				}
				return nil
			}),

		// The temp directory must be REMOVED on the refusal path, not merely
		// left unpromoted: it sits under steps/, where it is addressable as an
		// artifact and countable by the sweeper.
		CheckThat[ContainedDaemon]("no extraction temp directory is left under steps",
			func(in ContainedDaemon) error {
				entries, err := os.ReadDir(in.storePath("steps"))
				if err != nil {
					return fmt.Errorf("read the steps directory: %w", err)
				}
				var leaked []string
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), ".in-tmp-") {
						leaked = append(leaked, e.Name())
					}
				}
				if len(leaked) > 0 {
					return fmt.Errorf("a refused stream-in left extraction temp directories under steps/: %v", leaked)
				}
				return nil
			}),

		// Readlink rather than reading through the link, because three things
		// can go wrong and only one of them is visible from the contents: the
		// link can be dereferenced into a copy, its target can be rewritten,
		// or it can fail to resolve. This is the first two.
		CheckStringFor[ContainedDaemon]("the store's link {string} points at {string}",
			"the symlink's target",
			func(in ContainedDaemon, rel string) (string, error) {
				full := in.storePath(rel)
				info, err := os.Lstat(full)
				if err != nil {
					return "", fmt.Errorf("the artifact's internal symlink %q was not extracted: %w", rel, err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					return "", fmt.Errorf(
						"%q is not a symlink after extraction (mode %v) — it was dereferenced into a copy",
						rel, info.Mode())
				}
				return os.Readlink(full)
			}),

		CheckContains[ContainedDaemon]("the alias store on disk names {string}",
			"the alias store on disk",
			func(in ContainedDaemon) (string, error) { return in.aliasStoreContents() }),

		brine.DefineCheck[ContainedDaemon](
			"the alias store on disk does not name {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) error {
				name, err := paramAt("the alias store on disk does not name {string}", p, 0)
				if err != nil {
					return err
				}
				contents, err := in.aliasStoreContents()
				if err != nil {
					// No file at all means nothing was persisted, which
					// satisfies the claim. Reported so a scenario that meant
					// to assert against a written store can see it did not.
					return nil
				}
				if strings.Contains(contents, name) {
					return fmt.Errorf(
						"a refused registration was persisted to the alias store, so it survives a restart: %s",
						abbrev(contents))
				}
				return nil
			},
		),

		// -------------------------------------------------------------------
		// Checks over a sweep
		// -------------------------------------------------------------------

		brine.DefineCheck[ContainedDaemon](
			"every one of those answers was {int}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) error {
				want, err := intAt("every one of those answers was {int}", p, 0)
				if err != nil {
					return err
				}
				if len(in.Probes) == 0 {
					return fmt.Errorf("no requests were swept, so this check would pass vacuously")
				}
				var wrong []string
				for _, pr := range in.Probes {
					if pr.Status != want {
						wrong = append(wrong, fmt.Sprintf("%s -> %d %s", pr.Label, pr.Status, abbrev(pr.Body)))
					}
				}
				if len(wrong) > 0 {
					return fmt.Errorf("expected all %d answers to be %d; %d were not:\n    %s",
						len(in.Probes), want, len(wrong), strings.Join(wrong, "\n    "))
				}
				return nil
			},
		),

		brine.DefineCheck[ContainedDaemon](
			"every one of those refusals said {string}",
			func(in ContainedDaemon, p brine.Params, _ *brine.Recorder) error {
				want, err := paramAt("every one of those refusals said {string}", p, 0)
				if err != nil {
					return err
				}
				if len(in.Probes) == 0 {
					return fmt.Errorf("no requests were swept, so this check would pass vacuously")
				}
				var wrong []string
				for _, pr := range in.Probes {
					if pr.Status >= 200 && pr.Status < 300 {
						wrong = append(wrong, fmt.Sprintf("%s was not refused at all (%d)", pr.Label, pr.Status))
						continue
					}
					if !strings.Contains(pr.Body, want) {
						wrong = append(wrong, fmt.Sprintf("%s -> %d %s", pr.Label, pr.Status, abbrev(pr.Body)))
					}
				}
				if len(wrong) > 0 {
					return fmt.Errorf("expected every refusal to mention %q; %d did not:\n    %s",
						want, len(wrong), strings.Join(wrong, "\n    "))
				}
				return nil
			},
		),

		// The other half of refusing ".". A 4xx that still carried a tar of
		// the whole store would satisfy the status check above, and this is
		// the reason the sweep covers GET and not only DELETE: os.Root refuses
		// "." for removal but happily opens it.
		CheckThat[ContainedDaemon]("none of those answers listed the store's contents",
			func(in ContainedDaemon) error {
				if len(in.Probes) == 0 {
					return fmt.Errorf("no requests were swept, so this check would pass vacuously")
				}
				for _, pr := range in.Probes {
					for _, structural := range []string{"aliases.json", "steps/"} {
						if strings.Contains(pr.Body, structural) {
							return fmt.Errorf(
								"%s (status %d) enumerated the store — its answer carries %q: %s",
								pr.Label, pr.Status, structural, abbrev(pr.Body))
						}
					}
				}
				return nil
			}),
	}
}

// swapForLink replaces the path a key was registered at with a symlink out of
// the storage root, the way a compromised or careless step could.
func swapForLink(in ContainedDaemon, key, target string) error {
	path, ok := in.Registered[key]
	if !ok {
		return fmt.Errorf("nothing was registered under %q in this scenario", key)
	}
	if target == "" {
		return fmt.Errorf("no path outside the storage root was set up to link to")
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("clear the registered path %q: %w", path, err)
	}
	return os.Symlink(target, path)
}

// describeEntry says what is at a path, so "the store has nothing at X" fails
// with the thing it found rather than only the fact that it found something.
func describeEntry(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "unreadable"
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(path)
		return "a symlink to " + target
	case info.IsDir():
		entries, _ := os.ReadDir(path)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return fmt.Sprintf("a directory holding %v", names)
	default:
		return fmt.Sprintf("a %d-byte file", info.Size())
	}
}
