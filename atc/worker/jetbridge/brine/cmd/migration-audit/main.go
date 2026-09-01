package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

type manifest struct {
	Name           string         `json:"name"`
	TestPackage    string         `json:"test_package"`
	SourceTestFile string         `json:"source_test_file"`
	Feature        string         `json:"feature"`
	StepFiles      []string       `json:"step_files"`
	Cases          []mutationCase `json:"cases"`
}

type mutationCase struct {
	ID             string   `json:"id"`
	SourceFile     string   `json:"source_file"`
	Old            string   `json:"old"`
	New            string   `json:"new"`
	Tests          []string `json:"tests"`
	Scenarios      []string `json:"scenarios"`
	FailureContain string   `json:"failure_contains"`
}

type resultFile struct {
	Manifest      string       `json:"manifest"`
	GitHead       string       `json:"git_head"`
	CompletedAt   string       `json:"completed_at"`
	SourceTests   int          `json:"source_tests"`
	Philosophy    string       `json:"philosophy"`
	MutationCases []caseResult `json:"mutation_cases"`
}

type caseResult struct {
	ID               string   `json:"id"`
	MutationSHA256   string   `json:"mutation_sha256"`
	FailedSourceTest []string `json:"failed_source_tests"`
	FailedScenarios  []string `json:"failed_brine_scenarios"`
}

type ginkgoReport struct {
	SpecReports []struct {
		ContainerHierarchyTexts []string `json:"ContainerHierarchyTexts"`
		LeafNodeText            string   `json:"LeafNodeText"`
		State                   string   `json:"State"`
	} `json:"SpecReports"`
}

var prohibited = []*regexp.Regexp{
	regexp.MustCompile(`k8s\.io/client-go/kubernetes/fake`),
	regexp.MustCompile(`\bhttptest\b`),
	regexp.MustCompile(`\blagertest\b`),
	regexp.MustCompile(`\bruntimetest\b`),
	regexp.MustCompile(`\bProcessStub\b`),
	regexp.MustCompile(`(?i)\btype\s+\w*(stub|fake|fault|sink)\w*\b`),
}

func main() {
	var manifestPath string
	var resultPath string
	flag.StringVar(&manifestPath, "manifest", "", "path to a mutation-pair manifest")
	flag.StringVar(&resultPath, "results", "", "path to write verified JSON results")
	flag.Parse()
	if manifestPath == "" || resultPath == "" {
		fatalf("both -manifest and -results are required")
	}

	repo := commandOutput("", nil, "git", "rev-parse", "--show-toplevel")
	manifestAbs := absolute(repo, manifestPath)
	var m manifest
	readJSON(manifestAbs, &m)
	validateManifest(repo, m)
	checkPhilosophy(repo, m.StepFiles)

	brineDir := filepath.Join(repo, "atc/worker/jetbridge/brine")
	brineBinary := findBinary("brine", "/home/dev/brine-private/target/debug/brine")
	ginkgoBinary := findBinary("ginkgo", "/home/dev/.local/bin/ginkgo")
	gitHead := commandOutput(repo, nil, "git", "rev-parse", "HEAD")

	results := resultFile{
		Manifest:    relative(repo, manifestAbs),
		GitHead:     gitHead,
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		Philosophy:  "no-stub-no-sink-no-injected-fault-no-fake-no-mock",
	}
	seenTests := map[string]bool{}
	allowedScenarios := map[string]bool{}
	for _, mutation := range m.Cases {
		for _, scenario := range mutation.Scenarios {
			if allowedScenarios[scenario] {
				fatalf("scenario %q is claimed by more than one mutation", scenario)
			}
			allowedScenarios[scenario] = true
		}
	}

	for index, mutation := range m.Cases {
		fmt.Printf("[%d/%d] %s\n", index+1, len(m.Cases), mutation.ID)
		caseResult, err := runCase(repo, brineDir, brineBinary, ginkgoBinary, m, mutation, allowedScenarios)
		if err != nil {
			fatalf("%s: %v", mutation.ID, err)
		}
		for _, test := range mutation.Tests {
			if seenTests[test] {
				fatalf("%s: source test %q is claimed by more than one mutation", mutation.ID, test)
			}
			seenTests[test] = true
		}
		results.MutationCases = append(results.MutationCases, caseResult)
	}

	results.SourceTests = len(seenTests)
	writeJSON(absolute(repo, resultPath), results)
	fmt.Printf("PASS: %d individually paired source tests; results written to %s\n", results.SourceTests, resultPath)
}

