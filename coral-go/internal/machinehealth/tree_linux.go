package machinehealth

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"sync"
)

// childrenFunc reads /proc/<pid>/task/<tid>/children for the processes the walk visits only. A
// kernel without that file (CONFIG_PROC_CHILDREN off) gets the parents of every process from
// /proc/<pid>/stat instead: one small read each, no other work.
func childrenFunc(context.Context) (func(int32) []int32, error) {
	if _, err := os.Stat(procRoot + "/self/task/" + strconv.Itoa(os.Getpid()) + "/children"); err == nil {
		return procChildren, nil
	}
	parents, err := procParents()
	if err != nil {
		return nil, err
	}
	return childrenMap(parents), nil
}

// procRoot is where the process files are (a test points it at a fake tree).
var procRoot = "/proc"

// procBuf is the one read buffer the walk and the stat reads reuse (#4455: a glob and a fresh
// buffer per file cost most of a sample, 9% of a busy server's CPU while machine health was open).
var procBuf = struct {
	sync.Mutex
	b []byte
}{b: make([]byte, 0, 4096)}

// readProcFile reads path into the shared buffer and calls use with its bytes, which are valid only
// during the call.
func readProcFile(path string, use func([]byte)) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	procBuf.Lock()
	defer procBuf.Unlock()
	b := procBuf.b[:0]
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		n, err := f.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			break
		}
	}
	procBuf.b = b
	use(b)
	return true
}

// procChildren is every child of pid, from each of its threads' children file (a child started from
// another thread is listed only there). The task folder is listed by name, without a glob.
func procChildren(pid int32) []int32 {
	dir := procRoot + "/" + strconv.Itoa(int(pid)) + "/task"
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	tids, _ := d.Readdirnames(-1)
	d.Close()
	var out []int32
	for _, tid := range tids {
		readProcFile(dir+"/"+tid+"/children", func(b []byte) {
			for len(b) > 0 {
				var f []byte
				if i := bytes.IndexByte(b, ' '); i >= 0 {
					f, b = b[:i], b[i+1:]
				} else {
					f, b = b, nil
				}
				if c, err := strconv.Atoi(string(bytes.TrimSpace(f))); err == nil {
					out = append(out, int32(c))
				}
			}
		})
	}
	return out
}

// clockTicks is the kernel's USER_HZ, the unit of utime and stime in stat (100 on Linux; gopsutil
// assumes the same).
const clockTicks = 100

// procUsage is a process's CPU seconds (utime + stime) and resident bytes (rss pages), from one read
// of /proc/<pid>/stat: the same files and fields gopsutil's Times and MemoryInfo read separately.
func procUsage(_ context.Context, pid int32) (cpu float64, rss uint64, ok bool) {
	readProcFile(procRoot+"/"+strconv.Itoa(int(pid))+"/stat", func(b []byte) {
		// pid (comm) state ppid ...: comm may hold spaces and parentheses, so go from the last ')'.
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			return
		}
		f := bytes.Fields(b[i+1:])
		if len(f) < 22 { // utime is f[11], stime f[12], rss f[21] (stat fields 14, 15 and 24)
			return
		}
		ut, err1 := strconv.ParseUint(string(f[11]), 10, 64)
		st, err2 := strconv.ParseUint(string(f[12]), 10, 64)
		pages, err3 := strconv.ParseInt(string(f[21]), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return
		}
		cpu = float64(ut+st) / clockTicks
		if pages > 0 {
			rss = uint64(pages) * uint64(os.Getpagesize())
		}
		ok = true
	})
	return cpu, rss, ok
}

// procParents is every process's parent, from the field after the command name in its stat.
func procParents() (map[int32]int32, error) {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	parents := make(map[int32]int32, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(procRoot + "/" + e.Name() + "/stat")
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
