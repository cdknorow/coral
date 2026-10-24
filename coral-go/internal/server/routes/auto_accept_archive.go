package routes

import (
	"strings"
)

// Archive and sync commands write many files from one command line (#7106): tar -x and unzip put the members in a folder (the one -C or -d names, else the folder they run in), whatever paths the
// archive holds, and tar -c, zip, 7z a, gzip and rsync write or replace the files they are given. Each is judged by the places it writes to: outside the working folder, or a place that cannot be read,
// asks the person. What an archive's members are named is not read: a member with a .. or an absolute path (tar -P) can leave the folder, so tar -x with -P, and 7z x with -spf, ask too.

func archiveOutside(targets []string, at func([]string) []string, workDir string) string {
	if t := outsideTarget(at(notDevices(targets)), workDir); t != "" {
		return "it writes outside the working directory (" + t + ")"
	}
	return ""
}

// tarRisk judges tar and bsdtar: the archive it creates or appends to (-f FILE with -c, -r, -u, or --delete), the folder it extracts into (-C DIR, else the folder it runs in), and -P.
func tarRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	var create, extract, absolute, removeFiles bool
	var file string
	// -C is cumulative (GNU tar: -C a -C b is a/b, an absolute one starts over) and applies to what follows: dirs are every folder it set, cur the one now in force, and an operand is
	// judged from the folder in force where it stands (--remove-files deletes the files it archived).
	var dirs, removed, oneTop []string
	cur := "."
	setDir := func(v string) {
		next := changeDir(cur, []string{v}) // "" when it cannot be read: judged as outside
		if cur == "." {
			next = changeDirFromHere(v)
		}
		cur = next
		if cur == "" {
			dirs = append(dirs, "$UNREADABLE_DIR")
		} else {
			dirs = append(dirs, cur)
		}
	}
	// Old style: the first word is letters without a dash (tar xzf a.tgz): the values of f and C come from the words after it, in the order of the letters.
	var pending []byte
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && isLetters(rest[0]) {
		for i := 0; i < len(rest[0]); i++ {
			c := rest[0][i]
			setTarFlag(c, &create, &extract, &absolute)
			if c == 'f' || c == 'C' {
				pending = append(pending, c)
			}
		}
		rest = rest[1:]
	}
	take := func(c byte, v string) {
		switch c {
		case 'f':
			file = v
		case 'C':
			setDir(v)
		}
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case len(pending) > 0 && !strings.HasPrefix(a, "-"):
			take(pending[0], a)
			pending = pending[1:]
		case a == "--remove-files":
			create, removeFiles = true, true
		case a == "--create" || a == "--append" || a == "--update" || a == "--delete":
			create = true
		case a == "--extract" || a == "--get":
			extract = true
		case a == "--absolute-names":
			absolute = true
		case a == "--file" || a == "--directory":
			if i+1 < len(rest) {
				take(map[string]byte{"--file": 'f', "--directory": 'C'}[a], rest[i+1])
				i++
			}
		case strings.HasPrefix(a, "--file="):
			file = strings.TrimPrefix(a, "--file=")
		case strings.HasPrefix(a, "--directory="):
			setDir(strings.TrimPrefix(a, "--directory="))
		case a == "--one-top-level": // extracts into a folder named after the archive, in the folder in force
			oneTop = append(oneTop, inDir(cur, ".")) // a new folder inside the one in force
		case strings.HasPrefix(a, "--one-top-level="): // extracts into the folder it names, under the one in force
			oneTop = append(oneTop, inDir(cur, strings.TrimPrefix(a, "--one-top-level=")))
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for k := 1; k < len(a); k++ {
				c := a[k]
				setTarFlag(c, &create, &extract, &absolute)
				if c == 'f' || c == 'C' {
					if k+1 < len(a) { // value attached (-Cdir, -farchive)
						take(c, a[k+1:])
					} else if i+1 < len(rest) {
						take(c, rest[i+1])
						i++
					}
					break
				}
			}
		default:
			if removeFiles {
				removed = append(removed, inDir(cur, a))
			}
		}
	}
	var targets []string
	if create && file != "" && file != "-" {
		targets = append(targets, inDir(".", file))
	}
	if removeFiles {
		targets = append(targets, removed...) // the files it archived are deleted
	}
	if extract {
		if absolute {
			return "it extracts with absolute names (tar -P)"
		}
		if len(dirs) == 0 {
			dirs = []string{"."}
		}
		targets = append(targets, dirs...)
		targets = append(targets, oneTop...)
	}
	return archiveOutside(targets, at, workDir)
}

