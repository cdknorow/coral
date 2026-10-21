package background

import "errors"

// errNoWorkflowShell is the step's error when no POSIX shell can be found (Windows without Git for Windows).
var errNoWorkflowShell = errors.New("workflow shell steps need Git for Windows (sh was not found)")
