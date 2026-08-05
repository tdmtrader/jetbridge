package outputbuilder

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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
	// IntrinsicMetadata is the sealed snapshot's server-derived metadata,
	// forwarded verbatim from the authority - see InputAuthority.
	// IntrinsicMetadata. A type with none (or an authority built before this
	// field existed) simply omits it.
	IntrinsicMetadata json.RawMessage `json:"intrinsic_metadata,omitempty"`
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
	roots         map[string]*os.Root
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
	if canonicalizer.MaxEntries == 0 {
		canonicalizer.MaxEntries = snapshot.DefaultMaxSnapshotEntries
	}
	if canonicalizer.MaxContentBytes == 0 {
		canonicalizer.MaxContentBytes = snapshot.DefaultMaxSnapshotContentBytes
	}
	if err := snapshot.ValidateTempDir(canonicalizer.TempDir); err != nil {
		return nil, fmt.Errorf("output builder: canonicalizer: %w", err)
	}
	builder := &Builder{authority: authority.Clone(), validators: validators, canonicalizer: canonicalizer, roots: map[string]*os.Root{}, locks: map[string]*sync.Mutex{}}
	work, err := os.OpenRoot(builder.authority.WorkRoot)
	if err != nil {
		return nil, fmt.Errorf("output builder: open work root: %w", err)
	}
	defer work.Close()
	for name, input := range builder.authority.Inputs {
		root, err := work.OpenRoot(path.Base(input.MountRoot))
		if err != nil {
			builder.closeRoots()
			return nil, fmt.Errorf("output builder: open input %q: %w", name, err)
		}
		builder.roots["input:"+name] = root
	}
	for name, output := range builder.authority.Outputs {
		root, err := work.OpenRoot(path.Base(output.MountRoot))
		if err != nil {
			builder.closeRoots()
			return nil, fmt.Errorf("output builder: open output %q: %w", name, err)
		}
		builder.roots["output:"+name] = root
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
		inputs = append(inputs, InputDescription{
			Name: name, Type: input.Ref.Type, Digest: input.Ref.Digest, Candidate: input.Candidate,
			IntrinsicMetadata: input.IntrinsicMetadata,
		})
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
	outputRoot := builder.roots["output:"+request.Output]
	stage, err := createStage(outputRoot)
	if err != nil {
		return ValidationReport{}, fmt.Errorf("output builder: stage output: %w", err)
	}
	defer outputRoot.RemoveAll(stage)
	stageRoot, err := outputRoot.OpenRoot(stage)
	if err != nil {
		return ValidationReport{}, err
	}
	if err := writeStage(ctx, outputRoot, stageRoot, record, request.Content, builder.canonicalizer.MaxContentBytes, builder.canonicalizer.MaxEntries); err != nil {
		_ = stageRoot.Close()
		if strings.Contains(err.Error(), "snapshot content limit") {
			return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
		}
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
	if err := commitStage(outputRoot, stage); err != nil {
		return ValidationReport{}, err
	}
	return ValidationReport{Valid: true}, nil
}

func (builder *Builder) ValidateOutput(ctx context.Context, output string) (ValidationReport, error) {
	if err := builder.check(ctx); err != nil {
		return ValidationReport{}, err
	}
	_, _, validator, err := builder.resolveOutput(output)
	if err != nil {
		return ValidationReport{}, err
	}
	root := builder.roots["output:"+output]
	_, validateErr := validator.AdmitForSeal(ctx, root, builder.validationContext())
	if err := validateErr; err != nil {
		if errors.Is(err, context.Canceled) {
			return ValidationReport{}, err
		}
		return ValidationReport{Valid: false, Errors: []string{err.Error()}}, nil
	}
	return ValidationReport{Valid: true}, nil
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
	context, _ := snapshot.NewValidationContext(inputs, builder.openInput)
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
	return nil
}

func (builder *Builder) closeRoots() {
	for _, root := range builder.roots {
		_ = root.Close()
	}
}

func createStage(root *os.Root) (string, error) {
	var random [16]byte
	for attempts := 0; attempts < 8; attempts++ {
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".concourse-output-stage-" + fmt.Sprintf("%x", random[:])
		if err := root.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("output builder: cannot create unique output stage")
}

func writeStage(ctx context.Context, sourceRoot, stage *os.Root, record []byte, content []ContentRequest, limit, entryLimit int64) error {
	if int64(len(record)) > limit {
		return fmt.Errorf("output builder: snapshot content limit of %d bytes exceeded", limit)
	}
	if err := stage.WriteFile("record.json", record, 0o600); err != nil {
		return err
	}
	used := int64(len(record))
	entries := map[string]struct{}{"record.json": {}}
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
		if !strings.HasPrefix(request.Destination, "content/") || request.Destination == "content/" {
			return fmt.Errorf("output builder: content destination must be beneath content/")
		}
		for current := request.Destination; current != "."; current = path.Dir(current) {
			entries[current] = struct{}{}
		}
		if int64(len(entries)) > entryLimit {
			return fmt.Errorf("output builder: snapshot entry limit of %d exceeded", entryLimit)
		}
		copied, err := copyRegularNoSymlink(ctx, sourceRoot, stage, request.Source, request.Destination, limit-used)
		if err != nil {
			return err
		}
		used += copied
	}
	return ctx.Err()
}

func safeRelative(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%q must be a clean relative POSIX path", name)
	}
	return nil
}
func copyRegularNoSymlink(ctx context.Context, sourceRoot, stage *os.Root, source, destination string, remaining int64) (int64, error) {
	for end := 1; end <= len(strings.Split(source, "/")); end++ {
		part := strings.Join(strings.Split(source, "/")[:end], "/")
		info, err := sourceRoot.Lstat(part)
		if err != nil {
			return 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("output builder: content source %q contains a symlink", source)
		}
	}
	info, err := sourceRoot.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("output builder: content source %q must be a regular file", source)
	}
	if info.Size() > remaining {
		return 0, fmt.Errorf("output builder: snapshot content limit exceeded while opening %q", source)
	}
	sourceFile, err := sourceRoot.Open(source)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close()
	if err := stage.MkdirAll(path.Dir(destination), 0o700); err != nil {
		return 0, err
	}
	destinationFile, err := stage.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(destinationFile, contextReader{ctx: ctx, reader: io.LimitReader(sourceFile, info.Size()+1)})
	closeErr := destinationFile.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return 0, err
	}
	if written != info.Size() {
		return 0, fmt.Errorf("output builder: content source %q changed while reading", source)
	}
	return written, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func commitStage(root *os.Root, stage string) error {
	backupRecord, backupContent := stage+"-old-record", stage+"-old-content"
	hadRecord, hadContent := false, false
	if _, err := root.Lstat("record.json"); err == nil {
		if err := root.Rename("record.json", backupRecord); err != nil {
			return err
		}
		hadRecord = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := root.Lstat("content"); err == nil {
		if err := root.Rename("content", backupContent); err != nil {
			if hadRecord {
				_ = root.Rename(backupRecord, "record.json")
			}
			return err
		}
		hadContent = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hasContent := false
	if _, err := root.Lstat(stage + "/content"); err == nil {
		hasContent = true
		if err := root.Rename(stage+"/content", "content"); err != nil {
			return rollback(root, backupRecord, backupContent, hadRecord, hadContent, false)
		}
	}
	if err := root.Rename(stage+"/record.json", "record.json"); err != nil {
		return rollback(root, backupRecord, backupContent, hadRecord, hadContent, hasContent)
	}
	if hadRecord {
		if err := root.RemoveAll(backupRecord); err != nil {
			return err
		}
	}
	if hadContent {
		if err := root.RemoveAll(backupContent); err != nil {
			return err
		}
	}
	return nil
}
func rollback(root *os.Root, record, content string, hadRecord, hadContent, newContent bool) error {
	var errs []error
	if newContent {
		errs = append(errs, root.RemoveAll("content"))
	}
	if hadContent {
		errs = append(errs, root.Rename(content, "content"))
	}
	if hadRecord {
		errs = append(errs, root.Rename(record, "record.json"))
	}
	return errors.Join(errs...)
}

func (builder *Builder) openInput(ctx context.Context, name string, ref snapshot.SnapshotRef) (io.ReadCloser, error) {
	input, found := builder.authority.Inputs[name]
	if !found || input.Ref != ref {
		return nil, fmt.Errorf("output builder: input %q is not an exact authority binding", name)
	}
	pipeReader, pipeWriter := io.Pipe()
	go func() { pipeWriter.CloseWithError(writeRootTar(ctx, builder.roots["input:"+name], pipeWriter)) }()
	tree, err := builder.canonicalizer.Capture(ctx, pipeReader)
	_ = pipeReader.Close()
	if err != nil {
		return nil, err
	}
	if tree.Digest != ref.Digest {
		_ = tree.Close()
		return nil, fmt.Errorf("output builder: input %q canonical digest does not match its authority", name)
	}
	file, err := os.Open(tree.ArchivePath)
	if err != nil {
		_ = tree.Close()
		return nil, err
	}
	return inputArchiveReader{File: file, tree: tree}, nil
}

type inputArchiveReader struct {
	*os.File
	tree *snapshot.CapturedTree
}

func (reader inputArchiveReader) Close() error {
	return errors.Join(reader.File.Close(), reader.tree.Close())
}
func writeRootTar(ctx context.Context, root *os.Root, destination *io.PipeWriter) error {
	writer := tar.NewWriter(destination)
	defer writer.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = strings.TrimPrefix(name, "./")
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, contextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		}
		return nil
	})
}
