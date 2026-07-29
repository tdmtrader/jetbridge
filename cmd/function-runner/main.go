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
//	judge            derive a deterministic measurements/v1 record from one
//	                 sealed review/v1 candidate. It starts no model and consults
//	                 no clock, which is what lets an experiment evaluator built
//	                 on it be effect-free, zero-dollar, and reproducible.
//	merge-preflight  compute the prospective delivery merge and always emit a
//	                 sealed validation/v1 record describing it. Exits 0 whether
//	                 the merge is clean or conflicted, so the report is always
//	                 sealed and a human can see conflicts before approving.
//	merge-prepare    compute the same merge and emit the merged
//	                 repository-change/v1. Exits non-zero on conflict, because a
//	                 typed output only exists when the step succeeded.
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

	"github.com/concourse/concourse/agent/functions/judge"
	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	exitOK      = 0
	exitRejects = 1
	exitUsage   = 2

	validationType   snapshot.TypeRef = "validation/v1"
	measurementsType snapshot.TypeRef = "measurements/v1"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "function-runner: a mode is required (judge, merge-preflight, merge-prepare, dev-validate)")
		return exitUsage
	}
	mode := args[0]
	switch mode {
	case "judge":
		return runJudgeMode(ctx, args[1:], stdout, stderr)
	case "merge-preflight", "merge-prepare":
		return runMergeMode(ctx, mode, args[1:], stdout, stderr)
	case "dev-validate":
		return runDevValidate(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: function-runner <judge|merge-preflight|merge-prepare|dev-validate> [flags]")
		return exitOK
	default:
		fmt.Fprintf(stderr, "function-runner: unknown mode %q\n", mode)
		return exitUsage
	}
}

type judgeOptions struct {
	root      string
	candidate string
	output    string
}

func runJudgeMode(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options := judgeOptions{}
	flags := flag.NewFlagSet("function-runner judge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&options.root, "root", ".", "directory the task mounts are relative to")
	flags.StringVar(&options.candidate, "candidate", "", "review/v1 input mount to measure, as `name` or name=path")
	flags.StringVar(&options.output, "output", "", "measurements/v1 output mount to materialize into, as `name` or name=path")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "function-runner: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if err := executeJudge(ctx, options, stdout); err != nil {
		fmt.Fprintf(stderr, "function-runner: judge: %v\n", err)
		return exitUsage
	}
	return exitOK
}

// executeJudge measures one sealed review and seals the measurements. There is
// no partial outcome to report: unlike a merge, which legitimately concludes
// "conflicted", a candidate that cannot be measured is one that failed its own
// declared contract, so the step fails rather than sealing zeros a scorecard
// would read as a real observation.
func executeJudge(ctx context.Context, options judgeOptions, stdout io.Writer) error {
	for name, value := range map[string]string{"candidate": options.candidate, "output": options.output} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("-%s is required", name)
		}
	}
	candidatePort, candidatePath, err := parseMount(options.root, options.candidate)
	if err != nil {
		return err
	}
	outputPort, outputPath, err := parseMount(options.root, options.output)
	if err != nil {
		return err
	}
	if candidatePort == outputPort {
		return fmt.Errorf("candidate and output must be distinct mounts")
	}
	candidate, err := declaredInput(candidatePort)
	if err != nil {
		return err
	}
	record, err := judge.Measure(ctx, judge.Request{
		Candidate:             candidate,
		CandidateInput:        candidatePort,
		CandidateRoot:         candidatePath,
		MeasurementsAuthority: measurementsAuthority(outputPort),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %d metrics\n", record.Body.Conclusion, len(record.Body.Metrics))
	for _, metric := range record.Body.Metrics {
		fmt.Fprintf(stdout, "  %s = %g %s (%s)\n", metric.ID, metric.Value, metric.Unit, metric.Direction)
	}
	return judge.WriteMeasurements(ctx, outputPath, record)
}

// declaredInput resolves the contract identity the PLATFORM declared for one
// typed input mount, published as AGENT_INPUT_<PORT>_SNAPSHOT_TYPE and
// _SNAPSHOT_DIGEST (atc/exec/record_authority_env.go).
//
// Those two values are copied verbatim into the produced record's subject, for
// the same reason the output record authority is copied: the web node re-binds
// every subject against its own view of the step when it seals the output, so a
// pod that re-derived either half would be advertising an identity the sealing
// side never declared. There is deliberately NO fallback — unlike a schema
// digest, a snapshot digest has no compiled default that could be honest, and a
// judge run outside a typed workflow step has nothing to bind its measurements
// to. The snapshot ID below is a local ordinal: a pod is never told the durable
// identity, and nothing downstream reads it.
func declaredInput(port string) (snapshot.SnapshotRef, error) {
	prefix := "AGENT_INPUT_" + authorityEnvPort(port)
	declaredType := strings.TrimSpace(os.Getenv(prefix + "_SNAPSHOT_TYPE"))
	declaredDigest := strings.TrimSpace(os.Getenv(prefix + "_SNAPSHOT_DIGEST"))
	if declaredType == "" || declaredDigest == "" {
		return snapshot.SnapshotRef{}, fmt.Errorf(
			"input mount %q has no declared snapshot identity; %s_SNAPSHOT_TYPE and %s_SNAPSHOT_DIGEST are set by the platform for every typed input, so this mode only runs as a workflow task node with input_types declared",
			port, prefix, prefix,
		)
	}
	id, err := snapshot.NewSnapshotID(1)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	ref := snapshot.SnapshotRef{
		ID:     id,
		Type:   snapshot.TypeRef(declaredType),
		Digest: snapshot.Digest(declaredDigest),
	}
	if err := ref.Validate(); err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("input mount %q declared snapshot identity: %w", port, err)
	}
	return ref, nil
}

