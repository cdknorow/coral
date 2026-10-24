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

// riskRun is one judgement of one command. The three readings (the rules, the parsed leaves, the separated text) each meet a shell wrapper's inner command (bash -c '...') and judge it
// again, so a wrapper in a wrapper in a wrapper was read three times per level (#7086, QA's round 2: 2.5 times more per level than the old linear reading). A run remembers what it has judged
// by (command, folder, directory): each distinct inner command is judged once, whichever reading meets it first, and the cost is the depth times one pass.
type riskRun struct{ memo map[riskKey]string }

type riskKey struct{ command, workDir, dir string }

func newRiskRun() *riskRun { return &riskRun{memo: map[riskKey]string{}} }

// from is shellRiskFrom within this run.
func (r *riskRun) from(command, workDir, dir string) string {
	k := riskKey{command, workDir, dir}
	if why, ok := r.memo[k]; ok {
		return why
	}
	why := shellRiskCore(command, workDir, dir, r.from)
	if why == "" {
		why = r.compound(command, workDir, dir)
	}
	r.memo[k] = why
	return why
}

// compound is why a command is high-risk once its compound structure is read ("" when it is not): the parsed reading, then the separator reading. Each stands on its own (the tests
// run each alone on every form); the second always runs, not only when the parser failed, because the parser reads the Bash grammar, and a command written for another shell (PowerShell's
// ForEach-Object { ... }, zsh's glob qualifiers) can parse as Bash and still be something else.
func (r *riskRun) compound(command, workDir, dir string) string {
	if why := r.parsed(command, workDir, dir); why != "" {
		return why
	}
	return r.separated(command, workDir, dir)
}

// parsed judges every simple command the parser finds, in source order. It also reads what the separator reading cannot: a single & between commands, |&, a line continuation. A command
// so nested that reading it would cost more than a few passes over its own text (a $( ) inside a $( ) a thousand times: each level is a command with the rest inside it) is not read: it is
// answered as risky, so the person is asked, which fails closed and takes the time of one pass (#7086, QA's round 1).
func (r *riskRun) parsed(command, workDir, dir string) string {
	if len(command) > maxParsedCommand {
		return ""
	}
	leaves, status := readLeaves(command)
	switch status {
	case leavesTooComplex:
		return "it is nested too deeply to check"
	case leavesUnreadable:
		return ""
	}
	return shellRiskCore(strings.Join(leaves, "\n"), workDir, dir, r.from)
}

// separated judges the command with the constructs' punctuation and keywords turned into separators.
func (r *riskRun) separated(command, workDir, dir string) string {
	return shellRiskCore(separatedCompound(command), workDir, dir, r.from)
}

// compoundRisk, parsedRisk and separatedRisk are the readings in a run of their own (the tests run each alone).
func compoundRisk(command, workDir, dir string) string {
	return newRiskRun().compound(command, workDir, dir)
}
func parsedRisk(command, workDir, dir string) string {
	return newRiskRun().parsed(command, workDir, dir)
}
func separatedRisk(command, workDir, dir string) string {
	return newRiskRun().separated(command, workDir, dir)
}

// leafCommands is every simple command of command, in source order, each as one line of text, and ok false when the parser could not read it or the command is too complex to read. A
// compound command's own redirections come out as the redirections of a no-op command.
func leafCommands(command string) (leaves []string, ok bool) {
	leaves, status := readLeaves(command)
	return leaves, status == leavesRead
}

// leafStatus says how readLeaves ended.
type leafStatus int

const (
	leavesRead       leafStatus = iota // every simple command was found
	leavesUnreadable                   // the parser could not read the command
	leavesTooComplex                   // the commands found would cost more to print than leafBudget allows
)

// leafBudget is how much text readLeaves may print: a leaf holds the text of everything nested in its words, so a command nested n deep prints n times its own length. A real command prints
// about its own length once; four times that and a little more is room for the deepest ordinary nesting, and what needs more is answered as too complex.
func leafBudget(command string) int { return 4*len(command) + 8192 }

func readLeaves(command string) (leaves []string, status leafStatus) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, leavesUnreadable
	}
	pr := syntax.NewPrinter(syntax.SingleLine(true))
	budget := leafBudget(command)
	printed := 0
	complex := false
	print := func(st *syntax.Stmt) {
		if complex {
			return
		}
		var b strings.Builder
		if pr.Print(&b, st) == nil {
			printed += b.Len()
			if printed > budget {
				complex = true
				return
			}
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
		return !complex // stop walking once it is too complex
	})
	if complex {
		return nil, leavesTooComplex
	}
	return leaves, leavesRead
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
