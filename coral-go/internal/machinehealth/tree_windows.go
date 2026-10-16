//go:build windows

package machinehealth

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// childrenFunc reads every process's parent from ONE process snapshot (#4011). gopsutil's
// Ppid took a full snapshot for each process (N snapshots of N processes per walk), so a
// walk on a busy machine took seconds: the machine-health sample cost that much CPU, and
// TestDescendantsFindsGrandchildren timed out on the Windows runner.
func childrenFunc(_ context.Context) (func(int32) []int32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	parents := map[int32]int32{}
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		parents[int32(pe.ProcessID)] = int32(pe.ParentProcessID)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return childrenMap(parents), nil
}
