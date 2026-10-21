package background

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// buildStepEnv constructs environment variables for a workflow step.
func (wr *WorkflowRunner) buildStepEnv(workflow *store.Workflow, runID int64, runDir string, stepIndex int, stepDir string, totalSteps int, steps []StepDef) []string {
	runIDStr := strconv.FormatInt(runID, 10)
	portStr := strconv.Itoa(wr.port)
	coralURL := fmt.Sprintf("http://%s:%s", wr.host, portStr)
	env := []string{
		// Server-level Coral variables
		"CORAL_PORT=" + portStr,
		"CORAL_HOST=" + wr.host,
		"CORAL_URL=" + coralURL,
		"CORAL_DIR=" + wr.dataDir,
		"CORAL_DATA_DIR=" + wr.dataDir,
		// Workflow-specific variables
		"CORAL_WORKFLOW_RUN_DIR=" + runDir,
		"CORAL_WORKFLOW_STEP=" + strconv.Itoa(stepIndex),
		"CORAL_WORKFLOW_STEP_DIR=" + stepDir,
		"CORAL_WORKFLOW_NAME=" + workflow.Name,
		"CORAL_WORKFLOW_RUN_ID=" + runIDStr,
		"CORAL_WORKFLOW_REPO_PATH=" + workflow.RepoPath,
	}

	// Ensure Coral tools (coral-board, hooks, etc.) and agent CLIs are in PATH
	if toolsDir := agent.CoralToolsDir(); toolsDir != "" {
		env = append(env, "PATH="+agent.PathWithDir(toolsDir, os.Getenv("PATH")))
	}

	// Previous step references
	if stepIndex > 0 {
		prevDir := filepath.Join(runDir, fmt.Sprintf("step_%d", stepIndex-1))
		env = append(env,
			"CORAL_PREV_DIR="+prevDir,
			"CORAL_PREV_STDOUT="+filepath.Join(prevDir, "stdout.txt"),
			"CORAL_PREV_STDERR="+filepath.Join(prevDir, "stderr.txt"),
		)
		// If previous step has an output_artifact, set path and read content
		// Only an artifact inside that step's folder, never through a symlink (#3423): its
		// content in the environment when it fits (MED-03), else the path alone.
		if stepIndex-1 < len(steps) && steps[stepIndex-1].OutputArtifact != "" {
			if rel := steps[stepIndex-1].OutputArtifact; filepath.IsLocal(rel) {
				p := filepath.Join(prevDir, rel)
				env = append(env, "CORAL_PREV_ARTIFACT="+p)
				if data, err := readInRun(runDir, p, maxArtifactEnv+1); err == nil {
					if len(data) > maxArtifactEnv {
						env = append(env, "CORAL_PREV_ARTIFACT_TRUNCATED=1")
					} else {
						env = append(env, "CORAL_PREV_ARTIFACT_CONTENT="+stripNUL(string(data)))
					}
				}
			}
		}
	}

	// All previous step directories as CORAL_STEP_N_DIR / CORAL_STEP_N_STDOUT
	for n := 0; n < stepIndex; n++ {
		nDir := filepath.Join(runDir, fmt.Sprintf("step_%d", n))
		env = append(env,
			fmt.Sprintf("CORAL_STEP_%d_DIR=%s", n, nDir),
			fmt.Sprintf("CORAL_STEP_%d_STDOUT=%s", n, filepath.Join(nDir, "stdout.txt")),
		)
	}

	return env
}

// templatePattern matches {{variable_name}} patterns for template expansion.
var templatePattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// envKeyForTemplate maps template variable names to their environment variable equivalents.
var envKeyForTemplate = map[string]string{
	"run_dir":       "CORAL_WORKFLOW_RUN_DIR",
	"run_id":        "CORAL_WORKFLOW_RUN_ID",
	"step_dir":      "CORAL_WORKFLOW_STEP_DIR",
	"prev_dir":      "CORAL_PREV_DIR",
	"prev_stdout":   "CORAL_PREV_STDOUT",
	"prev_stderr":   "CORAL_PREV_STDERR",
	"prev_artifact": "CORAL_PREV_ARTIFACT",
}

// stepNPattern matches {{step_N_dir}} and {{step_N_stdout}} patterns.
var stepNPattern = regexp.MustCompile(`^step_(\d+)_(dir|stdout)$`)

// expandTemplates replaces {{var}} placeholders for an agent prompt: paths, and the
// content of a previous step's output as plain text.
func expandTemplates(text string, env []string) string { return expandTemplatesFor(text, env, false) }

// expandShellTemplates is expandTemplates for a shell step's command (#3423, CRIT-01): a
// content template ({{prev_stdout_content}}, {{step_N_stdout_content}},
// {{prev_artifact_content}}) becomes ONE single-quoted shell word, so whatever a previous
// step printed can't change the command. Trailing newlines are dropped first, as command
// substitution does (F1); a missing or refused artifact is an empty word, never the
// template text (F2); content over 64 KiB is an error that names the file form instead
// (F3). Prefer the environment ("$CORAL_PREV_STDOUT", "$CORAL_PREV_ARTIFACT") or reading
// the file in the command.
func expandShellTemplates(text string, env []string) (string, error) {
	var tooBig error
	out := expandTemplatesWith(text, env, true, func(name string, n int) {
		if tooBig == nil {
			tooBig = fmt.Errorf("{{%s}} is %d bytes, more than %d KiB can go on a command line: use the file instead ({{%s}} or $%s)",
				name, n, maxTemplateContent>>10, strings.TrimSuffix(name, "_content"), templateFileEnv(strings.TrimSuffix(name, "_content")))
		}
	})
	return out, tooBig
}

