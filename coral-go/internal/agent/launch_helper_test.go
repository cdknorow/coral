package agent

import "fmt"

// buildLaunch is LaunchCommand for tests that build a command and inspect it: a failure to
// write the temp files is a broken test machine, not a case under test.
func buildLaunch(ag Agent, p LaunchParams) string {
	cmd, err := ag.LaunchCommand(p)
	if err != nil {
		panic(fmt.Sprintf("LaunchCommand: %v", err))
	}
	return cmd
}

// mustTempFile is writeTempFile for tests that need the file.
func mustTempFile(prefix, id, ext string, content []byte) string {
	p, err := writeTempFile(prefix, id, ext, content)
	if err != nil {
		panic(fmt.Sprintf("writeTempFile: %v", err))
	}
	return p
}