// measurementsAuthority resolves the contract identity the measurements record
// must advertise, with the same precedence rule reportAuthority uses: the
// platform's declaration wins, and this build's compiled identity is only a
// hand-run fallback.
func measurementsAuthority(outputPort string) judge.RecordAuthority {
	prefix := "AGENT_OUTPUT_" + authorityEnvPort(outputPort)
	declaredType := strings.TrimSpace(os.Getenv(prefix + "_RECORD_TYPE"))
	declaredSchema := strings.TrimSpace(os.Getenv(prefix + "_RECORD_SCHEMA"))
	if declaredType != "" && declaredSchema != "" {
		return judge.RecordAuthority{
			Type:   snapshot.TypeRef(declaredType),
			Schema: snapshot.Digest(declaredSchema),
		}
	}
	schema, _ := contracts.SchemaDigestFor(measurementsType)
	return judge.RecordAuthority{Type: measurementsType, Schema: schema}
}

type mergeOptions struct {
	root                 string
	candidate            string
	target               string
	base                 string
	output               string
	method               string
	message              string
	profileDigest        string
	configDigest         string
	capabilityImage      string
	workflowDefinitionID int
	workflowVersion      int
	toolchain            string
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
	flags.StringVar(&options.method, "method", string(repositorymerge.MethodRebase), "merge, squash, or rebase")
	flags.StringVar(&options.message, "message", "Merge delivered change", "commit message for the merge commit")
	flags.StringVar(&options.profileDigest, "profile-digest", "", "server-owned fixed merge policy digest")
	flags.StringVar(&options.configDigest, "config-digest", "", "server-owned fixed merge configuration digest")
	flags.StringVar(&options.capabilityImage, "capability-image", "", "server-owned pinned runner image")
	flags.IntVar(&options.workflowDefinitionID, "workflow-definition-id", 0, "authenticated workflow definition identity")
	flags.IntVar(&options.workflowVersion, "workflow-version", 0, "authenticated workflow version")
	flags.StringVar(&options.toolchain, "toolchain", "", "server-owned fixed merge toolchain")
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
		Base:            bindings.refs[basePort],
		BaseInput:       basePort,
		Target:          bindings.refs[targetPort],
		TargetInput:     targetPort,
		TargetRoot:      targetPath,
		Inputs:          bindings.refs,
		OpenInput:       bindings.Open,
		Method:          repositorymerge.Method(options.method),
		Message:         options.message,
		ReportAuthority: reportAuthority(mode, outputPort, options),
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
func reportAuthority(mode, outputPort string, options mergeOptions) repositorymerge.RecordAuthority {
	image := "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64)
	profile := snapshot.Digest("sha256:" + strings.Repeat("b", 64))
	config := snapshot.Digest("sha256:" + strings.Repeat("c", 64))
	imageDigest := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	authority := repositorymerge.RecordAuthority{
		ProfileDigest: profile, ProtectedConfigDigest: config,
		CapabilityImage: image, CapabilityImageDigest: imageDigest,
		WorkflowDefinitionID: 1, WorkflowVersion: 1,
		Toolchain: "function-runner/merge-preflight/v1",
	}
	if mode == "merge-preflight" && options.profileDigest != "" {
		authority.ProfileDigest = snapshot.Digest(options.profileDigest)
		authority.ProtectedConfigDigest = snapshot.Digest(options.configDigest)
		authority.CapabilityImage = options.capabilityImage
		if at := strings.LastIndexByte(options.capabilityImage, '@'); at >= 0 {
			authority.CapabilityImageDigest = snapshot.Digest(options.capabilityImage[at+1:])
		}
		authority.WorkflowDefinitionID, authority.WorkflowVersion, authority.Toolchain = options.workflowDefinitionID, options.workflowVersion, options.toolchain
	}
	if mode == "merge-preflight" {
		prefix := "AGENT_OUTPUT_" + authorityEnvPort(outputPort)
		declaredType := strings.TrimSpace(os.Getenv(prefix + "_RECORD_TYPE"))
		declaredSchema := strings.TrimSpace(os.Getenv(prefix + "_RECORD_SCHEMA"))
		if declaredType != "" && declaredSchema != "" {
			authority.Type = snapshot.TypeRef(declaredType)
			authority.Schema = snapshot.Digest(declaredSchema)
			return authority
		}
	}
	schema, _ := contracts.SchemaDigestFor(validationType)
	authority.Type, authority.Schema = validationType, schema
	return authority
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