// templatePath is a path template's value: in a shell command, with forward slashes. Steps
// run in sh, which on Windows (Git's sh) takes the backslashes of an unquoted C:\Users\...
// as escapes, so {{prev_stdout}} named a file that doesn't exist (#3638); C:/Users/... is the
// same file to sh and to Windows. On Unix ToSlash changes nothing. An agent prompt keeps the
// native path.
//
// In a shell command the path is also quoted for where it sits (#3930, Security's #3927
// finding 7): a data folder with a space, an apostrophe or a shell character must not break or
// change the command. Outside quotes it is one single-quoted word; inside double quotes (an
// older workflow's "{{run_dir}}/x") its \, $, ` and " are escaped; inside single quotes an
// apostrophe closes, escapes and reopens the quote.
func templatePath(v string, shell bool, quote byte) string {
	if !shell {
		return v
	}
	v = stripNUL(filepath.ToSlash(v))
	switch quote {
	case '"':
		return strings.NewReplacer(`\`, `\\`, `$`, `\$`, "`", "\\`", `"`, `\"`).Replace(v)
	case '\'':
		return strings.ReplaceAll(v, "'", `'\''`)
	}
	return shellWord(v)
}

// shellQuoteAt is the POSIX quote open at byte i of cmd: a single quote, a double quote or 0.
func shellQuoteAt(cmd string, i int) byte {
	var q byte
	for j := 0; j < i && j < len(cmd); j++ {
		c := cmd[j]
		switch {
		case q == 0 && c == '\\':
			j++
		case q == '"' && c == '\\':
			j++
		case q == 0 && (c == '\'' || c == '"'):
			q = c
		case q != 0 && c == q:
			q = 0
		}
	}
	return q
}

// maxTemplateContent caps a content template in a shell command (F3): above it, the
// command line could not be executed (Linux allows 128 KiB per argument).
const maxTemplateContent = 64 << 10

// templateFileEnv names the variable holding the file a content template reads.
func templateFileEnv(base string) string {
	if ek, ok := envKeyForTemplate[base]; ok {
		return ek
	}
	if m := stepNPattern.FindStringSubmatch(base); m != nil {
		return fmt.Sprintf("CORAL_STEP_%s_STDOUT", m[1])
	}
	return "CORAL_PREV_STDOUT"
}

func expandTemplatesFor(text string, env []string, shell bool) string {
	return expandTemplatesWith(text, env, shell, nil)
}

func expandTemplatesWith(text string, env []string, shell bool, tooBig func(name string, n int)) string {
	// Build lookup from env slice
	envMap := make(map[string]string, len(env))
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			envMap[e[:idx]] = e[idx+1:]
		}
	}

	var quote byte // the quote open where the current template starts (shell commands only)
	replace := func(match string) string {
		varName := match[2 : len(match)-2] // strip {{ and }}

		// Check direct mapping
		if envKey, ok := envKeyForTemplate[varName]; ok {
			if val, found := envMap[envKey]; found {
				return templatePath(val, shell, quote)
			}
			return match // leave unexpanded if env var not set
		}

		// Check step_N_dir / step_N_stdout patterns
		if m := stepNPattern.FindStringSubmatch(varName); m != nil {
			suffix := "DIR"
			if m[2] == "stdout" {
				suffix = "STDOUT"
			}
			envKey := fmt.Sprintf("CORAL_STEP_%s_%s", m[1], suffix)
			if val, found := envMap[envKey]; found {
				return templatePath(val, shell, quote)
			}
			return match
		}

		// Check _content suffix — reads file content instead of returning path
		// e.g., {{prev_stdout_content}} reads the file at CORAL_PREV_STDOUT
		// e.g., {{step_0_stdout_content}} reads the file at CORAL_STEP_0_STDOUT
		if strings.HasSuffix(varName, "_content") {
			baseVar := varName[:len(varName)-len("_content")]
			var envKey string
			if ek, ok := envKeyForTemplate[baseVar]; ok {
				envKey = ek
			} else if m := stepNPattern.FindStringSubmatch(baseVar); m != nil {
				suffix := "DIR"
				if m[2] == "stdout" {
					suffix = "STDOUT"
				}
				envKey = fmt.Sprintf("CORAL_STEP_%s_%s", m[1], suffix)
			}
			if envKey != "" {
				filePath, found := envMap[envKey]
				var data []byte
				readOK := false
				if found {
					if d, err := readInRun(envMap["CORAL_WORKFLOW_RUN_DIR"], filePath, maxArtifactRead); err == nil {
						data, readOK = d, true
					}
				}
				if !readOK {
					// A refused or missing artifact (or an unreadable output) is an empty
					// word in a command, never the template text (F2); in a prompt, nothing.
					if found || baseVar == "prev_artifact" {
						if shell {
							return "''"
						}
						return ""
					}
					return match
				}
				if !shell {
					return stripNUL(string(data))
				}
				// As command substitution: trailing newlines dropped, embedded ones kept (F1).
				content := strings.TrimRight(stripNUL(string(data)), "\n")
				if len(content) > maxTemplateContent {
					if tooBig != nil {
						tooBig(varName, len(content))
					}
					return "''"
				}
				return shellWord(content)
			}
			return match
		}

		return match // unknown template, leave as-is
	}
	if !shell {
		return templatePattern.ReplaceAllStringFunc(text, replace)
	}
	var b strings.Builder
	last := 0
	for _, loc := range templatePattern.FindAllStringIndex(text, -1) {
		b.WriteString(text[last:loc[0]])
		quote = shellQuoteAt(text, loc[0])
		b.WriteString(replace(text[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
