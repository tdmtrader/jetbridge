//go:build linux || darwin

package hangar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func materializeCapturedTree(ctx context.Context, storagePath, handle, volume string, ref TreeRef, sourcePath string, hooks materializerHooks) (result error) {
	resolvedStorage, err := filepath.EvalSymlinks(storagePath)
	if err != nil {
		return fmt.Errorf("hangar: resolve materialization storage: %w", err)
	}
	resolvedSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return fmt.Errorf("hangar: resolve captured tree: %w", err)
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

	stageName, stage, err := randomDirectoryAt(handleDir, ".hangar-stage-")
	if err != nil {
		return fmt.Errorf("hangar: create private materialization stage: %w", err)
	}
	stageOwned := true
	defer func() {
		if stageOwned {
			_ = makeOpenedTreeWritable(stage)
			wipeErr := removeOpenedDirectoryContents(context.Background(), stage)
			cleanupErr := removeTreeAt(context.Background(), handleDir, stageName)
			result = errors.Join(result, wipeErr, cleanupErr)
		}
		closeErr := stage.Close()
		result = errors.Join(result, closeErr)
	}()

	source, err := openAbsoluteDirectoryNoFollow(resolvedSource)
	if err != nil {
		return fmt.Errorf("hangar: anchor captured tree: %w", err)
	}
	defer source.Close()
	if err := copyOpenedTree(ctx, source, stage, ""); err != nil {
		return fmt.Errorf("hangar: copy captured tree: %w", err)
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

	publishedByRename, err := publishMaterializationAt(ctx, handleDir, stageName, stage, volume, ref, hooks)
	if err != nil {
		return err
	}
	if publishedByRename {
		stageOwned = false
	}
	return nil
}

func publishMaterializationAt(ctx context.Context, parent *os.File, stageName string, stage *os.File, destinationName string, ref TreeRef, hooks materializerHooks) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	destination, err := openDirectoryAt(parent, destinationName, false, 0)
	if errors.Is(err, unix.ENOENT) {
		if err := sealOpenedTreeContents(stage); err != nil {
			return false, fmt.Errorf("hangar: seal materialization stage: %w", err)
		}
		if err := unix.Fsync(int(stage.Fd())); err != nil {
			return false, fmt.Errorf("hangar: sync sealed materialization stage: %w", err)
		}
		if err := renameNoReplaceAt(parent, stageName, destinationName); err != nil {
			if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
				if exact, verifyErr := completedMaterializationAt(parent, destinationName, ref); verifyErr == nil && exact {
					return false, nil
				}
				return false, ErrConflict
			}
			return false, fmt.Errorf("hangar: publish absent materialization destination: %w", err)
		}
		if err := unix.Fchmod(int(stage.Fd()), 0555); err != nil {
			return true, fmt.Errorf("hangar: seal materialization root after receipt publication: %w", err)
		}
		if hooks.afterReceipt != nil {
			if err := hooks.afterReceipt(); err != nil {
				return true, fmt.Errorf("hangar: after materialization receipt publication: %w", err)
			}
		}
		if err := unix.Fsync(int(parent.Fd())); err != nil {
			return true, fmt.Errorf("hangar: sync materialization parent after receipt publication: %w", err)
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
			same, compareErr := sameOpenedTree(stage, destination)
			if compareErr == nil && same {
				return false, nil
			}
		}
		return false, ErrConflict
	}

	entries, err := readOpenedEntries(stage)
	if err != nil {
		return false, err
	}
	committed := false
	cleanup := func(publicationErr error) error {
		if committed {
			return publicationErr
		}
		return errors.Join(publicationErr, removeOpenedDirectoryContents(context.Background(), destination))
	}
	for _, entry := range entries {
		if entry == materializationReceiptName {
			continue
		}
		if err := ctx.Err(); err != nil {
			return false, cleanup(err)
		}
		if err := unix.Renameat(int(stage.Fd()), entry, int(destination.Fd()), entry); err != nil {
			return false, cleanup(fmt.Errorf("hangar: transfer materialization entry %q: %w", entry, err))
		}
	}
	if err := sealOpenedTreeContents(destination); err != nil {
		return false, cleanup(fmt.Errorf("hangar: seal existing materialization contents: %w", err))
	}
	if hooks.beforeReceipt != nil {
		if err := hooks.beforeReceipt(); err != nil {
			return false, cleanup(fmt.Errorf("hangar: before materialization receipt publication: %w", err))
		}
	}
	if err := unix.Renameat(int(stage.Fd()), materializationReceiptName, int(destination.Fd()), materializationReceiptName); err != nil {
		return false, cleanup(fmt.Errorf("hangar: publish materialization receipt: %w", err))
	}
	committed = true
	if hooks.afterReceipt != nil {
		if err := hooks.afterReceipt(); err != nil {
			return false, fmt.Errorf("hangar: after materialization receipt publication: %w", err)
		}
	}
	if err := unix.Fchmod(int(destination.Fd()), 0555); err != nil {
		return false, fmt.Errorf("hangar: seal existing destination after receipt publication: %w", err)
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
		if !validMaterializationSegment(segment) {
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
	if !validMaterializationSegment(name) {
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
			if err := unix.Mkdirat(int(destination.Fd()), name, 0700); err != nil {
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
			destinationFD, err := unix.Openat(int(destination.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
			if err != nil {
				sourceFile.Close()
				return err
			}
			destinationFile := os.NewFile(uintptr(destinationFD), entryPath)
			copyErr := copyFileContext(ctx, destinationFile, sourceFile)
			afterInfo, afterInfoErr := sourceFile.Stat()
			unchanged, recheckErr := sameOpenEntryAt(source, name, sourceFile)
			syncErr := destinationFile.Sync()
			chmodErr := unix.Fchmod(destinationFD, 0444)
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
		if !validMaterializationSegment(entry.Name()) {
			return nil, fmt.Errorf("unsafe directory entry")
		}
		names = append(names, entry.Name())
	}
	return names, nil
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
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, err := openDirectoryAt(directory, name, false, 0)
			if err != nil {
				return err
			}
			sealErr := sealOpenedTreeContents(child)
			if sealErr == nil {
				sealErr = unix.Fchmod(int(child.Fd()), 0555)
			}
			closeErr := child.Close()
			if sealErr != nil || closeErr != nil {
				return errors.Join(sealErr, closeErr)
			}
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
	syncErr := file.Sync()
	chmodErr := unix.Fchmod(fd, mode)
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, chmodErr, closeErr)
}

func completedMaterializationAt(parent *os.File, name string, ref TreeRef) (bool, error) {
	destination, err := openDirectoryAt(parent, name, false, 0)
	if err != nil {
		return false, err
	}
	defer destination.Close()
	return completedMaterializationOpened(destination, ref)
}

func completedMaterializationOpened(destination *os.File, ref TreeRef) (bool, error) {
	var destinationStat unix.Stat_t
	if err := unix.Fstat(int(destination.Fd()), &destinationStat); err != nil {
		return false, err
	}
	if destinationStat.Mode&0777 != 0555 {
		return false, nil
	}
	fd, err := unix.Openat(int(destination.Fd()), materializationReceiptName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), materializationReceiptName)
	var receiptStat unix.Stat_t
	if err := unix.Fstat(fd, &receiptStat); err != nil || receiptStat.Mode&unix.S_IFMT != unix.S_IFREG || receiptStat.Mode&0777 != 0444 {
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

func sameOpenedTree(expected, actual *os.File) (bool, error) {
	expectedNames, err := readOpenedEntries(expected)
	if err != nil {
		return false, err
	}
	actualNames, err := readOpenedEntries(actual)
	if err != nil {
		return false, err
	}
	filterReceipt := func(names []string) []string {
		filtered := names[:0]
		for _, name := range names {
			if name != materializationReceiptName {
				filtered = append(filtered, name)
			}
		}
		return filtered
	}
	expectedNames = filterReceipt(expectedNames)
	actualNames = filterReceipt(actualNames)
	if len(expectedNames) != len(actualNames) {
		return false, nil
	}
	actualSet := make(map[string]struct{}, len(actualNames))
	for _, name := range actualNames {
		actualSet[name] = struct{}{}
	}
	for _, name := range expectedNames {
		if _, exists := actualSet[name]; !exists {
			return false, nil
		}
		var expectedStat, actualStat unix.Stat_t
		if err := unix.Fstatat(int(expected.Fd()), name, &expectedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return false, err
		}
		if err := unix.Fstatat(int(actual.Fd()), name, &actualStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return false, err
		}
		if expectedStat.Mode&unix.S_IFMT != actualStat.Mode&unix.S_IFMT || expectedStat.Mode&0777 != actualStat.Mode&0777 {
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
			same, compareErr := sameOpenedTree(expectedChild, actualChild)
			closeErr := errors.Join(expectedChild.Close(), actualChild.Close())
			if compareErr != nil || closeErr != nil {
				return false, errors.Join(compareErr, closeErr)
			}
			if !same {
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
			equal, compareErr := equalOpenedFiles(expectedFile, actualFile)
			closeErr := errors.Join(expectedFile.Close(), actualFile.Close())
			if compareErr != nil || closeErr != nil {
				return false, errors.Join(compareErr, closeErr)
			}
			if !equal {
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
		default:
			return false, nil
		}
	}
	return true, nil
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
