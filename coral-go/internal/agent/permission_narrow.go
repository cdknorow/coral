package agent

// What a permission mode and a capability set each allow, and the narrower of the two (#6425, Security's rule): an explicit mode is always applied and capabilities only narrow it for
// the CLIs that keep only a sandbox and an approval mode (Codex, Antigravity). Claude keeps the lists and the mode apart.

const codexSandboxNone = "danger-full-access"

// codexPair is Codex's sandbox and approval policy as they are put on the command line; "" is "not set".
type codexPair struct{ sandbox, approval string }

func codexSandboxRank(s string) int {
	switch s {
	case "read-only":
		return 1
	case "workspace-write":
		return 2
	case codexSandboxNone:
		return 3
	}
	return 2 // not set: Codex's own default is workspace-write
}

func codexApprovalRank(a string) int {
	if a == "never" {
		return 2
	}
	return 1 // on-request, and what is not set (it asks)
}

// codexPairOf is the pair the capabilities stand for.
func codexPairOf(perms *CodexPermissions) codexPair {
	switch {
	case perms.FullAuto:
		return codexPair{"workspace-write", "never"}
	}
	return codexPair{perms.SandboxMode, perms.ApprovalPolicy}
}

// codexPairForMode is the pair an explicit mode stands for; false for "" and "default" (the CLI's confirm mode is no explicit choice) and for a mode Codex has nothing for.
func codexPairForMode(mode string) (codexPair, bool) {
	switch mode {
	case "plan":
		return codexPair{"read-only", "on-request"}, true
	case "acceptEdits":
		return codexPair{"workspace-write", "on-request"}, true
	case "auto", "dontAsk":
		return codexPair{"workspace-write", "never"}, true
	case "bypassPermissions":
		return codexPair{codexSandboxNone, "never"}, true
	}
	return codexPair{}, false
}

// narrowCodexPair is the narrower sandbox and the narrower approval of the two.
func narrowCodexPair(a, b codexPair) codexPair {
	out := a
	if codexSandboxRank(b.sandbox) < codexSandboxRank(a.sandbox) {
		out.sandbox = b.sandbox
	}
	if codexApprovalRank(b.approval) < codexApprovalRank(a.approval) {
		out.approval = b.approval
	}
	return out
}

// Antigravity keeps one mode, widest to narrowest: nothing asks (--dangerously-skip-permissions), accept-edits, plan, and the default that asks before acting.
const (
	agyRankDefault = iota
	agyRankPlan
	agyRankAcceptEdits
	agyRankSkip
)

func agyRankForMode(mode string) (int, bool) {
	switch mode {
	case "plan", "dontAsk":
		return agyRankPlan, true
	case "acceptEdits":
		return agyRankAcceptEdits, true
	case "auto", "bypassPermissions":
		return agyRankSkip, true
	}
	return 0, false
}

func agyRankOf(p *AgyPermissions) int {
	switch {
	case p.DangerouslySkipPermissions:
		return agyRankSkip
	case p.Mode == "accept-edits":
		return agyRankAcceptEdits
	case p.Mode == "plan":
		return agyRankPlan
	}
	return agyRankDefault
}

// narrowAgyPermissions is the explicit mode's rank, or the capabilities' when that is narrower. Shell and Write with no deny (skip permissions) restrict nothing, so the mode stands.
func narrowAgyPermissions(caps *AgyPermissions, mode int) *AgyPermissions {
	rank := agyRankOf(caps)
	if caps.DangerouslySkipPermissions || rank > mode {
		rank = mode
	}
	out := &AgyPermissions{Sandbox: caps.Sandbox}
	switch rank {
	case agyRankSkip:
		out.DangerouslySkipPermissions = true
	case agyRankAcceptEdits:
		out.Mode = "accept-edits"
	case agyRankPlan:
		out.Mode = "plan"
	}
	return out
}
