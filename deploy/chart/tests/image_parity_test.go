package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every binary the chart runs has to be in the image the chart runs it from.
//
// This is the flag-drift failure one layer down, and it is worse: a rendered
// flag the binary rejects at least produces a message naming the flag, while a
// command that is not in the image produces `exec: "...": stat ...: no such file
// or directory` on a Pod nobody was watching. helm lint, every render test and
// go vet are all satisfied, because nothing in this repository otherwise reads
// the Dockerfile and the chart together.
//
// The output plane made this real rather than theoretical: it adds five commands
// at once, across two Dockerfiles, one of which is referenced by no build script
// in the tree and is therefore exactly the one that would be left behind.

// commandPath matches an absolute path into the image's bin directory, wherever
// it appears -- a `command:` entry, an init container, an args list.
var commandPath = regexp.MustCompile(`/usr/local/concourse/bin/([a-z0-9][a-z0-9-]*)`)

// dockerfileCopy matches the COPY that puts a binary at that path.
var dockerfileCopy = regexp.MustCompile(`(?m)^COPY\s+.*\s+/usr/local/concourse/bin/([a-z0-9][a-z0-9-]*)\s*$`)

// imagesThatRunCommands are the Dockerfiles that build the image the chart
// points at. Both, because one of them being stale is the whole failure.
var imagesThatRunCommands = []string{"Dockerfile.build", "Dockerfile.local"}

func TestEveryCommandTheChartRunsIsInTheImage(t *testing.T) {
	root := repoRoot(t)

	// What the chart runs, read from the template TEXT rather than a render: a
	// command behind `{{- if .Values.x.enabled }}` is exactly the one that
	// drifts unnoticed, and a default render would not emit it.
	wanted := map[string][]string{}
	templates, err := filepath.Glob(filepath.Join(root, "deploy", "chart", "templates", "*.yaml"))
	if err != nil {
		t.Fatalf("listing templates: %v", err)
	}
	for _, template := range templates {
		body, err := os.ReadFile(template)
		if err != nil {
			t.Fatalf("reading %s: %v", template, err)
		}
		for _, match := range commandPath.FindAllStringSubmatch(string(body), -1) {
			name := filepath.Base(template)
			if !contains(wanted[match[1]], name) {
				wanted[match[1]] = append(wanted[match[1]], name)
			}
		}
	}

	if len(wanted) < 6 {
		t.Fatalf("found only %d commands across the chart's templates (%v), which is fewer "+
			"than this chart runs; the scan failed and this rule would pass vacuously",
			len(wanted), sortedCommandNames(wanted))
	}

	for _, dockerfile := range imagesThatRunCommands {
		body, err := os.ReadFile(filepath.Join(root, dockerfile))
		if err != nil {
			t.Fatalf("reading %s: %v", dockerfile, err)
		}
		copied := map[string]bool{}
		for _, match := range dockerfileCopy.FindAllStringSubmatch(string(body), -1) {
			copied[match[1]] = true
		}
		if len(copied) == 0 {
			t.Fatalf("%s copies nothing into /usr/local/concourse/bin; the COPY pattern no "+
				"longer matches this file and the rule below would pass vacuously", dockerfile)
		}

		var missing []string
		for command := range wanted {
			if !copied[command] {
				missing = append(missing, command)
			}
		}
		sort.Strings(missing)
		if len(missing) != 0 {
			for _, command := range missing {
				t.Errorf("%s does not put %s in the image, and %s runs it by absolute path.\n\n"+
					"The Pod does not fail to render, it fails to START, with "+
					"\"no such file or directory\" -- and helm lint, every other render test "+
					"and go vet are all satisfied, because nothing else in this repository "+
					"reads the Dockerfile and the chart together.",
					dockerfile, command, strings.Join(wanted[command], ", "))
			}
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, one := range haystack {
		if one == needle {
			return true
		}
	}

	return false
}

func sortedCommandNames(wanted map[string][]string) []string {
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}
