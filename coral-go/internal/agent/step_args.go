package agent

import (
	at "github.com/subgentic/coral/internal/agenttypes"
)

// StepPrintArgs is the argument list for a one-shot, non-interactive run of agentType's CLI on prompt (a workflow
// step), as real argv. The prompt can carry earlier steps' output and board text, so it is placed where the CLI
// can only read it as the prompt (#36, Security's W1), whatever it starts with:
//
//   - codex: "exec ... -" with the prompt on stdin (codex exec reads its instructions from stdin for "-"; it takes
//     no --print);
//   - agy: --print is its own flag and takes the next argument as its value, so the prompt is the last pair and
//     nothing follows it, with --disable-slash-commands before it;
//   - pi and any other CLI: --print and the prompt as the last argument, a leading newline in front of one that
//     starts with "-" or "@" (guardPositionalPrompt).
//
// stdin is the prompt for a CLI that reads it there, "" otherwise. flags and modelArgs go before the prompt. Claude
// has its own path (PrintArgs, the prompt on stdin).
func StepPrintArgs(agentType string, flags, modelArgs []string, prompt string) (args []string, stdin string) {
	switch agentType {
	case at.Codex:
		args = append([]string{"exec"}, modelArgs...)
		args = append(args, flags...)
		return append(args, "-"), prompt
	case at.Agy:
		args = append(append([]string{}, modelArgs...), flags...)
		// --disable-slash-commands: agy expands slash commands and skills in the prompt, and a step prompt carries
		// earlier steps' output and board text; whitespace in front does not stop it.
		return append(args, "--disable-slash-commands", "--print", prompt), ""
	default:
		args = append([]string{"--print"}, modelArgs...)
		args = append(args, flags...)
		return append(args, guardPositionalPrompt(prompt)), ""
	}
}
