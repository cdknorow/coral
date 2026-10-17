package license

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"

	"github.com/cdknorow/coral/internal/testguard"
	terms "github.com/cdknorow/coral/pkg/license"
)

// EULAText is the Terms of Service the user accepts, and EULAVersion its
// version (pkg/license, which the Coral Hub imports too).
const (
	EULAText    = terms.EULAText
	EULAVersion = terms.EULAVersion
)

// EULAMarkerPath returns the path to the EULA acceptance marker file.
func EULAMarkerPath() string { return eulaMarkerPath() }

// eulaMarkerPath is replaced in tests (the real one is under ~/.coral).
var eulaMarkerPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".coral", ".eula-accepted")
}

// The acceptance marker (~/.coral/.eula-accepted) records which terms were
// accepted, how, and their digest:
//
//	accepted
//	version=2026-03-26
//	sha256=c1997b07…
//	source=dialog|legacy|hub
//	rewritten_at=<RFC3339>   (a legacy marker's rewrite only)
//
// Before versions were recorded the marker held just "accepted" (written by the
// terms dialog, and by the Windows installer after showing tools/license.rtf).
// Every text such a marker could have been written after is today's text
// (verified from git history: one EULAText since the marker was introduced, and
// license.rtf equal to it, pinned by TestInstallerTermsMatchEULAText), so a legacy
// marker is rewritten in place as an acceptance of the current version, source
// "legacy". A marker for other terms (version or digest) is not acceptance.

// eulaNow is the clock for rewritten_at (tests pin it).
var eulaNow = time.Now

// eulaMarker is a parsed acceptance marker.
type eulaMarker struct {
	Version, Digest, Source string
	Legacy                  bool // "accepted" only, from before versions were recorded
}

func parseEULAMarker(content string) eulaMarker {
	var m eulaMarker
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "version":
				m.Version = v
			case "sha256":
				m.Digest = v
			case "source":
				m.Source = v
			}
		}
	}
	m.Legacy = m.Version == ""
	return m
}

func (m eulaMarker) current() bool {
	return m.Legacy || (m.Version == EULAVersion && m.Digest == terms.EULADigest())
}

func readEULAMarker() (eulaMarker, bool) {
	marker := EULAMarkerPath()
	if marker == "" {
		return eulaMarker{Version: EULAVersion, Digest: terms.EULADigest()}, true
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return eulaMarker{}, false
	}
	return parseEULAMarker(string(data)), true
}

// AcceptedEULAVersion returns the version of the terms the user accepted, or ""
// when they have not accepted any. A legacy marker is the current version.
func AcceptedEULAVersion() string {
	m, ok := readEULAMarker()
	switch {
	case !ok:
		return ""
	case m.Legacy:
		return EULAVersion
	}
	return m.Version
}

// IsEULAAccepted reports whether the user accepted the current terms: the
// marker names EULAVersion with this text's digest, or is a legacy marker.
func IsEULAAccepted() bool {
	m, ok := readEULAMarker()
	return ok && m.current()
}

// MigrateLegacyEULAMarker rewrites a legacy "accepted" marker as an acceptance
// of the current version with its digest (source "legacy"). It runs at every
// launch before the terms check, never prompts and never blocks: if the marker
// cannot be rewritten (a read-only home), the legacy marker keeps working as it
// did, and that is logged.
func MigrateLegacyEULAMarker() {
	m, ok := readEULAMarker()
	if !ok || !m.Legacy || EULAMarkerPath() == "" {
		return
	}
	if err := writeEULAMarker("legacy"); err != nil {
		log.Printf("[EULA] legacy acceptance marker kept as is (could not record its version: %v)", err)
		return
	}
	log.Printf("[EULA] legacy acceptance marker recorded as terms version %s", EULAVersion)
}

// WriteEULAMarker records that the user accepted the current terms in Coral's
// own terms dialog.
func WriteEULAMarker() error { return writeEULAMarker("dialog") }

