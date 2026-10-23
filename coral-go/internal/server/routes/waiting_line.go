package routes

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/clitext"
)

// What an agent is waiting for is the agent's own text (Security's rule for the Inbox row, #6581): "Claude needs your permission to use Bash: <command>" can hold a command, paths, a
// token, or anything the agent was shown. The live row's waiting_summary is built once here, so the local dashboard and Coral Hub get the same bounded line and the Hub never has to
// clean it: the WHOLE text is cleaned (no escape, control, bidi or zero-width characters), then redacted whole (a token is found by its whole length and a key by its header and
// footer, so nothing is cut or split first), then the home folder is shown as "~", then it is one line of at most maxWaitingLine characters with an ellipsis, and a line of the text that is
// one unbroken run of base64, hex or token characters is never shown. When the text cannot be checked for secrets nothing is shown, never the raw text. At View through Coral
// Hub the connector drops the field altogether (pkg/tunnel/viewdrop.go); in a push, an email or a notification it is never used.
const maxWaitingLine = 200

// homeFolder is the home folder text is masked against (a test sets it).
var homeFolder = func() string {
	h, _ := os.UserHomeDir()
	return h
}

// redactWaiting is the redactor the line goes through (a test replaces it to make it fail).
var redactWaiting = redactComment

// waitingLine is the line to show for text, or nil when there is none to show.
func waitingLine(text string) any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	text = boundedText(text, false)
	red, ok := redactWaiting(clitext.Clean(text))
	if !ok {
		return nil
	}
	// A blob is judged per line of the text, before the lines become one: a key's body lines (the redactor was not given its header) would otherwise run together with spaces
	// between them and no longer look like blobs.
	var kept []string
	for _, l := range strings.Split(maskHome(red), "\n") {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" || isBlob(l) {
			continue
		}
		kept = append(kept, l)
	}
	line := runOneLine(strings.Join(kept, " "), maxWaitingLine)
	if line == "" || isBlob(line) {
		return nil
	}
	return line
}

// waitingSource is the text a row's waiting_summary is made from: what the open request wants to do (the agent's PermissionRequest, stored already sanitized) when it said, else the
// summary. Only the display reads it; the classifiers and auto-accept read the summary alone (#6622).
func waitingSource(wants, summary string) string {
	if strings.TrimSpace(wants) != "" {
		return wants
	}
	return summary
}

// sanitizeWants turns the words a PermissionRequest sent for its wants into what is stored: the same line waitingLine shows, or nothing when there is none to show. The raw text is never
// stored, logged or sent anywhere (#6622).
func sanitizeWants(detail any) any {
	d, ok := detail.(map[string]any)
	if !ok {
		return detail
	}
	raw, has := d["wants"]
	if !has {
		return detail
	}
	text, _ := raw.(string)
	if line, ok := waitingLine(text).(string); ok && line != "" {
		d["wants"] = line
	} else {
		delete(d, "wants")
	}
	return d
}

// waitingLineIf is waitingLine when cond holds, else nil (an agent that is not waiting has no line).
func waitingLineIf(cond bool, text string) any {
	if !cond {
		return nil
	}
	return waitingLine(text)
}

// maskHome shows the home folder as "~" wherever text names it, as a path component (the folder itself or something under it), in either slash form.
func maskHome(text string) string {
	home := homeFolder()
	if len(home) < 2 {
		return text
	}
	forms := []string{home}
	if slash := filepath.ToSlash(home); slash != home {
		forms = append(forms, slash)
	}
	for _, h := range forms {
		h = strings.TrimRight(h, `/\`)
		if h == "" {
			continue
		}
		var b strings.Builder
		for {
			i := strings.Index(text, h)
			if i < 0 {
				break
			}
			end := i + len(h)
			if (i > 0 && isPathWordByte(text[i-1])) || (end < len(text) && isPathWordByte(text[end])) {
				b.WriteString(text[:end]) // part of a longer name: left as it is
				text = text[end:]
				continue
			}
			b.WriteString(text[:i])
			b.WriteString("~")
			text = text[end:]
		}
		b.WriteString(text)
		text = b.String()
	}
	return text
}

// isPathWordByte reports whether c continues a folder name (so "/Users/ann" is not the start of "/Users/annabel").
func isPathWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}
