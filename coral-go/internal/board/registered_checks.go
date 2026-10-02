package board

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCheckTimeout = 30 * time.Second
	maxCheckTimeout     = 2 * time.Minute
	maxCheckLog         = 64 * 1024
)

// RegisteredCheckEvidence is produced by Coral's trusted runner. Completion
// requests cannot supply these fields for a registered_check gate.
type RegisteredCheckEvidence struct {
	CheckID          string
	Passed           bool
	Details          string
	Command          string
	Runner           string
	OutputDigest     string
	ObservedRevision string
	ExitCode         *int
	Log              string
	StartedAt        string
	FinishedAt       string
}

// CompletionCheckRunner is intentionally narrow. Implementations must select
// commands from a reviewed registry; task bodies and gate parameters are never
// interpreted as shell code.
type CompletionCheckRunner interface {
	Run(ctx context.Context, checkID, candidateRevision string, parameters map[string]string) RegisteredCheckEvidence
}

// LocalRegisteredCheckRunner runs only the two reviewed built-ins. The server
// supplies workdir through trusted configuration (CORAL_CHECK_WORKDIR), not a
// task request. An empty workdir makes checks explicitly unavailable.
type LocalRegisteredCheckRunner struct {
	Workdir string
}

func NewLocalRegisteredCheckRunner(workdir string) *LocalRegisteredCheckRunner {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return &LocalRegisteredCheckRunner{}
	}
	return &LocalRegisteredCheckRunner{Workdir: filepath.Clean(workdir)}
}

var safeRevision = regexp.MustCompile(`^[A-Za-z0-9._/@:-]{1,256}$`)
var safePackage = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func (r *LocalRegisteredCheckRunner) Run(ctx context.Context, checkID, candidateRevision string, parameters map[string]string) RegisteredCheckEvidence {
	evidence := RegisteredCheckEvidence{CheckID: checkID, Runner: "coral-local-registered", StartedAt: nowUTC()}
	finish := func() RegisteredCheckEvidence { evidence.FinishedAt = nowUTC(); return evidence }
	if r == nil || r.Workdir == "" {
		evidence.Details = "registered check runner unavailable: configure CORAL_CHECK_WORKDIR"
		return finish()
	}
	if _, err := os.Stat(r.Workdir); err != nil {
		evidence.Details = "registered check runner workdir unavailable"
		return finish()
	}
	if !safeRevision.MatchString(candidateRevision) {
		evidence.Details = "candidate revision is not a safe registered-check input"
		return finish()
	}
	timeout := defaultCheckTimeout
	if raw := parameters["timeout_seconds"]; raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 {
			evidence.Details = "invalid check timeout"
			return finish()
		}
		timeout = time.Duration(seconds) * time.Second
		if timeout > maxCheckTimeout {
			timeout = maxCheckTimeout
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch checkID {
	case "go_test":
		packages := parameters["packages"]
		if packages == "" {
			packages = "./..."
		}
		packageArgs := strings.Fields(packages)
		if len(packageArgs) == 0 || len(packageArgs) > 32 {
			evidence.Details = "go_test packages are empty or exceed the registered limit"
			return finish()
		}
		for _, pkg := range packageArgs {
			if !safePackage.MatchString(pkg) {
				evidence.Details = "go_test package contains unsupported characters"
				return finish()
			}
		}
		evidence.Command = "go test " + strings.Join(packageArgs, " ")
		resolved := runBoundedCommand(checkCtx, r.Workdir, "git", "rev-parse", "--verify", candidateRevision)
		if resolved.Err != nil {
			evidence.Details = "candidate revision is not a commit in the designated repository: " + strings.TrimSpace(resolved.Output)
			return finishRun(evidence, resolved)
		}
		evidence.ObservedRevision = strings.TrimSpace(resolved.Output)
		head := runBoundedCommand(checkCtx, r.Workdir, "git", "rev-parse", "HEAD")
		if head.Err != nil || strings.TrimSpace(head.Output) != evidence.ObservedRevision {
			evidence.Details = "designated repository HEAD does not match candidate_revision: " + strings.TrimSpace(head.Output)
			return finishRun(evidence, head)
		}
		command := append([]string{"go", "test"}, packageArgs...)
		return finishRun(evidence, runBoundedCommand(checkCtx, r.Workdir, command...))
	case "git_ancestry":
		remote := parameters["remote"]
		branch := parameters["branch"]
		if remote == "" || branch == "" || !safeRevision.MatchString(remote) || !safeRevision.MatchString(branch) {
			evidence.Details = "git_ancestry requires safe remote and branch parameters"
			return finish()
		}
		evidence.Command = fmt.Sprintf("git merge-base --is-ancestor %s %s/%s", candidateRevision, remote, branch)
		resolved := runBoundedCommand(checkCtx, r.Workdir, "git", "rev-parse", "--verify", candidateRevision)
		if resolved.Err != nil {
			evidence.Details = "candidate revision is not a commit in the designated repository"
			return finishRun(evidence, resolved)
		}
		evidence.ObservedRevision = strings.TrimSpace(resolved.Output)
		result := runBoundedCommand(checkCtx, r.Workdir, "git", "merge-base", "--is-ancestor", candidateRevision, remote+"/"+branch)
		return finishRun(evidence, result)
	default:
		evidence.Details = fmt.Sprintf("registered check %q is unavailable", checkID)
		return finish()
	}
}

type boundedCommandResult struct {
	Output   string
	Digest   string
	ExitCode *int
	Err      error
}

func runBoundedCommand(ctx context.Context, workdir string, args ...string) boundedCommandResult {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workdir
	// The runner receives only a minimal controlled environment. In particular,
	// no provider keys or arbitrary task environment are inherited.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local"}
	var stdout, stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	combined := redactCheckOutput(stdout.String() + stderr.String())
	digest := sha256.Sum256([]byte(combined))
	result := boundedCommandResult{Output: combined, Digest: "sha256:" + hex.EncodeToString(digest[:]), Err: err}
	if err == nil {
		code := 0
		result.ExitCode = &code
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		result.ExitCode = &code
	} else if ctx.Err() != nil {
		code := -1
		result.ExitCode = &code
	}
	return result
}

func redactCheckOutput(output string) string {
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "password=") || strings.Contains(lower, "token=") || strings.Contains(lower, "secret=") || strings.Contains(lower, "api_key=") || strings.Contains(lower, "authorization: bearer ") {
			lines[i] = "[coral registered check output redacted]"
		}
	}
	return strings.Join(lines, "\n")
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := maxCheckLog - b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return b.Buffer.Write(p)
}

func finishRun(e RegisteredCheckEvidence, result boundedCommandResult) RegisteredCheckEvidence {
	e.OutputDigest = result.Digest
	e.ExitCode = result.ExitCode
	e.Log = result.Output
	if result.Err == nil {
		if e.Details == "" {
			e.Passed = true
			e.Details = "registered check passed"
		}
	} else if result.Err == context.DeadlineExceeded || result.Err == context.Canceled {
		if e.Details == "" {
			e.Details = "registered check timed out or was canceled"
		}
	} else {
		if e.Details == "" {
			e.Details = "registered check failed"
		}
	}
	return e
}

var _ io.Writer = (*boundedBuffer)(nil)
