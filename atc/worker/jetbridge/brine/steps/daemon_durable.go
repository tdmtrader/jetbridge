package steps

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// The durable tier, driven against the daemon that implements it.
//
// step-closing.feature already has nine durable-tier scenarios, and they drive
// a DOUBLE of the daemon: an http.Server answering /resource-caches/ and
// /durable/restore out of a map. That double is the right tool for its
// question, which is what the ATC does with an answer — it can be made to
// advertise no tier, to go away, to hold bytes on a peer.
//
// It cannot say whether the answers are RIGHT, and for this tier that gap is
// wider than usual, because half of what the durable path does is not an
// answer at all: it is a change to a filesystem. Whether a warmed copy lands
// where the sweeper reclaims it, whether an upload is filed under the content
// key or under a Postgres row id, whether a refused extraction leaves residue
// behind — none of those are visible in a response, and the double had no
// filesystem to get them wrong on.
//
// So these run the real binary with --durable-store=filesystem --durable-path,
// which makes the "bucket" an ordinary directory this file can seed and
// inspect. That is what turns "the store was not touched" from a call count
// into an outcome: give the store DIFFERENT bytes from the node's copy, and
// the assertion names which bytes arrived. Nothing here counts a request and
// nothing here records one.
//
// No Kubernetes is involved and no scenario passes --node-name: the daemon
// builds a Kubernetes client the moment it is given one, and os.Exit(1)s
// outside a cluster.
//
// DAEMONS STARTED HERE: eight, one per scenario, in the Given and never as a
// resource. brine acquires every ScopeScenario resource before EVERY scenario
// in the suite, so a daemon registered that way is started and killed once per
// scenario in the whole corpus to be used eight times — measured at 70 seconds
// when the sibling file tried it. Started this way each costs ~100ms, after a
// one-time `go build ./cmd/artifact-daemon` that this file shares with
// realdaemon.go through its sync.Once.

// DurableDaemon is a running daemon, the directory standing in for its bucket,
// and the last answer it gave.
type DurableDaemon struct {
	Daemon *realDaemon

	// StorePath is the durable store's root. A filesystem store maps a key
	// straight onto a path under it, so seeding and inspection are ordinary
	// file operations rather than an interface a test had to implement.
	StorePath string

	// Snapshot is what the storage root held when the daemon started, so a
	// scenario can assert that a refused request added nothing to it.
	Snapshot []string

	// Victim is the file a hostile object in the store tries to reach.
	Victim string

	Status int
	Header http.Header
	Body   []byte
	Err    error
}

var durableHTTP = &http.Client{Timeout: 60 * time.Second}

func (s DurableDaemon) request(method, path, body string) DurableDaemon {
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.Daemon.URL+path, payload)
	if err != nil {
		s.Status, s.Header, s.Body, s.Err = 0, nil, nil, err
		return s
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := durableHTTP.Do(req)
	if err != nil {
		s.Status, s.Header, s.Body, s.Err = 0, nil, nil, err
		return s
	}
	defer resp.Body.Close()

	read, err := io.ReadAll(resp.Body)
	s.Status, s.Header, s.Body, s.Err = resp.StatusCode, resp.Header, read, err
	return s
}

// restoreAnswer is the part of the /durable/restore response that says where
// the bytes came from. "restored" is false when the daemon found the artifact
// already on its own disk and did not go to the store at all.
type restoreAnswer struct {
	Restored bool   `json:"restored"`
	Path     string `json:"path"`
}

func (s DurableDaemon) restoreAnswer() (restoreAnswer, error) {
	var answer restoreAnswer
	if err := json.Unmarshal(s.Body, &answer); err != nil {
		return answer, fmt.Errorf("the daemon's answer is not the JSON a restore returns (%d): %s",
			s.Status, abbrev(string(s.Body)))
	}
	return answer, nil
}

// startDurableDaemon brings up one daemon over its own storage root and its
// own store directory, and arranges for both to be cleaned up whether the
// scenario passes, fails or is interrupted.
func startDurableDaemon(rec *brine.Recorder, extra ...string) (DurableDaemon, error) {
	store, err := os.MkdirTemp("", "brine-durable-store-*")
	if err != nil {
		return DurableDaemon{}, err
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(store) })

	args := append([]string{"--durable-store", "filesystem", "--durable-path", store}, extra...)
	d, err := startRealDaemon(args...)
	if err != nil {
		return DurableDaemon{}, err
	}
	rec.RegisterDisposer(func() { _ = d.stop() })

	return DurableDaemon{Daemon: d, StorePath: store, Snapshot: rootEntries(d.Root)}, nil
}

