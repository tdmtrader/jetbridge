// Command function-runner executes the deterministic version-3 workflow
// functions that run inside an ordinary task pod.
//
// Every mode reads its inputs and writes its outputs through task mounts named
// on the command line. It takes no configuration from a serialized environment
// blob, resolves no remote, and never writes a credential — anything that must
// touch the outside world is a publish_snapshot boundary owned by the web node.
//
// Modes:
//
//	merge-preflight  compute the prospective delivery merge and always emit a
//	                 sealed validation/v1 record describing it. Exits 0 whether
//	                 the merge is clean or conflicted, so the report is always
//	                 sealed and a human can see conflicts before approving.
//	merge-prepare    compute the same merge and emit the merged
//	                 repository-change/v1. Exits non-zero on conflict, because a
//	                 typed output only exists when the step succeeded.
//
// Further functions (gates, judge, repository validation) plug in as additional
// modes beside these.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	exitOK      = 0
	exitRejects = 1
	exitUsage   = 2

	validationType snapshot.TypeRef = "validation/v1"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "function-runner: a mode is required (merge-preflight, merge-prepare)")
		return exitUsage
	}
	mode := args[0]
	switch mode {
	case "merge-preflight", "merge-prepare":
		return runMergeMode(ctx, mode, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: function-runner <merge-preflight|merge-prepare> [flags]")
		return exitOK
	default:
		fmt.Fprintf(stderr, "function-runner: unknown mode %q\n", mode)
		return exitUsage
	}
}

type mergeOptions struct {
	root      string
	candidate string
	target    string
	base      string
	output    string
	method    string
	message   string
}

func runMergeMode(ctx context.Context, mode string, args []string, stdout, stderr io.Writer) int {
	options := mergeOptions{}
	flags := flag.NewFlagSet("function-runner "+mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.root, "root", ".", "directory the task mounts are relative to")
	flags.StringVar(&options.candidate, "candidate", "", "repository-change/v1 input mount, as `name` or name=path")
	flags.StringVar(&options.target, "target", "", "repository/v1 delivery-target input mount, as `name` or name=path")
	flags.StringVar(&options.base, "base", "", "repository/v1 mount holding the candidate's own base, as `name` or name=path")
	flags.StringVar(&options.output, "output", "", "output mount to materialize into, as `name` or name=path")
	flags.StringVar(&options.method, "method", string(repositorymerge.MethodMerge), "merge or squash")
	flags.StringVar(&options.message, "message", "Merge delivered change", "commit message for the merge commit")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "function-runner: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	conclusion, err := executeMerge(ctx, mode, options, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "function-runner: %s: %v\n", mode, err)
		return exitUsage
	}
	if mode == "merge-prepare" && conclusion != mergeConclusionPassed {
		// A conflicted or unevaluable merge has no merged change to seal. Fail
		// the step so no downstream approval or publication can proceed.
		fmt.Fprintf(stderr, "function-runner: %s: merge is %s; see the preflight report\n", mode, conclusion)
		return exitRejects
	}
	return exitOK
}

// mergeConclusionPassed is the one validation/v1 conclusion that means a merged
// change exists. Every other conclusion ("failed", "error", "incomplete") means
// there is nothing to seal.
const mergeConclusionPassed = "passed"

// executeMerge returns the merge report's derived conclusion.
func executeMerge(ctx context.Context, mode string, options mergeOptions, stdout io.Writer) (string, error) {
	for name, value := range map[string]string{
		"candidate": options.candidate, "target": options.target,
		"base": options.base, "output": options.output,
	} {
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("-%s is required", name)
		}
	}
	candidatePort, candidatePath, err := parseMount(options.root, options.candidate)
	if err != nil {
		return "", err
	}
	targetPort, targetPath, err := parseMount(options.root, options.target)
	if err != nil {
		return "", err
	}
	basePort, basePath, err := parseMount(options.root, options.base)
	if err != nil {
		return "", err
	}
	outputPort, outputPath, err := parseMount(options.root, options.output)
	if err != nil {
		return "", err
	}
	if candidatePort == targetPort || candidatePort == basePort {
		return "", fmt.Errorf("candidate, target, and base must be distinct input mounts")
	}

	canonicalizer := snapshot.Canonicalizer{}
	registry, err := contracts.NewRegistry(contracts.WithCanonicalizer(canonicalizer))
	if err != nil {
		return "", fmt.Errorf("build snapshot contract registry: %w", err)
	}
	runner, err := repositorymerge.NewRunner(registry)
	if err != nil {
		return "", err
	}

	bindings, err := bindInputs(ctx, canonicalizer, []mount{
		{port: basePort, path: basePath, typeRef: "repository/v1"},
		{port: targetPort, path: targetPath, typeRef: "repository/v1"},
		{port: candidatePort, path: candidatePath, typeRef: "repository-change/v1"},
	})
	if err != nil {
		return "", err
	}
	defer bindings.Close()

	merged, err := runner.WithCanonicalizer(canonicalizer).Merge(ctx, repositorymerge.Request{
		Candidate:       bindings.refs[candidatePort],
		CandidateRoot:   candidatePath,
		CandidateInput:  candidatePort,
		Target:          bindings.refs[targetPort],
		TargetInput:     targetPort,
		TargetRoot:      targetPath,
		Inputs:          bindings.refs,
		OpenInput:       bindings.Open,
		Method:          repositorymerge.Method(options.method),
		Message:         options.message,
		ReportAuthority: reportAuthority(mode, outputPort),
	})
	if err != nil {
		return "", err
	}
	defer merged.Close()

	fmt.Fprintf(stdout, "%s: %s\n", merged.Conclusion(), merged.Report.Body.Summary)
	for _, check := range merged.Report.Body.Checks {
		fmt.Fprintf(stdout, "  %s: %s %s\n", check.Status, check.Name, check.Detail)
	}

	switch mode {
	case "merge-preflight":
		if err := repositorymerge.WriteReport(ctx, outputPath, merged.Report); err != nil {
			return "", err
		}
	case "merge-prepare":
		if merged.Conclusion() != mergeConclusionPassed {
			return merged.Conclusion(), nil
		}
		if err := repositorymerge.WriteMergedChange(ctx, outputPath, merged); err != nil {
			return "", err
		}
	}
	return merged.Conclusion(), nil
}

