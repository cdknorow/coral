package routes

import "strings"

// git settings that run a program, held by auto-accept (#182, Security's #181 F2). A git option such as core.hooksPath, core.fsmonitor, core.sshCommand or an alias that
// starts with "!" makes git run a command the next time it does something ordinary (status, commit, fetch): set with `git config --global` or `--system` it does so in
// EVERY repository, set locally in that repository, and given with `git -c key=value` for that one command. Any of them is a way to run a program without that program
// ever being judged, so the command that sets one asks, on every platform (the PowerShell and cmd words reach it too: winStageRisk). Harmless keys (user.name, a plain alias,
// core.autocrlf, ...) and reads/unsets stay allowed.
//
// The limit that stays: a program that is simply RUN is not judged here (a download that is then run, npm -g, pip install, a build script); only git's own switches for
// running programs are. Environment variables that do the same for one command (GIT_SSH_COMMAND, GIT_EXTERNAL_DIFF, GIT_CONFIG_COUNT with GIT_CONFIG_KEY_n) are not read either.

// gitExecKeys are the keys (lower case) whose value, when it is not empty, names a program git runs.
var gitExecKeys = map[string]bool{
	"core.hookspath":            true,
	"core.fsmonitor":            true,
	"core.sshcommand":           true,
	"core.pager":                true,
	"core.editor":               true,
	"core.askpass":              true,
	"core.gitproxy":             true,
	"credential.helper":         true,
	"diff.external":             true,
	"sequence.editor":           true,
	"gpg.program":               true,
	"gpg.ssh.program":           true,
	"gpg.x509.program":          true,
	"gpg.openpgp.program":       true,
	"gpg.ssh.defaultkeycommand": true,
	"include.path":              true,
}

// gitExecKey says whether setting key (any case) to value makes git run a program. An empty value resets the setting and is harmless.
func gitExecKey(key, value string) (bool, string) {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" || strings.TrimSpace(value) == "" {
		return false, ""
	}
	if gitExecKeys[k] {
		return true, key
	}
	parts := strings.Split(k, ".")
	last := parts[len(parts)-1]
	switch {
	case parts[0] == "alias" && len(parts) > 1 && strings.HasPrefix(strings.TrimSpace(value), "!"):
		return true, key // an alias that is a shell command
	case parts[0] == "submodule" && last == "update" && strings.HasPrefix(strings.TrimSpace(value), "!"):
		return true, key
	case len(parts) >= 3 && parts[0] == "credential" && last == "helper": // credential.<url>.helper
		return true, key
	case len(parts) >= 3 && parts[0] == "includeif" && last == "path": // includeIf.<condition>.path
		return true, key
	case len(parts) >= 3 && parts[0] == "filter" && (last == "clean" || last == "smudge" || last == "process"):
		return true, key
	case len(parts) >= 3 && parts[0] == "diff" && (last == "textconv" || last == "command"):
		return true, key
	case len(parts) >= 3 && parts[0] == "merge" && last == "driver":
		return true, key
	case len(parts) >= 3 && (parts[0] == "mergetool" || parts[0] == "difftool") && (last == "cmd" || last == "path"):
		return true, key
	case len(parts) >= 3 && parts[0] == "remote" && (last == "uploadpack" || last == "receivepack" || last == "vcs"):
		return true, key
	case len(parts) >= 3 && parts[0] == "browser" && last == "cmd", len(parts) >= 3 && parts[0] == "man" && last == "cmd":
		return true, key
	case len(parts) >= 3 && parts[0] == "gpg" && last == "program":
		return true, key
	}
	return false, ""
}

// gitKeyValue splits "key=value" as git's -c does (no "=" is the key set to nothing).
func gitKeyValue(kv string) (key, value string) {
	key, value, _ = strings.Cut(kv, "=")
	return key, value
}

func gitExecWhy(key string) string {
	return "it sets a git option that runs a program (" + key + "), which would run it in later git commands without it being judged"
}

