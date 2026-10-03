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
	Notify     Notify     `yaml:"notify"`
}

type Repository struct {
	URI       string `yaml:"uri"`
	Main      string `yaml:"main"`
	Candidate string `yaml:"candidate"`
}

type Admission struct {
	Source  string   `yaml:"source"`
	Require []string `yaml:"require"`
}

// Batch is the only section the core receives, as this plain struct.
type Batch struct {
	Max       int    `yaml:"max"`
	RetryNone int    `yaml:"retry_none"`
	Bisect    string `yaml:"bisect"`
}

type Compose struct {
	Hook string `yaml:"hook"`
}

type Lander struct {
	Mode       string `yaml:"mode"`
	Credential string `yaml:"credential"`
}

type Notify struct {
	Kind    string `yaml:"kind"`
	Command string `yaml:"command"`
}

// Defaults are read from tdmtrader/jetbridge's history.
func Defaults() Config {
	return Config{
		// GitHub's default branch is main, but all work lands on core.
		Repository: Repository{Main: "core", Candidate: "queue-next"},
		// 0 merged PRs ever: work arrives as pushed branches.
		Admission: Admission{Source: "refs", Require: []string{}},
		// Small batches bisect cheaply; one retry rides out an infra blip.
		Batch: Batch{Max: 4, RetryNone: 1, Bisect: "halves"},
		// 0 merge commits on core since 2026-09-09 across 1119 commits, and
		// merge commits are disabled in the repo settings.
		Lander: Lander{Mode: "ff-only"},
	}
}

// known lists the keys allowed at each path; runner is absent, so never walked.
var known = map[string][]string{
	"":           {"apiVersion", "repository", "admission", "batch", "compose", "runner", "lander", "notify"},
	"repository": {"uri", "main", "candidate"},
	"admission":  {"source", "require"},
	"batch":      {"max", "retry_none", "bisect"},
	"compose":    {"hook"},
	"lander":     {"mode", "credential"},
	"notify":     {"kind", "command"},
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
	if err := checkKeys(doc.Content[0], ""); err != nil {
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
	return errors.Join(
		oneOf("apiVersion", c.APIVersion, APIVersion),
		oneOf("admission.source", c.Admission.Source, "refs", "github-pr"),
		oneOf("batch.bisect", c.Batch.Bisect, "halves"),
		oneOf("lander.mode", c.Lander.Mode, "ff-only"),
	)
}

func oneOf(key, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s %q is not allowed; use one of: %s", key, value, strings.Join(allowed, ", "))
}

func checkKeys(n *yaml.Node, path string) error {
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
		if err := checkKeys(n.Content[i+1], join(path, k)); err != nil {
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

// distance is the Levenshtein edit distance between a and b.
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