// reportAuthority resolves the contract identity the merge report must advertise.
//
// In merge-preflight the output mount IS the validation/v1 port, so the platform
// published its declared identity as AGENT_OUTPUT_<PORT>_RECORD_TYPE and
// _RECORD_SCHEMA (atc/exec/record_authority_env.go) and those exact values are
// copied through. merge-prepare writes no report — its output port is the merged
// change, whose identity is a different type entirely — so there is nothing to
// copy and the in-memory report, which that mode only reads a conclusion from,
// carries this build's own identity. The same fallback covers a hand-run CLI.
func reportAuthority(mode, outputPort string) repositorymerge.RecordAuthority {
	if mode == "merge-preflight" {
		prefix := "AGENT_OUTPUT_" + authorityEnvPort(outputPort)
		declaredType := strings.TrimSpace(os.Getenv(prefix + "_RECORD_TYPE"))
		declaredSchema := strings.TrimSpace(os.Getenv(prefix + "_RECORD_SCHEMA"))
		if declaredType != "" && declaredSchema != "" {
			return repositorymerge.RecordAuthority{
				Type:   snapshot.TypeRef(declaredType),
				Schema: snapshot.Digest(declaredSchema),
			}
		}
	}
	schema, _ := contracts.SchemaDigestFor(validationType)
	return repositorymerge.RecordAuthority{Type: validationType, Schema: schema}
}

// authorityEnvPort mangles a port name into the environment-variable spelling the
// platform uses. It must stay identical to authorityEnvPort in
// atc/exec/record_authority_env.go, which is what produced the rows being read;
// duplicating eight lines keeps this pod-side binary from importing the web.
func authorityEnvPort(port string) string {
	return strings.Map(func(value rune) rune {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			return unicode.ToUpper(value)
		}
		return '_'
	}, port)
}

// parseMount resolves one task mount. The bare form names an artifact whose
// directory is the identically named child of root, which is exactly how
// Concourse lays task inputs and outputs out; the name=path form exists for
// callers that mount somewhere else.
func parseMount(root, value string) (string, string, error) {
	name, path, explicit := strings.Cut(value, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", fmt.Errorf("mount %q has no artifact name", value)
	}
	if strings.ContainsAny(name, "/\\") {
		return "", "", fmt.Errorf("mount name %q must be a bare artifact name", name)
	}
	if !explicit {
		path = filepath.Join(root, name)
	}
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("mount %q has no path", value)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve mount %q: %w", value, err)
	}
	return name, resolved, nil
}

type mount struct {
	port    string
	path    string
	typeRef snapshot.TypeRef
}

// boundInputs holds one content-derived reference per input mount.
//
// A task pod is never told the durable snapshot identities of its inputs, so
// the digest here is re-derived from the materialized bytes and the numeric ID
// is a local ordinal. The digest is the half that carries meaning: it is what a
// snapshot validator compares an opened input against. The platform re-runs the
// authoritative validation with true identities when it seals the step's typed
// outputs, so nothing downstream trusts these ordinals.
type boundInputs struct {
	refs     map[string]snapshot.SnapshotRef
	archives map[string]string
	trees    []*snapshot.CapturedTree
}

func (bound *boundInputs) Close() error {
	var err error
	for _, tree := range bound.trees {
		err = errors.Join(err, tree.Close())
	}
	return err
}

func (bound *boundInputs) Open(ctx context.Context, name string, _ snapshot.SnapshotRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, found := bound.archives[name]
	if !found {
		return nil, fmt.Errorf("input %q is not mounted", name)
	}
	return os.Open(path)
}

func bindInputs(ctx context.Context, canonicalizer snapshot.Canonicalizer, mounts []mount) (*boundInputs, error) {
	sorted := append([]mount(nil), mounts...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].port < sorted[right].port })

	bound := &boundInputs{
		refs:     make(map[string]snapshot.SnapshotRef, len(sorted)),
		archives: make(map[string]string, len(sorted)),
	}
	for index, entry := range sorted {
		tree, err := repositorymerge.CaptureDirectory(ctx, canonicalizer, entry.path)
		if err != nil {
			_ = bound.Close()
			return nil, fmt.Errorf("canonicalize input %q: %w", entry.port, err)
		}
		bound.trees = append(bound.trees, tree)
		id, err := snapshot.NewSnapshotID(int64(index + 1))
		if err != nil {
			_ = bound.Close()
			return nil, err
		}
		bound.refs[entry.port] = snapshot.SnapshotRef{ID: id, Type: entry.typeRef, Digest: tree.Digest}
		bound.archives[entry.port] = tree.ArchivePath
	}
	return bound, nil
}
