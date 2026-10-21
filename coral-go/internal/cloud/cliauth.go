package cloud

import "github.com/subgentic/coral/internal/cliauth"

// The command-line handshake lives in internal/cliauth (#3418), so the small agent and
// command-line tools can sign requests without linking the cloud connector and its store.
const (
	CLIAuthHeader = cliauth.CLIAuthHeader
	CLIHelloPath  = cliauth.CLIHelloPath
)

type (
	CLIAuth   = cliauth.CLIAuth
	CLIClient = cliauth.CLIClient
)

var (
	ErrNotCoral    = cliauth.ErrNotCoral
	NewCLIAuth     = cliauth.NewCLIAuth
	NewCLINonce    = cliauth.NewCLINonce
	SignCLIRequest = cliauth.SignCLIRequest
)
