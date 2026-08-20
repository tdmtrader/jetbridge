//go:build linux || darwin

package hangar

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func materializeCapturedTree(ctx context.Context, storagePath, handle, volume string, ref TreeRef, sourceRoot *os.Root, hooks materializerHooks) (result error) {
	resolvedStorage, err := filepath.EvalSymlinks(storagePath)
	if err != nil {
		return fmt.Errorf("hangar: resolve materialization storage: %w", err)
	}
	storage, err := openAbsoluteDirectoryNoFollow(resolvedStorage)
	if err != nil {
		return fmt.Errorf("hangar: anchor materialization storage: %w", err)
	}
	defer storage.Close()
	steps, err := openDirectoryAt(storage, "steps", true, 0755)
	if err != nil {
		return fmt.Errorf("hangar: anchor materialization steps: %w", err)
	}
	defer steps.Close()
	handleDir, err := openDirectoryAt(steps, handle, true, 0755)
	if err != nil {
		return fmt.Errorf("hangar: anchor materialization handle: %w", err)
	}
	defer handleDir.Close()
	if hooks.beforeLock != nil {
		if err := hooks.beforeLock(); err != nil {
			return fmt.Errorf("hangar: before materialization destination lock: %w", err)
		}
	}
	destinationLock, err := acquireMaterializationLock(ctx, handleDir, volume)
	if err != nil {
		return fmt.Errorf("hangar: acquire materialization destination lock: %w", err)
	}
	defer func() { result = errors.Join(result, releaseMaterializationLock(destinationLock)) }()

	stageName, stage, err := randomDirectoryAt(handleDir, ".hangar-stage-")
	if err != nil {
		return fmt.Errorf("hangar: create private materialization stage: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = makeOpenedTreeWritable(stage)
			wipeErr := removeOpenedDirectoryContents(context.Background(), stage)
			var cleanupErr error
			if same, identityErr := sameOpenEntryAt(handleDir, stageName, stage); identityErr == nil && same {
				cleanupErr = unix.Unlinkat(int(handleDir.Fd()), stageName, unix.AT_REMOVEDIR)
			} else if identityErr != nil && !errors.Is(identityErr, unix.ENOENT) {
				cleanupErr = identityErr
			}
			result = errors.Join(result, wipeErr, cleanupErr)
		}
		closeErr := stage.Close()
		result = errors.Join(result, closeErr)
	}()

	source, err := sourceRoot.Open(".")
	if err != nil {
		return fmt.Errorf("hangar: anchor captured tree: %w", err)
	}
	defer source.Close()
	if err := copyOpenedTree(ctx, source, stage, ""); err != nil {
		return fmt.Errorf("hangar: copy captured tree: %w", err)
	}
	sameSource, err := sameOpenedTree(source, stage, nil)
	if err != nil || !sameSource {
		return fmt.Errorf("hangar: staged tree differs from anchored captured tree: %w", errors.Join(err, ErrCorrupt))
	}
	stageDigest, err := canonicalDigestOpenedTree(ctx, stage)
	if err != nil {
		return fmt.Errorf("hangar: rebind staged tree digest: %w", err)
	}
	if stageDigest != ref.Digest {
		return fmt.Errorf("hangar: staged tree digest differs from exact reference: %w", ErrCorrupt)
	}
	receipt, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	if err := writeExclusiveFileAt(stage, materializationReceiptName, receipt, 0444); err != nil {
		return fmt.Errorf("hangar: create materialization receipt: %w", err)
	}
	if err := unix.Fsync(int(stage.Fd())); err != nil {
		return fmt.Errorf("hangar: sync materialization stage: %w", err)
	}
	if hooks.afterStage != nil {
		if err := hooks.afterStage(filepath.Join(resolvedStorage, "steps", handle, stageName)); err != nil {
			return fmt.Errorf("hangar: after preparing materialization stage: %w", err)
		}
	}
	if hooks.beforePublish != nil {
		if err := hooks.beforePublish(); err != nil {
			return fmt.Errorf("hangar: before materialization publication: %w", err)
		}
	}
	if same, err := sameOpenAbsoluteDirectory(resolvedStorage, storage); err != nil || !same {
		return fmt.Errorf("hangar: materialization storage changed before publication: %w", err)
	}
	if same, err := sameOpenEntryAt(storage, "steps", steps); err != nil || !same {
		return fmt.Errorf("hangar: materialization steps changed before publication: %w", err)
	}
	if same, err := sameOpenEntryAt(steps, handle, handleDir); err != nil || !same {
		return fmt.Errorf("hangar: materialization handle changed before publication: %w", err)
	}
	if same, err := sameOpenEntryAt(handleDir, stageName, stage); err != nil || !same {
		return fmt.Errorf("hangar: materialization stage changed before publication: %w", err)
	}

	verifyAuthority := func(destination *os.File) error {
		if same, err := sameOpenAbsoluteDirectory(resolvedStorage, storage); err != nil || !same {
			return errors.Join(err, fmt.Errorf("materialization storage authority changed"))
		}
		if same, err := sameOpenEntryAt(storage, "steps", steps); err != nil || !same {
			return errors.Join(err, fmt.Errorf("materialization steps authority changed"))
		}
		if same, err := sameOpenEntryAt(steps, handle, handleDir); err != nil || !same {
			return errors.Join(err, fmt.Errorf("materialization handle authority changed"))
		}
		if same, err := sameOpenEntryAt(handleDir, volume, destination); err != nil || !same {
			return errors.Join(err, fmt.Errorf("materialization destination authority changed"))
		}
		return nil
	}
	publishedByRename, err := publishMaterializationAt(ctx, handleDir, stageName, stage, source, volume, ref, hooks, verifyAuthority)
	if publishedByRename {
		stageOwned = false
	}
	if err != nil {
		return err
	}
	return nil
}