func validateManifest(repo string, m manifest) {
	if m.Name == "" || m.TestPackage == "" || m.SourceTestFile == "" || m.Feature == "" || len(m.Cases) == 0 {
		fatalf("manifest is missing a required top-level field")
	}
	for _, path := range append([]string{m.SourceTestFile, m.Feature}, m.StepFiles...) {
		if _, err := os.Stat(absolute(repo, path)); err != nil {
			fatalf("manifest path %s: %v", path, err)
		}
	}
	seen := map[string]bool{}
	for _, mutation := range m.Cases {
		if mutation.ID == "" || mutation.SourceFile == "" || mutation.Old == "" || mutation.Old == mutation.New || len(mutation.Tests) == 0 || len(mutation.Scenarios) == 0 {
			fatalf("mutation case %q is incomplete", mutation.ID)
		}
		if len(mutation.Tests) != len(mutation.Scenarios) {
			fatalf("%s: tests (%d) and scenarios (%d) must pair one-to-one", mutation.ID, len(mutation.Tests), len(mutation.Scenarios))
		}
		if seen[mutation.ID] {
			fatalf("duplicate mutation id %q", mutation.ID)
		}
		seen[mutation.ID] = true
	}
}

func checkPhilosophy(repo string, files []string) {
	if len(files) == 0 {
		fatalf("philosophy check matched zero step files")
	}
	for _, path := range files {
		data, err := os.ReadFile(absolute(repo, path))
		if err != nil {
			fatalf("read philosophy input %s: %v", path, err)
		}
		for _, pattern := range prohibited {
			if pattern.Match(data) {
				fatalf("%s violates the philosophy guard: %s", path, pattern)
			}
		}
	}
}

func runCase(repo, brineDir, brineBinary, ginkgoBinary string, m manifest, mutation mutationCase, allowedScenarios map[string]bool) (caseResult, error) {
	temp, err := os.MkdirTemp("", "brine-mutation-"+safeName(mutation.ID)+"-")
	if err != nil {
		return caseResult{}, err
	}
	defer os.RemoveAll(temp)

	sourcePath := absolute(repo, mutation.SourceFile)
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return caseResult{}, err
	}
	if count := bytes.Count(source, []byte(mutation.Old)); count != 1 {
		return caseResult{}, fmt.Errorf("mutation anchor occurs %d times in %s, want exactly 1", count, mutation.SourceFile)
	}
	mutant := bytes.Replace(source, []byte(mutation.Old), []byte(mutation.New), 1)
	mutantPath := filepath.Join(temp, filepath.Base(sourcePath))
	if err := os.WriteFile(mutantPath, mutant, 0o600); err != nil {
		return caseResult{}, err
	}
	overlayPath := filepath.Join(temp, "overlay.json")
	writeJSON(overlayPath, map[string]any{"Replace": map[string]string{sourcePath: mutantPath}})

	testReport := filepath.Join(temp, "ginkgo.json")
	focus := exactAlternation(mutation.Tests)
	testEnv := append(os.Environ(), "GOFLAGS=-overlay="+overlayPath)
	var testOutput string
	var testErr error
	lockErr := withFileLock("/tmp/concourse-migration-audit-ginkgo.lock", func() {
		testOutput, testErr = run(repo, testEnv, ginkgoBinary,
			"--focus-file=^"+regexp.QuoteMeta(absolute(repo, m.SourceTestFile))+"$",
			"--focus="+focus,
			"--fail-on-empty",
			"--json-report="+testReport,
			m.TestPackage,
		)
	})
	if lockErr != nil {
		return caseResult{}, fmt.Errorf("serialize Ginkgo invocation: %w", lockErr)
	}
	if testErr == nil {
		return caseResult{}, fmt.Errorf("source tests stayed green under mutation\n%s", tail(testOutput, 30))
	}
	failedTests, err := failedGinkgoTests(testReport)
	if err != nil {
		return caseResult{}, err
	}
	if err := equalSets(failedTests, mutation.Tests); err != nil {
		return caseResult{}, fmt.Errorf("source failure attribution: %w\n%s", err, tail(testOutput, 40))
	}

	adapter := filepath.Join(temp, "brine-adapter-jetbridge")
	buildOutput, buildErr := run(brineDir, nil, "go", "build", "-overlay="+overlayPath, "-o", adapter, "./cmd/brine-adapter-jetbridge")
	if buildErr != nil {
		return caseResult{}, fmt.Errorf("build mutant adapter: %w\n%s", buildErr, tail(buildOutput, 50))
	}
	featureDir := filepath.Join(temp, "features")
	if err := os.Mkdir(featureDir, 0o700); err != nil {
		return caseResult{}, err
	}
	featureSource := absolute(repo, m.Feature)
	featureTarget := filepath.Join(featureDir, filepath.Base(featureSource))
	featureBytes, err := os.ReadFile(featureSource)
	if err != nil {
		return caseResult{}, err
	}
	if err := os.WriteFile(featureTarget, featureBytes, 0o600); err != nil {
		return caseResult{}, err
	}
	config := fmt.Sprintf("runner:\n  name: jetbridge\n  binary: %s\n  contract: 3\ncontract: 3\nfeatures: \"features/*.feature\"\nbudgets:\n  undefined: 0\n", adapter)
	if err := os.WriteFile(filepath.Join(temp, ".brine"), []byte(config), 0o600); err != nil {
		return caseResult{}, err
	}
	brineOutput, brineErr := run(temp, nil, brineBinary, "run", "--mode", "sync", filepath.Join("features", filepath.Base(featureTarget)))
	if brineErr == nil {
		return caseResult{}, fmt.Errorf("Brine stayed green under mutation\n%s", tail(brineOutput, 40))
	}
	failedScenarios, errorsByScenario, runEnded, err := failedBrineScenarios(brineOutput)
	if err != nil {
		return caseResult{}, err
	}
	if !runEnded {
		return caseResult{}, errors.New("Brine did not emit a terminal run_end event")
	}
	if err := expectedAndKnown(failedScenarios, mutation.Scenarios, allowedScenarios); err != nil {
		return caseResult{}, fmt.Errorf("Brine failure attribution: %w\n%s", err, tail(brineOutput, 60))
	}
	if mutation.FailureContain != "" {
		for _, scenario := range mutation.Scenarios {
			if !strings.Contains(errorsByScenario[scenario], mutation.FailureContain) {
				return caseResult{}, fmt.Errorf("scenario %q error %q does not contain %q", scenario, errorsByScenario[scenario], mutation.FailureContain)
			}
		}
	}

	hash := sha256.Sum256(mutant)
	return caseResult{
		ID:               mutation.ID,
		MutationSHA256:   hex.EncodeToString(hash[:]),
		FailedSourceTest: append([]string(nil), failedTests...),
		FailedScenarios:  append([]string(nil), failedScenarios...),
	}, nil
}