// changeDirFromHere is -C's first folder: a path relative to the folder tar runs in, kept relative ("." and such) so the caller's own folder resolves it.
func changeDirFromHere(v string) string {
	if v == "" {
		return "."
	}
	return v
}

// inDir is a name given to tar as seen from the folder -C has set ("." is the folder tar runs in): the name under it, or the name itself when it is absolute or cannot be read.
func inDir(cur, name string) string {
	if cur == "." || cur == "" || rooted(name) || unreadablePath(name) {
		if cur == "" && !rooted(name) {
			return "$UNREADABLE_DIR/" + name
		}
		return name
	}
	return cur + "/" + name
}

func setTarFlag(c byte, create, extract, absolute *bool) {
	switch c {
	case 'c', 'r', 'u':
		*create = true
	case 'x':
		*extract = true
	case 'P':
		*absolute = true
	}
}

func isLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !(s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z') {
			return false
		}
	}
	return s != ""
}

// unzipRisk judges unzip: it extracts into -d DIR, else the folder it runs in, unless it only lists or tests or writes to the output (-l, -t, -v, -p, -Z, -c).
func unzipRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	dir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" && i+1 < len(args):
			dir = args[i+1]
			i++
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			dir = a[2:]
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "ltvpZc") && !strings.HasPrefix(a, "-d"):
			return ""
		}
	}
	if dir == "" {
		dir = "."
	}
	return archiveOutside([]string{dir}, at, workDir)
}

// zipRisk judges zip: the archive is its first operand, written (or changed) there; -O/--output-file names another output (the first operand is then only read), and -b/--temp-path a folder the
// temporary file is made in.
func zipRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	takes := map[string]bool{"-t": true, "-tt": true, "-n": true, "-x": true, "-i": true, "-P": true, "-Z": true, "-fz-": false}
	var targets []string
	output := false
	archive := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-O" || a == "--output-file":
			if i+1 < len(args) {
				targets = append(targets, args[i+1])
				output = true
				i++
			}
		case strings.HasPrefix(a, "--output-file="):
			targets = append(targets, strings.TrimPrefix(a, "--output-file="))
			output = true
		case a == "-b" || a == "--temp-path":
			if i+1 < len(args) {
				targets = append(targets, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--temp-path="):
			targets = append(targets, strings.TrimPrefix(a, "--temp-path="))
		case takes[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			if archive == "" {
				archive = a
			}
		}
	}
	if !output && archive != "" {
		targets = append(targets, archive)
	}
	return archiveOutside(targets, at, workDir)
}

// sevenZipRisk judges 7z, 7za and 7zr: x and e extract into -oDIR (else the folder they run in), a, u and d change the archive (the first operand after the command).
func sevenZipRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	if len(args) == 0 {
		return ""
	}
	var operands []string
	out := ""
	for _, a := range args[1:] {
		switch {
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			out = a[2:]
		case strings.HasPrefix(a, "-spf"):
			return "it extracts with absolute names (7z -spf)"
		case strings.HasPrefix(a, "-"):
		default:
			operands = append(operands, a)
		}
	}
	switch args[0] {
	case "x", "e":
		if out == "" {
			out = "."
		}
		return archiveOutside([]string{out}, at, workDir)
	case "a", "u", "d", "rn", "h":
		if len(operands) > 0 {
			return archiveOutside([]string{operands[0]}, at, workDir)
		}
	}
	return ""
}

// compressRisk judges gzip, gunzip, bzip2, bunzip2, xz, unxz and zstd: they replace each file they are given (unless -c, -l, -t or -d to the output), so a file outside the folder is a write there. zstd's
// -o/--output, --output-dir-flat and --output-dir-mirror name where the output goes: then the files are only read (unless --rm removes them).
func compressRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	takes := map[string]bool{"-S": true, "--suffix": true, "-T": true, "--threads": true, "-M": true, "--memlimit": true}
	var files, outputs []string
	removes := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--stdout" || a == "--to-stdout" || a == "--list" || a == "--test":
			return ""
		case a == "-o" || a == "--output" || a == "--output-dir-flat" || a == "--output-dir-mirror":
			if i+1 < len(args) {
				outputs = append(outputs, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--output="), strings.HasPrefix(a, "--output-dir-flat="), strings.HasPrefix(a, "--output-dir-mirror="):
			_, v, _ := strings.Cut(a, "=")
			outputs = append(outputs, v)
		case a == "--rm" || a == "--remove":
			removes = true
		case takes[a]:
			i++
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "clt"):
			return ""
		case strings.HasPrefix(a, "-"):
		default:
			files = append(files, a)
		}
	}
	if len(outputs) > 0 {
		if removes {
			outputs = append(outputs, files...)
		}
		return archiveOutside(outputs, at, workDir)
	}
	return archiveOutside(files, at, workDir)
}