func acquireMaterializationLock(ctx context.Context, parent *os.File, volume string) (*os.File, error) {
	sum := sha256.Sum256([]byte(volume))
	name := ".hangar-lock-" + hex.EncodeToString(sum[:16])
	fd := -1
	var err error
	for attempt := 0; attempt < 128; attempt++ {
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("open lock: %w", err)
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create lock: %w", err)
		}
	}
	if fd < 0 {
		return nil, fmt.Errorf("open lock: exhausted creation races")
	}
	lock := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() {
		lock.Close()
		return nil, errors.Join(err, fmt.Errorf("materialization lock has invalid identity"))
	}
	if same, err := sameOpenEntryAt(parent, name, lock); err != nil || !same {
		lock.Close()
		return nil, errors.Join(fmt.Errorf("recheck opened lock: %w", err), fmt.Errorf("materialization lock changed while opening"))
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			if same, recheckErr := sameOpenEntryAt(parent, name, lock); recheckErr != nil || !same {
				_ = unix.Flock(fd, unix.LOCK_UN)
				lock.Close()
				return nil, errors.Join(fmt.Errorf("recheck acquired lock: %w", recheckErr), fmt.Errorf("materialization lock changed while acquiring"))
			}
			return lock, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			lock.Close()
			return nil, fmt.Errorf("flock lock: %w", err)
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func releaseMaterializationLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	return errors.Join(unix.Flock(int(lock.Fd()), unix.LOCK_UN), lock.Close())
}

