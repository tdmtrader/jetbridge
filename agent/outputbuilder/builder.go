package outputbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type SubjectRequest struct {
	ID    string                `json:"id"`
	Role  contracts.SubjectRole `json:"role"`
	Input string                `json:"input"`
}
type ContentRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}
type WriteRequest struct {
	Output   string           `json:"output"`
	Subjects []SubjectRequest `json:"subjects"`
	Body     json.RawMessage  `json:"body"`
	Content  []ContentRequest `json:"content,omitempty"`
}
type InputDescription struct {
	Name      string           `json:"name"`
	Type      snapshot.TypeRef `json:"type"`
	Digest    snapshot.Digest  `json:"digest"`
	Candidate bool             `json:"candidate"`
}
type Description struct {
	Port   snapshot.Port            `json:"port"`
	Schema contracts.SchemaDocument `json:"schema"`
	Inputs []InputDescription       `json:"inputs"`
}
type ValidationReport struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

type Builder struct {
	authority     NodeAuthority
	validators    snapshot.ValidatorRegistry
	canonicalizer snapshot.Canonicalizer
	bound         map[string]os.FileInfo
	locks         map[string]*sync.Mutex
}

func New(authority NodeAuthority, validators snapshot.ValidatorRegistry, canonicalizer snapshot.Canonicalizer) (*Builder, error) {
	if validators == nil {
		return nil, fmt.Errorf("output builder: validator registry is required")
	}
	if err := authority.Validate(); err != nil {
		return nil, fmt.Errorf("output builder: authority: %w", err)
	}
	if canonicalizer.MaxEntries < 0 || canonicalizer.MaxContentBytes < 0 {
		return nil, fmt.Errorf("output builder: canonicalizer limits must not be negative")
	}
	builder := &Builder{authority: authority.Clone(), validators: validators, canonicalizer: canonicalizer, bound: map[string]os.FileInfo{}, locks: map[string]*sync.Mutex{}}
	for name, input := range builder.authority.Inputs {
		info, err := os.Lstat(input.MountRoot)
		if err != nil {
			return nil, err
		}
		builder.bound["input:"+name] = info
	}
	for name, output := range builder.authority.Outputs {
		info, err := os.Lstat(output.MountRoot)
		if err != nil {
			return nil, err
		}
		builder.bound["output:"+name] = info
		builder.locks[name] = new(sync.Mutex)
	}
	return builder, nil
}

func (builder *Builder) DescribeOutput(ctx context.Context, output string) (Description, error) {
	if err := builder.check(ctx); err != nil {
		return Description{}, err
	}
	declaration, found := builder.authority.Outputs[output]
	if !found {
		return Description{}, fmt.Errorf("output builder: unknown output %q", output)
	}
	if _, found := contracts.BuiltinRawRecordCodec(declaration.Port.Type); !found {
		return Description{}, fmt.Errorf("output builder: output %q is not a built-in record output", output)
	}
	schema, found := contracts.SchemaDocumentFor(declaration.Port.Type)
	if !found {
		return Description{}, fmt.Errorf("output builder: output %q has no schema", output)
	}
	inputs := make([]InputDescription, 0, len(builder.authority.Inputs))
	for _, name := range sortedNames(builder.authority.Inputs) {
		input := builder.authority.Inputs[name]
		inputs = append(inputs, InputDescription{Name: name, Type: input.Ref.Type, Digest: input.Ref.Digest, Candidate: input.Candidate})
	}
	return Description{Port: declaration.Port, Schema: schema, Inputs: inputs}, nil
}

