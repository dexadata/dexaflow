package procgroup

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// StartTime returns when the process pid started, in clock ticks since boot
// (field 22 of /proc/<pid>/stat). Together with the pid it names one process:
// a pid the kernel reuses belongs to a process with a later start time.
func StartTime(pid int) (uint64, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// The command name (field 2) is in parentheses and may contain spaces or
	// parentheses, so fields are counted from the last ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state), so field 22 is fields[19].
	if len(fields) < 20 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return 0, fmt.Errorf("malformed start time in /proc/%d/stat", pid)
	}
	return start, nil
}
