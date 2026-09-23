package executil

import (
	"runtime"
	"testing"
)

func TestEditorCommandUnknown(t *testing.T) {
	if _, err := editorCommand("notepad", "/tmp/x.go", 1); err == nil {
		t.Fatal("expected an error for an unknown editor")
	}
}

func TestEditorCommandArgs(t *testing.T) {
	for _, ed := range knownEditors {
		cmd, err := editorCommand(ed.ID, "/repo/main.go", 12)
		if err != nil {
			continue // not installed here
		}
		args := cmd.Args
		last := args[len(args)-1]
		if ed.jetbrains {
			if last != "/repo/main.go" || args[len(args)-2] != "12" || args[len(args)-3] != "--line" {
				t.Errorf("%s: args %v", ed.ID, args)
			}
		} else if runtime.GOOS != "darwin" || args[0] != "open" {
			if last != "/repo/main.go:12" || args[len(args)-2] != "-g" {
				t.Errorf("%s: args %v", ed.ID, args)
			}
		}
	}
}
