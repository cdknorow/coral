//go:build windows

// winstop starts a program in a console of its own and, when told, stops it one of the ways Windows ends a program, then reports how it ended (#6921). It is the test rig of
// tests/stress/worktree_stops.sh, not part of Coral.
//
//	winstop -mode ctrlc|ctrlbreak|close|stdin|kill -go <file> -out <file> -log <file> -- <program> <args...>
//
// The program's output goes to -log. When the -go file appears, the program is stopped by -mode and one line is written to -out: how it ended and how long that took.
//
//	ctrlc      CTRL_C_EVENT to every process in the console (what Ctrl-C in a terminal does)
//	ctrlbreak  CTRL_BREAK_EVENT to the program's process group
//	close      WM_CLOSE to the console window (the window's close button: the program gets CTRL_CLOSE_EVENT)
//	stdin      the program's standard input closed (how the tray asks its server to stop)
//	kill       TerminateProcess (a forced end: Task Manager, taskkill /F, the tray after its 15 s)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	user32           = syscall.NewLazyDLL("user32.dll")
	procAllocConsole = kernel32.NewProc("AllocConsole")
	procFreeConsole  = kernel32.NewProc("FreeConsole")
	procSetCtrl      = kernel32.NewProc("SetConsoleCtrlHandler")
	procGenCtrl      = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procGetConWin    = kernel32.NewProc("GetConsoleWindow")
	procPostMessage  = user32.NewProc("PostMessageW")
)

const (
	ctrlC     = 0
	ctrlBreak = 1
	wmClose   = 0x0010
)

func main() {
	mode := flag.String("mode", "", "")
	goFile := flag.String("go", "", "")
	outFile := flag.String("out", "", "")
	logFile := flag.String("log", "", "")
	flag.Parse()
	args := flag.Args()
	if *mode == "" || *goFile == "" || *outFile == "" || len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: winstop -mode M -go F -out F -log F -- program args...")
		os.Exit(2)
	}
	// A console of this program's own, so what is sent to it reaches only the program started here.
	procFreeConsole.Call()
	if r, _, err := procAllocConsole.Call(); r == 0 {
		fmt.Fprintln(os.Stderr, "AllocConsole:", err)
	}

	lf, err := os.Create(*logFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = lf, lf
	if *mode == "ctrlbreak" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	}
	var stdin interface{ Close() error }
	if *mode == "stdin" {
		w, err := cmd.StdinPipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		stdin = w
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(2)
	}
	// Ignore Ctrl-C in this program only now: a process started after this call would inherit the setting and ignore it too.
	procSetCtrl.Call(0, 1)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		if _, err := os.Stat(*goFile); err == nil {
			break
		}
		select {
		case err := <-done:
			os.WriteFile(*outFile, []byte(fmt.Sprintf("exited before it was told to stop: %v\n", err)), 0o644)
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	t0 := time.Now()
	var how string
	switch *mode {
	case "ctrlc":
		r, _, err := procGenCtrl.Call(ctrlC, 0)
		how = fmt.Sprintf("GenerateConsoleCtrlEvent(CTRL_C_EVENT): %v %v", r != 0, errText(r, err))
	case "ctrlbreak":
		r, _, err := procGenCtrl.Call(ctrlBreak, uintptr(cmd.Process.Pid))
		how = fmt.Sprintf("GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT): %v %v", r != 0, errText(r, err))
	case "close":
		hwnd, _, _ := procGetConWin.Call()
		r, _, err := procPostMessage.Call(hwnd, wmClose, 0, 0)
		how = fmt.Sprintf("WM_CLOSE to the console window (hwnd set: %v): %v %v", hwnd != 0, r != 0, errText(r, err))
		select { // some consoles take the system menu's Close and not a posted WM_CLOSE
		case err := <-done:
			done <- err
		case <-time.After(3 * time.Second):
			procPostMessage.Call(hwnd, 0x0112, 0xF060, 0) // WM_SYSCOMMAND, SC_CLOSE
			how += " then WM_SYSCOMMAND SC_CLOSE after 3 s"
		}
	case "stdin":
		stdin.Close()
		how = "standard input closed"
	case "kill":
		cmd.Process.Kill()
		how = "TerminateProcess"
	}
	select {
	case err := <-done:
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		os.WriteFile(*outFile, []byte(fmt.Sprintf("ended: %s; exit code %d after %d ms\n", how, code, time.Since(t0).Milliseconds())), 0o644)
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		os.WriteFile(*outFile, []byte(fmt.Sprintf("did not end in 30 s: %s; killed by the rig\n", how)), 0o644)
	}
}

func errText(r uintptr, err error) string {
	if r != 0 || err == nil {
		return ""
	}
	return err.Error()
}
