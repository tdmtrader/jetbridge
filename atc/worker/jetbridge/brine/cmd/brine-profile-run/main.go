// Command brine-profile-run says where a brine run's time went, from its
// structured event log.
//
// The full local+live gate takes over an hour in CI and the log that could
// explain why stays inside the task container. This prints, from that log,
// what a human needs to decide what to speed up: the time per manifest
// (tier), per feature file, and the slowest scenarios, with the share each
// holds of the whole. It never fails a run; brine-verify-run is the gate.
//
// Usage:
//
//	brine-profile-run [-top N] LOG
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type featureStart struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type scenarioEnd struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Manifest   string `json:"manifest"`
}

type stepEnd struct {
	Keyword    string `json:"keyword"`
	Text       string `json:"text"`
	DurationMs int64  `json:"duration_ms"`
}

// stepTiming is one step execution, keyed by its phrase so the same step
// across many scenarios adds up: that is where a fixture's fixed cost shows.
type stepTiming struct {
	phrase   string
	duration time.Duration
}

// timing is one scenario with the feature it ran under. scenario_end carries
// no feature, so the profiler attributes each one to the most recent
// feature_start, which is how the runner orders them.
type timing struct {
	feature  string
	manifest string
	name     string
	status   string
	duration time.Duration
}

type bucket struct {
	name  string
	count int
	total time.Duration
}

func main() {
	top := 25
	arguments := os.Args[1:]
	for len(arguments) > 0 && strings.HasPrefix(arguments[0], "-") {
		if arguments[0] != "-top" && arguments[0] != "--top" {
			fmt.Fprintf(os.Stderr, "unknown flag %q\nusage: brine-profile-run [-top N] LOG\n", arguments[0])
			os.Exit(2)
		}
		if len(arguments) < 2 {
			fmt.Fprintln(os.Stderr, "-top needs a count")
			os.Exit(2)
		}
		n, err := strconv.Atoi(arguments[1])
		if err != nil || n < 1 {
			fmt.Fprintf(os.Stderr, "-top needs a positive count, got %q\n", arguments[1])
			os.Exit(2)
		}
		top = n
		arguments = arguments[2:]
	}
	if len(arguments) != 1 {
		fmt.Fprintln(os.Stderr, "usage: brine-profile-run [-top N] LOG")
		os.Exit(2)
	}
	file, err := os.Open(arguments[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	timings, steps, err := collect(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report(os.Stdout, timings, steps, top)
}

// collect reads every scenario_end out of the log, attributing each to the
// feature_start that preceded it. Lines that are not protocol records (the
// fixtures' diagnostics share the file) are skipped.
func collect(input io.Reader) ([]timing, []stepTiming, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	feature := "(before any feature)"
	var timings []timing
	var steps []stepTiming
	for scanner.Scan() {
		line := scanner.Bytes()
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		switch head.Type {
		case "feature_start":
			var event featureStart
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, nil, fmt.Errorf("feature_start will not decode: %v: %s", err, excerpt(line))
			}
			feature = event.Source
			if feature == "" {
				feature = event.Name
			}
		case "step_end":
			var event stepEnd
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, nil, fmt.Errorf("step_end will not decode: %v: %s", err, excerpt(line))
			}
			steps = append(steps, stepTiming{
				phrase:   strings.TrimSpace(event.Keyword + " " + event.Text),
				duration: time.Duration(event.DurationMs) * time.Millisecond,
			})
		case "scenario_end":
			var event scenarioEnd
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, nil, fmt.Errorf("scenario_end will not decode: %v: %s", err, excerpt(line))
			}
			timings = append(timings, timing{
				feature:  feature,
				manifest: event.Manifest,
				name:     event.Name,
				status:   event.Status,
				duration: time.Duration(event.DurationMs) * time.Millisecond,
			})
		}
	}
	return timings, steps, scanner.Err()
}

