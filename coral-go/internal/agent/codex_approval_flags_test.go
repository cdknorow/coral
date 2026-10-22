package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5890: Codex 0.163 takes the approval policy as -a or --ask-for-approval (both with a space and with =), and only the values on-request and never. It rejects --approval-mode (the name
// of an older Codex), and untrusted and on-failure as values. Coral still reads the old spellings and values (permission profiles stored them), so what it emits must be translated.
// The table is what `codex <flags> --help` answers in a scratch HOME: accepted = exit 0.
var codex163Accepts = struct {
	names  map[string]bool
	values map[string]bool
}{
	names:  map[string]bool{"-a": true, "--ask-for-approval": true},
	values: map[string]bool{"on-request": true, "never": true},
}

// codexAcceptsApproval reports whether Codex 0.163 takes the approval flag in argv[i:] (one token with =, two otherwise), and how many tokens it used.
func codexAcceptsApproval(argv []string, i int) (ok bool, n int) {
	name, value, inline := strings.Cut(argv[i], "=")
	if inline {
		return codex163Accepts.names[name] && codex163Accepts.values[value], 1
	}
	if i+1 >= len(argv) {
		return false, 1
	}
	return codex163Accepts.names[name] && codex163Accepts.values[argv[i+1]], 2
}

func approvalFlagsOf(argv []string) (flags [][]string, bad []string) {
	for i := 0; i < len(argv); i++ {
		name, _, _ := strings.Cut(argv[i], "=")
		if name == "-a" || name == "--ask-for-approval" || name == "--approval-mode" {
			ok, n := codexAcceptsApproval(argv, i)
			end := min(i+n, len(argv))
			flags = append(flags, argv[i:end])
			if !ok {
				bad = append(bad, strings.Join(argv[i:end], " "))
			}
			i += n - 1
		}
	}
	return flags, bad
}

func TestEveryLegacyApprovalSpellingAndValueGivesArgvCodex163Accepts(t *testing.T) {
	names := []string{"-a", "--ask-for-approval", "--approval-mode"}
	values := []string{"untrusted", "on-failure", "on-request", "never"}
	want := map[string]string{"untrusted": "on-request", "on-failure": "on-request", "on-request": "on-request", "never": "never"}
	for _, name := range names {
		for _, value := range values {
			// the name's own spelling is kept when Codex has it; the removed --approval-mode is emitted as -a
			emitted := name
			if name == "--approval-mode" {
				emitted = "-a"
			}
			for _, form := range []struct {
				flags []string
				argv  []string
			}{
				{[]string{name, value}, []string{emitted, want[value]}},
				{[]string{name + "=" + value}, []string{emitted + "=" + want[value]}},
			} {
				parts, _, applied := codexUserFlags(nil, form.flags, false, false)
				assert.Equal(t, form.argv, parts, strings.Join(form.flags, " "))
				assert.True(t, applied, strings.Join(form.flags, " "))
				_, bad := approvalFlagsOf(parts)
				assert.Empty(t, bad, "Codex 0.163 rejects: "+strings.Join(parts, " "))
			}
		}
	}
}

// Nothing in a whole launch command is a flag Codex 0.163 rejects, whatever legacy spelling went in, and a policy is never widened: untrusted and on-failure ask, only an explicit never skips.
func TestCodexLaunchCommandHasNoRejectedApprovalFlag(t *testing.T) {
	for _, flags := range [][]string{
		{"--approval-mode", "untrusted"}, {"--approval-mode=on-request"}, {"--ask-for-approval", "untrusted"}, {"--ask-for-approval=untrusted"},
		{"-a", "untrusted"}, {"-a=on-failure"}, {"--sandbox", "read-only", "--approval-mode", "never"},
	} {
		cmd, err := (&CodexAgent{}).LaunchCommand(LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", CLIPath: "codex", Flags: flags})
		require.NoError(t, err)
		_, bad := approvalFlagsOf(strings.Fields(cmd))
		assert.Empty(t, bad, strings.Join(flags, " ")+" -> "+cmd)
		assert.NotContains(t, cmd, "--approval-mode", strings.Join(flags, " "))
		assert.NotContains(t, cmd, "untrusted", strings.Join(flags, " "))
		assert.NotContains(t, cmd, "on-failure", strings.Join(flags, " "))
	}
	// untrusted must not become never
	parts, _, _ := codexUserFlags(nil, []string{"--ask-for-approval", "untrusted"}, false, false)
	assert.Equal(t, []string{"--ask-for-approval", "on-request"}, parts)
}

// The table above against the real Codex, when asked to: CORAL_TEST_REAL_CODEX=<path to the codex binary>. It runs only `--help` with each flag, in a scratch HOME and CODEX_HOME and an empty environment.
func TestCodex163ApprovalTableMatchesTheRealParser(t *testing.T) {
	bin := os.Getenv("CORAL_TEST_REAL_CODEX")
	if bin == "" {
		t.Skip("set CORAL_TEST_REAL_CODEX to the codex binary to check the table against it")
	}
	home := t.TempDir()
	for _, name := range []string{"-a", "--ask-for-approval", "--approval-mode"} {
		for _, value := range []string{"untrusted", "on-failure", "on-request", "never"} {
			for _, argv := range [][]string{{name, value}, {name + "=" + value}} {
				cmd := exec.Command(bin, append(argv, "--help")...)
				cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, "codex"), "PATH=/usr/bin:/bin"}
				err := cmd.Run()
				accepted, _ := codexAcceptsApproval(argv, 0)
				assert.Equal(t, accepted, err == nil, "codex "+strings.Join(argv, " ")+" --help: the table and the parser disagree")
			}
		}
	}
}
