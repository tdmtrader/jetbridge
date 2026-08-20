package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/hangar"
)

const (
	HangarReadyLabel                = "concourse.dev/hangar-v1"
	defaultHangarControlBytes int64 = 1 << 20
)

type HangarService struct {
	Store           hangar.Store
	Canonicalizer   hangar.Canonicalizer
	Materializer    *hangar.Materializer
	GrantVerifier   *hangar.GrantVerifier
	MaxContentBytes int64
	MaxEntries      int64
	MaxArchiveBytes int64
	MaxControlBytes int64
}

type hangarOptions struct {
	Enabled         bool
	ScratchDir      string
	CapabilityKey   string
	MaxContentBytes int64
	MaxEntries      int64
	DurableKind     string
	Bucket          string
	Prefix          string
	Endpoint        string
	Timeout         time.Duration
	TLSCert         string
	TLSKey          string
	TLSCACert       string
}

func validateDaemonLabelKeys(legacyLabelKey string) error {
	if legacyLabelKey == HangarReadyLabel {
		return fmt.Errorf("--label-key must not collide with %s", HangarReadyLabel)
	}
	return nil
}

func prepareDaemonLabels(ctx context.Context, hangarLabeler, legacyLabeler *NodeLabeler) error {
	if hangarLabeler != nil {
		if err := hangarLabeler.RemoveLabel(ctx); err != nil {
			return fmt.Errorf("clear stale Hangar readiness: %w", err)
		}
	}
	if legacyLabeler != nil {
		if err := legacyLabeler.AddLabel(ctx); err != nil {
			return fmt.Errorf("add legacy readiness: %w", err)
		}
	}
	return nil
}

func listenAndAdvertiseHangar(ctx context.Context, address string, labeler *NodeLabeler, listen func(string, string) (net.Listener, error)) (net.Listener, error) {
	listener, err := listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if labeler != nil {
		if err := labeler.AddLabel(ctx); err != nil {
			return nil, errors.Join(err, listener.Close())
		}
	}
	return listener, nil
}

func cleanupDaemonServices(ctx context.Context, hangarLabeler, legacyLabeler *NodeLabeler, shutdown, closeHangar func() error) error {
	var errs []error
	if hangarLabeler != nil {
		errs = append(errs, hangarLabeler.RemoveLabel(ctx))
	}
	if legacyLabeler != nil {
		errs = append(errs, legacyLabeler.RemoveLabel(ctx))
	}
	if shutdown != nil {
		errs = append(errs, shutdown())
	}
	if closeHangar != nil {
		errs = append(errs, closeHangar())
	}
	return errors.Join(errs...)
}