// gitCodeExecRisk is why git's arguments (after the word git) set an option that runs a program, or "". It reads the global options before the subcommand (-c key=value,
// --config-env=key=ENV, --exec-path=dir), `git config` writes, and the -c / --config of clone and init.
func gitCodeExecRisk(args []string) string {
	sub, subAt := "", -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c":
			if i+1 < len(args) {
				i++
				if bad, key := gitExecKey(gitKeyValue(args[i])); bad {
					return gitExecWhy(key)
				}
			}
		case strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--") && len(a) > 2 && strings.Contains(a, "="):
			if bad, key := gitExecKey(gitKeyValue(a[2:])); bad {
				return gitExecWhy(key)
			}
		case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--super-prefix" || a == "--attr-source":
			i++
		case a == "--config-env" && i+1 < len(args):
			i++
			key, _ := gitKeyValue(args[i])
			if bad, k := gitExecKey(key, "env"); bad { // the value comes from the environment: it is not empty as far as can be told
				return gitExecWhy(k)
			}
		case strings.HasPrefix(a, "--config-env="):
			key, _ := gitKeyValue(strings.TrimPrefix(a, "--config-env="))
			if bad, k := gitExecKey(key, "env"); bad {
				return gitExecWhy(k)
			}
		case strings.HasPrefix(a, "--exec-path="):
			return "it runs git commands from another folder (--exec-path)"
		case strings.HasPrefix(a, "-"):
		default:
			sub, subAt = a, i
		}
		if sub != "" {
			break
		}
	}
	if sub == "" {
		return ""
	}
	rest := args[subAt+1:]
	switch sub {
	case "config":
		return gitConfigWriteRisk(rest)
	case "clone", "init":
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			switch {
			case (a == "-c" || a == "--config") && i+1 < len(rest):
				i++
				if bad, key := gitExecKey(gitKeyValue(rest[i])); bad {
					return gitExecWhy(key)
				}
			case strings.HasPrefix(a, "--config="):
				if bad, key := gitExecKey(gitKeyValue(strings.TrimPrefix(a, "--config="))); bad {
					return gitExecWhy(key)
				}
			}
		}
	}
	return ""
}

// gitConfigWriteRisk is gitCodeExecRisk for `git config ...`: a write of a program-running key, in any scope, and the editor on the global or system file. Reads, lists and
// unsets are allowed.
func gitConfigWriteRisk(args []string) string {
	var pos []string
	edit, wide, readOnly := false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--global" || a == "--system":
			wide = true
		case a == "-e" || a == "--edit":
			edit = true
		case a == "-f" || a == "--file" || a == "--blob" || a == "--default" || a == "--type" || a == "--rename-section":
			i++
			if a == "--rename-section" {
				readOnly = true
			}
		case a == "--get" || a == "--get-all" || a == "--get-regexp" || a == "--get-urlmatch" || a == "--list" || a == "-l" ||
			a == "--unset" || a == "--unset-all" || a == "--remove-section" || a == "--name-only" || a == "--get-color" || a == "--get-colorbool":
			readOnly = true
		case strings.HasPrefix(a, "--rename-section=") || strings.HasPrefix(a, "--get-urlmatch="):
			readOnly = true
		case strings.HasPrefix(a, "-"):
		default:
			pos = append(pos, a)
		}
	}
	if edit && wide {
		return "it opens the global git config in an editor (any option in it can run a program)"
	}
	if readOnly {
		return ""
	}
	if len(pos) > 0 {
		switch pos[0] {
		case "get", "list", "unset", "remove-section", "rename-section":
			return ""
		case "set":
			pos = pos[1:]
		case "edit":
			if wide {
				return "it opens the global git config in an editor (any option in it can run a program)"
			}
			return ""
		}
	}
	if len(pos) >= 2 {
		if bad, key := gitExecKey(pos[0], pos[1]); bad {
			return gitExecWhy(key)
		}
	}
	return ""
}
