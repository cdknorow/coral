package routes

import (
	"strings"
	"testing"
)

func TestTerminalAttachPassesCommandAsArgument(t *testing.T) {
	command := `'/Applications/Coral "Test".app/Contents/MacOS/tmux' -S '/tmp/a b.sock' attach -t test`
	args := terminalAttachArgs(command)
	if len(args) != 4 || args[0] != "-e" || args[2] != "--" || args[3] != command {
		t.Fatalf("command not preserved as an argument: %#v", args)
	}
	if strings.Contains(args[1], command) || !strings.Contains(args[1], "item 1 of argv") {
		t.Fatal("attach command interpolated into AppleScript")
	}
}
