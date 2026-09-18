// Verify non-empty execution of every required manifest from structured events.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

func verify(input io.Reader, manifests []string) (map[string]int, error) {
	counts := map[string]int{}
	for _, name := range manifests {
		counts[name] = 0
	}
	if len(counts) == 0 {
		return nil, fmt.Errorf("no required manifests")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	ended := false
	for scanner.Scan() {
		var event struct {
			Type, Manifest, Name, Status string
			Failed                       int
		}
		// Diagnostics share this file, but only structured protocol records count.
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "scenario_end":
			if _, ok := counts[event.Manifest]; !ok {
				return nil, fmt.Errorf("scenario %q belongs to unexpected manifest %q", event.Name, event.Manifest)
			}
			if event.Status != "passed" {
				return nil, fmt.Errorf("scenario %q: %s", event.Name, event.Status)
			}
			counts[event.Manifest]++
		case "run_end":
			if event.Failed != 0 {
				return nil, fmt.Errorf("run reports %d failures", event.Failed)
			}
			ended = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !ended {
		return nil, fmt.Errorf("missing run_end record")
	}
	for name, count := range counts {
		if count == 0 {
			return nil, fmt.Errorf("required manifest %q executed no passing cases", name)
		}
	}
	return counts, nil
}
func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: brine-verify-run LOG MANIFEST...")
		os.Exit(2)
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	counts, err := verify(file, os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0
	for _, name := range names {
		fmt.Printf("%s: %d passed\n", name, counts[name])
		total += counts[name]
	}
	fmt.Printf("%d passed, 0 failed across %d manifests\n", total, len(counts))
}