func publishMaterializationAt(ctx context.Context, parent *os.File, stageName string, stage, source *os.File, destinationName string, ref TreeRef, hooks materializerHooks, verifyAuthority func(*os.File) error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	destination, err := openDirectoryAt(parent, destinationName, false, 0)
	if errors.Is(err, unix.ENOENT) {
		if err := sealOpenedTreeContents(stage); err != nil {
			return false, fmt.Errorf("hangar: seal materialization stage: %w", err)
		}
		if hooks.beforeRootChmod != nil {
			if err := hooks.beforeRootChmod(); err != nil {
				return false, fmt.Errorf("hangar: before sealing materialization stage root: %w", err)
			}
		}
		if err := unix.Fchmod(int(stage.Fd()), 0555); err != nil {
			return false, fmt.Errorf("hangar: seal materialization stage root: %w", err)
		}
		if hooks.beforeRootSync != nil {
			if err := hooks.beforeRootSync(); err != nil {
				return false, fmt.Errorf("hangar: before syncing materialization stage root: %w", err)
			}
		}
		if err := unix.Fsync(int(stage.Fd())); err != nil {
			return false, fmt.Errorf("hangar: sync sealed materialization stage: %w", err)
		}
		same, compareErr := sameOpenedPayload(source, stage, nil)
		if compareErr != nil || !same {
			return false, fmt.Errorf("hangar: sealed staged payload differs from anchored captured tree: %w", errors.Join(compareErr, ErrCorrupt))
		}
		renameErr := renameNoReplaceAt(parent, stageName, destinationName)
		if renameErr != nil && os.Geteuid() != 0 && (errors.Is(renameErr, unix.EACCES) || errors.Is(renameErr, unix.EPERM)) {
			_ = unix.Fchmod(int(stage.Fd()), 0700)
			renameErr = renameNoReplaceAt(parent, stageName, destinationName)
			if renameErr == nil {
				if resealErr := errors.Join(unix.Fchmod(int(stage.Fd()), 0555), unix.Fsync(int(stage.Fd()))); resealErr != nil {
					return true, fmt.Errorf("hangar: reseal published materialization root: %w", resealErr)
				}
			}
		}
		if renameErr != nil {
			if errors.Is(renameErr, unix.EEXIST) || errors.Is(renameErr, unix.ENOTEMPTY) {
				existing, openErr := openDirectoryAt(parent, destinationName, false, 0)
				if openErr == nil {
					defer existing.Close()
					receiptExact, receiptErr := completedMaterializationOpened(existing, ref)
					treeExact, compareErr := sameOpenedTree(stage, existing, nil)
					authorityErr := verifyAuthority(existing)
					if receiptErr == nil && compareErr == nil && authorityErr == nil && receiptExact && treeExact {
						return false, nil
					}
				}
				return false, ErrConflict
			}
			return false, fmt.Errorf("hangar: publish absent materialization destination: %w", renameErr)
		}
		if hooks.afterReceipt != nil {
			if err := hooks.afterReceipt(); err != nil {
				return true, fmt.Errorf("hangar: after materialization receipt publication: %w", err)
			}
		}
		if err := verifyAuthority(stage); err != nil {
			return true, fmt.Errorf("hangar: recheck authority after receipt publication: %w", err)
		}
		if err := unix.Fsync(int(parent.Fd())); err != nil {
			return true, fmt.Errorf("hangar: sync materialization parent after receipt publication: %w", err)
		}
		if err := verifyAuthority(stage); err != nil {
			return true, fmt.Errorf("hangar: recheck authority before materialization success: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("hangar: open materialization destination: %w", err)
	}
	defer destination.Close()
	if hooks.afterDestinationOpen != nil {
		if err := hooks.afterDestinationOpen(); err != nil {
			return false, fmt.Errorf("hangar: after opening materialization destination: %w", err)
		}
	}
	unchanged, err := sameOpenEntryAt(parent, destinationName, destination)
	if err != nil || !unchanged {
		return false, fmt.Errorf("hangar: materialization destination changed while opening: %w", err)
	}
	empty, err := openedDirectoryEmpty(destination)
	if err != nil {
		return false, err
	}
	if !empty {
		if err := sealOpenedTreeContents(stage); err != nil {
			return false, fmt.Errorf("hangar: seal retry comparison tree: %w", err)
		}
		if exact, verifyErr := completedMaterializationOpened(destination, ref); verifyErr == nil && exact {
			same, compareErr := sameOpenedTree(stage, destination, hooks.duringRetryCompare)
			if compareErr == nil && same {
				if authorityErr := verifyAuthority(destination); authorityErr != nil {
					return false, fmt.Errorf("hangar: recheck retry authority before materialization success: %w", authorityErr)
				}
				return false, nil
			}
		}
		return false, ErrConflict
	}
	var destinationInitial unix.Stat_t
	if err := unix.Fstat(int(destination.Fd()), &destinationInitial); err != nil {
		return false, err
	}

	entries, err := readOpenedEntries(stage)
	if err != nil {
		return false, err
	}
	committed := false
	owned := make([]ownedMaterializationEntry, 0, len(entries))
	cleanup := func(publicationErr error) error {
		if committed {
			return publicationErr
		}
		_ = unix.Fchmod(int(destination.Fd()), 0700)
		cleanupErr := cleanupOwnedMaterializationEntries(destination, owned)
		restoreErr := unix.Fchmod(int(destination.Fd()), uint32(destinationInitial.Mode&0777))
		return errors.Join(publicationErr, cleanupErr, restoreErr)
	}
	for _, entry := range entries {
		if entry == materializationReceiptName {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, cleanup(err)
		}
		ownership, err := captureOwnedMaterializationEntry(stage, entry)
		if err != nil {
			return false, cleanup(err)
		}
		if err := unix.Renameat(int(stage.Fd()), entry, int(destination.Fd()), entry); err != nil {
			return false, cleanup(fmt.Errorf("hangar: transfer materialization entry %q: %w", entry, err))
		}
		if same, err := ownership.sameAt(destination); err != nil || !same {
			return false, cleanup(errors.Join(err, fmt.Errorf("hangar: transferred entry changed identity")))
		}
		owned = append(owned, ownership)
	}
	if hooks.beforePayloadSeal != nil {
		if err := hooks.beforePayloadSeal(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before sealing materialization payload: %w", err))
		}
	}
	if err := sealOpenedTreeContents(destination); err != nil {
		return false, cleanup(fmt.Errorf("hangar: seal existing materialization contents: %w", err))
	}
	if hooks.beforeRootChmod != nil {
		if err := hooks.beforeRootChmod(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before sealing existing materialization root: %w", err))
		}
	}
	if err := unix.Fchmod(int(destination.Fd()), 0555); err != nil {
		return false, cleanup(fmt.Errorf("hangar: seal existing materialization root: %w", err))
	}
	if hooks.beforeRootSync != nil {
		if err := hooks.beforeRootSync(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before syncing existing materialization root: %w", err))
		}
	}
	if err := unix.Fsync(int(destination.Fd())); err != nil {
		return false, cleanup(fmt.Errorf("hangar: sync existing materialization root: %w", err))
	}
	if hooks.beforeReceipt != nil {
		if err := hooks.beforeReceipt(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before materialization receipt publication: %w", err))
		}
	}
	if err := verifyAuthority(destination); err != nil {
		return false, cleanup(fmt.Errorf("hangar: authority changed before receipt publication: %w", err))
	}
	if exact, err := validateOwnedMaterializationDestination(destination, owned); err != nil || !exact {
		return false, cleanup(errors.Join(err, fmt.Errorf("hangar: destination changed before receipt publication")))
	}
	if hooks.beforeReceiptRename != nil {
		if err := hooks.beforeReceiptRename(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before materialization receipt rename: %w", err))
		}
	}
	if err := verifyAuthority(destination); err != nil {
		return false, cleanup(fmt.Errorf("hangar: authority changed immediately before receipt publication: %w", err))
	}
	same, compareErr := sameOpenedPayload(source, destination, nil)
	if compareErr != nil || !same {
		return false, cleanup(fmt.Errorf("hangar: sealed destination payload differs from anchored captured tree: %w", errors.Join(compareErr, ErrCorrupt)))
	}
	receiptMoved, err := renameIntoSealedDirectory(stage, materializationReceiptName, destination, materializationReceiptName)
	if receiptMoved {
		committed = true
	}
	if err != nil {
		return false, cleanup(fmt.Errorf("hangar: publish materialization receipt: %w", err))
	}
	committed = true
	if hooks.afterReceipt != nil {
		if err := hooks.afterReceipt(); err != nil {
			return false, fmt.Errorf("hangar: after materialization receipt publication: %w", err)
		}
	}
	if err := verifyAuthority(destination); err != nil {
		return false, fmt.Errorf("hangar: recheck authority after receipt publication: %w", err)
	}
	if err := unix.Fsync(int(destination.Fd())); err != nil {
		return false, fmt.Errorf("hangar: sync existing destination after receipt publication: %w", err)
	}
	unchanged, err = sameOpenEntryAt(parent, destinationName, destination)
	if err != nil || !unchanged {
		return false, fmt.Errorf("hangar: destination changed after receipt publication: %w", err)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		return false, fmt.Errorf("hangar: sync parent after receipt publication: %w", err)
	}
	if err := verifyAuthority(destination); err != nil {
		return false, fmt.Errorf("hangar: recheck authority before materialization success: %w", err)
	}
	return false, nil
}

func openAbsoluteDirectoryNoFollow(name string) (*os.File, error) {
	name = filepath.Clean(name)
	if !filepath.IsAbs(name) {
		return nil, fmt.Errorf("path must be absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	for _, segment := range strings.Split(strings.TrimPrefix(name, string(filepath.Separator)), string(filepath.Separator)) {
		if segment == "" {
			continue
		}
		if !validFilesystemComponent(segment) {
			current.Close()
			return nil, fmt.Errorf("unsafe absolute directory segment")
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), segment)
		current.Close()
		current = next
	}
	return current, nil
}

func sameOpenAbsoluteDirectory(name string, opened *os.File) (bool, error) {
	fresh, err := openAbsoluteDirectoryNoFollow(name)
	if err != nil {
		return false, err
	}
	defer fresh.Close()
	var first, second unix.Stat_t
	if err := unix.Fstat(int(opened.Fd()), &first); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(fresh.Fd()), &second); err != nil {
		return false, err
	}
	return first.Dev == second.Dev && first.Ino == second.Ino && first.Mode&unix.S_IFMT == second.Mode&unix.S_IFMT, nil
}

func openDirectoryAt(parent *os.File, name string, create bool, mode uint32) (*os.File, error) {
	if !validFilesystemComponent(name) {
		return nil, fmt.Errorf("unsafe directory segment")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil && create && errors.Is(err, unix.ENOENT) {
		if mkdirErr := unix.Mkdirat(int(parent.Fd()), name, mode); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return nil, mkdirErr
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	opened := os.NewFile(uintptr(fd), name)
	same, sameErr := sameOpenEntryAt(parent, name, opened)
	if sameErr != nil || !same {
		opened.Close()
		return nil, fmt.Errorf("directory changed while opening: %w", sameErr)
	}
	return opened, nil
}

func sameOpenEntryAt(parent *os.File, name string, opened *os.File) (bool, error) {
	var named, descriptor unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(opened.Fd()), &descriptor); err != nil {
		return false, err
	}
	return named.Dev == descriptor.Dev && named.Ino == descriptor.Ino && named.Mode&unix.S_IFMT == descriptor.Mode&unix.S_IFMT, nil
}

func randomDirectoryAt(parent *os.File, prefix string) (string, *os.File, error) {
	var random [16]byte
	for attempt := 0; attempt < 128; attempt++ {
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", nil, err
		}
		opened, err := openDirectoryAt(parent, name, false, 0)
		if err != nil {
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, err
		}
		return name, opened, nil
	}
	return "", nil, fmt.Errorf("exhausted private stage names")
}

func copyOpenedTree(ctx context.Context, source, destination *os.File, prefix string) error {
	entries, err := readOpenedEntries(source)
	if err != nil {
		return err
	}
	for _, name := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryPath := name
		if prefix != "" {
			entryPath = path.Join(prefix, name)
		}
		if entryPath == materializationReceiptName {
			return fmt.Errorf("hangar: source tree collides with internal receipt name")
		}
		var before unix.Stat_t
		if err := unix.Fstatat(int(source.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			sourceChild, err := openDirectoryAt(source, name, false, 0)
			if err != nil {
				return err
			}
			if err := unix.Mkdirat(int(destination.Fd()), name, uint32(before.Mode&0777)); err != nil {
				sourceChild.Close()
				return err
			}
			destinationChild, err := openDirectoryAt(destination, name, false, 0)
			if err != nil {
				sourceChild.Close()
				return err
			}
			copyErr := copyOpenedTree(ctx, sourceChild, destinationChild, entryPath)
			unchanged, recheckErr := sameOpenEntryAt(source, name, sourceChild)
			closeErr := errors.Join(sourceChild.Close(), destinationChild.Close())
			if copyErr != nil || recheckErr != nil || !unchanged || closeErr != nil {
				return errors.Join(copyErr, recheckErr, closeErr, errorUnless(unchanged, "source directory changed during copy"))
			}
		case unix.S_IFREG:
			fd, err := unix.Openat(int(source.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			sourceFile := os.NewFile(uintptr(fd), entryPath)
			beforeInfo, infoErr := sourceFile.Stat()
			var opened unix.Stat_t
			if err := unix.Fstat(fd, &opened); infoErr != nil || err != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Dev != before.Dev || opened.Ino != before.Ino {
				sourceFile.Close()
				return errors.Join(infoErr, err, fmt.Errorf("source file changed while opening"))
			}
			destinationFD, err := unix.Openat(int(destination.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(opened.Mode&0777))
			if err != nil {
				sourceFile.Close()
				return err
			}
			destinationFile := os.NewFile(uintptr(destinationFD), entryPath)
			copyErr := copyFileContext(ctx, destinationFile, sourceFile)
			afterInfo, afterInfoErr := sourceFile.Stat()
			unchanged, recheckErr := sameOpenEntryAt(source, name, sourceFile)
			chmodErr := unix.Fchmod(destinationFD, uint32(opened.Mode&0777))
			syncErr := destinationFile.Sync()
			closeErr := errors.Join(sourceFile.Close(), destinationFile.Close())
			metadataSame := beforeInfo != nil && afterInfo != nil && os.SameFile(beforeInfo, afterInfo) && beforeInfo.Size() == afterInfo.Size() && beforeInfo.Mode() == afterInfo.Mode() && beforeInfo.ModTime() == afterInfo.ModTime()
			if copyErr != nil || afterInfoErr != nil || recheckErr != nil || !unchanged || !metadataSame || syncErr != nil || chmodErr != nil || closeErr != nil {
				return errors.Join(copyErr, afterInfoErr, recheckErr, errorUnless(unchanged && metadataSame, "source file changed during copy"), syncErr, chmodErr, closeErr)
			}
		case unix.S_IFLNK:
			target, err := readlinkAt(int(source.Fd()), name)
			if err != nil {
				return err
			}
			if err := validateContainedSymlink(entryPath, target); err != nil {
				return err
			}
			if err := unix.Symlinkat(target, int(destination.Fd()), name); err != nil {
				return err
			}
			again, err := readlinkAt(int(source.Fd()), name)
			if err != nil || again != target {
				return fmt.Errorf("source symlink changed during copy")
			}
			var after unix.Stat_t
			if err := unix.Fstatat(int(source.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || after.Dev != before.Dev || after.Ino != before.Ino || after.Mode&unix.S_IFMT != unix.S_IFLNK {
				return fmt.Errorf("source symlink changed during copy")
			}
		default:
			return fmt.Errorf("hangar: source contains unsupported file type")
		}
	}
	return nil
}

func errorUnless(condition bool, message string) error {
	if condition {
		return nil
	}
	return errors.New(message)
}

func readOpenedEntries(directory *os.File) ([]string, error) {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validFilesystemComponent(entry.Name()) {
			return nil, fmt.Errorf("unsafe directory entry")
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func validFilesystemComponent(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\\\x00")
}

func copyFileContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			if writeErr != nil {
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

type openedCanonicalEntry struct {
	name   string
	kind   uint32
	mode   int64
	size   int64
	target string
}

func canonicalDigestOpenedTree(ctx context.Context, root *os.File) (Digest, error) {
	entries := make([]openedCanonicalEntry, 0)
	if err := collectOpenedCanonicalEntries(ctx, root, "", &entries); err != nil {
		return "", err
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].name < entries[right].name })
	hash := sha256.New()
	writer := tar.NewWriter(hash)
	epoch := time.Unix(0, 0).UTC()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return "", err
		}
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: entry.size, Linkname: entry.target,
			Uid: 0, Gid: 0, ModTime: epoch, AccessTime: epoch, ChangeTime: epoch, Format: tar.FormatGNU,
		}
		switch entry.kind {
		case unix.S_IFDIR:
			header.Typeflag = tar.TypeDir
		case unix.S_IFREG:
			header.Typeflag = tar.TypeReg
		case unix.S_IFLNK:
			header.Typeflag = tar.TypeSymlink
		default:
			_ = writer.Close()
			return "", fmt.Errorf("hangar: unsupported staged entry type")
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = writer.Close()
			return "", err
		}
		if entry.kind != unix.S_IFREG {
			continue
		}
		file, err := openRegularAt(root, entry.name)
		if err != nil {
			_ = writer.Close()
			return "", err
		}
		copied, copyErr := io.CopyN(writer, &contextReader{ctx: ctx, reader: file}, entry.size)
		var trailing [1]byte
		trailingCount, trailingErr := file.Read(trailing[:])
		closeErr := file.Close()
		if copyErr != nil || copied != entry.size || trailingCount != 0 || !errors.Is(trailingErr, io.EOF) || closeErr != nil {
			_ = writer.Close()
			return "", errors.Join(copyErr, trailingErr, closeErr, errorUnless(copied == entry.size && trailingCount == 0, "staged file changed while hashing"))
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	return Digest(fmt.Sprintf("sha256:%x", hash.Sum(nil))), nil
}

func collectOpenedCanonicalEntries(ctx context.Context, directory *os.File, prefix string, entries *[]openedCanonicalEntry) error {
	names, err := readOpenedEntries(directory)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		entryName := name
		if prefix != "" {
			entryName = path.Join(prefix, name)
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		entry := openedCanonicalEntry{name: entryName, kind: uint32(stat.Mode & unix.S_IFMT), mode: int64(stat.Mode & 0777)}
		switch entry.kind {
		case unix.S_IFDIR:
			child, err := openDirectoryAt(directory, name, false, 0)
			if err != nil {
				return err
			}
			*entries = append(*entries, entry)
			collectErr := collectOpenedCanonicalEntries(ctx, child, entryName, entries)
			unchanged, recheckErr := sameOpenEntryAt(directory, name, child)
			closeErr := child.Close()
			if collectErr != nil || recheckErr != nil || !unchanged || closeErr != nil {
				return errors.Join(collectErr, recheckErr, closeErr, errorUnless(unchanged, "staged directory changed while hashing"))
			}
		case unix.S_IFREG:
			entry.size = stat.Size
			*entries = append(*entries, entry)
		case unix.S_IFLNK:
			target, err := readlinkAt(int(directory.Fd()), name)
			if err != nil {
				return err
			}
			entry.target = target
			entry.mode = 0777
			*entries = append(*entries, entry)
		default:
			return fmt.Errorf("hangar: unsupported staged entry type")
		}
	}
	return nil
}

func openRegularAt(root *os.File, relative string) (*os.File, error) {
	components := strings.Split(relative, "/")
	current, err := duplicateFile(root)
	if err != nil {
		return nil, err
	}
	for _, segment := range components[:len(components)-1] {
		next, err := openDirectoryAt(current, segment, false, 0)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	name := components[len(components)-1]
	fd, err := unix.Openat(int(current.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	current.Close()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), relative)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.Join(err, fmt.Errorf("staged path is not regular"))
	}
	return file, nil
}

func duplicateFile(file *os.File) (*os.File, error) {
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func sealOpenedTreeContents(directory *os.File) error {
	entries, err := readOpenedEntries(directory)
	if err != nil {
		return err
	}
	for _, name := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := openDirectoryAt(directory, name, false, 0)
			if err != nil {
				return err
			}
			sealErr := sealOpenedTreeContents(child)
			if sealErr == nil {
				sealErr = unix.Fchmod(int(child.Fd()), 0555)
			}
			if sealErr == nil {
				sealErr = unix.Fsync(int(child.Fd()))
			}
			closeErr := child.Close()
			if sealErr != nil || closeErr != nil {
				return errors.Join(sealErr, closeErr)
			}
		case unix.S_IFREG:
			fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(fd), name)
			chmodErr := unix.Fchmod(fd, 0444)
			syncErr := error(nil)
			if chmodErr == nil {
				syncErr = unix.Fsync(fd)
			}
			unchanged, recheckErr := sameOpenEntryAt(directory, name, file)
			closeErr := file.Close()
			if chmodErr != nil || syncErr != nil || recheckErr != nil || !unchanged || closeErr != nil {
				return errors.Join(chmodErr, syncErr, recheckErr, errorUnless(unchanged, "sealed file changed identity"), closeErr)
			}
		case unix.S_IFLNK:
			if _, err := readlinkAt(int(directory.Fd()), name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("hangar: unsupported entry while sealing")
		}
	}
	return nil
}

func makeOpenedTreeWritable(directory *os.File) error {
	_ = unix.Fchmod(int(directory.Fd()), 0700)
	entries, err := readOpenedEntries(directory)
	if err != nil {
		return err
	}
	for _, name := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, err := openDirectoryAt(directory, name, false, 0)
			if err != nil {
				return err
			}
			writableErr := makeOpenedTreeWritable(child)
			closeErr := child.Close()
			if writableErr != nil || closeErr != nil {
				return errors.Join(writableErr, closeErr)
			}
		}
	}
	return nil
}

func writeExclusiveFileAt(directory *os.File, name string, contents []byte, mode uint32) error {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(contents)
	chmodErr := unix.Fchmod(fd, mode)
	syncErr := error(nil)
	if writeErr == nil && chmodErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, chmodErr, closeErr)
}

func completedMaterializationOpened(destination *os.File, ref TreeRef) (bool, error) {
	var destinationStat unix.Stat_t
	if err := unix.Fstat(int(destination.Fd()), &destinationStat); err != nil {
		return false, err
	}
	if destinationStat.Mode&07777 != 0555 {
		return false, nil
	}
	fd, err := unix.Openat(int(destination.Fd()), materializationReceiptName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), materializationReceiptName)
	var receiptStat unix.Stat_t
	if err := unix.Fstat(fd, &receiptStat); err != nil || receiptStat.Mode&unix.S_IFMT != unix.S_IFREG || receiptStat.Mode&07777 != 0444 {
		file.Close()
		return false, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(contents) > 4096 {
		return false, errors.Join(readErr, closeErr)
	}
	want, _ := json.Marshal(ref)
	return string(contents) == string(want), nil
}

func sameOpenedTree(expected, actual *os.File, duringEntry func() error) (bool, error) {
	return sameOpenedTreeWithModes(expected, actual, duringEntry, true)
}

func sameOpenedPayload(expected, actual *os.File, duringEntry func() error) (bool, error) {
	return sameOpenedTreeWithModes(expected, actual, duringEntry, false)
}

func sameOpenedTreeWithModes(expected, actual *os.File, duringEntry func() error, compareModes bool) (bool, error) {
	var expectedRootBefore, actualRootBefore unix.Stat_t
	if err := unix.Fstat(int(expected.Fd()), &expectedRootBefore); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(actual.Fd()), &actualRootBefore); err != nil {
		return false, err
	}
	expectedNames, err := readOpenedPayloadEntries(expected)
	if err != nil {
		return false, err
	}
	actualNames, err := readOpenedPayloadEntries(actual)
	if err != nil {
		return false, err
	}
	if !sameOpenedNames(expectedNames, actualNames) {
		return false, nil
	}
	for _, name := range expectedNames {
		var expectedStat, actualStat unix.Stat_t
		if err := unix.Fstatat(int(expected.Fd()), name, &expectedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return false, err
		}
		if err := unix.Fstatat(int(actual.Fd()), name, &actualStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return false, err
		}
		if duringEntry != nil {
			if err := duringEntry(); err != nil {
				return false, err
			}
		}
		if expectedStat.Mode&unix.S_IFMT != actualStat.Mode&unix.S_IFMT || compareModes && expectedStat.Mode&07777 != actualStat.Mode&07777 {
			return false, nil
		}
		switch expectedStat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			expectedChild, err := openDirectoryAt(expected, name, false, 0)
			if err != nil {
				return false, err
			}
			actualChild, err := openDirectoryAt(actual, name, false, 0)
			if err != nil {
				expectedChild.Close()
				return false, err
			}
			if matches, err := openedFileMatchesStat(expectedChild, &expectedStat); err != nil || !matches {
				expectedChild.Close()
				actualChild.Close()
				return false, err
			}
			if matches, err := openedFileMatchesStat(actualChild, &actualStat); err != nil || !matches {
				expectedChild.Close()
				actualChild.Close()
				return false, err
			}
			same, compareErr := sameOpenedTreeWithModes(expectedChild, actualChild, duringEntry, compareModes)
			expectedStable, expectedStatErr := openedFileMatchesStat(expectedChild, &expectedStat)
			actualStable, actualStatErr := openedFileMatchesStat(actualChild, &actualStat)
			expectedUnchanged, expectedRecheckErr := sameOpenEntryAt(expected, name, expectedChild)
			actualUnchanged, actualRecheckErr := sameOpenEntryAt(actual, name, actualChild)
			closeErr := errors.Join(expectedChild.Close(), actualChild.Close())
			if compareErr != nil || expectedStatErr != nil || actualStatErr != nil || expectedRecheckErr != nil || actualRecheckErr != nil || closeErr != nil {
				return false, errors.Join(compareErr, expectedStatErr, actualStatErr, expectedRecheckErr, actualRecheckErr, closeErr)
			}
			if !same || !expectedStable || !actualStable || !expectedUnchanged || !actualUnchanged {
				return false, nil
			}
		case unix.S_IFREG:
			if expectedStat.Size != actualStat.Size {
				return false, nil
			}
			expectedFD, err := unix.Openat(int(expected.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return false, err
			}
			actualFD, err := unix.Openat(int(actual.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				unix.Close(expectedFD)
				return false, err
			}
			expectedFile := os.NewFile(uintptr(expectedFD), name)
			actualFile := os.NewFile(uintptr(actualFD), name)
			if matches, err := openedFileMatchesStat(expectedFile, &expectedStat); err != nil || !matches {
				expectedFile.Close()
				actualFile.Close()
				return false, err
			}
			if matches, err := openedFileMatchesStat(actualFile, &actualStat); err != nil || !matches {
				expectedFile.Close()
				actualFile.Close()
				return false, err
			}
			equal, compareErr := equalOpenedFiles(expectedFile, actualFile)
			expectedStable, expectedStatErr := openedFileMatchesStat(expectedFile, &expectedStat)
			actualStable, actualStatErr := openedFileMatchesStat(actualFile, &actualStat)
			expectedUnchanged, expectedRecheckErr := sameOpenEntryAt(expected, name, expectedFile)
			actualUnchanged, actualRecheckErr := sameOpenEntryAt(actual, name, actualFile)
			closeErr := errors.Join(expectedFile.Close(), actualFile.Close())
			if compareErr != nil || expectedStatErr != nil || actualStatErr != nil || expectedRecheckErr != nil || actualRecheckErr != nil || closeErr != nil {
				return false, errors.Join(compareErr, expectedStatErr, actualStatErr, expectedRecheckErr, actualRecheckErr, closeErr)
			}
			if !equal || !expectedStable || !actualStable || !expectedUnchanged || !actualUnchanged {
				return false, nil
			}
		case unix.S_IFLNK:
			expectedTarget, err := readlinkAt(int(expected.Fd()), name)
			if err != nil {
				return false, err
			}
			actualTarget, err := readlinkAt(int(actual.Fd()), name)
			if err != nil {
				return false, err
			}
			if expectedTarget != actualTarget {
				return false, nil
			}
			var expectedAfter, actualAfter unix.Stat_t
			if err := unix.Fstatat(int(expected.Fd()), name, &expectedAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return false, err
			}
			if err := unix.Fstatat(int(actual.Fd()), name, &actualAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return false, err
			}
			if !sameRelevantStat(&expectedStat, &expectedAfter) || !sameRelevantStat(&actualStat, &actualAfter) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	expectedNamesAfter, err := readOpenedPayloadEntries(expected)
	if err != nil {
		return false, err
	}
	actualNamesAfter, err := readOpenedPayloadEntries(actual)
	if err != nil {
		return false, err
	}
	if !sameOpenedNames(expectedNames, expectedNamesAfter) || !sameOpenedNames(actualNames, actualNamesAfter) || !sameOpenedNames(expectedNamesAfter, actualNamesAfter) {
		return false, nil
	}
	var expectedRootAfter, actualRootAfter unix.Stat_t
	if err := unix.Fstat(int(expected.Fd()), &expectedRootAfter); err != nil {
		return false, err
	}
	if err := unix.Fstat(int(actual.Fd()), &actualRootAfter); err != nil {
		return false, err
	}
	if !sameRelevantStat(&expectedRootBefore, &expectedRootAfter) || !sameRelevantStat(&actualRootBefore, &actualRootAfter) {
		return false, nil
	}
	return true, nil
}

func readOpenedPayloadEntries(directory *os.File) ([]string, error) {
	names, err := readOpenedEntries(directory)
	if err != nil {
		return nil, err
	}
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != materializationReceiptName {
			filtered = append(filtered, name)
		}
	}
	sort.Strings(filtered)
	return filtered, nil
}

func sameOpenedNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameRelevantStat(before, after *unix.Stat_t) bool {
	return identityFromStat(before) == identityFromStat(after) &&
		before.Mode == after.Mode &&
		before.Nlink == after.Nlink &&
		before.Uid == after.Uid &&
		before.Gid == after.Gid &&
		before.Rdev == after.Rdev &&
		before.Size == after.Size
}

func openedFileMatchesStat(file *os.File, before *unix.Stat_t) (bool, error) {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return false, err
	}
	return sameRelevantStat(before, &opened), nil
}

func equalOpenedFiles(left, right *os.File) (bool, error) {
	leftBuffer := make([]byte, 128*1024)
	rightBuffer := make([]byte, 128*1024)
	for {
		leftRead, leftErr := io.ReadFull(left, leftBuffer)
		rightRead, rightErr := io.ReadFull(right, rightBuffer)
		if leftRead != rightRead || string(leftBuffer[:leftRead]) != string(rightBuffer[:rightRead]) {
			return false, nil
		}
		leftDone := errors.Is(leftErr, io.EOF) || errors.Is(leftErr, io.ErrUnexpectedEOF)
		rightDone := errors.Is(rightErr, io.EOF) || errors.Is(rightErr, io.ErrUnexpectedEOF)
		if leftDone || rightDone {
			return leftDone && rightDone, nil
		}
		if leftErr != nil || rightErr != nil {
			return false, errors.Join(leftErr, rightErr)
		}
	}
}

func openedDirectoryEmpty(directory *os.File) (bool, error) {
	entries, err := readOpenedEntries(directory)
	return len(entries) == 0, err
}

type materializationEntryIdentity struct {
	device uint64
	inode  uint64
	kind   uint32
}

type ownedMaterializationEntry struct {
	name     string
	identity materializationEntryIdentity
	children []ownedMaterializationEntry
}

func identityFromStat(stat *unix.Stat_t) materializationEntryIdentity {
	return materializationEntryIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), kind: uint32(stat.Mode & unix.S_IFMT)}
}

