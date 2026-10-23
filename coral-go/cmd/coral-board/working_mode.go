package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/subgentic/coral/internal/clierr"
	"net/url"
	"os"
)

func cmdWorkingMode(args []string) {
	if err := runWorkingMode(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		clierr.Exit1()
	}
}

// workingModeFlags is a working-mode command line.
type workingModeFlags struct {
	board, custom, mode, create, edit, reset, name, instructions, file string
	guidance, presets                                                  bool
	seen                                                               map[string]bool // the flags given
}

func runWorkingMode(args []string) error {
	f, err := parseWorkingModeFlags(args)
	if err != nil {
		return err
	}
	if err := checkWorkingModeFlags(f.seen); err != nil {
		return err
	}
	project := f.board
	if project == "" {
		if st := loadState(); st != nil {
			project = st.Project
		}
	}
	if project == "" {
		return fmt.Errorf("working-mode requires --board outside a joined board")
	}
	request := func(method, target string, body any) ([]byte, error) {
		data, status, err := apiCallRaw(method, target, body)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("working-mode: %s", clierr.Describe(nil, status, data))
		}
		return data, nil
	}
	method, path, body, err := workingModeRequest(f, project)
	if err != nil {
		return err
	}
	if body == nil && method == "PUT" { // a selection: the current mode, with the flags' changes
		if body, err = updatedWorkingMode(f, func() ([]byte, error) { return request("GET", path, nil) }); err != nil {
			return err
		}
	}
	data, err := request(method, path, body)
	if err != nil {
		return err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

// parseWorkingModeFlags reads working-mode's flags, noting which were given.
func parseWorkingModeFlags(args []string) (workingModeFlags, error) {
	var f workingModeFlags
	fs := flag.NewFlagSet("working-mode", flag.ContinueOnError)
	fs.StringVar(&f.board, "board", "", "Team board (defaults to current board)")
	fs.StringVar(&f.custom, "custom-instructions", "", "Custom team rules; empty clears them")
	fs.StringVar(&f.mode, "mode", "", "Select a built-in or custom workflow preset")
	fs.BoolVar(&f.guidance, "dependency-guidance", false, "Include guidance about dependent tasks")
	fs.BoolVar(&f.presets, "presets", false, "List presets and effective team instructions")
	fs.StringVar(&f.create, "create", "", "Create custom preset ID")
	fs.StringVar(&f.edit, "edit", "", "Edit preset ID (built-ins become team-local overrides)")
	fs.StringVar(&f.reset, "reset", "", "Reset built-in preset ID to shipped instructions")
	fs.StringVar(&f.name, "name", "", "Custom preset display name")
	fs.StringVar(&f.instructions, "instructions", "", "Preset instructions; empty is allowed")
	fs.StringVar(&f.file, "instructions-file", "", "Read preset instructions from a UTF-8 file")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("unexpected working-mode arguments")
	}
	f.seen = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.seen[fl.Name] = true })
	return f, nil
}

// checkWorkingModeFlags: one preset operation (list, create, edit, reset) or an update of the selected mode, never
// both; preset content only with --create or --edit, which need it; instructions inline or from a file, not both.
func checkWorkingModeFlags(seen map[string]bool) error {
	operations := 0
	for _, key := range []string{"presets", "create", "edit", "reset"} {
		if seen[key] {
			operations++
		}
	}
	selection := seen["mode"] || seen["custom-instructions"] || seen["dependency-guidance"]
	if operations > 1 || operations > 0 && selection {
		return fmt.Errorf("use one preset operation or update the selected working mode")
	}
	editing := seen["create"] || seen["edit"]
	if !editing && (seen["name"] || seen["instructions"] || seen["instructions-file"]) {
		return fmt.Errorf("preset content requires --create or --edit")
	}
	if seen["instructions"] && seen["instructions-file"] {
		return fmt.Errorf("use --instructions or --instructions-file, not both")
	}
	if editing && !seen["instructions"] && !seen["instructions-file"] {
		return fmt.Errorf("--create and --edit require --instructions or --instructions-file")
	}
	return nil
}

// workingModeRequest is the request the flags make on project's working mode: GET it (or its presets), POST a new
// preset or PUT an edited one (instructions inline or read from the file), POST a built-in's reset, or PUT the
// selected mode updated (its body nil here: updatedWorkingMode fills it from the current one).
func workingModeRequest(f workingModeFlags, project string) (method, path string, body any, err error) {
	path = "/" + url.PathEscape(project) + "/working-mode"
	method = "GET"
	if f.presets {
		path += "/presets"
	}
	switch {
	case f.seen["create"] || f.seen["edit"]:
		instructions := f.instructions
		if f.seen["instructions-file"] {
			data, err := os.ReadFile(f.file)
			if err != nil {
				return "", "", nil, err
			}
			instructions = string(data)
		}
		body = map[string]string{"id": f.create, "name": f.name, "instructions": instructions}
		path += "/presets"
		method = "POST"
		if f.seen["edit"] {
			method = "PUT"
			path += "/" + url.PathEscape(f.edit)
		}
	case f.seen["reset"]:
		method = "POST"
		path += "/presets/" + url.PathEscape(f.reset) + "/reset"
	case f.seen["mode"] || f.seen["custom-instructions"] || f.seen["dependency-guidance"]:
		method = "PUT"
	}
	return method, path, body, nil
}

// updatedWorkingMode is the current working mode (read by get) with the flags' mode, custom instructions and
// dependency guidance in place, and without its computed instructions.
func updatedWorkingMode(f workingModeFlags, get func() ([]byte, error)) (map[string]any, error) {
	data, err := get()
	if err != nil {
		return nil, err
	}
	var current map[string]any
	if err := json.Unmarshal(data, &current); err != nil {
		return nil, err
	}
	if f.seen["mode"] {
		current["mode"] = f.mode
	}
	if f.seen["custom-instructions"] {
		current["custom_instructions"] = f.custom
	}
	if f.seen["dependency-guidance"] {
		current["dependency_guidance"] = f.guidance
	}
	delete(current, "instructions")
	return current, nil
}
