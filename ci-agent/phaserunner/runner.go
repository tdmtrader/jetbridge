package phaserunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/concourse/ci-agent/llm"
	"github.com/concourse/ci-agent/phaseconfig"
	"github.com/concourse/ci-agent/provenance"
	"github.com/concourse/ci-agent/schema"
	citracing "github.com/concourse/ci-agent/tracing"
)

// Options configures a phase run.
type Options struct {
	ConfigPath string
	Config     *phaseconfig.Config
	ConfigData []byte // raw YAML for hashing
	OutputDir  string
	Model      string
	Client     llm.Client
	BaseDir    string // base directory for resolving relative template paths
}

// StepResult captures the output of a single step.
type StepResult struct {
	Name     string          `json:"name"`
	Output   json.RawMessage `json:"output"`
	Verified *bool           `json:"verified,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Run executes a phase: for each step, render prompt, call LLM, write artifacts, optionally verify.
func Run(ctx context.Context, opts Options) (*schema.Results, error) {
	ctx, phaseSpan := citracing.Tracer().Start(ctx, "phase.run",
	)
	defer phaseSpan.End()
	phaseSpan.SetAttributes(attribute.String("phase.name", opts.Config.Name))
	if opts.Model != "" {
		phaseSpan.SetAttributes(attribute.String("phase.model", opts.Model))
	}

	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	// Event writer
	eventsPath := filepath.Join(opts.OutputDir, "events.ndjson")
	eventsFile, err := os.Create(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("create events file: %w", err)
	}
	defer eventsFile.Close()
	ew := schema.NewEventWriter(eventsFile)

	emitEvent(ew, schema.EventAgentStart, map[string]string{"phase": opts.Config.Name})

	// Resolve env vars
	env, err := opts.Config.ResolveEnv()
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("env resolution: %v", err))
	}

	// Run steps
	stepOutputsRaw := make(map[string]json.RawMessage)
	stepOutputsStr := make(map[string]string)
	var stepResults []StepResult
	allPassed := true

	for _, step := range opts.Config.Steps {
		stepCtx, stepSpan := citracing.Tracer().Start(ctx, "phase.step",
		)
		stepSpan.SetAttributes(attribute.String("step.name", step.Name))

		emitEvent(ew, "step.start", map[string]string{"step": step.Name})

		// Render prompt template
		prompt, err := RenderTemplate(opts.BaseDir, step.Template, TemplateData{
			Env:         env,
			StepOutputs: stepOutputsStr,
		})
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.SetStatus(codes.Error, err.Error())
			stepSpan.End()
			emitEvent(ew, schema.EventError, map[string]string{"step": step.Name, "error": err.Error()})
			stepResults = append(stepResults, StepResult{Name: step.Name, Error: err.Error()})
			allPassed = false
			continue
		}

		// Call LLM
		cr, err := opts.Client.Call(stepCtx, prompt, llm.CallOpts{
			Model: opts.Model,
			Dir:   env["repo_dir"],
		})
		if err != nil {
			stepSpan.RecordError(err)
			stepSpan.SetStatus(codes.Error, err.Error())
			stepSpan.End()
			emitEvent(ew, schema.EventError, map[string]string{"step": step.Name, "error": err.Error()})
			stepResults = append(stepResults, StepResult{Name: step.Name, Error: err.Error()})
			allPassed = false
			continue
		}

		output := cr.Result
		stepOutputsRaw[step.Name] = output
		stepOutputsStr[step.Name] = string(output)

		// Write artifacts
		for _, art := range step.Artifacts {
			artPath := filepath.Join(opts.OutputDir, art.Path)
			if err := writeArtifact(artPath, stepOutputsRaw[step.Name], art); err != nil {
				emitEvent(ew, schema.EventError, map[string]string{"step": step.Name, "error": err.Error()})
			} else {
				emitEvent(ew, schema.EventArtifactWritten, map[string]string{"name": art.Name, "path": art.Path})
			}
		}

		// Verify
		sr := StepResult{Name: step.Name, Output: output}
		if step.VerifyCmd != "" {
			passed := runVerify(stepCtx, step.VerifyCmd, env)
			sr.Verified = &passed
			if !passed {
				allPassed = false
				stepSpan.SetAttributes(attribute.Bool("step.verified", false))
			} else {
				stepSpan.SetAttributes(attribute.Bool("step.verified", true))
			}
		}

		stepResults = append(stepResults, sr)
		emitEvent(ew, "step.end", map[string]string{"step": step.Name})
		stepSpan.End()
	}

	// Build provenance
	var prov *provenance.Record
	if opts.ConfigPath != "" {
		promptPaths := collectTemplatePaths(opts.BaseDir, opts.Config.Steps)
		prov, _ = provenance.Build(opts.ConfigPath, promptPaths, opts.Model, opts.Config.MCP)
	}

	// Build results
	status := schema.StatusPass
	if !allPassed {
		status = schema.StatusFail
	}

	artifacts := collectArtifacts(opts.Config.Steps)
	artifacts = append(artifacts, schema.Artifact{
		Name: "events", Path: "events.ndjson", MediaType: "application/x-ndjson",
	})

	metadata := map[string]string{
		"phase": opts.Config.Name,
	}
	if prov != nil {
		metadata["config_hash"] = prov.PhaseConfig.Hash
		if prov.Model != "" {
			metadata["model"] = prov.Model
		}
	}

	results := &schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1.0,
		Summary:       fmt.Sprintf("Phase %s: %s", opts.Config.Name, status),
		Artifacts:     artifacts,
		Metadata:      metadata,
	}

	// Write results.json
	resultsData, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile(filepath.Join(opts.OutputDir, "results.json"), resultsData, 0644)

	// Write provenance.json
	if prov != nil {
		provData, _ := json.MarshalIndent(prov, "", "  ")
		os.WriteFile(filepath.Join(opts.OutputDir, "provenance.json"), provData, 0644)
	}

	// Write step-results.json for detailed inspection
	stepData, _ := json.MarshalIndent(stepResults, "", "  ")
	os.WriteFile(filepath.Join(opts.OutputDir, "step-results.json"), stepData, 0644)

	emitEvent(ew, schema.EventAgentEnd, map[string]string{"status": string(status)})

	phaseSpan.SetAttributes(
		attribute.String("phase.status", string(status)),
		attribute.Int("phase.steps", len(opts.Config.Steps)),
	)

	return results, nil
}

// TemplateData is the data available to prompt templates.
type TemplateData struct {
	Env         map[string]string
	StepOutputs map[string]string
}

// RenderTemplate loads and renders a Go text/template from a file.
func RenderTemplate(baseDir, templatePath string, data TemplateData) (string, error) {
	fullPath := templatePath
	if baseDir != "" && !filepath.IsAbs(templatePath) {
		fullPath = filepath.Join(baseDir, templatePath)
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", fullPath, err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).
		Option("missingkey=zero").
		Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", templatePath, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %s: %w", templatePath, err)
	}

	return buf.String(), nil
}

func writeArtifact(path string, output json.RawMessage, art phaseconfig.Artifact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// For markdown artifacts, try to extract a specific field from the JSON output
	if strings.HasSuffix(art.Path, ".md") {
		var m map[string]interface{}
		if json.Unmarshal(output, &m) == nil {
			// Try common field names for markdown content
			for _, key := range []string{art.Name + "_markdown", art.Name, "markdown", "content"} {
				if v, ok := m[key]; ok {
					if s, ok := v.(string); ok {
						return os.WriteFile(path, []byte(s), 0644)
					}
				}
			}
		}
	}

	// Default: write the raw JSON output
	return os.WriteFile(path, []byte(output), 0644)
}

func runVerify(ctx context.Context, cmdStr string, env map[string]string) bool {
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)

	// Export system env vars plus resolved phase env vars to the child shell.
	// The shell handles parameter expansion (e.g., ${VAR:-default}) natively.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if dir, ok := env["repo_dir"]; ok && dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run() == nil
}

func collectTemplatePaths(baseDir string, steps []phaseconfig.Step) []string {
	var paths []string
	for _, s := range steps {
		p := s.Template
		if baseDir != "" && !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

func collectArtifacts(steps []phaseconfig.Step) []schema.Artifact {
	var artifacts []schema.Artifact
	for _, s := range steps {
		for _, a := range s.Artifacts {
			artifacts = append(artifacts, schema.Artifact{
				Name:      a.Name,
				Path:      a.Path,
				MediaType: a.MediaType,
			})
		}
	}
	return artifacts
}

func emitEvent(ew *schema.EventWriter, eventType schema.EventType, data interface{}) {
	raw, _ := json.Marshal(data)
	ew.Write(schema.Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		EventType: eventType,
		Data:      raw,
	})
}

func errorResult(outputDir string, ew *schema.EventWriter, msg string) (*schema.Results, error) {
	emitEvent(ew, schema.EventError, map[string]string{"error": msg})
	emitEvent(ew, schema.EventAgentEnd, map[string]string{"status": "error"})

	results := &schema.Results{
		SchemaVersion: "1.0",
		Status:        schema.StatusError,
		Confidence:    0.0,
		Summary:       msg,
		Artifacts:     []schema.Artifact{{Name: "events", Path: "events.ndjson", MediaType: "application/x-ndjson"}},
	}

	resultsData, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile(filepath.Join(outputDir, "results.json"), resultsData, 0644)

	return results, nil
}
