package machinehealth

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// childrenFunc reads /proc/<pid>/task/<tid>/children for the processes the walk visits only. A
// kernel without that file (CONFIG_PROC_CHILDREN off) gets the parents of every process from
// /proc/<pid>/stat instead: one small read each, no other work.
func childrenFunc(context.Context) (func(int32) []int32, error) {
	if _, err := os.Stat("/proc/self/task/" + strconv.Itoa(os.Getpid()) + "/children"); err == nil {
		return procChildren, nil
	}
	parents, err := procParents()
	if err != nil {
		return nil, err
	}
	return childrenMap(parents), nil
}

func procChildren(pid int32) []int32 {
	tasks, _ := filepath.Glob("/proc/" + strconv.Itoa(int(pid)) + "/task/*/children")
	var out []int32
	for _, t := range tasks {
		b, err := os.ReadFile(t)
		if err != nil {
			continue
		}
		for _, f := range bytes.Fields(b) {
			if c, err := strconv.Atoi(string(f)); err == nil {
				out = append(out, int32(c))
			}
		}
	}
	return out
}

// procParents is every process's parent, from the field after the command name in its stat.
func procParents() (map[int32]int32, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	parents := make(map[int32]int32, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: comm may hold spaces and parentheses, so go from the last ')'.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := bytes.Fields(b[i+1:])
		if len(f) < 2 {
			continue
		}
		if pp, err := strconv.Atoi(string(f[1])); err == nil {
			parents[int32(pid)] = int32(pp)
		}
	}
	return parents, nil
}
