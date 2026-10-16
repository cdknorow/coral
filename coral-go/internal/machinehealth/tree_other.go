//go:build !linux && !darwin && !windows

package machinehealth

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// childrenFunc reads every process's parent through gopsutil (the BSDs and the rest; Windows has tree_windows.go).
func childrenFunc(ctx context.Context) (func(int32) []int32, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	parents := make(map[int32]int32, len(procs))
	for _, p := range procs {
		if pp, err := p.PpidWithContext(ctx); err == nil {
			parents[p.Pid] = pp
		}
	}
	return childrenMap(parents), nil
}
