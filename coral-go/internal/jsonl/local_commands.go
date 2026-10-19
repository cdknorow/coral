package jsonl

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// LocalCommand is a slash command a Claude Code user ran in the session (/model, /effort, ...), as the transcript records it: the
// command's name and arguments, and what Claude Code printed for it (#4969). The transcript writes it as system entries of subtype
// "local_command": one with <command-name>, <command-message> and <command-args>, and one with <local-command-stdout>.
type LocalCommand struct {
	Name   string    // "/model"
	Args   string    // the text after the name, "" when none
	Output string    // what the command printed, cleaned and bounded; "" until its output entry is written
	At     time.Time // when the command entry was written
}

var (
	localCommandNameR   = regexp.MustCompile(`<command-name>\s*(/[A-Za-z0-9_:-]{1,40})\s*</command-name>`)
	localCommandArgsR   = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	localCommandStdoutR = regexp.MustCompile(`(?s)<local-command-stdout>(.*?)</local-command-stdout>`)
)

// localCommandTail is how much of the end of a transcript is searched: a command just run is at its end.
const localCommandTail = 256 << 10

// ReadClaudeLocalCommands returns the slash commands a Claude session ran at or after since, oldest first, from the end of its
// transcript. A command's output is the output entry that follows it. Nothing is returned when the transcript is not found.
func (r *SessionReader) ReadClaudeLocalCommands(sessionID, workingDir string, since time.Time) []LocalCommand {
	path := resolveTranscriptPath(sessionID, workingDir, "claude")
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > localCommandTail {
		start = info.Size() - localCommandTail
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil
	}
	if start > 0 { // begin at a line boundary
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return parseLocalCommands(buf, since)
}

func parseLocalCommands(data []byte, since time.Time) []LocalCommand {
	var out []LocalCommand
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"local_command"`)) {
			continue
		}
		var rec struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "system" || rec.Subtype != "local_command" {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if m := localCommandNameR.FindStringSubmatch(rec.Content); m != nil {
			cmd := LocalCommand{Name: m[1], At: at}
			if a := localCommandArgsR.FindStringSubmatch(rec.Content); a != nil {
				cmd.Args = cleanCommandText(a[1], 120)
			}
			out = append(out, cmd)
			continue
		}
		if m := localCommandStdoutR.FindStringSubmatch(rec.Content); m != nil && len(out) > 0 && out[len(out)-1].Output == "" {
			out[len(out)-1].Output = cleanCommandText(m[1], 300)
		}
	}
	kept := out[:0]
	for _, c := range out {
		if !c.At.Before(since) {
			kept = append(kept, c)
		}
	}
	return kept
}

// cleanCommandText is what a command printed as one short line of plain text: control characters (a terminal escape among them)
// and the formatting backticks removed, runs of space folded, at most n characters.
func cleanCommandText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}
