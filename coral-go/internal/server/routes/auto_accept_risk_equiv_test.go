package routes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// shellRiskFrom was rewritten for #4599 item 9 (a rule per command in place of one switch). It is the auto-accept
// safety gate, so the rewrite must give exactly the old answer for every command (Security's condition): the old
// function is kept below and the two are compared on every string in this package's risk tests, on a seeded corpus
// of risky command shapes, and by FuzzShellRiskAgreesWithBefore4599.

// shellRiskFromBefore4599 is shellRiskFrom exactly as it was before #4599 item 9 (one switch over the commands),
// kept here as the reference the rewrite must agree with on every input.
func shellRiskFromBefore4599(command, workDir, dir string) string {
	if curlPipeRe.MatchString(command) || curlSubstRe.MatchString(command) {
		return "it pipes a download into a shell"
	}
	// The directory later commands run in: a cd out of the working directory moves it
	// ("" = somewhere that cannot be read literally, e.g. cd ~ or cd $X).
	cwd := dir
	at := func(args []string) []string { return fromDir(args, cwd) }
	// ">|" (overwrite despite noclobber) is a redirection, not a pipe.
	for _, seg := range shellSplitRe.Split(strings.ReplaceAll(command, ">|", "> "), -1) {
		seg, targets := redirections(seg)
		if o := outsideTarget(at(notDevices(targets)), workDir); o != "" {
			return "it writes outside the working directory (" + o + ")"
		}
		words := shellWords(seg)
		// Skip env assignments and shell wrappers (bash -lc '…' is split by the caller).
		words = unwrapCommand(words)
		if len(words) == 0 {
			continue
		}
		for _, w := range words {
			if b := filepath.Base(bareCommand(w)); b == "sudo" || b == "doas" {
				return "it uses sudo"
			} else if b == "su" && bareCommand(w) != w {
				return "it switches user (su)" // $(su …), (su …), `su …`: a command, not the word "su"
			}
		}
		cmd := filepath.Base(bareCommand(words[0]))
		args := words[1:]
		switch cmd {
		case "cd", "pushd":
			cwd = changeDir(cwd, args)
		case "rm":
			if hasFlag(args, "r", "R", "--recursive") {
				if t := outsideTarget(at(args), workDir); t != "" {
					return "it deletes recursively outside the working directory (" + t + ")"
				}
			}
		case "tee", "cp", "mv", "install", "ln", "dd":
			if t := outsideTarget(at(notDevices(writeTargets(cmd, args))), workDir); t != "" {
				return "it writes outside the working directory (" + t + ")"
			}
		case "find":
			if roots, deletes := findDeletes(args); deletes {
				if t := outsideTarget(at(roots), workDir); t != "" {
					return "it deletes files outside the working directory (" + t + ")"
				}
			}
		case "su":
			return "it switches user (su)"
		case "perl":
			if files, inPlace := perlInPlaceFiles(args); inPlace {
				if t := outsideTarget(at(files), workDir); t != "" {
					return "it edits a file outside the working directory (" + t + ")"
				}
			}
		case "sed":
			if files, inPlace := sedInPlaceFiles(args); inPlace {
				if t := outsideTarget(at(files), workDir); t != "" {
					return "it edits a file outside the working directory (" + t + ")"
				}
			}
		case "chmod", "chown":
			if hasFlag(args, "R", "", "--recursive") {
				if t := outsideTarget(at(args), workDir); t != "" {
					return "it changes ownership or permissions recursively outside the working directory (" + t + ")"
				}
			}
		case "git":
			sub, rest := gitSubcommand(args)
			switch sub {
			case "push":
				for _, a := range rest {
					if a == "-f" || a == "--force" || a == "--mirror" || strings.HasPrefix(a, "--force-with-lease") || strings.HasPrefix(a, "--force-if-includes") ||
						(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")) ||
						(strings.HasPrefix(a, "+") && len(a) > 1) {
						return "it force-pushes"
					}
					if a == "--delete" || a == "-d" || (strings.HasPrefix(a, ":") && len(a) > 1) {
						return "it deletes a remote branch"
					}
				}
			case "reset":
				for _, a := range rest {
					if a == "--hard" {
						return "it runs git reset --hard"
					}
				}
			case "clean":
				if !hasFlag(rest, "n", "", "--dry-run") {
					return "it runs git clean (deletes untracked files)"
				}
			case "checkout":
				for i, a := range rest {
					if a == "." || a == "-f" || a == "--force" || (a == "--" && i+1 < len(rest)) {
						return "it discards uncommitted changes (git checkout " + strings.Join(rest, " ") + ")"
					}
				}
			case "restore":
				staged, worktree := false, false
				for _, a := range rest {
					switch {
					case a == "--staged" || a == "-S":
						staged = true
					case a == "--worktree" || a == "-W":
						worktree = true
					case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "W"):
						worktree = true
					case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "S"):
						staged = true
					}
				}
				if !staged || worktree {
					return "it discards uncommitted changes (git restore)"
				}
			}
		default:
			// bash -c '…' / zsh -lc '…': inspect the inner command.
			if shellWrapperRe.MatchString(words[0]) {
				for i, a := range args {
					if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+1 < len(args) {
						if why := shellRiskFromBefore4599(strings.Join(args[i+1:], " "), workDir, cwd); why != "" {
							return why
						}
						break
					}
				}
			}
		}
	}
	return ""
}

