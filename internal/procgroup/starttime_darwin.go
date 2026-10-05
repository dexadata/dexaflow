package procgroup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// StartTime returns when the process pid started, in microseconds since the
// epoch (kinfo_proc p_starttime). Together with the pid it names one process:
// a pid the kernel reuses belongs to a process with a later start time.
func StartTime(pid int) (uint64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	tv := kp.Proc.P_starttime
	if kp.Proc.P_pid != int32(pid) || tv.Sec <= 0 { //nolint:gosec // pids fit in int32 on darwin
		return 0, fmt.Errorf("no process %d", pid)
	}
	return uint64(tv.Sec)*1_000_000 + uint64(tv.Usec), nil //nolint:gosec // both are positive here
}
