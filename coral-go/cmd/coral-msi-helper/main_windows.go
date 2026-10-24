//go:build windows

// coral-msi-helper is the MSI's custom-action program (#335). It is carried in the MSI's Binary table and run by Windows Installer from a temporary
// copy, as the person installing (immediate actions: impersonated, never SYSTEM); it is never installed. Its argv is fixed by tools/coral.wxs.
//
//	coral-msi-helper stop  --folder DIR [--folder DIR] [--wait SECONDS]
//	coral-msi-helper start
//
// stop, once the person has allowed the install (for an install before its files-in-use check, for an uninstall after it): closes this
// user's Coral the normal way. The processes it considers are this user's, in this session, whose program is in one of the Coral folders, each checked on its own handle. Each tray among them (a coral-tray that is not
// a server child: no --server-child on its command line) is asked to quit with its quit event (internal/trayevent): it then quits as its
// menu's Quit does, agents marked to wake. A tray that has no quit event (older than #335), or hasn't ended a while after being asked, is
// ended as coral-tray --stop always ended it on Windows: that process alone, through the same handle it was checked on, after which its
// server child stops the normal way, its standard input closed. A Coral window (coral-app) still open then gets WM_CLOSE. Then it waits for
// every such process to end. It never ends a server child, an agent, or another account's or session's process. When it found a tray it
// marks Coral to be started again (internal/installrestart). It never removes a mark: a second stop in the same install finds nothing.
//
// start, after InstallFinalize: when stop marked Coral, and this user's shell is running in this session, it starts coral-tray.exe from the
// folder of the product Windows Installer registered per machine (never a folder from its command line or a property)
// through explorer.exe (by its full path), which hands it to Windows' unelevated Explorer factory: Coral runs with the signed-in person's own
// token and environment, as the Start Menu would start it, never the installer's.
//
// It always exits 0: the custom actions ignore its exit code. What it did goes to a new coral-msi-helper-<random>.log in this user's
// temporary folder.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/installrestart"
	"github.com/subgentic/coral/internal/msiproduct"
	"github.com/subgentic/coral/internal/trayevent"
)

func main() {
	closeLog := openLog()
	defer closeLog()
	if len(os.Args) < 2 {
		log.Printf("usage: coral-msi-helper stop --folder DIR... | start")
		return
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var folders multiFlag
	fs.Var(&folders, "folder", "a Coral install folder")
	wait := fs.Int("wait", 60, "seconds to wait for Coral to end")
	// stop takes the folders to look in; start takes none: it starts Coral from the registered product, never from a folder on its command line.
	if err := fs.Parse(os.Args[2:]); err != nil || (len(folders) == 0) != (os.Args[1] == "start") || fs.NArg() != 0 {
		log.Printf("%s: bad arguments", os.Args[1])
		return
	}
	me, err := currentIdentity()
	if err != nil {
		log.Printf("this user: %v", err)
		return
	}
	switch os.Args[1] {
	case "stop":
		stop(me, folders, time.Duration(*wait)*time.Second)
	case "start":
		start(me)
	default:
		log.Printf("unknown mode %q", os.Args[1])
	}
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ";") }
func (m *multiFlag) Set(v string) error {
	if !filepath.IsAbs(v) {
		return errors.New("not an absolute folder")
	}
	*m = append(*m, finalPath(filepath.Clean(v)))
	return nil
}

