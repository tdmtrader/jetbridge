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
	"github.com/concourse/concourse/hangar/diskclient"
	hangargcs "github.com/concourse/concourse/hangar/gcsstore"
	"github.com/concourse/concourse/hangar/treestore"
)

const (
	HangarReadyLabel                = "concourse.dev/hangar-v1"
	defaultHangarControlBytes int64 = 1 << 20
	daemonLabelCleanupTimeout       = 10 * time.Second
)

type HangarService struct {
	Store           hangar.Store
	Canonicalizer   hangar.Canonicalizer
	Materializer    *hangar.Materializer
	WarrantVerifier *hangar.WarrantVerifier
	MaxContentBytes int64
	MaxEntries      int64
	MaxArchiveBytes int64
	MaxControlBytes int64
}

type hangarOptions struct {
	Enabled         bool
	ScratchDir      string
	WarrantKey      string
	MaxContentBytes int64
	MaxEntries      int64
	WarrantTTL      time.Duration
	DurableKind     string
	Store           string
	StoreID         string
	TokenFile       string
	CACert          string
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

func prepareDaemonLabels(ctx context.Context, legacyLabelKey string, hangarLabeler, legacyLabeler *NodeLabeler) error {
	if hangarLabeler != nil {
		if err := hangarLabeler.RemoveLabel(ctx); err != nil {
			return errors.Join(fmt.Errorf("clear stale Hangar readiness: %w", err), cleanupFailedDaemonLabelPreparation(hangarLabeler, legacyLabeler))
		}
	}
	if err := validateDaemonLabelKeys(legacyLabelKey); err != nil {
		return errors.Join(err, cleanupFailedDaemonLabelPreparation(hangarLabeler, legacyLabeler))
	}
	if legacyLabeler != nil {
		if err := legacyLabeler.AddLabel(ctx); err != nil {
			return errors.Join(fmt.Errorf("add legacy readiness: %w", err), cleanupFailedDaemonLabelPreparation(hangarLabeler, legacyLabeler))
		}
	}
	return nil
}

func cleanupFailedDaemonLabelPreparation(hangarLabeler, legacyLabeler *NodeLabeler) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonLabelCleanupTimeout)
	defer cancel()
	return cleanupDaemonServices(ctx, hangarLabeler, legacyLabeler, nil, nil)
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
	if opts.Store == "" {
		opts.Store = opts.DurableKind
	}
	if opts.Store != "gcs" && opts.Store != "disk" {
		return fmt.Errorf("Hangar requires --hangar-store=gcs or disk (legacy --durable-store=gcs)")
	}
	if opts.Store == "disk" && (opts.StoreID == "" || opts.Endpoint == "" || opts.TokenFile == "") {
		return fmt.Errorf("disk Hangar requires --hangar-store-id, --hangar-endpoint and --hangar-token-file")
	}
	if opts.Bucket == "" {
		return fmt.Errorf("Hangar requires --hangar-bucket (or legacy --durable-bucket)")
	}
	if !filepath.IsAbs(opts.ScratchDir) {
		return fmt.Errorf("--hangar-scratch-dir must be absolute")
	}
	if opts.MaxContentBytes <= 0 || opts.MaxEntries <= 0 {
		return fmt.Errorf("Hangar content and entry limits must be positive")
	}
	if opts.WarrantTTL <= 0 || opts.WarrantTTL > hangar.MaxWarrantTTL {
		return fmt.Errorf("--hangar-warrant-ttl must be positive and no greater than %s", hangar.MaxWarrantTTL)
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("Hangar store timeout must be positive")
	}
	if err := validatePrivateHangarScratch(opts.ScratchDir, storagePath); err != nil {
		return err
	}
	key, err := os.ReadFile(opts.WarrantKey)
	if err != nil {
		return fmt.Errorf("read --hangar-warrant-key: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("--hangar-warrant-key must contain exactly 32 raw bytes")
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
	key, err := os.ReadFile(opts.WarrantKey)
	if err != nil {
		return nil, nil, fmt.Errorf("read --hangar-warrant-key: %w", err)
	}
	if len(key) != 32 {
		return nil, nil, fmt.Errorf("--hangar-warrant-key must contain exactly 32 raw bytes")
	}
	verifier, err := hangar.NewWarrantVerifier(key, opts.WarrantTTL, nil)
	if err != nil {
		return nil, nil, err
	}
	archiveLimit, err := hangar.CanonicalArchiveByteLimit(opts.MaxContentBytes, opts.MaxEntries)
	if err != nil {
		return nil, nil, err
	}
	var store hangar.Store
	closeClient := func() error { return nil }
	if opts.Store == "" {
		opts.Store = opts.DurableKind
	}
	if opts.Store == "disk" {
		objects, openErr := diskclient.New(diskclient.Config{Endpoint: opts.Endpoint, StoreID: opts.StoreID, TokenFile: opts.TokenFile, CACert: opts.CACert, Timeout: opts.Timeout})
		if openErr != nil {
			return nil, nil, openErr
		}
		store, err = treestore.New(objects, treestore.Config{Bucket: opts.Bucket, Prefix: opts.Prefix, ScratchDir: opts.ScratchDir, ReadTimeout: opts.Timeout, WriteTimeout: opts.Timeout})
	} else {
		var gcsStore *hangargcs.GCSStore
		gcsStore, closeClient, err = hangargcs.NewGCSStore(ctx, opts.Endpoint, hangargcs.GCSConfig{Bucket: opts.Bucket, Prefix: opts.Prefix, ScratchDir: opts.ScratchDir, ReadTimeout: opts.Timeout, WriteTimeout: opts.Timeout})
		if err == nil {
			validationCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
			err = gcsStore.ValidateBucket(validationCtx)
			cancel()
			store = gcsStore
		}
	}
	if err != nil {
		_ = closeClient()
		return nil, nil, fmt.Errorf("create Hangar store: %w", err)
	}

	canonicalizer := hangar.Canonicalizer{TempDir: opts.ScratchDir, MaxContentBytes: opts.MaxContentBytes, MaxEntries: opts.MaxEntries}
	service := &HangarService{
		Store: store, Canonicalizer: canonicalizer, WarrantVerifier: verifier,
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
	// Private before validated, not after: the chart's scratch is an emptyDir,
	// which kubelet creates 0777 without the sticky bit, and ValidateTempDir
	// rightly refuses that. The symlink and containment checks above have
	// already established this is the daemon's own directory to tighten.
	if err := os.Chmod(cleanScratch, 0700); err != nil {
		return fmt.Errorf("set Hangar scratch directory permissions: %w", err)
	}
	if err := hangar.ValidateTempDir(resolvedScratch); err != nil {
		return err
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
