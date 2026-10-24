package routes

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Compound shell commands (#7086, QA's #7069 point 6). shellRiskSimple cuts a command at ; | && || and newlines and reads the pieces as simple commands, so a risky command inside
// { rm -rf /etc; }, if ...; then ...; fi, for/while/until ... do ...; done, case ... esac, select, a function body, ( rm -rf /etc ) with spaces, after ! or time, or inside $( ), <( ) and
// backticks was read as the words of the construct, not as a command, and auto-accept let it through.
//
// The command is read with a real parser (mvdan.cc/sh/v3/syntax, Bash dialect, BSD-3-Clause, standard library only) and every simple command in it, wherever the grammar puts it, is
// handed to the same rules, in source order, so a cd earlier in the command still moves the directory later ones run in. A redirection on a compound command ({ ...; } > /etc/x) is
// judged as the redirection of a command. Every command is also judged with the construct's punctuation and keywords turned
// into separators (what a parser failure, PowerShell or zsh-only syntax would otherwise slip past), which only ever adds splits: fail closed, never weaker than the first reading.

// maxParsedCommand bounds what is parsed: a longer command is read with the separator reading only.
const maxParsedCommand = 256 << 10

// compoundRisk is why a command is high-risk once its compound structure is read ("" when it is not): the parsed reading, then the separator reading. Each stands on its own (the tests
// run each alone on every form); the second always runs, not only when the parser failed, because the parser reads the Bash grammar, and a command written for another shell (PowerShell's
// ForEach-Object { ... }, zsh's glob qualifiers) can parse as Bash and still be something else.
func compoundRisk(command, workDir, dir string) string {
	if why := parsedRisk(command, workDir, dir); why != "" {
		return why
	}
	return separatedRisk(command, workDir, dir)
}

// parsedRisk judges every simple command the parser finds, in source order. It also reads what the separator reading cannot: a single & between commands, |&, a line continuation.
func parsedRisk(command, workDir, dir string) string {
	if len(command) > maxParsedCommand {
		return ""
	}
	leaves, ok := leafCommands(command)
	if !ok {
		return ""
	}
	return shellRiskCore(strings.Join(leaves, "\n"), workDir, dir, shellRiskFrom)
}

// separatedRisk judges the command with the constructs' punctuation and keywords turned into separators.
func separatedRisk(command, workDir, dir string) string {
	return shellRiskCore(separatedCompound(command), workDir, dir, shellRiskFrom)
}

// leafCommands is every simple command of command, in source order, each as one line of text, and ok false when the parser could not read it. A compound command's own redirections
// come out as the redirections of a no-op command.
func leafCommands(command string) (leaves []string, ok bool) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	pr := syntax.NewPrinter(syntax.SingleLine(true))
	print := func(st *syntax.Stmt) {
		var b strings.Builder
		if pr.Print(&b, st) == nil {
			leaves = append(leaves, b.String())
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		st, isStmt := n.(*syntax.Stmt)
		if !isStmt {
			return true
		}
		leaf := *st
		leaf.Negated, leaf.Background, leaf.Coprocess = false, false, false
		switch st.Cmd.(type) {
		case nil, *syntax.CallExpr:
			print(&leaf)
		default:
			if len(st.Redirs) > 0 {
				leaf.Cmd = &syntax.CallExpr{Args: []*syntax.Word{{Parts: []syntax.WordPart{&syntax.Lit{Value: ":"}}}}}
				print(&leaf)
			}
		}
		return true
	})
	return leaves, true
}

var (
	compoundPunctRe = regexp.MustCompile("[(){}`]|(^|[\\s;&|])!([\\s;&|]|$)")
	compoundWordRe  = regexp.MustCompile(`(^|[\s;&|])(if|then|else|elif|fi|while|until|do|done|case|esac|select|in|function|coproc|time)([\s;&|]|$)`)
)

// separatedCompound turns the punctuation and the keywords of the compound constructs into ";" so the separator reading sees each command of them. It also splits inside quotes, which
// can only make the reading stricter. Applied twice for a keyword that sits next to another.
func separatedCompound(command string) string {
	out := compoundPunctRe.ReplaceAllString(command, ";")
	for i := 0; i < 2; i++ {
		out = compoundWordRe.ReplaceAllString(out, "$1;$3")
	}
	return out
}
