// Package config reads one queue's config file, strictly. Only Batch is meant
// for the core; every other section belongs to one adapter.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

const APIVersion = "jetbridge.dev/queue/v2"

type Config struct {
	APIVersion string     `yaml:"apiVersion"`
	Repository Repository `yaml:"repository"`
	Admission  Admission  `yaml:"admission"`
	Batch      Batch      `yaml:"batch"`
	Compose    Compose    `yaml:"compose"`
	Runner     yaml.Node  `yaml:"runner"` // opaque: kept raw, never interpreted here
	Lander     Lander     `yaml:"lander"`
	Notify     yaml.Node  `yaml:"notify"` // opaque: kept raw, never interpreted here
	Store      Store      `yaml:"store"`
	Pause      Pause      `yaml:"pause"`
	Health     Health     `yaml:"health"`
}

type Repository struct {
	URI       string `yaml:"uri"`
	Main      string `yaml:"main"`
	Candidate string `yaml:"candidate"`
}

type Admission struct {
	Prefix        string `yaml:"prefix"`         // refs: a change is admitted by pushing it to <prefix><id>
	ControlPrefix string `yaml:"control_prefix"` // the operator's requests to the runner go under it
	// OperatorsFile, if set, is a git allowed-signers file (ssh keys): only a change, or a resume, promote, withdraw or resolve request, signed by one of its keys is honoured.
	OperatorsFile string `yaml:"operators_file"`
}

// Batch is the only section the core receives, as this plain struct.
type Batch struct {
	Max       int    `yaml:"max"`
	RetryNone int    `yaml:"retry_none"`
	Strategy  string `yaml:"strategy"` // which core Strategy runs the queue
	Order     string `yaml:"order"`    // proven-first or strict: who may land ahead of arrival order
	// Adaptive, if set, sizes batches from Start by their results; nil keeps max fixed.
	Adaptive *Adaptive `yaml:"adaptive"`
}

type Adaptive struct {
	Start     int `yaml:"start"`
	Min       int `yaml:"min"`
	GrowAfter int `yaml:"grow_after"`
}

// Compose squashes: each change lands as one commit on main.
// Pause is the core's only other section. Cooldown is how long a pause for no verdict lasts before
// the queue resumes itself; 0 means it waits for an operator.
type Pause struct {
	Cooldown time.Duration `yaml:"cooldown"`
}

// Health holds the limits the health command turns red at.
type Health struct {
	PauseAfter    time.Duration `yaml:"pause_after"`    // a pause nothing resumes by itself
	ResumeOverdue time.Duration `yaml:"resume_overdue"` // past the cool-down, an auto-resume still not done
	MaxInFlight   time.Duration `yaml:"max_in_flight"`  // a run in flight this long is stuck
}

type Compose struct {
	Committer   Committer     `yaml:"committer"`
	Hook        []string      `yaml:"hook"`         // argv, no shell; run in the composed tree, its changes join the candidate
	HookOwned   []string      `yaml:"hook_owned"`   // if set, path prefixes the hook may change; any other change is a failure
	HookTimeout time.Duration `yaml:"hook_timeout"` // a hook still running after this is a failure
	HookScript  string        `yaml:"hook_script"`  // instead of hook: a script on main that the test job runs; see hooked.go
}

type Committer struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

type Lander struct {
	MaxFailures int    `yaml:"max_failures"` // land errors in a row before the queue pauses
	LeaseRef    string `yaml:"lease_ref"`    // the ref that holds the lander's fence
	Scratch     string `yaml:"scratch"`      // parent dir for its private repo; default os.TempDir
}

type Store struct {
	Ref string `yaml:"ref"` // the one ref holding the queue's state and lease
}

// Defaults are read from tdmtrader/jetbridge's history.
func Defaults() Config {
	return Config{
		// GitHub's default branch is main, but all work lands on core.
		Repository: Repository{Main: "core", Candidate: "queue-next"},
		// 0 merged PRs ever: work arrives as pushed branches.
		Admission: Admission{Prefix: "refs/queue/admit/", ControlPrefix: "refs/queue/control/"},
		// Small batches bisect cheaply; one retry rides out an infra blip.
		// Order is fairness, not correctness: every landing is tested on the main it lands on.
		Batch:   Batch{Max: 4, RetryNone: 1, Strategy: "serial", Order: "proven-first"},
		Compose: Compose{Committer: Committer{Name: "merge-queue", Email: "merge-queue@localhost"}, HookTimeout: 15 * time.Minute},
		// Merge commits are disabled in the repo settings. Three land errors in a
		// row is past a race on main; a person should look.
		Lander: Lander{MaxFailures: 3, LeaseRef: "refs/queue/lease"},
		Store:  Store{Ref: "refs/queue/state"},
		// An outage that left no verdict usually ends within minutes.
		Pause:  Pause{Cooldown: 5 * time.Minute},
		Health: Health{PauseAfter: 5 * time.Minute, ResumeOverdue: 2 * time.Minute, MaxInFlight: time.Hour},
	}
}

