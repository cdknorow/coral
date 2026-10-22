package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
)

func agyCommand(t *testing.T, flags ...string) (string, error) {
	t.Helper()
	return (&AgyAgent{}).LaunchCommand(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", CLIPath: "agy", Flags: flags})
}

// #5901: agy has --mode, not --approval-mode (Gemini's former spelling, which Coral's permission profiles and the launch dialog may still carry). The flag's name used to be dropped and its
// value left on the command line as a stray argument, and the = form went through untouched. A recognised value is translated to agy's own flag with both tokens consumed; another is refused.
func TestAgyLegacyApprovalModeIsTranslatedWithBothTokensAndUnknownValuesRefused(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  string // what the command gets, "" for nothing
	}{
		{[]string{"--approval-mode", "plan"}, "--mode plan"},
		{[]string{"--approval-mode=plan"}, "--mode plan"},
		{[]string{"--approval-mode", "auto_edit"}, "--mode accept-edits"},
		{[]string{"--approval-mode=yolo"}, "--dangerously-skip-permissions"},
		{[]string{"--approval-mode", "default"}, ""},
		{[]string{"--approval-mode=default"}, ""},
	} {
		cmd, err := agyCommand(t, tc.flags...)
		require.NoError(t, err, strings.Join(tc.flags, " "))
		assert.NotContains(t, cmd, "--approval-mode", strings.Join(tc.flags, " "))
		if tc.want != "" {
			assert.Contains(t, cmd, tc.want, strings.Join(tc.flags, " "))
		}
		// no stray value: "plan" is on the line once, as --mode's own, when that is what was asked for; auto_edit, yolo and default are not on it at all
		words := strings.Fields(cmd)
		count := func(w string) (n int) {
			for _, x := range words {
				if x == w {
					n++
				}
			}
			return n
		}
		wantPlan := 0
		if tc.want == "--mode plan" {
			wantPlan = 1
		}
		assert.Equal(t, wantPlan, count("plan"), strings.Join(tc.flags, " ")+": "+cmd)
		for _, w := range []string{"auto_edit", "yolo", "default"} {
			assert.Zero(t, count(w), strings.Join(tc.flags, " ")+": a stray "+w)
		}
	}
	for _, flags := range [][]string{{"--approval-mode", "bogus"}, {"--approval-mode=bogus"}, {"--approval-mode"}, {"--approval-mode", "--model", "x"}, {"--approval-mode="}} {
		_, err := agyCommand(t, flags...)
		assert.Error(t, err, strings.Join(flags, " ")+": refused, not passed")
	}
	// the mode a capability set chose is not widened by it
	cmd, err := (&AgyAgent{}).LaunchCommand(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", CLIPath: "agy", Capabilities: &Capabilities{Allow: []string{CapFileRead}}, Flags: []string{"--approval-mode", "yolo"}})
	require.NoError(t, err)
	assert.NotContains(t, cmd, "--dangerously-skip-permissions")
	assert.NotContains(t, cmd, "yolo")
}

// #5901: the gemini type is Antigravity compatibility and runs agy. A cli_path_gemini (or the agy one, which they share) that is the real Gemini CLI would be started with agy's flags it does
// not understand; it is refused with a setup error. The two programs' --help tell them apart ("Usage: gemini ... Gemini CLI - ..." against "Usage of agy:").
func TestAConfiguredPathThatIsTheRealGeminiCLIIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in program is a shell script")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "tool")
	require.NoError(t, os.WriteFile(prog, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	realGemini := "Usage: gemini [options] [command]\n\nGemini CLI - Defaults to interactive mode. Use -p/--prompt for non-interactive (headless) mode.\n"
	agyHelp := "Usage of agy:\n  --add-dir   Add a directory to the workspace (repeatable)\n"
	help := realGemini
	prev := cliHelp
	cliHelp = func(string) string { return help }
	t.Cleanup(func() {
		cliHelp = prev
		geminiGuardCache.Range(func(k, _ any) bool { geminiGuardCache.Delete(k); return true })
	})

	for _, typ := range []string{at.Gemini, at.Agy, at.Antigravity} {
		_, err := FindCLI(map[string]string{CLIPathSettingKey(typ): prog}, typ)
		var pe *CLIPathError
		require.ErrorAs(t, err, &pe, typ)
		assert.ErrorIs(t, err, ErrRealGeminiCLI, typ)
		assert.Contains(t, err.Error(), CLIPathSettingKey(typ), "the error names the setting")
	}
	// the other CLIs are not asked
	_, err := FindCLI(map[string]string{CLIPathSettingKey(at.Codex): prog}, at.Codex)
	assert.NoError(t, err)

	// agy itself passes, and a program that cannot be asked (no help at all) is not refused
	require.NoError(t, os.WriteFile(prog, []byte("#!/bin/sh\necho agy\n"), 0o755)) // a new file state: the answer is not the cached one
	help = agyHelp
	got, err := FindCLI(map[string]string{CLIPathSettingKey(at.Gemini): prog}, at.Gemini)
	require.NoError(t, err)
	assert.Equal(t, prog, got)
	assert.False(t, isRealGeminiHelp(""))
	assert.False(t, isRealGeminiHelp("Usage of agy:\n Gemini CLI mentioned in a flag's text"), "agy's own help is never the real one")
	assert.True(t, isRealGeminiHelp(realGemini))
}
