package board

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const completionChecksFeatureEnv = "CORAL_ENABLE_COMPLETION_CHECKS"

func completionChecksEnabledFromEnv() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(completionChecksFeatureEnv)), "true")
}

// CompletionGate is an orchestrator-declared requirement checked when a task
// is completed. Gate definitions are data, never shell commands.
type CompletionGate struct {
	Type       string            `json:"type"`               // report, test_evidence, landed_revision
	Name       string            `json:"name,omitempty"`     // human-readable gate label
	Artifact   string            `json:"artifact,omitempty"` // required artifact name
	Branch     string            `json:"branch,omitempty"`
	Remote     string            `json:"remote,omitempty"`
	CheckID    string            `json:"check_id,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// CompletionGateResult preserves the check decision and its bounded reason in
// the immutable task workflow record.
type CompletionGateResult struct {
	Type              string `json:"type"`
	Name              string `json:"name,omitempty"`
	Artifact          string `json:"artifact,omitempty"`
	Passed            bool   `json:"passed"`
	Details           string `json:"details"`
	CandidateRevision string `json:"candidate_revision,omitempty"`
	CheckedAt         string `json:"checked_at"`
	CheckID           string `json:"check_id,omitempty"`
	Command           string `json:"command,omitempty"`
	Runner            string `json:"runner,omitempty"`
	OutputDigest      string `json:"output_digest,omitempty"`
	ObservedRevision  string `json:"observed_revision,omitempty"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	Log               string `json:"log,omitempty"`
}

func validateCompletionGates(gates []CompletionGate) error {
	if len(gates) > 8 {
		return fmt.Errorf("at most 8 completion gates are allowed")
	}
	for _, gate := range gates {
		switch gate.Type {
		case "report", "test_evidence", "landed_revision":
		case "registered_check":
			if strings.TrimSpace(gate.CheckID) == "" {
				return fmt.Errorf("registered_check gate requires check_id")
			}
		default:
			return fmt.Errorf("completion gate type %q is unsupported; registered check scripts are not configured", gate.Type)
		}
		if gate.Type != "registered_check" && strings.TrimSpace(gate.Artifact) == "" {
			return fmt.Errorf("completion gate %q requires an artifact name", gate.Type)
		}
		if len(gate.Artifact) > 128 || len(gate.Name) > 256 || len(gate.Branch) > 256 || len(gate.Remote) > 256 {
			return fmt.Errorf("completion gate fields are too long")
		}
		if gate.Type == "landed_revision" && (strings.TrimSpace(gate.Branch) == "" || strings.TrimSpace(gate.Remote) == "") {
			return fmt.Errorf("landed_revision gate requires branch and remote")
		}
	}
	return nil
}

func validateCompletionGateConfiguration(gates []CompletionGate, enabled bool) error {
	if err := validateCompletionGates(gates); err != nil {
		return err
	}
	if !enabled && len(gates) > 0 {
		return fmt.Errorf("completion_gates are an experimental feature disabled by server policy; set %s=true during an explicit server rollout", completionChecksFeatureEnv)
	}
	return nil
}

func checkCompletionGates(gates []CompletionGate, artifacts []TaskArtifact, candidateRevision string) ([]CompletionGateResult, error) {
	return checkCompletionGatesWithRunner(gates, artifacts, candidateRevision, nil)
}

func checkCompletionGatesWithRunner(gates []CompletionGate, artifacts []TaskArtifact, candidateRevision string, runner CompletionCheckRunner) ([]CompletionGateResult, error) {
	return checkCompletionGatesWithPolicy(gates, artifacts, candidateRevision, runner, true)
}