// rootEntries lists the storage root and its steps/ directory, which together
// are everywhere a request can leave something behind.
func rootEntries(root string) []string {
	var found []string
	for _, dir := range []string{root, filepath.Join(root, "steps")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel, err := filepath.Rel(root, filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			found = append(found, filepath.ToSlash(rel))
		}
	}
	sort.Strings(found)
	return found
}

// stepEntries lists what the daemon's steps/ directory holds. A restored copy
// belongs here as a DIRECT child: the sweeper reclaims direct children, so a
// copy one level down is one the node never reclaims.
func stepEntries(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "steps"))
	if err != nil {
		return nil, fmt.Errorf("read the daemon's steps directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// storeKeys lists the objects in the store, the way the daemon's own
// enumeration does: one optional class-prefix directory deep, skipping the
// dot-prefixed temporary files an upload in flight leaves beside its object.
func storeKeys(storePath string) ([]string, error) {
	var keys []string
	top, err := os.ReadDir(storePath)
	if err != nil {
		return nil, fmt.Errorf("read the durable store: %w", err)
	}
	for _, entry := range top {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			keys = append(keys, entry.Name())
			continue
		}
		nested, err := os.ReadDir(filepath.Join(storePath, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read the durable store's %q prefix: %w", entry.Name(), err)
		}
		for _, child := range nested {
			if child.IsDir() || strings.HasPrefix(child.Name(), ".") {
				continue
			}
			keys = append(keys, entry.Name()+"/"+child.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func writeStoreObject(storePath, key string, body []byte) error {
	full := filepath.Join(storePath, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create the store's prefix for %q: %w", key, err)
	}
	return os.WriteFile(full, body, 0o644)
}

// durableTarOfOneFile builds what a promotion of a one-file directory produces:
// an UNCOMPRESSED tar whose member carries the path it had inside the
// directory. The durable store holds plain tars — the restore path hands the
// object straight to the tar reader — so this must not be gzipped, unlike the
// stream-in archives in tarhelp.go.
func durableTarOfOneFile(name, content string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DaemonDurableDefinitions drives the durable tier of an actual
// artifact-daemon process.
func DaemonDurableDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, DurableDaemon](
			"a real artifact daemon with a durable store",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (DurableDaemon, error) {
				return startDurableDaemon(rec)
			},
		),

		// The reclaim half needs two more flags, and running them through the
		// real binary is the point: the Go test called sweep() directly, so
		// nothing there could tell whether main() still wires
		// --durable-retention into the maintainer at all.
		//
		// The interval is short because the maintainer jitters its first walk
		// over one interval and then repeats; a scenario that waits for a
		// reclaim would otherwise wait fifteen minutes for the first pass.
		brine.DefineMap[brine.Empty, DurableDaemon](
			"a real artifact daemon reclaiming the class {string} after {int} hours",
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder) (DurableDaemon, error) {
				class, err := paramAt("a real artifact daemon reclaiming the class {string} after {int} hours", p, 0)
				if err != nil {
					return DurableDaemon{}, err
				}
				hours, err := intAt("a real artifact daemon reclaiming the class {string} after {int} hours", p, 1)
				if err != nil {
					return DurableDaemon{}, err
				}
				return startDurableDaemon(rec,
					"--durable-retention", fmt.Sprintf("%s=%dh", class, hours),
					"--durable-maintenance-interval", "200ms",
				)
			},
		),

		// Seeding the store directly is the whole reason the filesystem
		// backend is used here. An object in a bucket got there from some
		// other node on some other day, and that is the state a warm exists
		// for; producing it through this daemon's own upload path would make
		// every restore scenario depend on the promote path being right.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the durable store holds the cache {string} whose file {string} reads {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the durable store holds the cache {string} whose file {string} reads {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				file, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				archive, err := durableTarOfOneFile(file, content)
				if err != nil {
					return in, err
				}
				return in, writeStoreObject(in.StorePath, key, archive)
			},
		),

		// The same seeding under a second spelling. A probe fallback would
		// look up the BARE key, so a store holding only the class-prefixed one
		// cannot falsify the 404 the scenario is about.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the durable store also holds the cache {string} whose file {string} reads {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the durable store also holds the cache {string} whose file {string} reads {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				file, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				archive, err := durableTarOfOneFile(file, content)
				if err != nil {
					return in, err
				}
				return in, writeStoreObject(in.StorePath, key, archive)
			},
		),

		// Age is written rather than waited for: a retention rule is stated in
		// hours and days, and sleeping for one is not an option.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the durable store holds the object {string}, last written {int} hours ago",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the durable store holds the object {string}, last written {int} hours ago"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				hours, err := intAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				if err := writeStoreObject(in.StorePath, key, []byte("an object")); err != nil {
					return in, err
				}
				when := time.Now().Add(-time.Duration(hours) * time.Hour)
				full := filepath.Join(in.StorePath, filepath.FromSlash(key))
				return in, os.Chtimes(full, when, when)
			},
		),

		// Every daemon in the cluster reads the same store, so an object in it
		// is input from somewhere the daemon does not control. This one is
		// written straight into the store rather than through an upload,
		// because the premise is exactly that it was never produced by us.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the durable store holds a hostile object under {string} that links out to a file reading {string}",
			func(in DurableDaemon, p brine.Params, rec *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the durable store holds a hostile object under {string} that links out to a file reading {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}

				outside, err := os.MkdirTemp("", "brine-durable-outside-*")
				if err != nil {
					return in, err
				}
				rec.RegisterDisposer(func() { _ = os.RemoveAll(outside) })

				victim := filepath.Join(outside, "victim.txt")
				if err := os.WriteFile(victim, []byte(content), 0o644); err != nil {
					return in, err
				}

				// Two members: a symlink pointing out of the destination, then
				// a file written THROUGH it. Neither is hostile alone, which
				// is why the daemon has to refuse on the target rather than on
				// the file's own name.
				var buf bytes.Buffer
				tw := tar.NewWriter(&buf)
				if err := tw.WriteHeader(&tar.Header{
					Name: "hatch", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0o777,
				}); err != nil {
					return in, err
				}
				payload := []byte("OVERWRITTEN BY AN OBJECT FROM THE BUCKET")
				if err := tw.WriteHeader(&tar.Header{
					Name: "hatch/victim.txt", Typeflag: tar.TypeReg, Size: int64(len(payload)), Mode: 0o644,
				}); err != nil {
					return in, err
				}
				if _, err := tw.Write(payload); err != nil {
					return in, err
				}
				if err := tw.Close(); err != nil {
					return in, err
				}

				in.Victim = victim
				return in, writeStoreObject(in.StorePath, key, buf.Bytes())
			},
		),

		// What a get step leaves behind: files in its output directory on the
		// node. Nothing has told the daemon they are there — that is the
		// registration, and in these scenarios the registration is the thing
		// under description.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"a get step on this node left the cache {string} whose file {string} reads {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "a get step on this node left the cache {string} whose file {string} reads {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				file, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				content, err := paramAt(pattern, p, 2)
				if err != nil {
					return in, err
				}
				dir := filepath.Join(in.Daemon.Root, "steps", key)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return in, err
				}
				return in, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
			},
		),

		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the ATC asks it whether the cache {string} is on this node",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				key, err := paramAt("the ATC asks it whether the cache {string} is on this node", p, 0)
				if err != nil {
					return in, err
				}
				return in.request(http.MethodHead, "/resource-caches/"+key, ""), nil
			},
		),

		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the ATC asks it to warm {string} from the content key {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the ATC asks it to warm {string} from the content key {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				durableKey, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				return in.request(http.MethodPost, "/durable/restore",
					fmt.Sprintf(`{"key":%q,"durable_key":%q}`, key, durableKey)), nil
			},
		),

		// The empty content key rather than an absent field: this is what an
		// ATC that KNOWS about the tier sends for an artifact it has decided
		// not to keep, which is the case that has to stay silent.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the ATC registers {string} with it, naming no content key",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				key, err := paramAt("the ATC registers {string} with it, naming no content key", p, 0)
				if err != nil {
					return in, err
				}
				local := filepath.Join(in.Daemon.Root, "steps", key)
				return in.request(http.MethodPost, "/register",
					fmt.Sprintf(`{"key":%q,"local_path":%q,"durable_key":""}`, key, local)), nil
			},
		),

		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the ATC registers {string} with it under the content key {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				const pattern = "the ATC registers {string} with it under the content key {string}"
				key, err := paramAt(pattern, p, 0)
				if err != nil {
					return in, err
				}
				durableKey, err := paramAt(pattern, p, 1)
				if err != nil {
					return in, err
				}
				local := filepath.Join(in.Daemon.Root, "steps", key)
				return in.request(http.MethodPost, "/register",
					fmt.Sprintf(`{"key":%q,"local_path":%q,"durable_key":%q}`, key, local, durableKey)), nil
			},
		),

		brine.DefineMap[DurableDaemon, DurableDaemon](
			"a build fetches the cache {string} from it",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				key, err := paramAt("a build fetches the cache {string} from it", p, 0)
				if err != nil {
					return in, err
				}
				return in.request(http.MethodGet, "/resource-caches/"+key, ""), nil
			},
		),

		// A promotion is detached from the register response on purpose — the
		// build's next step must not wait on an upload — so this waits for the
		// object to appear and fails if it never does.
		//
		// It doubles as the barrier the "nothing else" assertion needs. The
		// unkeyed registration is issued FIRST and the keyed one second, so by
		// the time the second upload has landed the daemon has had its chance
		// at the first; the settle after it makes that margin explicit rather
		// than implicit in scheduling.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the upload of {string} lands in the durable store",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				key, err := paramAt("the upload of {string} lands in the durable store", p, 0)
				if err != nil {
					return in, err
				}
				full := filepath.Join(in.StorePath, filepath.FromSlash(key))
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(full); err == nil {
						time.Sleep(300 * time.Millisecond)
						return in, nil
					}
					time.Sleep(25 * time.Millisecond)
				}
				held, _ := storeKeys(in.StorePath)
				return in, fmt.Errorf("the durable store never received %q; it holds %v", key, held)
			},
		),

		// The reclaim runs on the daemon's own schedule, so this waits for its
		// effect rather than calling the pass. A rule that reclaims nothing
		// fails here; the checks that follow are what fails when it reclaims
		// too much.
		brine.DefineMap[DurableDaemon, DurableDaemon](
			"the daemon's reclaim pass removes {string}",
			func(in DurableDaemon, p brine.Params, _ *brine.Recorder) (DurableDaemon, error) {
				key, err := paramAt("the daemon's reclaim pass removes {string}", p, 0)
				if err != nil {
					return in, err
				}
				full := filepath.Join(in.StorePath, filepath.FromSlash(key))
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(full); os.IsNotExist(err) {
						return in, nil
					}
					time.Sleep(50 * time.Millisecond)
				}
				held, _ := storeKeys(in.StorePath)
				return in, fmt.Errorf("%q was still in the durable store after 30s; it holds %v", key, held)
			},
		),

		CheckInt[DurableDaemon]("the daemon's answer is {int}",
			"the daemon's status",
			func(in DurableDaemon) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("no answer at all: %v", in.Err)
				}
				return in.Status, nil
			},
			func(in DurableDaemon) string { return "body: " + abbrev(string(in.Body)) }),

		CheckContains[DurableDaemon]("the daemon's answer explains {string}",
			"the daemon's refusal",
			func(in DurableDaemon) (string, error) {
				if in.Status >= 200 && in.Status < 300 {
					return "", fmt.Errorf("expected a refusal, the daemon answered %d", in.Status)
				}
				return string(in.Body), nil
			}),

		// The two answers a restore can give, told apart by what the caller is
		// entitled to conclude. "Already here" means no request went to the
		// store at all, which is the whole point of probing the node first.
		CheckThat[DurableDaemon]("the answer says the bytes were already here", func(in DurableDaemon) error {
			answer, err := in.restoreAnswer()
			if err != nil {
				return err
			}
			if tier := in.Header.Get("X-Artifact-Tier"); tier != "local" {
				return fmt.Errorf("expected the daemon to report the bytes as local, it reported %q (status %d)",
					tier, in.Status)
			}
			if answer.Restored {
				return fmt.Errorf("expected the daemon to report that it restored nothing, it reported a restore from %q",
					answer.Path)
			}
			return nil
		}),

		CheckThat[DurableDaemon]("the answer says the bytes came from the durable store", func(in DurableDaemon) error {
			answer, err := in.restoreAnswer()
			if err != nil {
				return err
			}
			if tier := in.Header.Get("X-Artifact-Tier"); tier != "durable" {
				return fmt.Errorf("expected the daemon to report the bytes as durable, it reported %q (status %d)",
					tier, in.Status)
			}
			if !answer.Restored {
				return fmt.Errorf("expected the daemon to report a restore, it reported none")
			}
			return nil
		}),

		// Capability rides an answer the ATC already asks for. It is only ever
		// consulted on a miss, so a header that appears on hits alone is a
		// header nobody can read.
		CheckThat[DurableDaemon]("the answer advertises that this daemon can warm from the store", func(in DurableDaemon) error {
			if in.Err != nil {
				return fmt.Errorf("no answer at all: %v", in.Err)
			}
			if got := in.Header.Get("X-Durable-Tier"); got != "enabled" {
				return fmt.Errorf("expected the %d answer to advertise a durable tier, X-Durable-Tier was %q",
					in.Status, got)
			}
			return nil
		}),

		CheckStringFor[DurableDaemon]("the cache holds {string} reading {string}",
			"the file in the archive the daemon served",
			func(in DurableDaemon, name string) (string, error) {
				if in.Status != http.StatusOK {
					return "", fmt.Errorf("the daemon did not serve the cache; it answered %d: %s",
						in.Status, abbrev(string(in.Body)))
				}
				// The same tar reader the sibling real-daemon scenarios use.
				return RealDaemonState{Body: in.Body}.tarMember(name)
			}),

		// Flatness, stated as what the directory holds. This is not reachable
		// over HTTP: an artifact read falls back to the registry alias, which
		// points at the copy wherever it landed, so a nested restore serves
		// perfectly well and is only wrong later — when the sweeper walks the
		// direct children of steps/ and never sees it, and node disk grows
		// without bound the way task caches on hostPath once did.
		CheckString[DurableDaemon]("the node's steps directory holds only {string}",
			"what the daemon's steps directory holds",
			func(in DurableDaemon) (string, error) {
				names, err := stepEntries(in.Daemon.Root)
				if err != nil {
					return "", err
				}
				return strings.Join(names, ", "), nil
			}),

		CheckString[DurableDaemon]("the durable store holds exactly {string}",
			"what the durable store holds",
			func(in DurableDaemon) (string, error) {
				keys, err := storeKeys(in.StorePath)
				if err != nil {
					return "", err
				}
				return strings.Join(keys, ", "), nil
			}),

		CheckMember[DurableDaemon]("the durable store still holds {string}",
			"the objects in the durable store",
			func(in DurableDaemon) ([]string, error) { return storeKeys(in.StorePath) }),

		CheckString[DurableDaemon]("the file it tried to escape to still reads {string}",
			"the file outside the restore destination",
			func(in DurableDaemon) (string, error) {
				if in.Victim == "" {
					return "", fmt.Errorf("no scenario step put a file outside the destination")
				}
				body, err := os.ReadFile(in.Victim)
				if err != nil {
					return "", fmt.Errorf("the file outside the destination is unreadable, "+
						"which is itself an escape: %w", err)
				}
				return string(body), nil
			}),

		// A refusal must leave nothing behind — not the destination it was
		// about to promote, and not the temporary directory it extracts
		// through, which is a sibling of the destination under steps/ and
		// would otherwise be residue for the sweeper to find.
		CheckThat[DurableDaemon]("nothing new appeared under the storage root", func(in DurableDaemon) error {
			now := rootEntries(in.Daemon.Root)
			before := map[string]bool{}
			for _, e := range in.Snapshot {
				before[e] = true
			}
			var added []string
			for _, e := range now {
				if !before[e] {
					added = append(added, e)
				}
			}
			if len(added) > 0 {
				return fmt.Errorf("a refused request created %v under the storage root (it held %v)",
					added, in.Snapshot)
			}
			return nil
		}),
	}
}
