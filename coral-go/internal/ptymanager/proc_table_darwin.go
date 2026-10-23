//go:build darwin

package ptymanager

import "golang.org/x/sys/unix"

// scanProcTable is the whole process table from one kern.proc.all call: each process's parent and start time, in the form
// gopsutil's Ppid and CreateTime give (so a member found here equals one found by process.NewProcess). gopsutil's own
// Processes() asked the kernel again for every process, several times, per look (#6562: 58% of the server's CPU at 20 agents).
func scanProcTable() (map[int]procInfo, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	table := make(map[int]procInfo, len(ks))
	for i := range ks {
		k := &ks[i]
		table[int(k.Proc.P_pid)] = procInfo{ppid: int(k.Eproc.Ppid), created: k.Proc.P_starttime.Sec*1000 + int64(k.Proc.P_starttime.Usec)/1000}
	}
	return table, nil
}
