//go:build !linux && !darwin

package machinehealth

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// childrenFunc reads every process's parent through gopsutil (Windows and the rest).
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
