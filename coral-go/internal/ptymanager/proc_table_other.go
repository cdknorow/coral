//go:build !windows && !darwin

package ptymanager

import "github.com/shirou/gopsutil/v4/process"

// scanProcTable is the process table: each process's parent and start time, read from /proc by gopsutil without its per-pid
// existence check (a process that ends mid-scan is just left out).
func scanProcTable() (map[int]procInfo, error) {
	pids, err := process.Pids()
	if err != nil {
		return nil, err
	}
	table := make(map[int]procInfo, len(pids))
	for _, pid := range pids {
		p := &process.Process{Pid: pid}
		pp, err := p.Ppid()
		if err != nil {
			continue
		}
		created, err := p.CreateTime()
		if err != nil {
			continue
		}
		table[int(pid)] = procInfo{ppid: int(pp), created: created}
	}
	return table, nil
}
