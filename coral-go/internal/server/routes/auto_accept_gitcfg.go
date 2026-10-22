package routes

import (
	"path/filepath"
	"strings"
)

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
	"core.hookspath":             true,
	"core.fsmonitor":             true,
	"core.sshcommand":            true,
	"core.pager":                 true,
	"core.editor":                true,
	"core.askpass":               true,
	"core.gitproxy":              true,
	"credential.helper":          true,
	"diff.external":              true,
	"sequence.editor":            true,
	"gpg.program":                true,
	"gpg.ssh.program":            true,
	"gpg.x509.program":           true,
	"gpg.openpgp.program":        true,
	"gpg.ssh.defaultkeycommand":  true,
	"include.path":               true,
	"uploadpack.packobjectshook": true,
	"interactive.difffilter":     true,
	"sendemail.smtpserver":       true, // a path to a program when it starts with /
}

const gitExpansionKey = "<unresolved shell expansion>"

// gitSettingExpansion treats decoded dollar/backtick words as unresolved. This
// also holds ANSI-C/localized quoting, whose leading dollar survives quote
// removal. Never expand variables or execute substitutions to classify them.
// A quoted literal dollar can be held too: a deliberate conservative cost.
func gitSettingExpansion(words ...string) bool {
	for _, word := range words {
		if strings.ContainsAny(word, "$`") {
			return true
		}
	}
	return false
}

// gitExecKey says whether setting key (any case) to value makes git run a program,
// or cannot be judged because a shell expansion can change either. Literal empty
// values reset a setting and remain harmless.
func gitExecKey(key, value string) (bool, string) {
	if gitSettingExpansion(key, value) {
		return true, gitExpansionKey
	}
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" || strings.TrimSpace(value) == "" {
		return false, ""
	}
	if gitExecKeys[k] {
		return true, key
	}
	parts := strings.Split(k, ".")
	last := parts[len(parts)-1]
	v := strings.ToLower(strings.TrimSpace(value))
	switch {
	case k == "protocol.ext.allow" && v != "never", k == "protocol.allow" && v == "always":
		return true, key // git's ext:: transport runs the command in the URL
	case parts[0] == "pager" && len(parts) >= 2 && !gitBoolWord[v]:
		return true, key // pager.<command> is a program unless it is true or false
	case parts[0] == "trailer" && len(parts) >= 3 && (last == "cmd" || last == "command"):
		return true, key
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

// gitBoolWord are the values of a boolean git setting: pager.<command> set to one of them runs nothing.
var gitBoolWord = map[string]bool{"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true, "1": true, "0": true}

// gitKeyValue splits "key=value" as git's -c does (no "=" is the key set to nothing).
func gitKeyValue(kv string) (key, value string) {
	key, value, _ = strings.Cut(kv, "=")
	return key, value
}

func gitExecWhy(key string) string {
	if key == gitExpansionKey {
		return "it uses a git setting with shell expansion that cannot be judged"
	}
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
		case strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--") && len(a) > 2 && (strings.Contains(a, "=") || gitSettingExpansion(a[2:])):
			if bad, key := gitExecKey(gitKeyValue(a[2:])); bad {
				return gitExecWhy(key)
			}
		case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--super-prefix" || a == "--attr-source":
			i++
		case a == "--config-env" && i+1 < len(args):
			i++
			if why := gitConfigEnvRisk(args[i]); why != "" {
				return why
			}
		case strings.HasPrefix(a, "--config-env="):
			if why := gitConfigEnvRisk(strings.TrimPrefix(a, "--config-env=")); why != "" {
				return why
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
	file := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--global" || a == "--system":
			wide = true
		case a == "-e" || a == "--edit":
			edit = true
		case a == "-f" || a == "--file" || a == "--blob" || a == "--default" || a == "--type" || a == "--rename-section":
			if (a == "-f" || a == "--file") && i+1 < len(args) {
				file = args[i+1]
			}
			i++
			if a == "--rename-section" {
				readOnly = true
			}
		case a == "--get" || a == "--get-all" || a == "--get-regexp" || a == "--get-urlmatch" || a == "--list" || a == "-l" ||
			a == "--unset" || a == "--unset-all" || a == "--remove-section" || a == "--get-color" || a == "--get-colorbool":
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
	// A single expanded positional can split into key and value in the shell.
	// Read/unset modes returned above; only setting positions are considered here.
	for _, word := range pos[:min(2, len(pos))] {
		if gitSettingExpansion(word) {
			return gitExecWhy(gitExpansionKey)
		}
	}
	if len(pos) >= 2 {
		if bad, key := gitExecKey(pos[0], pos[1]); bad {
			return gitExecWhy(key)
		}
		if file != "" && gitHookPath(file) { // any write to .git/config by `git config -f` (a harmless key too: the file is the place) (#186)
			return gitHookWhy(file)
		}
	}
	return ""
}

// gitQuotedConfigRisk receives the original POSIX segment, before shellWords
// splits quoted spaces and loses their grouping. The existing stage tokenizer
// removes quotes/escapes using its POSIX mode, so -c and --config-env keys and
// values reach gitExecKey as one shell word (alias.x=' !evil', for example).
// Windows callers already pass winStages' decoded words to gitCodeExecRisk.
func gitQuotedConfigRisk(segment string) (why string, decoded bool) {
	for _, stage := range commandStages(segment, true) {
		words := unwrapCommand(stage.words)
		if len(words) == 0 {
			continue
		}
		if filepath.Base(bareCommand(words[0])) == "git" {
			decoded = true
			if why := gitCodeExecRisk(words[1:]); why != "" {
				return why, true
			}
		} else if shellWrapperRe.MatchString(words[0]) {
			// A shell's -c value is another command, not words to flatten: doing
			// that would drop quotes inside its Git key/value a second time.
			for i, a := range words[1:] {
				if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && i+2 < len(words) {
					why, innerDecoded := gitQuotedConfigRisk(words[i+2])
					decoded = decoded || innerDecoded
					if why != "" {
						return why, decoded
					}
					break
				}
			}
		}
	}
	return "", decoded
}

// The environment variable's name can contain shell expansion too. Even with a
// literal name, its unknown value could start with ! (alias/submodule commands).
func gitConfigEnvRisk(kv string) string {
	key, env := gitKeyValue(kv)
	if gitSettingExpansion(key, env) {
		return gitExecWhy(gitExpansionKey)
	}
	if bad, k := gitExecKey(key, "!"); bad {
		return gitExecWhy(k)
	}
	return ""
}