// known lists the keys allowed at each path; runner and notify are absent, so never walked.
var known = map[string][]string{
	"":                  {"apiVersion", "repository", "admission", "batch", "pause", "health", "compose", "runner", "lander", "notify", "store"},
	"repository":        {"uri", "main", "candidate"},
	"admission":         {"prefix", "control_prefix", "operators_file"},
	"batch":             {"max", "retry_none", "strategy", "order", "adaptive"},
	"batch.adaptive":    {"start", "min", "grow_after"},
	"compose":           {"committer", "hook", "hook_owned", "hook_timeout", "hook_script"},
	"compose.committer": {"name", "email"},
	"lander":            {"max_failures", "lease_ref", "scratch"},
	"store":             {"ref"},
	"pause":             {"cooldown"},
	"health":            {"pause_after", "resume_overdue", "max_in_flight"},
}

// Parse reads one queue's config file, refusing unknown keys and values.
func Parse(data []byte) (Config, error) { return ParseWith(data, nil) }

// ParseWith is Parse with set, if not nil, applied before validation, so a
// caller may supply values the file leaves out.
func ParseWith(data []byte, set func(*Config)) (Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, errors.New("config is not valid YAML") // a yaml error may quote a value
	}
	if len(doc.Content) == 0 {
		return Config{}, errors.New("config file is empty")
	}
	if err := urls(doc.Content[0], ""); err != nil {
		return Config{}, err
	}
	if err := Strict(doc.Content[0], "", known); err != nil {
		return Config{}, err
	}
	c := Defaults()
	if err := doc.Content[0].Decode(&c); err != nil {
		return Config{}, errors.New("config: a value has the wrong type") // a yaml error may quote a value
	}
	if set != nil {
		set(&c)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.Repository.URI == "" {
		return errors.New("repository.uri is required; it is never guessed")
	}
	if c.Batch.Max < 1 || c.Batch.RetryNone < 0 {
		return errors.New("batch.max must be at least 1 and batch.retry_none at least 0")
	}
	if a := c.Batch.Adaptive; a != nil && (a.Min < 1 || a.Min > a.Start || a.Start > c.Batch.Max || a.GrowAfter < 1) {
		return errors.New("batch.adaptive needs 1 <= min <= start <= batch.max and grow_after >= 1")
	}
	if r := c.Repository; r.Candidate == "" || r.Candidate == r.Main {
		return errors.New("repository.candidate: must be set and differ from repository.main; the queue force-pushes it")
	}
	if !strings.HasPrefix(c.Store.Ref, "refs/") {
		return errors.New("store.ref must be a full ref name under refs/")
	}
	if p := c.Admission.Prefix; !strings.HasPrefix(p, "refs/") || !strings.HasSuffix(p, "/") {
		return errors.New("admission.prefix must be a ref prefix under refs/ ending in /")
	}
	if p := c.Admission.ControlPrefix; !strings.HasPrefix(p, "refs/") || !strings.HasSuffix(p, "/") {
		return errors.New("admission.control_prefix must be a ref prefix under refs/ ending in /")
	}
	if c.Pause.Cooldown < 0 {
		return errors.New("pause.cooldown must be at least 0")
	}
	if h := c.Compose.Hook; h != nil && (len(h) == 0 || slices.Contains(h, "")) {
		return errors.New("compose.hook must be a command and its arguments, none empty")
	}
	if slices.Contains(c.Compose.HookOwned, "") || (c.Compose.HookOwned != nil && c.Compose.Hook == nil) {
		return errors.New("compose.hook_owned needs compose.hook and prefixes that are not empty")
	}
	if s := c.Compose.HookScript; s != "" && (!RepoPath(s) || c.Compose.Hook != nil || c.Compose.HookOwned != nil) {
		return errors.New("compose.hook_script must be a path in the repository, without compose.hook or compose.hook_owned")
	}
	if c.Compose.HookTimeout <= 0 {
		return errors.New("compose.hook_timeout must be positive")
	}
	if h := c.Health; h.PauseAfter <= 0 || h.ResumeOverdue <= 0 || h.MaxInFlight <= 0 {
		return errors.New("health.pause_after, health.resume_overdue and health.max_in_flight must be positive")
	}
	if c.Lander.MaxFailures < 1 {
		return errors.New("lander.max_failures must be at least 1")
	}
	if !strings.HasPrefix(c.Lander.LeaseRef, "refs/") {
		return errors.New("lander.lease_ref must be a full ref name under refs/")
	}
	owned := []string{c.Store.Ref, c.Lander.LeaseRef, head(c.Repository.Main), head(c.Repository.Candidate), c.Admission.Prefix, c.Admission.ControlPrefix}
	for i, p := range owned[4:] {
		for j, r := range owned {
			if j != i+4 && (strings.HasPrefix(r, p) || strings.HasPrefix(p, r+"/")) {
				return fmt.Errorf("%s overlaps a ref the queue owns", []string{"admission.prefix", "admission.control_prefix"}[i])
			}
		}
	}
	return errors.Join(
		oneOf("apiVersion", c.APIVersion, APIVersion),
		oneOf("batch.strategy", c.Batch.Strategy, "serial"),
		oneOf("batch.order", c.Batch.Order, "proven-first", "strict"),
	)
}

