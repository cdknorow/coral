package background

import (
	"errors"
	"fmt"
	"strings"
)

// Workflow step artifacts and step-output templates (#3423, audit CRIT-01, CRIT-02, MED-03).

// maxArtifactEnv caps CORAL_PREV_ARTIFACT_CONTENT: a bigger artifact is left out of the
// environment (CORAL_PREV_ARTIFACT_TRUNCATED=1; read the file at CORAL_PREV_ARTIFACT), so a
// large one can't make the step fail to start (E2BIG).
const maxArtifactEnv = 64 << 10

// maxArtifactRead bounds what Coral reads back from an artifact for a template.
const maxArtifactRead = 16 << 20

var errArtifactPath = errors.New("output_artifact must be a relative path inside the step's folder")

// stripNUL removes NUL bytes, which neither an environment variable nor a shell word can hold.
func stripNUL(s string) string { return strings.ReplaceAll(s, "\x00", "") }

// shellWord quotes s as one POSIX shell word: inside single quotes nothing is expanded, so
// step output can never change the command's syntax (CRIT-01).
func shellWord(s string) string {
	return "'" + strings.ReplaceAll(stripNUL(s), "'", `'\''`) + "'"
}

// noteArtifactRefused makes a refused output_artifact visible in the step (#3423 F2): a
// note in its result and a line at the end of its stderr, not only the server log.
func noteArtifactRefused(sf stepFiles, rel string, err error, result *StepResult) {
	note := fmt.Sprintf("output_artifact %q was not written: %v", rel, err)
	if result != nil {
		result.Note = note
	}
	sf.appendNote("stderr.txt", "coral: "+note) // through the run folder's root (#3587)
}
