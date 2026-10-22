package hubproxy

import (
	"mime"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #297 (QA's #278 row 7): a download keeps a name in any script. filename= stays the sanitised ASCII name every client reads;
// filename* (UTF-8, RFC 5987) carries the name's letters, digits and marks, so a browser saves the stored name.
func TestDispositionCarriesAUTF8Name(t *testing.T) {
	got := Disposition("attachment", "Lebenslauf-Müller.pdf")
	assert.Equal(t, "attachment; filename=Lebenslauf-M_ller.pdf; filename*=UTF-8''Lebenslauf-M%C3%BCller.pdf", got)
	_, params, err := mime.ParseMediaType(got)
	require.NoError(t, err)
	assert.Equal(t, "Lebenslauf-Müller.pdf", params["filename"], "a client that reads filename* gets the stored name")

	_, params, err = mime.ParseMediaType(Disposition("inline", "報告書.md"))
	require.NoError(t, err)
	assert.Equal(t, "報告書.md", params["filename"])

	assert.Equal(t, "attachment; filename=report.md", Disposition("attachment", "report.md"), "an ASCII name has no filename*")
	_, params, err = mime.ParseMediaType(Disposition("attachment", `..\..\dir/ü.txt`))
	require.NoError(t, err)
	assert.Equal(t, "ü.txt", params["filename"], "only the base name")
}

// The UTF-8 name never carries what reorders or hides text: an override that would show "report.pdf" for "report‮fdp.exe"
// becomes '_', and so do controls, spaces and quotes. A name with no letter or digit has no filename*.
func TestDispositionUTF8NameHasNoReorderingOrControls(t *testing.T) {
	for _, in := range []string{"report‮fdp.exe", "a​b⁦c.txt", "x\x07y\r\nz.txt", `"quoted" name;.txt`} {
		got := Disposition("attachment", in)
		_, params, err := mime.ParseMediaType(got)
		require.NoError(t, err, got)
		for _, bad := range []string{"‮", "​", "⁦", "\x07", "\r", "\n", `"`, ";", " "} {
			assert.NotContains(t, params["filename"], bad, "%q -> %q", in, got)
		}
	}
	assert.Equal(t, "attachment; filename=___", Disposition("attachment", "‮‮‮"), "nothing to keep: no filename*")
}

// Through Coral Hub the relayed header is rebuilt the same way: filename= in ASCII and the UTF-8 name in filename*.
func TestStrictResponseFilterKeepsTheUTF8Name(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Content-Type": {"application/pdf"}, "Content-Disposition": {Disposition("attachment", "Lebenslauf-Müller.pdf")}}}
	require.NoError(t, StrictResponseFilter(resp))
	cd := resp.Header.Get("Content-Disposition")
	assert.True(t, strings.HasPrefix(cd, "attachment; filename=Lebenslauf-M_ller.pdf; filename*=UTF-8''"), cd)
	_, params, err := mime.ParseMediaType(cd)
	require.NoError(t, err)
	assert.Equal(t, "Lebenslauf-Müller.pdf", params["filename"])
}