// RecordHubTermsAcceptance records the acceptance the Coral Hub confirmed when a
// machine enrolled: only when the Hub's terms_accepted is exactly this binary's
// EULAVersion (contract §12.1). A local marker, legacy or not, is never
// evidence for the Hub; enrollment sends eula_version and writes this only
// after the Hub's answer.
func RecordHubTermsAcceptance(termsAccepted string) error {
	if termsAccepted != EULAVersion {
		return fmt.Errorf("the Hub accepted terms version %q, this Coral has %q", termsAccepted, EULAVersion)
	}
	return writeEULAMarker("hub")
}

func writeEULAMarker(source string) error {
	marker := EULAMarkerPath()
	if marker == "" {
		return nil
	}
	testguard.CheckDataPath(marker)
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		return err
	}
	content := "accepted\nversion=" + EULAVersion + "\nsha256=" + terms.EULADigest() + "\nsource=" + source + "\n"
	if source == "legacy" {
		content += "rewritten_at=" + eulaNow().UTC().Format(time.RFC3339) + "\n"
	}
	return os.WriteFile(marker, []byte(content), 0644)
}

// CheckAndPromptEULA checks if the EULA has been accepted. If not, uses the
// provided showDialog function to prompt the user. Returns true if accepted.
func CheckAndPromptEULA(showDialog func(tosText string) bool) bool {
	MigrateLegacyEULAMarker()
	if IsEULAAccepted() {
		log.Println("[EULA] previously accepted")
		return true
	}
	log.Println("[EULA] showing acceptance dialog")
	if !showDialog(EULAText) {
		log.Println("[EULA] user declined")
		return false
	}
	log.Println("[EULA] user accepted")
	if err := WriteEULAMarker(); err != nil {
		log.Printf("[EULA] WARNING: could not write marker file: %v", err)
	}
	return true
}

// powerShellQuote doubles every character PowerShell reads as a single quote: the ASCII apostrophe and U+2018, U+2019, U+201A and U+201B.
var powerShellQuote = strings.NewReplacer("'", "''", "\u2018", "\u2018\u2018", "\u2019", "\u2019\u2019", "\u201a", "\u201a\u201a", "\u201b", "\u201b\u201b")

// executablePath is the running program's path; a variable so a test can name one.
var executablePath = os.Executable

// eulaCommandLines are the lines a person can paste into a terminal to be asked: this very program, quoted for the shell it runs in (#4360). A path with a
// control character in it (anything unicode.IsPrint refuses except an ordinary space: a newline, an escape, 0x7f, a C1 control, U+2028 or U+2029, a bidi or zero-width character) is never printed as it is, so a hostile path cannot rewrite
// the terminal or smuggle in a second command: the program's usual name stands in. On Windows both forms are given, PowerShell's call operator with a
// single-quoted path (an apostrophe doubled) and the Command Prompt's double-quoted path; elsewhere one POSIX single-quoted form when a shell would split
// or expand the path.
func eulaCommandLines(goos, path string) []string {
	if path == "" {
		return []string{"coral"}
	}
	for _, r := range path {
		if r != ' ' && !unicode.IsPrint(r) {
			return []string{"coral"}
		}
	}
	if goos == "windows" {
		return []string{
			"PowerShell:      & '" + powerShellQuote.Replace(path) + "'",
			"Command Prompt:  \"" + path + "\"",
		}
	}
	if !strings.ContainsAny(path, " \t'\"$`\\&;|<>()*?[]{}!#~") {
		return []string{path}
	}
	return []string{"'" + strings.ReplaceAll(path, "'", `'\''`) + "'"}
}

func eulaCommand() []string {
	path, err := executablePath()
	if err != nil {
		path = ""
	}
	return eulaCommandLines(runtime.GOOS, path)
}

// TerminalEULADialog shows the TOS in the terminal and prompts for acceptance.
func TerminalEULADialog(tosText string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Nobody can answer: a background start must not block in read(0).
		fmt.Println("The Terms of Service have not been accepted and stdin is not a terminal, so Coral cannot ask.")
		fmt.Println("To accept them, run this once in a terminal, read the terms and type 'accept':")
		fmt.Println()
		for _, line := range eulaCommand() {
			fmt.Println("    " + line)
		}
		return false
	}
	fmt.Println(tosText)
	fmt.Println("---")
	fmt.Print("Type 'accept' to agree to the Terms of Service, or press Enter to decline: ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(strings.ToLower(scanner.Text())) == "accept"
	}
	return false
}
