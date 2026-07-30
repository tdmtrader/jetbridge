//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const handledFilesystemAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
	unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
	unix.LANDLOCK_ACCESS_FS_MAKE_REG |
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
	unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
	unix.LANDLOCK_ACCESS_FS_REFER |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE

const readOnlyFilesystemAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_DIR

const writableFilesystemAccess = handledFilesystemAccess &^ unix.LANDLOCK_ACCESS_FS_EXECUTE

func Apply(policy Policy) error {
	policy.ReadOnlyPaths = append(policy.ReadOnlyPaths, ExistingEssentialReadPaths()...)
	canonical, err := policy.Canonical()
	if err != nil {
		return err
	}
	policy = canonical
	abi, err := landlockABI()
	if err := ValidateAvailability(abi, err); err != nil {
		return err
	}

	attribute := unix.LandlockRulesetAttr{Access_fs: handledFilesystemAccess}
	ruleset, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attribute)),
		unsafe.Sizeof(attribute),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("broker sandbox: create Landlock ruleset: %w", errno)
	}
	defer unix.Close(int(ruleset))

	if err := addPathRule(int(ruleset), policy.WritableRoot, writableFilesystemAccess); err != nil {
		return err
	}
	for _, path := range policy.ReadOnlyPaths {
		if err := addPathRule(int(ruleset), path, readOnlyFilesystemAccess); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("broker sandbox: set no_new_privs: %w", err)
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return fmt.Errorf("broker sandbox: restrict child filesystem: %w", errno)
	}
	return nil
}

func Exec(arguments []string) error {
	policy, binary, nativeArguments, err := ParseExecArgs(arguments)
	if err != nil {
		return err
	}
	resolvedBinary, err := canonicalExecutable(binary)
	if err != nil {
		return fmt.Errorf("broker sandbox: resolve harness binary: %w", err)
	}
	policy.ReadOnlyPaths = append(policy.ReadOnlyPaths, resolvedBinary)
	if err := Apply(policy); err != nil {
		return err
	}
	if err := installChildSeccomp(); err != nil {
		return err
	}
	return unix.Exec(resolvedBinary, append([]string{resolvedBinary}, nativeArguments...), os.Environ())
}

func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		unix.LANDLOCK_CREATE_RULESET_VERSION,
	)
	if errno != 0 {
		return 0, errno
	}
	return int(abi), nil
}

func addPathRule(ruleset int, path string, allowed uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("broker sandbox: open rule path %q: %w", path, err)
	}
	defer unix.Close(fd)
	attribute := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(ruleset),
		unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attribute)),
		0, 0, 0,
	)
	if errno != 0 {
		return fmt.Errorf("broker sandbox: add rule for %q: %w", path, errno)
	}
	return nil
}

func HardenBrokerProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("broker sandbox: disable process dumpability: %w", err)
	}
	value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	return ValidateDumpability(value, err)
}
