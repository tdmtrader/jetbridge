//go:build linux && amd64

package sandbox

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	seccompDataNR         = 0
	seccompDataArch       = 4
	seccompDataArgument0  = 16
	seccompErrnoOperation = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	x32SyscallBit         = 0x40000000
)

func installChildSeccomp() error {
	filter := buildChildSeccompFilter(uint32(os.Getpid()), uint32(unix.Getpgrp()))
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("broker sandbox: set seccomp no_new_privs: %w", err)
	}
	if err := unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	); err != nil {
		return fmt.Errorf("broker sandbox: install child seccomp filter: %w", err)
	}
	return nil
}

func buildChildSeccompFilter(processID, processGroup uint32) []unix.SockFilter {
	filter := []unix.SockFilter{
		statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArch),
		jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AUDIT_ARCH_X86_64, 1, 0),
		statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_KILL_PROCESS),
		statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataNR),
		jump(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, x32SyscallBit, 0, 1),
		statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
	}

	conditional := map[uint32][]unix.SockFilter{
		unix.SYS_KILL: {
			statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArgument0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, processID, 3, 0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, 0, 2, 0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(-int32(processGroup)), 1, 0),
			statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
			statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
		},
		unix.SYS_TGKILL: {
			statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArgument0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, processID, 1, 0),
			statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
			statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
		},
		unix.SYS_RT_SIGQUEUEINFO: {
			statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArgument0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, processID, 1, 0),
			statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
			statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
		},
		unix.SYS_RT_TGSIGQUEUEINFO: {
			statement(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArgument0),
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, processID, 1, 0),
			statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
			statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW),
		},
	}
	for _, syscallNumber := range []uint32{
		unix.SYS_KILL,
		unix.SYS_TGKILL,
		unix.SYS_RT_SIGQUEUEINFO,
		unix.SYS_RT_TGSIGQUEUEINFO,
	} {
		rule := conditional[syscallNumber]
		filter = append(filter, jump(
			unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K,
			syscallNumber,
			0,
			uint8(len(rule)),
		))
		filter = append(filter, rule...)
	}
	for _, syscallNumber := range []uint32{
		unix.SYS_TKILL,
		unix.SYS_PIDFD_SEND_SIGNAL,
		unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_PIDFD_GETFD,
		unix.SYS_KCMP,
		unix.SYS_PRLIMIT64,
		unix.SYS_PROCESS_MADVISE,
		unix.SYS_PROCESS_MRELEASE,
		unix.SYS_GET_ROBUST_LIST,
		unix.SYS_SETPRIORITY,
		unix.SYS_IOPRIO_SET,
		unix.SYS_SCHED_SETAFFINITY,
		unix.SYS_SCHED_SETSCHEDULER,
		unix.SYS_SCHED_SETPARAM,
		unix.SYS_SCHED_SETATTR,
		unix.SYS_MIGRATE_PAGES,
		unix.SYS_MOVE_PAGES,
		unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_PIDFD_OPEN,
	} {
		filter = append(filter,
			jump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, syscallNumber, 0, 1),
			statement(unix.BPF_RET|unix.BPF_K, seccompErrnoOperation),
		)
	}
	filter = append(filter, statement(unix.BPF_RET|unix.BPF_K, unix.SECCOMP_RET_ALLOW))
	return filter
}

func statement(code uint16, value uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: value}
}

func jump(code uint16, value uint32, onTrue, onFalse uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: onTrue, Jf: onFalse, K: value}
}
