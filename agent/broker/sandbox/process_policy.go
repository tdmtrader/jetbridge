package sandbox

import (
	"fmt"
)

var deniedProcessSyscalls = []string{
	"kill",
	"tkill",
	"tgkill",
	"rt_sigqueueinfo",
	"rt_tgsigqueueinfo",
	"pidfd_send_signal",
	"ptrace",
	"process_vm_readv",
	"process_vm_writev",
	"pidfd_getfd",
	"kcmp",
	"prlimit64",
	"process_madvise",
	"process_mrelease",
	"get_robust_list",
	"setpriority",
	"ioprio_set",
	"sched_setaffinity",
	"sched_setscheduler",
	"sched_setparam",
	"sched_setattr",
	"migrate_pages",
	"move_pages",
	"perf_event_open",
	"pidfd_open",
}

func DeniedProcessSyscalls() []string {
	return append([]string(nil), deniedProcessSyscalls...)
}

func ValidateDumpability(value int, err error) error {
	if err != nil {
		return fmt.Errorf("broker sandbox: verify process dumpability: %w", err)
	}
	if value != 0 {
		return fmt.Errorf("broker sandbox: process remains dumpable")
	}
	return nil
}