// urls refuses a URL holding credentials in any scalar of the file, keys included, before
// any other check can quote one; the refusal names the key path, never the value. Each
// scalar is checked as it decodes, so a tag such as !!binary cannot hide a URL.
func urls(n *yaml.Node, path string) error {
	if n.Kind == yaml.ScalarNode {
		value := n.Value
		var v any
		if n.Decode(&v) == nil {
			if s, ok := v.(string); ok {
				value = s
			}
		}
		return URL(cmp.Or(path, "config"), value)
	}
	for i, child := range n.Content {
		p := path
		if n.Kind == yaml.MappingNode && i%2 == 1 { // the key before it was checked first
			p = join(path, n.Content[i-1].Value)
		}
		if err := urls(child, p); err != nil {
			return err
		}
	}
	return nil
}

var wholeURL = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// repoPath is a relative path inside the repository, as a hook script or the paths it owns.
var repoPath = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)

// RepoPath says whether p is a relative path that stays inside the repository.
func RepoPath(p string) bool { return repoPath.MatchString(p) && !strings.Contains(p, "..") }

// URL refuses a value that is a scheme:// URL if it does not parse or holds userinfo in its
// authority (between :// and the first / ? #); only an ssh:// login name with no password is
// allowed. Inside free text, a scheme:// is refused if the text after it, up to the first
// space or closing bracket or quote (not stopping at / ? #), holds an @; this fails closed. Either names the key, never the
// value. Values with no :// (paths, git@host:repo) are left to git.
func URL(key, value string) error {
	refuse := fmt.Errorf("%s: a URL must not hold credentials and must parse; give credentials to git or the runner out of band (see Credentials in the README)", key)
	free := strings.Split(value, "://")[1:]
	if v := strings.TrimSpace(value); wholeURL.MatchString(v) {
		_, rest, _ := strings.Cut(v, "://")
		authority := rest[:strings.IndexAny(rest+"/", "/?#")]
		u, err := url.Parse(v)
		if err != nil || (strings.Contains(authority, "@") || u.User != nil) && !sshLogin(u) {
			return refuse
		}
		free = free[1:] // any later scheme:// is free text
	}
	for _, rest := range free {
		end := strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(")]}>\"'", r) })
		if end < 0 {
			end = len(rest)
		}
		if strings.Contains(rest[:end], "@") {
			return refuse
		}
	}
	return nil
}

func sshLogin(u *url.URL) bool {
	_, pw := u.User.Password()
	return u.Scheme == "ssh" && u.User != nil && !pw
}

func head(branch string) string { return "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/") }

func oneOf(key, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s: unsupported value; use one of: %s", key, strings.Join(allowed, ", "))
}

// Strict refuses any key under n that known does not list for its path,
// naming the nearest; an adapter checks its own section with it.
func Strict(n *yaml.Node, path string, known map[string][]string) error {
	keys, walk := known[path]
	if !walk || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if v := n.Content[i+1]; v.Kind == yaml.AliasNode || k == "<<" {
			return fmt.Errorf("aliases are not supported (at %q)", join(path, k))
		}
		if !slices.Contains(keys, k) {
			return fmt.Errorf("unknown key %q; did you mean %q?", join(path, k), join(path, nearest(k, keys)))
		}
		if err := Strict(n.Content[i+1], join(path, k), known); err != nil {
			return err
		}
	}
	return nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func nearest(word string, keys []string) string {
	best := keys[0]
	for _, k := range keys[1:] {
		if distance(word, k) < distance(word, best) {
			best = k
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := []int{i}
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur = append(cur, min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(b)]
}