// finalPath is dir as Windows resolves it (links followed, long names), when it exists; else dir.
func finalPath(dir string) string {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return dir
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return dir
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || n == 0 || int(n) > len(buf) {
		return dir
	}
	s := windows.UTF16ToString(buf[:n])
	if rest, ok := strings.CutPrefix(s, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	return strings.TrimPrefix(s, `\\?\`)
}

// openLog sends the log to a new coral-msi-helper-<random>.log in this user's temporary folder, and standard error. A fixed name in a folder
// the user can write is a link an elevated run could be made to write through: a new file with a random name is created, never opened
// (Security on #335). The first line says whether this run's token is elevated.
func openLog() func() {
	log.SetFlags(log.LstdFlags)
	f, err := os.CreateTemp(os.TempDir(), "coral-msi-helper-*.log")
	if err != nil {
		return func() {}
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("coral-msi-helper %s (elevated: %v)", strings.Join(os.Args[1:], " "), windows.GetCurrentProcessToken().IsElevated())
	return func() { f.Close() }
}

// identity is a user in a session: this process's, or a process's from its token.
type identity struct {
	user    *windows.SID
	session uint32
}

func (a identity) is(b identity) bool {
	return a.user != nil && b.user != nil && a.user.Equals(b.user) && a.session == b.session
}

func currentIdentity() (identity, error) {
	return tokenIdentity(windows.GetCurrentProcessToken())
}

func tokenIdentity(tok windows.Token) (identity, error) {
	u, err := tok.GetTokenUser()
	if err != nil {
		return identity{}, err
	}
	sid, err := u.User.Sid.Copy()
	if err != nil {
		return identity{}, err
	}
	var session, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4, &n); err != nil {
		return identity{}, err
	}
	return identity{user: sid, session: session}, nil
}

// proc is a process, as its handle describes it.
type proc struct {
	pid, parent uint32
	image       string // its program's full path
	cmdline     string // its command line ("" when it can't be read)
	who         identity
}

func (p proc) name() string { return strings.ToLower(filepath.Base(p.image)) }

// isServerChild: a coral-tray started as the tray's server child (cmd/coral-tray/child.go).
func (p proc) isServerChild() bool {
	for _, f := range strings.Fields(p.cmdline) {
		f = strings.Trim(f, `"`)
		if f == "--server-child" || f == "-server-child" || strings.HasPrefix(f, "--server-child=") || strings.HasPrefix(f, "-server-child=") {
			return true
		}
	}
	return false
}

// isTray: a coral-tray whose command line could be read and is not a server child's.
func (p proc) isTray() bool {
	return p.name() == "coral-tray.exe" && p.cmdline != "" && !p.isServerChild()
}

// inspect reads what the process behind h is: its program, user, session and command line, all from that handle.
func inspect(h windows.Handle, pid, parent uint32) (proc, bool) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) != nil {
		return proc{}, false
	}
	var tok windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok) != nil {
		return proc{}, false
	}
	who, err := tokenIdentity(tok)
	tok.Close()
	if err != nil {
		return proc{}, false
	}
	return proc{pid: pid, parent: parent, image: windows.UTF16ToString(buf[:size]), cmdline: commandLine(h), who: who}, true
}

// commandLine is the process's command line ("" when it can't be read).
func commandLine(h windows.Handle) string {
	buf := make([]byte, 64<<10)
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n); err != nil {
		return ""
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Buffer == nil || us.Length == 0 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice(us.Buffer, us.Length/2))
}