func captureOwnedMaterializationEntry(parent *os.File, name string) (ownedMaterializationEntry, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return ownedMaterializationEntry{}, err
	}
	owned := ownedMaterializationEntry{name: name, identity: identityFromStat(&stat)}
	if owned.identity.kind != unix.S_IFDIR {
		return owned, nil
	}
	directory, err := openDirectoryAt(parent, name, false, 0)
	if err != nil {
		return ownedMaterializationEntry{}, err
	}
	defer directory.Close()
	names, err := readOpenedEntries(directory)
	if err != nil {
		return ownedMaterializationEntry{}, err
	}
	for _, childName := range names {
		child, err := captureOwnedMaterializationEntry(directory, childName)
		if err != nil {
			return ownedMaterializationEntry{}, err
		}
		owned.children = append(owned.children, child)
	}
	if same, err := owned.sameAt(parent); err != nil || !same {
		return ownedMaterializationEntry{}, errors.Join(err, fmt.Errorf("owned directory changed while recording"))
	}
	return owned, nil
}

func (owned ownedMaterializationEntry) sameAt(parent *os.File) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), owned.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	return owned.identity == identityFromStat(&stat), nil
}

func validateOwnedMaterializationDestination(destination *os.File, owned []ownedMaterializationEntry) (bool, error) {
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(destination.Fd()), &rootStat); err != nil {
		return false, err
	}
	if rootStat.Mode&unix.S_IFMT != unix.S_IFDIR || rootStat.Mode&07777 != 0555 {
		return false, nil
	}
	names, err := readOpenedEntries(destination)
	if err != nil {
		return false, err
	}
	if len(names) != len(owned) {
		return false, nil
	}
	byName := make(map[string]ownedMaterializationEntry, len(owned))
	for _, entry := range owned {
		byName[entry.name] = entry
	}
	for _, name := range names {
		entry, found := byName[name]
		if !found {
			return false, nil
		}
		same, err := validateOwnedMaterializationEntry(destination, entry)
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}