// riskDirs: the working directory, a folder under it, its parent, the root and an unreadable cwd ("").
func riskDirs(t testing.TB) (string, []string) {
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wd, []string{wd, filepath.Join(wd, "sub"), filepath.Dir(wd), "/", ""}
}

func agreeOn(t testing.TB, wd string, dirs []string, cmd string) {
	t.Helper()
	if msg := disagreeOn(wd, dirs, cmd); msg != "" {
		t.Fatal(msg)
	}
}

// disagreeOn says how the new and the old rule differ on a command, or "" when they agree (it touches no testing.T, so goroutines can call it).
func disagreeOn(wd string, dirs []string, cmd string) string {
	for _, dir := range dirs {
		if got, want := shellRiskFrom(cmd, wd, dir), shellRiskFromBefore4599(cmd, wd, dir); got != want {
			return fmt.Sprintf("shellRiskFrom(%q, wd, %q) = %q; before #4599 it was %q", cmd, dir, got, want)
		}
	}
	return ""
}

// riskTestStrings: every string literal in this package's test files that call shellRisk (the commands of
// TestShellRisk, TestShellWriteRisk and the rest, and their fragments).
func riskTestStrings(t testing.TB) []string {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "shellRisk(") || f == "auto_accept_risk_equiv_test.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
			return true
		})
	}
	return out
}

func TestShellRiskAgreesWithBefore4599OnEveryTestCommand(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	wd, dirs := riskDirs(t)
	cmds := riskTestStrings(t)
	if len(cmds) < 150 {
		t.Fatalf("only %d strings found in the risk tests: the harvest is broken", len(cmds))
	}
	for _, c := range cmds {
		agreeOn(t, wd, dirs, c)
		agreeOn(t, wd, dirs, "cd .. && "+c)     // after a cd out of the working directory
		agreeOn(t, wd, dirs, "bash -c '"+c+"'") // inside a shell wrapper
		agreeOn(t, wd, dirs, c+" "+wd+"/x")     // a path inside the working directory after it
	}
}

// riskWords: command words, flags, paths and shell syntax the rules look at, for random commands.
func riskWords(wd string) []string {
	return []string{
		"rm", "-rf", "-r", "-R", "-fr", "--recursive", "-f", "git", "push", "--force", "-f", "-uf", "--force-with-lease",
		"--force-if-includes", "--mirror", "--delete", "-d", ":old", "+main", "origin", "main", "reset", "--hard", "clean",
		"-n", "--dry-run", "-fd", "checkout", "--", ".", "restore", "--staged", "-S", "--worktree", "-W", "-SW", "-C", "repo",
		"sudo", "doas", `\sudo`, "s\\udo", "(sudo", "$(sudo", "`sudo", "/usr/bin/sudo", "su", "$(su", "-", "cd", "pushd", "..",
		"~", "$HOME", "$X", "/tmp", "/", "/etc", "/etc/hosts", "/dev/null", "/dev/tty", wd, wd + "/sub", wd + "/x.txt", wd + "/../y",
		"sub", "x.txt", "../z", "*", "/*", "find", "-delete", "-exec", "{}", "\\;", "-name", "sed", "-i", "-i.bak", "-e",
		"s/a/b/", "perl", "-pi", "-pie", "chmod", "chown", "777", "me:staff", "tee", "-a", "cp", "mv", "install", "ln", "-s",
		"dd", "if=/dev/zero", "of=/etc/x", "of=" + wd + "/o", "of=/dev/null", "curl", "wget", "-fsSL", "https://x.sh", "sh",
		"bash", "zsh", "/bin/zsh", "-c", "-lc", "<(curl", "'rm -rf /'", "\"git push -f\"", ">", ">>", ">|", "&>", "2>", "2>&1",
		"|", "||", "&&", ";", "\n", "FOO=1", "env", "echo", "make", "ls",
	}
}

func TestShellRiskAgreesWithBefore4599OnRandomCommands(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	wd, dirs := riskDirs(t)
	words := riskWords(wd)
	n := 200000
	if testing.Short() {
		n = 20000
	}
	// 200,000 commands in 100 blocks of 2,000, each from its own seeded stream, on several goroutines: the same number of commands as one
	// stream of them, a fraction of the time (#4852).
	const blocks = 100
	var once sync.Once
	parallelFor(blocks, func(b int) bool {
		rng := rand.New(rand.NewSource(4599 + int64(b)))
		for i := 0; i < n/blocks; i++ {
			k := 1 + rng.Intn(9)
			parts := make([]string, k)
			for j := range parts {
				parts[j] = words[rng.Intn(len(words))]
			}
			sep := " "
			if rng.Intn(4) == 0 {
				sep = ""
			}
			if msg := disagreeOn(wd, dirs, strings.Join(parts, sep)); msg != "" {
				once.Do(func() { t.Error(msg) })
				return false
			}
		}
		return true
	})
}

func FuzzShellRiskAgreesWithBefore4599(f *testing.F) {
	wd, dirs := riskDirs(f)
	for _, s := range riskTestStrings(f) {
		f.Add(s)
	}
	for _, w := range riskWords(wd) {
		f.Add(w)
	}
	f.Fuzz(func(t *testing.T, cmd string) { agreeOn(t, wd, dirs, cmd) })
}