// under reports whether path is in one of folders (case-insensitive, as Windows compares paths; never a folder that only starts the same).
func under(path string, folders []string) bool {
	p := strings.ToLower(filepath.Clean(path))
	for _, f := range folders {
		f = strings.ToLower(filepath.Clean(f))
		if strings.HasPrefix(p, f+`\`) {
			return true
		}
	}
	return false
}

// held is a process kept open from its check to whatever is done to it, so a pid reused meanwhile is never the one acted on.
type held struct {
	proc
	h windows.Handle
}

// list returns the processes that keep matches, each held open with access (and always the right to query it); the caller closes them.
func list(access uint32, keep func(proc) bool) ([]held, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	self := uint32(os.Getpid())
	var out []held
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == 0 || e.ProcessID == self {
			continue
		}
		h, oerr := windows.OpenProcess(access|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if oerr != nil {
			continue // not this user's to open
		}
		p, ok := inspect(h, e.ProcessID, e.ParentProcessID)
		if !ok || !keep(p) {
			windows.CloseHandle(h)
			continue
		}
		out = append(out, held{proc: p, h: h})
	}
	return out, nil
}

func closeAll(hs []held) {
	for _, h := range hs {
		windows.CloseHandle(h.h)
	}
}

// coralOf keeps the processes of me whose program is in folders.
func coralOf(me identity, folders []string) func(proc) bool {
	return func(p proc) bool { return p.who.is(me) && under(p.image, folders) }
}

func stop(me identity, folders []string, wait time.Duration) {
	procs, err := list(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, coralOf(me, folders))
	if err != nil {
		log.Printf("stop: the processes: %v", err)
		return
	}
	defer closeAll(procs)
	var trays []held
	for _, p := range procs {
		if p.isTray() {
			trays = append(trays, p)
		}
	}
	// A mark is only ever added here, never removed: a run that finds no tray may be a second one inside the same install, after the first
	// closed Coral (the old package's uninstall in an upgrade, #335 rehearsal step c). start takes the mark, and an old one starts nothing.
	if len(trays) == 0 {
		if len(procs) == 0 {
			log.Printf("stop: no Coral of this user is running")
			return
		}
	} else if err := installrestart.Mark(time.Now()); err != nil {
		log.Printf("stop: Coral won't be started again after the install: %v", err)
	}
	for _, t := range trays {
		quitTray(me, folders, t, wait)
	}
	// A Coral window still open (its tray ended the old way): close it as its close button would.
	apps, _ := list(0, func(p proc) bool { return coralOf(me, folders)(p) && p.name() == "coral-app.exe" })
	for _, a := range apps {
		log.Printf("stop: asking the Coral window %d to close", a.pid)
		closeWindows(a.pid)
	}
	closeAll(apps)
	waitGone(me, folders, wait)
}

// quitTray asks the tray t to quit with its quit event and gives it quitGrace (the whole --wait, so agents get marked to wake) to end; a tray with no event, or still running then, is ended
// as coral-tray --stop always ended it, through its handle, after checking again on that handle that it is still this user's tray.
func quitTray(me identity, folders []string, t held, quitGrace time.Duration) {
	err := trayevent.Signal(trayevent.Name(trayevent.Quit, me.user, int(t.pid)), me.user)
	if err == nil {
		log.Printf("stop: asked tray %d to quit", t.pid)
		if ev, werr := windows.WaitForSingleObject(t.h, uint32(quitGrace/time.Millisecond)); werr == nil && ev == windows.WAIT_OBJECT_0 {
			log.Printf("stop: tray %d quit", t.pid)
			return
		}
		log.Printf("stop: tray %d did not quit within %s: ending it as coral-tray --stop does", t.pid, quitGrace)
	} else {
		log.Printf("stop: tray %d has no quit event (%v): ending it as coral-tray --stop does; its server stops the normal way", t.pid, err)
	}
	again, ok := inspect(t.h, t.pid, t.parent)
	if !ok || !again.who.is(me) || !under(again.image, folders) || !again.isTray() {
		log.Printf("stop: tray %d is no longer this user's Coral tray: left alone", t.pid)
		return
	}
	if err := windows.TerminateProcess(t.h, 0); err != nil {
		log.Printf("stop: tray %d: %v", t.pid, err)
	}
}

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW             = user32.NewProc("PostMessageW")
)

// closeWindows posts WM_CLOSE to every top-level window owned by pid.
func closeWindows(pid uint32) {
	const wmClose = 0x0010
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var owner uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner == pid {
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
}

// waitGone waits up to wait for every process of me in folders to end, and says what is left.
func waitGone(me identity, folders []string, wait time.Duration) {
	end := time.Now().Add(wait)
	for {
		procs, err := list(0, coralOf(me, folders))
		closeAll(procs)
		if err == nil && len(procs) == 0 {
			log.Printf("stop: Coral has closed")
			return
		}
		if time.Now().After(end) {
			var left []string
			for _, p := range procs {
				left = append(left, fmt.Sprintf("%s (%d)", p.name(), p.pid))
			}
			log.Printf("stop: still running after %s: %s", wait, strings.Join(left, ", "))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// installedFolder is the folder of the product Windows Installer registered per machine, under Program Files, as the updater finds it
// (#147); a variable so a test can replace it.
var installedFolder = func() (string, error) {
	product, err := msiproduct.Registered(msiproduct.RelatedProducts, msiproduct.MachineProperty)
	if err != nil {
		return "", err
	}
	dir, err := msiproduct.Folder(product)
	if err != nil {
		return "", err
	}
	if why := msiproduct.Unsupported(dir); why != "" {
		return "", errors.New(why)
	}
	return dir, nil
}

func start(me identity) {
	if !installrestart.Take(time.Now()) {
		log.Printf("start: Coral was not running before the install: nothing to start")
		return
	}
	folder, err := installedFolder()
	if err != nil {
		log.Printf("start: the installed Coral: %v", err)
		return
	}
	tray := filepath.Join(folder, "coral-tray.exe")
	if fi, err := os.Lstat(tray); err != nil || !fi.Mode().IsRegular() {
		log.Printf("start: no coral-tray.exe in the install folder")
		return
	}
	// A Coral already running (started some other way meanwhile, as the updater's own start does) is not started twice.
	running, _ := list(0, func(p proc) bool { return coralOf(me, []string{folder})(p) && p.isTray() })
	closeAll(running)
	if len(running) > 0 {
		log.Printf("start: Coral is already running: nothing to start")
		return
	}
	win, err := windows.GetWindowsDirectory()
	if err != nil {
		log.Printf("start: the Windows folder: %v", err)
		return
	}
	explorer := filepath.Join(win, "explorer.exe")
	// Only through this user's running shell: with none (another account installing, a session with no Explorer) nothing starts, and
	// explorer.exe is never started here as a new shell.
	shells, _ := list(0, func(p proc) bool { return p.who.is(me) && strings.EqualFold(p.image, explorer) })
	closeAll(shells)
	if len(shells) == 0 {
		log.Printf("start: this user's shell is not running in this session: Coral is not started (start it from the Start Menu)")
		return
	}
	cmd := exec.Command(explorer, tray)
	cmd.Dir = folder
	if err := cmd.Start(); err != nil {
		log.Printf("start: %v", err)
		return
	}
	_ = cmd.Process.Release()
	log.Printf("start: Coral started again through explorer.exe")
}