// rsyncRisk judges rsync: the destination is its last operand (a remote host:path is not a file here); --delete and --remove-source-files also change the sources, so those are judged too.
func rsyncRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	takes := map[string]bool{"-e": true, "--rsh": true, "--exclude": true, "--include": true, "--exclude-from": true, "--include-from": true, "--filter": true, "-f": true, "--port": true, "--bwlimit": true, "--backup-dir": true, "--partial-dir": true, "--log-file": true, "--temp-dir": true, "-T": true, "--files-from": true, "--compare-dest": true, "--link-dest": true}
	var operands []string
	var extra []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case takes[a]:
			if a == "--backup-dir" || a == "--partial-dir" || a == "--log-file" || a == "--temp-dir" || a == "-T" {
				if i+1 < len(args) {
					extra = append(extra, args[i+1])
				}
			}
			i++
		case strings.HasPrefix(a, "--backup-dir="), strings.HasPrefix(a, "--log-file="), strings.HasPrefix(a, "--temp-dir="), strings.HasPrefix(a, "--partial-dir="):
			_, v, _ := strings.Cut(a, "=")
			extra = append(extra, v)
		case strings.HasPrefix(a, "-"):
		default:
			operands = append(operands, a)
		}
	}
	targets := append([]string(nil), extra...)
	if len(operands) > 0 {
		dest := operands[len(operands)-1]
		if !strings.Contains(strings.SplitN(dest, "/", 2)[0], ":") { // host:path and rsync://host/module are remote
			targets = append(targets, dest)
		}
		if hasRsyncFlag(args, "--remove-source-files", "--delete") && len(operands) > 1 {
			for _, src := range operands[:len(operands)-1] {
				if !strings.Contains(strings.SplitN(src, "/", 2)[0], ":") {
					targets = append(targets, src)
				}
			}
		}
	}
	return archiveOutside(targets, at, workDir)
}

func hasRsyncFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") || (n == "--delete" && strings.HasPrefix(a, "--delete-")) {
				return true
			}
		}
	}
	return false
}

// cpioRisk judges cpio: -i/--extract writes into the folder it runs in or the one -D/--directory names, -p/--pass-through copies into its first operand (a folder), -O/--file and -F/--file name the archive
// it writes (-o) or extracts from.
func cpioRisk(_ string, args []string, at func([]string) []string, workDir string) string {
	var extract, passThrough, create bool
	var dir, outFile, archive string
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-D" || a == "--directory":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--directory="):
			dir = strings.TrimPrefix(a, "--directory=")
		case a == "-O":
			if i+1 < len(args) {
				outFile = args[i+1]
				i++
			}
		case a == "-F" || a == "--file":
			if i+1 < len(args) {
				archive = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--file="):
			archive = strings.TrimPrefix(a, "--file=")
		case a == "--extract":
			extract = true
		case a == "--pass-through":
			passThrough = true
		case a == "--create":
			create = true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for k := 1; k < len(a); k++ {
				switch a[k] {
				case 'i':
					extract = true
				case 'p':
					passThrough = true
				case 'o':
					create = true
				case 'D', 'O', 'F': // the value is attached or the next word
					v := a[k+1:]
					if v == "" && i+1 < len(args) {
						i++
						v = args[i]
					}
					switch a[k] {
					case 'D':
						dir = v
					case 'O':
						outFile = v
					case 'F':
						archive = v
					}
					k = len(a)
				}
			}
		default:
			operands = append(operands, a)
		}
	}
	var targets []string
	if extract || passThrough {
		if dir == "" {
			dir = "."
		}
		targets = append(targets, dir)
	}
	if passThrough && len(operands) > 0 {
		targets = append(targets, operands[0]) // the destination folder
	}
	if outFile != "" {
		targets = append(targets, outFile)
	}
	if create && archive != "" {
		targets = append(targets, archive)
	}
	return archiveOutside(targets, at, workDir)
}