func validateHangarOptions(opts hangarOptions, storagePath string) error {
	if !opts.Enabled {
		return nil
	}
	if opts.TLSCert == "" || opts.TLSKey == "" || opts.TLSCACert == "" {
		return fmt.Errorf("Hangar requires --tls-cert, --tls-key, and --tls-ca-cert")
	}
	if opts.DurableKind != "gcs" {
		return fmt.Errorf("Hangar requires --durable-store=gcs")
	}
	if opts.Bucket == "" {
		return fmt.Errorf("Hangar requires --durable-bucket")
	}
	if !filepath.IsAbs(opts.ScratchDir) {
		return fmt.Errorf("--hangar-scratch-dir must be absolute")
	}
	if opts.MaxContentBytes <= 0 || opts.MaxEntries <= 0 {
		return fmt.Errorf("Hangar content and entry limits must be positive")
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("Hangar store timeout must be positive")
	}
	if err := validatePrivateHangarScratch(opts.ScratchDir, storagePath); err != nil {
		return err
	}
	key, err := os.ReadFile(opts.CapabilityKey)
	if err != nil {
		return fmt.Errorf("read --hangar-capability-key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("--hangar-capability-key must contain exactly 32 raw bytes")
	}
	return nil
}

func buildHangarService(ctx context.Context, logger lager.Logger, storagePath string, opts hangarOptions) (*HangarService, func() error, error) {
	if !opts.Enabled {
		return nil, func() error { return nil }, nil
	}
	if err := validateHangarOptions(opts, storagePath); err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(opts.CapabilityKey)
	if err != nil {
		return nil, nil, fmt.Errorf("read --hangar-capability-key: %w", err)
	}
	if len(key) != 32 {
		return nil, nil, fmt.Errorf("--hangar-capability-key must contain exactly 32 raw bytes")
	}
	verifier, err := hangar.NewGrantVerifier(key, hangar.MaxGrantTTL, nil)
	if err != nil {
		return nil, nil, err
	}
	archiveLimit, err := hangar.CanonicalArchiveByteLimit(opts.MaxContentBytes, opts.MaxEntries)
	if err != nil {
		return nil, nil, err
	}
	client, err := hangar.NewStorageClient(ctx, opts.Endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("create Hangar GCS client: %w", err)
	}
	closeClient := func() error { return client.Close() }
	store, err := hangar.NewGCSStore(client, hangar.GCSConfig{
		Bucket: opts.Bucket, Prefix: opts.Prefix, ScratchDir: opts.ScratchDir,
		ReadTimeout: opts.Timeout, WriteTimeout: opts.Timeout,
	})
	if err != nil {
		_ = closeClient()
		return nil, nil, err
	}
	validationCtx, cancelValidation := context.WithTimeout(ctx, opts.Timeout)
	_, validationErr := client.Bucket(opts.Bucket).Attrs(validationCtx)
	cancelValidation()
	if validationErr != nil {
		_ = closeClient()
		return nil, nil, fmt.Errorf("validate Hangar GCS bucket: %w", validationErr)
	}
	canonicalizer := hangar.Canonicalizer{TempDir: opts.ScratchDir, MaxContentBytes: opts.MaxContentBytes, MaxEntries: opts.MaxEntries}
	service := &HangarService{
		Store: store, Canonicalizer: canonicalizer, GrantVerifier: verifier,
		Materializer:    &hangar.Materializer{Store: store, Canonicalizer: canonicalizer, StoragePath: storagePath, MaxTreeBytes: archiveLimit},
		MaxContentBytes: opts.MaxContentBytes, MaxEntries: opts.MaxEntries, MaxArchiveBytes: archiveLimit, MaxControlBytes: defaultHangarControlBytes,
	}
	logger.Info("hangar-store-validated")
	return service, closeClient, nil
}

func validatePrivateHangarScratch(scratch, storage string) error {
	cleanScratch := filepath.Clean(scratch)
	cleanStorage := filepath.Clean(storage)
	if err := validateHangarScratchPaths(cleanScratch, cleanStorage); err != nil {
		return err
	}
	if err := os.MkdirAll(cleanScratch, 0700); err != nil {
		return fmt.Errorf("create Hangar scratch directory: %w", err)
	}
	info, err := os.Lstat(cleanScratch)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Hangar scratch directory must be a real directory")
	}
	resolvedScratch, err := filepath.EvalSymlinks(cleanScratch)
	if err != nil {
		return fmt.Errorf("resolve Hangar scratch directory: %w", err)
	}
	resolvedStorage, err := filepath.EvalSymlinks(cleanStorage)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resolve artifact storage directory: %w", err)
	}
	if err == nil {
		if err := validateHangarScratchPaths(resolvedScratch, resolvedStorage); err != nil {
			return err
		}
	}
	if err := hangar.ValidateTempDir(resolvedScratch); err != nil {
		return err
	}
	if err := os.Chmod(cleanScratch, 0700); err != nil {
		return fmt.Errorf("set Hangar scratch directory permissions: %w", err)
	}
	info, err = os.Lstat(cleanScratch)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("Hangar scratch directory must be a private 0700 directory")
	}
	return nil
}

func validateHangarScratchPaths(scratch, storage string) error {
	if !filepath.IsAbs(scratch) || !filepath.IsAbs(storage) {
		return fmt.Errorf("Hangar scratch and artifact storage paths must be absolute")
	}
	scratch = filepath.Clean(scratch)
	storage = filepath.Clean(storage)
	if scratch == string(filepath.Separator) || storage == string(filepath.Separator) || filepath.VolumeName(scratch) == scratch || filepath.VolumeName(storage) == storage {
		return fmt.Errorf("Hangar scratch and artifact storage paths must not be filesystem roots")
	}
	contains := func(parent, child string) (bool, error) {
		rel, err := filepath.Rel(parent, child)
		if err != nil {
			return false, err
		}
		return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
	}
	scratchContainsStorage, err := contains(scratch, storage)
	if err != nil {
		return fmt.Errorf("compare Hangar scratch and artifact storage: %w", err)
	}
	storageContainsScratch, err := contains(storage, scratch)
	if err != nil {
		return fmt.Errorf("compare artifact storage and Hangar scratch: %w", err)
	}
	if scratchContainsStorage || storageContainsScratch {
		return fmt.Errorf("--hangar-scratch-dir must be outside artifact storage")
	}
	return nil
}
