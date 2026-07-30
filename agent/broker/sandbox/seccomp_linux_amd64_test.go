//go:build linux && amd64

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestChildSeccompRejectsX32SyscallNumbers(t *testing.T) {
	filter := buildChildSeccompFilter(123, 123)
	found := false
	for index := 0; index+1 < len(filter); index++ {
		if filter[index].Code == unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K &&
			filter[index].K == x32SyscallBit &&
			filter[index+1].Code == unix.BPF_RET|unix.BPF_K &&
			filter[index+1].K == seccompErrnoOperation {
			found = true
		}
	}
	if !found {
		t.Fatal("seccomp filter does not reject x32 syscall numbers")
	}
}

func TestChildSeccompDeniesEveryCrossProcessRoute(t *testing.T) {
	filter := buildChildSeccompFilter(123, 123)
	for _, syscallNumber := range []uint32{
		unix.SYS_KILL,
		unix.SYS_TKILL,
		unix.SYS_TGKILL,
		unix.SYS_RT_SIGQUEUEINFO,
		unix.SYS_RT_TGSIGQUEUEINFO,
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
		if decision := evaluateSeccomp(filter, syscallNumber, 999); decision != seccompErrnoOperation {
			t.Errorf("syscall %d decision = %#x, want EPERM", syscallNumber, decision)
		}
	}
	if decision := evaluateSeccomp(filter, unix.SYS_TGKILL, 123); decision != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("own-process tgkill decision = %#x, want allow", decision)
	}
	if decision := evaluateSeccomp(filter, unix.SYS_GETPID, 0); decision != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("ordinary getpid decision = %#x, want allow", decision)
	}
}

func TestChildSeccompBlocksParentButAllowsOwnProcessGroup(t *testing.T) {
	if os.Getenv("CONCOURSE_SECCOMP_HELPER") == "1" {
		parent := os.Getppid()
		if err := installChildSeccomp(); err != nil {
			t.Fatal(err)
		}
		if err := unix.Kill(os.Getpid(), 0); err != nil {
			t.Fatalf("signal own process: %v", err)
		}
		if err := unix.Kill(0, 0); err != nil {
			t.Fatalf("signal own process group: %v", err)
		}
		if err := unix.Kill(parent, 0); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("signal parent error = %v, want EPERM", err)
		}
		if err := unix.PtraceAttach(parent); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("ptrace parent error = %v, want EPERM", err)
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestChildSeccompBlocksParentButAllowsOwnProcessGroup$")
	command.Env = append(os.Environ(), "CONCOURSE_SECCOMP_HELPER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seccomp helper: %v\n%s", err, output)
	}
}

func evaluateSeccomp(filter []unix.SockFilter, syscallNumber, argument0 uint32) uint32 {
	var accumulator uint32
	for pc := 0; pc < len(filter); {
		instruction := filter[pc]
		switch instruction.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			switch instruction.K {
			case seccompDataNR:
				accumulator = syscallNumber
			case seccompDataArch:
				accumulator = unix.AUDIT_ARCH_X86_64
			case seccompDataArgument0:
				accumulator = argument0
			default:
				panic("unexpected seccomp data offset")
			}
			pc++
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if accumulator == instruction.K {
				pc += int(instruction.Jt) + 1
			} else {
				pc += int(instruction.Jf) + 1
			}
		case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
			if accumulator&instruction.K != 0 {
				pc += int(instruction.Jt) + 1
			} else {
				pc += int(instruction.Jf) + 1
			}
		case unix.BPF_RET | unix.BPF_K:
			return instruction.K
		default:
			panic("unexpected seccomp instruction")
		}
	}
	panic("seccomp filter did not return")
}
