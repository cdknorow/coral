package executil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// Editor is a desktop code editor Coral can open a file in.
type Editor struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	jetbrains bool     // JetBrains launcher flags (--line N file) vs VS Code style (-g file:line)
	apps      []string // macOS app bundle names
	macBin    string   // VS Code style CLI inside the app bundle
	cli       string   // CLI name on PATH (all platforms)
}

var knownEditors = []Editor{
	{ID: "vscode", Name: "VS Code", apps: []string{"Visual Studio Code.app"}, macBin: "Contents/Resources/app/bin/code", cli: "code"},
	{ID: "cursor", Name: "Cursor", apps: []string{"Cursor.app"}, macBin: "Contents/Resources/app/bin/cursor", cli: "cursor"},
	{ID: "goland", Name: "GoLand", jetbrains: true, apps: []string{"GoLand.app"}, cli: "goland"},
	{ID: "idea", Name: "IntelliJ IDEA", jetbrains: true, apps: []string{"IntelliJ IDEA.app", "IntelliJ IDEA Ultimate.app", "IntelliJ IDEA CE.app"}, cli: "idea"},
	{ID: "pycharm", Name: "PyCharm", jetbrains: true, apps: []string{"PyCharm.app", "PyCharm Professional Edition.app", "PyCharm CE.app"}, cli: "pycharm"},
	{ID: "webstorm", Name: "WebStorm", jetbrains: true, apps: []string{"WebStorm.app"}, cli: "webstorm"},
}

// macAppDirs are where macOS apps are installed; JetBrains Toolbox installs
// into the user's ~/Applications.
func macAppDirs() []string {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"), filepath.Join(home, "Applications", "JetBrains Toolbox"))
	}
	return dirs
}

func (e Editor) macApp() string {
	for _, dir := range macAppDirs() {
		for _, app := range e.apps {
			p := filepath.Join(dir, app)
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return p
			}
		}
	}
	return ""
}

func (e Editor) cliPath() string {
	p, err := exec.LookPath(e.cli)
	if err != nil {
		return ""
	}
	return p
}

func (e Editor) installed() bool {
	if runtime.GOOS == "darwin" && e.macApp() != "" {
		return true
	}
	return e.cliPath() != ""
}

// InstalledEditors lists the known editors found on this machine.
func InstalledEditors() []Editor {
	var out []Editor
	for _, e := range knownEditors {
		if e.installed() {
			out = append(out, e)
		}
	}
	return out
}

// editorCommand builds the command that opens absPath (at line, when > 0) in
// the editor with the given id.
func editorCommand(id, absPath string, line int) (*exec.Cmd, error) {
	var ed *Editor
	for i := range knownEditors {
		if knownEditors[i].ID == id {
			ed = &knownEditors[i]
		}
	}
	if ed == nil {
		return nil, fmt.Errorf("unknown editor %q", id)
	}

	gotoArgs := []string{absPath}
	if ed.jetbrains {
		if line > 0 {
			gotoArgs = []string{"--line", strconv.Itoa(line), absPath}
		}
	} else {
		target := absPath
		if line > 0 {
			target += ":" + strconv.Itoa(line)
		}
		gotoArgs = []string{"-g", target}
	}

	if runtime.GOOS == "darwin" {
		if app := ed.macApp(); app != "" {
			if ed.jetbrains {
				// The launcher hands the file to an already running IDE.
				return exec.Command("open", append([]string{"-na", app, "--args"}, gotoArgs...)...), nil
			}
			if bin := filepath.Join(app, ed.macBin); fileExists(bin) {
				return exec.Command(bin, gotoArgs...), nil
			}
			return exec.Command("open", "-a", app, absPath), nil
		}
	}
	if cli := ed.cliPath(); cli != "" {
		return exec.Command(cli, gotoArgs...), nil
	}
	return nil, fmt.Errorf("%s is not installed", ed.Name)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// OpenInEditor opens absPath in the editor with the given id, at line when
// line > 0. It returns once the editor process has been started.
func OpenInEditor(id, absPath string, line int) error {
	cmd, err := editorCommand(id, absPath, line)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", id, err)
	}
	go cmd.Wait()
	return nil
}