func (builder *Builder) WriteOutput(ctx context.Context, request WriteRequest) (ValidationReport, error) {
	if err := builder.check(ctx); err != nil {
		return ValidationReport{}, err
	}
	output, codec, validator, err := builder.resolveOutput(request.Output)
	if err != nil {
		return ValidationReport{}, err
	}
	subjects, err := builder.subjects(request.Subjects)
	if err != nil {
		return ValidationReport{}, err
	}
	body, _, err := contracts.NormalizeRawRecordBody(output.Port.Type, subjects, request.Body)
	if err != nil {
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	record, err := codec.EncodeRecord(subjects, body)
	if err != nil {
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	lock := builder.locks[request.Output]
	lock.Lock()
	defer lock.Unlock()
	if err := builder.check(ctx); err != nil {
		return ValidationReport{}, err
	}
	stage, err := os.MkdirTemp(output.MountRoot, ".concourse-output-stage-")
	if err != nil {
		return ValidationReport{}, fmt.Errorf("output builder: stage output: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := writeStage(ctx, output.MountRoot, stage, record, request.Content); err != nil {
		return ValidationReport{}, err
	}
	if err := builder.enforceLimits(ctx, stage); err != nil {
		if errors.Is(err, context.Canceled) {
			return ValidationReport{}, err
		}
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		return ValidationReport{}, err
	}
	_, validationErr := validator.AdmitForSeal(ctx, stageRoot, builder.validationContext())
	closeErr := stageRoot.Close()
	if err := errors.Join(validationErr, closeErr); err != nil {
		if errors.Is(err, context.Canceled) {
			return ValidationReport{}, err
		}
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	if err := ctx.Err(); err != nil {
		return ValidationReport{}, err
	}
	if err := commitStage(stage, output.MountRoot, len(request.Content) > 0); err != nil {
		return ValidationReport{}, err
	}
	return ValidationReport{Valid: true}, nil
}

func (builder *Builder) ValidateOutput(ctx context.Context, output string) (ValidationReport, error) {
	if err := builder.check(ctx); err != nil {
		return ValidationReport{}, err
	}
	declaration, _, validator, err := builder.resolveOutput(output)
	if err != nil {
		return ValidationReport{}, err
	}
	root, err := os.OpenRoot(declaration.MountRoot)
	if err != nil {
		return ValidationReport{}, err
	}
	_, validateErr := validator.AdmitForSeal(ctx, root, builder.validationContext())
	closeErr := root.Close()
	if err := errors.Join(validateErr, closeErr); err != nil {
		if errors.Is(err, context.Canceled) {
			return ValidationReport{}, err
		}
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	return ValidationReport{Valid: true}, nil
}

func (builder *Builder) enforceLimits(ctx context.Context, root string) error {
	var entries, bytes int64
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == root {
			return nil
		}
		entries++
		if builder.canonicalizer.MaxEntries > 0 && entries > builder.canonicalizer.MaxEntries {
			return fmt.Errorf("output builder: snapshot entry limit of %d exceeded", builder.canonicalizer.MaxEntries)
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
			if builder.canonicalizer.MaxContentBytes > 0 && bytes > builder.canonicalizer.MaxContentBytes {
				return fmt.Errorf("output builder: snapshot content limit of %d bytes exceeded", builder.canonicalizer.MaxContentBytes)
			}
		}
		return nil
	})
}

func (builder *Builder) resolveOutput(name string) (OutputAuthority, contracts.RawRecordCodec, snapshot.Validator, error) {
	output, found := builder.authority.Outputs[name]
	if !found {
		return OutputAuthority{}, nil, nil, fmt.Errorf("output builder: unknown output %q", name)
	}
	codec, found := contracts.BuiltinRawRecordCodec(output.Port.Type)
	if !found {
		return OutputAuthority{}, nil, nil, fmt.Errorf("output builder: output %q is not a built-in record output", name)
	}
	validator, err := builder.validators.Lookup(output.Port.Type)
	if err != nil || validator == nil {
		if err == nil {
			err = fmt.Errorf("validator returned nil")
		}
		return OutputAuthority{}, nil, nil, fmt.Errorf("output builder: output %q validator: %w", name, err)
	}
	return output, codec, validator, nil
}

func (builder *Builder) subjects(requests []SubjectRequest) ([]contracts.Subject, error) {
	subjects := make([]contracts.Subject, 0, len(requests))
	for _, request := range requests {
		input, found := builder.authority.Inputs[request.Input]
		if !found {
			return nil, fmt.Errorf("output builder: subject %q uses undeclared input %q", request.ID, request.Input)
		}
		subjects = append(subjects, contracts.SubjectFromInput(request.ID, request.Role, request.Input, input.Ref))
	}
	return subjects, nil
}

func (builder *Builder) validationContext() snapshot.ValidationContext {
	inputs := make(map[string]snapshot.SnapshotRef, len(builder.authority.Inputs))
	for name, input := range builder.authority.Inputs {
		inputs[name] = input.Ref
	}
	context, _ := snapshot.NewValidationContext(inputs, nil)
	return context
}

func (builder *Builder) check(ctx context.Context) error {
	if builder == nil {
		return fmt.Errorf("output builder: builder is required")
	}
	if ctx == nil {
		return fmt.Errorf("output builder: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := builder.authority.Validate(); err != nil {
		return fmt.Errorf("output builder: authority changed: %w", err)
	}
	for key, initial := range builder.bound {
		parts := strings.SplitN(key, ":", 2)
		var root string
		if parts[0] == "input" {
			root = builder.authority.Inputs[parts[1]].MountRoot
		} else {
			root = builder.authority.Outputs[parts[1]].MountRoot
		}
		current, err := os.Lstat(root)
		if err != nil || !os.SameFile(initial, current) {
			return fmt.Errorf("output builder: authority mount %q changed after binding", root)
		}
	}
	return nil
}

func writeStage(ctx context.Context, sourceRoot, stage string, record []byte, content []ContentRequest) error {
	if err := os.WriteFile(filepath.Join(stage, "record.json"), record, 0o600); err != nil {
		return err
	}
	for _, request := range content {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safeRelative(request.Source); err != nil {
			return fmt.Errorf("output builder: content source: %w", err)
		}
		if err := safeRelative(request.Destination); err != nil {
			return fmt.Errorf("output builder: content destination: %w", err)
		}
		if !strings.HasPrefix(request.Destination, "content/") {
			return fmt.Errorf("output builder: content destination must be beneath content/")
		}
		data, err := readRegularNoSymlink(sourceRoot, request.Source)
		if err != nil {
			return err
		}
		destination := filepath.Join(stage, filepath.FromSlash(request.Destination))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func safeRelative(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%q must be a clean relative POSIX path", name)
	}
	return nil
}
func readRegularNoSymlink(root, name string) ([]byte, error) {
	current := root
	for _, element := range strings.Split(name, "/") {
		current = filepath.Join(current, element)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("output builder: content source %q contains a symlink", name)
		}
	}
	info, err := os.Lstat(current)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("output builder: content source %q must be a regular file", name)
	}
	return os.ReadFile(current)
}
func commitStage(stage, output string, hasContent bool) error {
	if err := os.Rename(filepath.Join(stage, "record.json"), filepath.Join(output, "record.json")); err != nil {
		return fmt.Errorf("output builder: commit record: %w", err)
	}
	if hasContent {
		target := filepath.Join(output, "content")
		_ = os.RemoveAll(target)
		if err := os.Rename(filepath.Join(stage, "content"), target); err != nil {
			return fmt.Errorf("output builder: commit content: %w", err)
		}
	}
	return nil
}
