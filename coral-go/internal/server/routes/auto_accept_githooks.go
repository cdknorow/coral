package routes

import "strings"

// Writes into the places git runs programs from are held by auto-accept (#186, Security's G4 on #86). Holding `git config core.hooksPath` is not enough if the same program can be
// run by writing a hook file: `echo evil > .git/hooks/pre-commit` runs it at the next commit, `Set-Content .husky/pre-commit evil` at the next one a husky install makes, and a
// `.git/config` or `.git/info/attributes` written by hand sets the same options `git config` would (core.fsmonitor, a filter driver). A write (a redirect, tee, a copy or move into the
// place, an in-place edit, a download's output, a link made there, `git config -f`, Set-Content/Add-Content/Out-File/New-Item, copy/move/xcopy/robocopy) into one of them asks, on every
// OS; reading them stays allowed.
//
// The places: .git/hooks/**, .git/config (and config.worktree), .git/info/attributes, .husky/** and .githooks/** at any depth. The folder core.hooksPath names in a repository's own
// config is not looked up (setting it is held, so only one that was already there matters): a known limit.

// gitHookPath says whether p names (or is inside) one of those places. It reads the path as text: either slash, any case, 8.3 GIT~1, a trailing dot or space and an NTFS stream name
// (hooks::$DATA) are folded away, so the spellings Windows treats as the same file are the same here.
func gitHookPath(p string) bool { return gitHookPlace(p, false) }

// gitHookPlace is gitHookPath, and with dest (the destination of a copy, a move or a link, or a folder something is created in) the hooks folders themselves count: copying
// into .git/hooks, .husky or .githooks puts a program there.
func gitHookPlace(p string, dest bool) bool {
	segs := strings.Split(strings.ReplaceAll(p, `\`, "/"), "/")
	for i := range segs {
		s := segs[i]
		if k := strings.IndexByte(s, ':'); k > 0 && !(k == 1 && i == 0) { // not a drive letter
			s = s[:k] // an NTFS alternate stream: the file itself
		}
		segs[i] = strings.ToLower(strings.TrimRight(s, ". "))
	}
	last := len(segs) - 1
	for last > 0 && segs[last] == "" {
		last--
	}
	for i, s := range segs {
		switch s {
		case ".husky", ".githooks":
			if i+1 < len(segs) && segs[i+1] != "" {
				return true
			}
			if dest && i == last {
				return true
			}
			// writing the folder itself (mkdir, New-Item -ItemType Directory) puts no program there
		case ".git", "git~1":
			if i+1 >= len(segs) {
				continue
			}
			switch next := segs[i+1]; next {
			case "hooks":
				if i+2 < len(segs) && segs[i+2] != "" {
					return true
				}
				if dest && i+1 == last {
					return true
				}
			case "config", "config.worktree":
				return true
			case "info":
				if i+2 < len(segs) && segs[i+2] == "attributes" {
					return true
				}
			}
		}
	}
	return false
}

func gitHookWhy(p string) string {
	return "it writes into a place git runs programs from (" + p + "): a hook or git's config"
}

// firstGitHookPath is the first of paths that is one of those places, or "".
func firstGitHookPath(paths []string) string { return firstGitHookPlace(paths, false) }

func firstGitHookPlace(paths []string, dest bool) string {
	for _, p := range paths {
		if gitHookPlace(p, dest) {
			return p
		}
	}
	return ""
}

// downloadOutputs are the files a download command writes to: curl's -o/--output, wget's -O/--output-document and -P/--directory-prefix, PowerShell's -OutFile; the option's value
// is the next word or glued on (-oFILE, --output=FILE, -OutFile:FILE).
func downloadOutputs(cmd string, args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		lower := strings.ToLower(a)
		next := func() {
			if i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
		}
		switch {
		case strings.HasPrefix(lower, "-outf") && strings.Contains(lower, ":") && strings.HasPrefix("-outfile", lower[:strings.IndexByte(lower, ':')]):
			out = append(out, a[strings.IndexByte(a, ':')+1:]) // -OutFile:path
		case isParam(a, "outfile", 4):
			next()
		case a == "-o" || lower == "--output" || lower == "--output-document" || lower == "--directory-prefix" || lower == "--output-dir" || (cmd == "wget" && (a == "-O" || a == "-P")):
			next()
		case strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--output-document=") || strings.HasPrefix(a, "--directory-prefix=") || strings.HasPrefix(a, "--output-dir="):
			out = append(out, a[strings.IndexByte(a, '=')+1:])
		case len(a) > 2 && ((cmd == "curl" && strings.HasPrefix(a, "-o")) || (cmd == "wget" && (strings.HasPrefix(a, "-O") || strings.HasPrefix(a, "-P")))):
			out = append(out, a[2:])
		}
	}
	return out
}

// posixHookWriteRisk is why a POSIX command writes into a place git runs programs from, or "": targets are its redirect targets, cmd/args the command.
func posixHookWriteRisk(cmd string, args, targets []string, at func([]string) []string) string {
	if p := firstGitHookPath(at(targets)); p != "" {
		return gitHookWhy(p)
	}
	var written []string
	dest := false
	switch cmd {
	case "tee", "cp", "mv", "install", "ln", "dd":
		written, dest = writeTargets(cmd, args), true
	case "sed":
		if files, inPlace := sedInPlaceFiles(args); inPlace {
			written = files
		}
	case "perl":
		if files, inPlace := perlInPlaceFiles(args); inPlace {
			written = files
		}
	case "curl", "wget":
		written = downloadOutputs(cmd, args)
	case "touch", "truncate":
		// creates or empties a file: no program arrives, but a hook that exists is emptied; not a write of one
	}
	if p := firstGitHookPlace(at(written), dest); p != "" {
		return gitHookWhy(p)
	}
	return ""
}

// winHookWriteRisk is the same for a PowerShell or cmd stage: name is the lower-case verb, args its arguments after it and paths the path-like ones (winPaths).
func winHookWriteRisk(name string, args, paths []string) string {
	switch name {
	case "new-item", "ni":
		// -Path <folder> -Name <file> makes <folder>\<file>: with a -Name the path is the folder it is made in.
		named := false
		for _, a := range args {
			named = named || isParam(a, "name", 1)
		}
		if p := firstGitHookPlace(paths, named); p != "" {
			return gitHookWhy(p)
		}
	case "set-content", "sc", "add-content", "ac", "out-file", "set-item", "si", "tee-object", "export-csv", "export-clixml", "rename-item", "rni", "ren", "rename":
		if p := firstGitHookPath(paths); p != "" {
			return gitHookWhy(p)
		}
	case "copy-item", "cpi", "cp", "copy", "xcopy", "robocopy", "move-item", "mi", "mv", "move":
		if len(paths) > 0 {
			if p := firstGitHookPlace(paths[len(paths)-1:], true); p != "" { // the destination
				return gitHookWhy(p)
			}
		}
		if name == "robocopy" && len(paths) > 1 {
			if p := firstGitHookPlace(paths[1:2], true); p != "" {
				return gitHookWhy(p)
			}
		}
	case "invoke-webrequest", "iwr", "invoke-restmethod", "irm", "curl", "wget":
		if p := firstGitHookPath(downloadOutputs(name, args)); p != "" {
			return gitHookWhy(p)
		}
	case "mklink":
		if p := firstGitHookPath(paths); p != "" {
			return gitHookWhy(p)
		}
	}
	return ""
}