func validateOwnedMaterializationEntry(parent *os.File, owned ownedMaterializationEntry) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), owned.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	if owned.identity != identityFromStat(&stat) {
		return false, nil
	}
	if owned.identity.kind == unix.S_IFDIR && stat.Mode&07777 != 0555 || owned.identity.kind == unix.S_IFREG && stat.Mode&07777 != 0444 {
		return false, nil
	}
	if owned.identity.kind != unix.S_IFDIR {
		return true, nil
	}
	directory, err := openDirectoryAt(parent, owned.name, false, 0)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	return validateOwnedMaterializationDestination(directory, owned.children)
}

func cleanupOwnedMaterializationEntries(destination *os.File, owned []ownedMaterializationEntry) error {
	var cleanupErr error
	for index := len(owned) - 1; index >= 0; index-- {
		cleanupErr = errors.Join(cleanupErr, cleanupOwnedMaterializationEntry(destination, owned[index]))
	}
	return cleanupErr
}

func cleanupOwnedMaterializationEntry(parent *os.File, owned ownedMaterializationEntry) error {
	same, err := owned.sameAt(parent)
	if errors.Is(err, unix.ENOENT) || !same {
		return nil
	}
	if err != nil {
		return err
	}
	if owned.identity.kind != unix.S_IFDIR {
		return unix.Unlinkat(int(parent.Fd()), owned.name, 0)
	}
	directory, err := openDirectoryAt(parent, owned.name, false, 0)
	if err != nil {
		return err
	}
	_ = unix.Fchmod(int(directory.Fd()), 0700)
	cleanupErr := cleanupOwnedMaterializationEntries(directory, owned.children)
	closeErr := directory.Close()
	if cleanupErr != nil || closeErr != nil {
		return errors.Join(cleanupErr, closeErr)
	}
	err = unix.Unlinkat(int(parent.Fd()), owned.name, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
		return nil
	}
	return err
}