func report(out io.Writer, timings []timing, steps []stepTiming, top int) {
	if len(timings) == 0 {
		fmt.Fprintln(out, "no scenario_end records: nothing ran, or the log is not brine's")
		return
	}
	var total time.Duration
	for _, t := range timings {
		total += t.duration
	}
	fmt.Fprintf(out, "%d scenarios, %s summed scenario time (%s mean)\n\n",
		len(timings), total.Round(time.Second), (total / time.Duration(len(timings))).Round(time.Millisecond))

	fmt.Fprintln(out, "By manifest (tier):")
	printBuckets(out, group(timings, func(t timing) string { return t.manifest }), total, 0)

	fmt.Fprintln(out, "\nBy feature, slowest first:")
	printBuckets(out, group(timings, func(t timing) string { return t.feature }), total, top)

	if len(steps) > 0 {
		// Steps run inside scenarios, so this sums to the scenario total
		// less whatever the runner spends between steps (resource
		// acquisition, disposer drains); that remainder is reported too,
		// because a large one means the cost is in the fixtures' setup and
		// teardown, not in any step.
		var inSteps time.Duration
		for _, s := range steps {
			inSteps += s.duration
		}
		fmt.Fprintf(out, "\nBy step phrase, slowest first (%s in steps, %s outside steps):\n",
			inSteps.Round(time.Second), (total - inSteps).Round(time.Second))
		byPhrase := map[string]*bucket{}
		var phrases []bucket
		for _, s := range steps {
			b, ok := byPhrase[s.phrase]
			if !ok {
				b = &bucket{name: s.phrase}
				byPhrase[s.phrase] = b
			}
			b.count++
			b.total += s.duration
		}
		for _, b := range byPhrase {
			phrases = append(phrases, *b)
		}
		sort.SliceStable(phrases, func(i, j int) bool { return phrases[i].total > phrases[j].total })
		printBuckets(out, phrases, total, top)
	}

	fmt.Fprintf(out, "\nSlowest %d scenarios:\n", top)
	sort.SliceStable(timings, func(i, j int) bool { return timings[i].duration > timings[j].duration })
	for i, t := range timings {
		if i == top {
			break
		}
		status := ""
		if t.status != "passed" {
			status = " [" + t.status + "]"
		}
		fmt.Fprintf(out, "  %8s %5.1f%%  %s: %s%s\n",
			t.duration.Round(time.Millisecond), share(t.duration, total), shortFeature(t.feature), t.name, status)
	}
}

func group(timings []timing, key func(timing) string) []bucket {
	index := map[string]int{}
	var buckets []bucket
	for _, t := range timings {
		k := key(t)
		i, ok := index[k]
		if !ok {
			i = len(buckets)
			index[k] = i
			buckets = append(buckets, bucket{name: k})
		}
		buckets[i].count++
		buckets[i].total += t.duration
	}
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].total > buckets[j].total })
	return buckets
}

// printBuckets prints at most limit buckets (0 = all), each with its share
// of the whole and its mean per scenario.
func printBuckets(out io.Writer, buckets []bucket, total time.Duration, limit int) {
	for i, b := range buckets {
		if limit > 0 && i == limit {
			fmt.Fprintf(out, "  ... %d more\n", len(buckets)-limit)
			break
		}
		fmt.Fprintf(out, "  %8s %5.1f%%  %4d scenarios  %8s mean  %s\n",
			b.total.Round(time.Second), share(b.total, total), b.count,
			(b.total / time.Duration(b.count)).Round(time.Millisecond), shortFeature(b.name))
	}
}

func share(part, whole time.Duration) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

// shortFeature trims a feature source path to what distinguishes it: the
// path from `features/` on, or the whole string if it has no such segment.
func shortFeature(source string) string {
	if i := strings.LastIndex(source, "features/"); i >= 0 {
		return source[i:]
	}
	return source
}

func excerpt(line []byte) string {
	const limit = 200
	if len(line) > limit {
		return string(line[:limit]) + "..."
	}
	return string(line)
}