func checkCompletionGatesWithPolicy(gates []CompletionGate, artifacts []TaskArtifact, candidateRevision string, runner CompletionCheckRunner, enabled bool) ([]CompletionGateResult, error) {
	if len(candidateRevision) > 256 {
		return nil, fmt.Errorf("candidate_revision must be at most 256 bytes")
	}
	if err := validateCompletionGates(gates); err != nil {
		return nil, err
	}
	results := make([]CompletionGateResult, 0, len(gates))
	if !enabled {
		for _, gate := range gates {
			results = append(results, CompletionGateResult{
				Type: gate.Type, Name: gate.Name, Artifact: gate.Artifact,
				CandidateRevision: candidateRevision, CheckedAt: nowUTC(), CheckID: gate.CheckID,
				Details: fmt.Sprintf("completion gate disabled by server policy (%s)", completionChecksFeatureEnv),
			})
		}
		return results, nil
	}
	for _, gate := range gates {
		result := CompletionGateResult{Type: gate.Type, Name: gate.Name, Artifact: gate.Artifact, CandidateRevision: candidateRevision, CheckedAt: nowUTC(), CheckID: gate.CheckID}
		if gate.Type == "registered_check" {
			if !enabled {
				result.Details = fmt.Sprintf("registered check disabled by server policy (%s)", completionChecksFeatureEnv)
				results = append(results, result)
				continue
			}
			if runner == nil {
				result.Details = "registered check runner unavailable"
			} else {
				evidence := runner.Run(context.Background(), gate.CheckID, candidateRevision, gate.Parameters)
				result.Passed, result.Details, result.Command = evidence.Passed, evidence.Details, evidence.Command
				result.Runner, result.OutputDigest, result.ObservedRevision = evidence.Runner, evidence.OutputDigest, evidence.ObservedRevision
				result.ExitCode, result.Log = evidence.ExitCode, evidence.Log
			}
			results = append(results, result)
			continue
		}
		var artifact *TaskArtifact
		for i := range artifacts {
			if artifacts[i].Name == gate.Artifact {
				artifact = &artifacts[i]
				break
			}
		}
		if artifact == nil {
			result.Details = fmt.Sprintf("required artifact %q is missing", gate.Artifact)
			results = append(results, result)
			continue
		}
		switch gate.Type {
		case "report":
			result.Passed = true
			result.Details = "report artifact present"
		case "test_evidence":
			switch {
			case artifact.Kind != "test":
				result.Details = "artifact kind must be test"
			case strings.TrimSpace(artifact.Command) == "":
				result.Details = "test evidence must name the executed command"
			case strings.TrimSpace(artifact.Runner) == "":
				result.Details = "test evidence must identify its runner"
			case artifact.ExitCode == nil || *artifact.ExitCode != 0:
				result.Details = "test evidence must report exit_code 0"
			case strings.TrimSpace(artifact.OutputDigest) == "":
				result.Details = "test evidence must include output_digest"
			case !parseGateCheckTime(artifact.StartedAt) || !parseGateCheckTime(artifact.FinishedAt):
				result.Details = "test evidence must include start and finish timestamps"
			case candidateRevision == "" || artifact.Revision != candidateRevision:
				result.Details = "test evidence revision does not match candidate_revision"
			default:
				result.Passed = true
				result.Details = "test evidence is complete for the submitted candidate"
			}
		case "landed_revision":
			switch {
			case candidateRevision == "" || artifact.Revision != candidateRevision:
				result.Details = "landed revision does not match candidate_revision"
			case artifact.Branch != gate.Branch:
				result.Details = "landed revision branch does not match the declared branch"
			case artifact.Remote != gate.Remote:
				result.Details = "landed revision remote does not match the declared remote"
			case !artifact.Landed:
				result.Details = "artifact does not attest that the revision landed"
			default:
				result.Passed = true
				result.Details = "submitted revision is attested on the designated remote branch"
			}
		}
		results = append(results, result)
	}
	for _, result := range results {
		if !result.Passed {
			return results, fmt.Errorf("completion gate %q failed: %s", result.NameOrType(), result.Details)
		}
	}
	return results, nil
}

func (r CompletionGateResult) NameOrType() string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return r.Type
}

func parseGateCheckTime(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
