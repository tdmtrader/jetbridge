// Package config reads one queue's config file, strictly. Only Batch is meant
// for the core; every other section belongs to one adapter.
package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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
}

type Repository struct {
	URI       string `yaml:"uri"`
	Main      string `yaml:"main"`
	Candidate string `yaml:"candidate"`
}

type Admission struct {
	Source        string `yaml:"source"`
	Prefix        string `yaml:"prefix"`         // refs: a change is admitted by pushing it to <prefix><id>
	ControlPrefix string `yaml:"control_prefix"` // the operator's requests to the runner go under it
}

// Batch is the only section the core receives, as this plain struct.
type Batch struct {
	Max       int    `yaml:"max"`
	RetryNone int    `yaml:"retry_none"`
	Strategy  string `yaml:"strategy"` // which core Strategy runs the queue
	// Adaptive, if set, sizes batches from Start by their results; nil keeps max fixed.
	Adaptive *Adaptive `yaml:"adaptive"`
}

type Adaptive struct {
	Start     int `yaml:"start"`
	Min       int `yaml:"min"`
	GrowAfter int `yaml:"grow_after"`
}

// Compose squashes: each change lands as one commit on main.
type Compose struct {
	Committer Committer `yaml:"committer"`
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
		Admission: Admission{Source: "refs", Prefix: "refs/queue/admit/", ControlPrefix: "refs/queue/control/"},
		// Small batches bisect cheaply; one retry rides out an infra blip.
		Batch:   Batch{Max: 4, RetryNone: 1, Strategy: "serial"},
		Compose: Compose{Committer: Committer{Name: "merge-queue", Email: "merge-queue@localhost"}},
		// Merge commits are disabled in the repo settings. Three land errors in a
		// row is past a race on main; a person should look.
		Lander: Lander{MaxFailures: 3, LeaseRef: "refs/queue/lease"},
		Store:  Store{Ref: "refs/queue/state"},
	}
}

// known lists the keys allowed at each path; runner and notify are absent, so never walked.
var known = map[string][]string{
	"":                  {"apiVersion", "repository", "admission", "batch", "compose", "runner", "lander", "notify", "store"},
	"repository":        {"uri", "main", "candidate"},
	"admission":         {"source", "prefix", "control_prefix"},
	"batch":             {"max", "retry_none", "strategy", "adaptive"},
	"batch.adaptive":    {"start", "min", "grow_after"},
	"compose":           {"committer"},
	"compose.committer": {"name", "email"},
	"lander":            {"max_failures", "lease_ref", "scratch"},
	"store":             {"ref"},
}

// Parse reads one queue's config file, refusing unknown keys and values.
func Parse(data []byte) (Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, err
	}
	if len(doc.Content) == 0 {
		return Config{}, errors.New("config file is empty")
	}
	if err := Strict(doc.Content[0], "", known); err != nil {
		return Config{}, err
	}
	c := Defaults()
	if err := doc.Content[0].Decode(&c); err != nil {
		return Config{}, err
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
		return fmt.Errorf("repository.candidate %q must be set and differ from repository.main; the queue force-pushes it", r.Candidate)
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
				return fmt.Errorf("prefix %q overlaps %q, which the queue owns", p, r)
			}
		}
	}
	return errors.Join(
		oneOf("apiVersion", c.APIVersion, APIVersion),
		oneOf("admission.source", c.Admission.Source, "refs"),
		oneOf("batch.strategy", c.Batch.Strategy, "serial"),
	)
}

func head(branch string) string { return "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/") }

func oneOf(key, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s %q is not allowed; use one of: %s", key, value, strings.Join(allowed, ", "))
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