func failedGinkgoTests(path string) ([]string, error) {
	var reports []ginkgoReport
	if err := readJSONErr(path, &reports); err != nil {
		return nil, fmt.Errorf("read Ginkgo report: %w", err)
	}
	var failed []string
	for _, suite := range reports {
		for _, spec := range suite.SpecReports {
			if spec.State == "failed" || spec.State == "panicked" || spec.State == "timedout" {
				parts := append(append([]string(nil), spec.ContainerHierarchyTexts...), spec.LeafNodeText)
				failed = append(failed, strings.Join(parts, " "))
			}
		}
	}
	sort.Strings(failed)
	return failed, nil
}

func failedBrineScenarios(output string) ([]string, map[string]string, bool, error) {
	var failed []string
	errorsByScenario := map[string]string{}
	runEnded := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, nil, false, fmt.Errorf("decode Brine event: %w", err)
		}
		if event.Type == "scenario_end" && event.Status == "failed" {
			failed = append(failed, event.Name)
			errorsByScenario[event.Name] = event.Error
		}
		if event.Type == "run_end" {
			runEnded = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, false, err
	}
	sort.Strings(failed)
	return failed, errorsByScenario, runEnded, nil
}

func exactAlternation(values []string) string {
	escaped := make([]string, len(values))
	for i, value := range values {
		escaped[i] = regexp.QuoteMeta(value)
	}
	// Ginkgo matches -focus against "<suite description> <spec text>".  The
	// manifest stores the exact spec text reported by its JSON output, without
	// that suite-description prefix, so require an exact suffix here.
	return `(?:^| )(?:` + strings.Join(escaped, "|") + `)$`
}

func equalSets(actual, expected []string) error {
	a := append([]string(nil), actual...)
	e := append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(e)
	if len(a) != len(e) {
		return fmt.Errorf("got %d failures %q, want %d %q", len(a), a, len(e), e)
	}
	for i := range a {
		if a[i] != e[i] {
			return fmt.Errorf("got failures %q, want %q", a, e)
		}
	}
	return nil
}

func expectedAndKnown(actual, expected []string, allowed map[string]bool) error {
	actualSet := map[string]bool{}
	for _, name := range actual {
		actualSet[name] = true
		if !allowed[name] {
			return fmt.Errorf("unclaimed scenario %q also failed; actual failures %q", name, actual)
		}
	}
	for _, name := range expected {
		if !actualSet[name] {
			return fmt.Errorf("expected scenario %q did not fail; actual failures %q", name, actual)
		}
	}
	return nil
}

func run(dir string, env []string, name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Dir = dir
	if env != nil {
		command.Env = env
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.String(), err
}

func withFileLock(path string, fn func()) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	fn()
	return nil
}

func commandOutput(dir string, env []string, name string, args ...string) string {
	output, err := run(dir, env, name, args...)
	if err != nil {
		fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func findBinary(name, fallback string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if info, err := os.Stat(fallback); err == nil && !info.IsDir() {
		return fallback
	}
	fatalf("required binary %s was not found", name)
	return ""
}

func readJSON(path string, target any) {
	if err := readJSONErr(path, target); err != nil {
		fatalf("read %s: %v", path, err)
	}
}

func readJSONErr(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeJSON(path string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatalf("encode %s: %v", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fatalf("write %s: %v", path, err)
	}
}

func absolute(repo, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(repo, filepath.Clean(path))
}

func relative(repo, path string) string {
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return path
	}
	return rel
}

func safeName(value string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(value, "-")
}

func tail(value string, lines int) string {
	parts := strings.Split(strings.TrimSpace(value), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "migration-audit: "+format+"\n", args...)
	os.Exit(1)
}
