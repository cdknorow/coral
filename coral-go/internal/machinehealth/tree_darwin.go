package machinehealth

import (
	"context"

	"golang.org/x/sys/unix"
)

// childrenFunc reads the process table in one sysctl (kern.proc.all): pid and parent only.
func childrenFunc(context.Context) (func(int32) []int32, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	parents := make(map[int32]int32, len(procs))
	for i := range procs {
		parents[procs[i].Proc.P_pid] = procs[i].Eproc.Ppid
	}
	return childrenMap(parents), nil
}