func renameIntoSealedDirectory(source *os.File, sourceName string, destination *os.File, destinationName string) (bool, error) {
	err := renameNoReplaceBetween(source, sourceName, destination, destinationName)
	return err == nil, err
}

func removeOpenedDirectoryContents(ctx context.Context, directory *os.File) error {
	entries, err := readOpenedEntries(directory)
	if err != nil {
		return err
	}
	for _, name := range entries {
		if err := removeTreeAt(ctx, directory, name); err != nil {
			return err
		}
	}
	return nil
}

func removeTreeAt(ctx context.Context, parent *os.File, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(int(parent.Fd()), name, 0)
	}
	directory, err := openDirectoryAt(parent, name, false, 0)
	if err != nil {
		return err
	}
	if err := unix.Fchmod(int(directory.Fd()), 0700); err != nil {
		directory.Close()
		return err
	}
	removeErr := removeOpenedDirectoryContents(ctx, directory)
	closeErr := directory.Close()
	if removeErr != nil || closeErr != nil {
		return errors.Join(removeErr, closeErr)
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

func readlinkAt(parent int, name string) (string, error) {
	for size := 256; size <= 8192; size *= 2 {
		buffer := make([]byte, size)
		length, err := unix.Readlinkat(parent, name, buffer)
		if err != nil {
			return "", err
		}
		if length < len(buffer) {
			return string(buffer[:length]), nil
		}
	}
	return "", fmt.Errorf("symlink target too long")
}

func validateContainedSymlink(name, target string) error {
	if target == "" || strings.ContainsRune(target, 0) || path.IsAbs(target) {
		return fmt.Errorf("hangar: unsafe symlink")
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
		return fmt.Errorf("hangar: symlink escapes tree")
	}
	return nil
}
