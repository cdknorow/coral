package routes

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4531 (Security's #4527 INFO 1): the credential locations Hub Browse and file reads refuse under the home folder.
var auditHomeDirs = []string{
	".gnupg", ".kube", ".docker", ".azure", ".azure-devops", ".gcloud", ".config/gcloud", ".config/gh", ".password-store",
	".oci", ".ibmcloud", ".bluemix", ".aliyun", ".minikube", ".pulumi", ".vault", ".helm", ".cloudflared", ".wrangler",
	".heroku", ".stripe", ".ngrok", ".subversion", ".nuget", ".composer", ".kaggle", ".huggingface", ".cache/huggingface",
	".1password", ".op", ".bitwarden", ".lastpass", ".thunderbird", ".mutt", ".zsh_sessions", ".vercel", ".netlify",
	".local/share/keyrings", ".local/share/gnome-keyring", ".local/share/kwalletd", ".local/share/python_keyring",
	".mozilla/firefox", "snap/firefox/common", "snap/chromium/current",
	"Library/Keychains", "Library/Application Support/Google/Chrome", "Library/Application Support/Firefox",
	"Library/Containers/com.example.app", "Library/Group Containers/g.example", "Library/Preferences", "Library/Accounts",
	"Library/Cookies", "Library/Safari", "Library/Mail", "Library/Messages",
	"AppData/Roaming/gcloud", "AppData/Roaming/Microsoft/Credentials", "AppData/Local/Google/Chrome/User Data",
	"AppData/Roaming/Mozilla", "AppData/Local/Microsoft/Edge", "AppData/Local/Microsoft/Vault", "AppData/Roaming/GitHub CLI",
	"AppData/Roaming/Microsoft/Protect", "AppData/Local/BraveSoftware", "AppData/Roaming/Signal", "AppData/Local/Packages/x", "AppData/Local/Comms",
	"AppData/Local/1Password", "AppData/Local/Opera Software", "AppData/Local/Vivaldi", "AppData/Local/Mozilla", "AppData/LocalLow/x/y",
	".claude", ".codex", ".gemini", ".agy", ".cursor", ".aider", ".opencode", ".local/share/opencode",
}

var auditHomeFiles = []string{
	".git-credentials", ".terraformrc", ".vault-token", ".databrickscfg", ".boto", ".dockercfg", ".netrc", "_netrc", ".npmrc",
	".pypirc", ".pgpass", ".yarnrc", ".condarc", ".msmtprc", ".authinfo", ".mylogin.cnf", ".hgrc", ".wgetrc", ".curlrc",
	".zsh_history", ".bash_history", "NTUSER.DAT", "kaggle.json", "login.keychain-db", "work.keychain", "client.ovpn",
	"vars.tfvars", "AuthKey_ABC.p8", "wallet.jceks", "keys.kdbx",
}

func TestSensitivePathAuditTable(t *testing.T) {
	e := newBoundsEnv(t)
	for _, d := range auditHomeDirs {
		p := filepath.Join(e.home, filepath.FromSlash(d), "file.txt")
		assert.True(t, sensitivePath(p), d)
		// the folder itself, and upper-cased where the OS folds case
		assert.True(t, sensitivePath(filepath.Join(e.home, filepath.FromSlash(d))), d)
		assert.True(t, sensitivePath(strings.ToUpper(p)), d+" upper")
	}
	for _, f := range auditHomeFiles {
		assert.True(t, sensitivePath(filepath.Join(e.home, f)), f)
		assert.True(t, sensitivePath(filepath.Join(e.repo, "sub", f)), "repo "+f)
	}
	for _, ok := range []string{"projects", "projects/huggingface", "projects/op-tools", "local", "Documents/notes.txt",
		"projects/Library/readme.md", "go/src/app/main.go", "snap/notes", "projects/appdata/x",
		// Windows keeps the temp folder, tools and checkouts under AppData\Local: none of it is a credential folder (the gate's
		// windows-tests run the suites with their temp folders there).
		"AppData/Local/Temp/x/y", "AppData/Local/Programs/tool/a.go", "AppData/Local/Temp"} {
		assert.False(t, sensitivePath(filepath.Join(e.home, filepath.FromSlash(ok))), ok)
	}
}

func TestSensitivePathAuditThroughTheHub(t *testing.T) {
	e := newBoundsEnv(t)
	for _, d := range auditHomeDirs {
		dir := filepath.Join(e.home, filepath.FromSlash(d))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("secret"), 0o600))
	}
	for _, f := range auditHomeFiles {
		require.NoError(t, os.WriteFile(filepath.Join(e.home, f), []byte("secret"), 0o600))
	}
	for _, d := range auditHomeDirs {
		code, body := e.do("GET", "/api/filesystem/list", url.Values{"path": {"~/" + d}}, "", true)
		assert.Equal(t, 403, code, "list "+d)
		assert.NotContains(t, body, "file.txt", d)
		// is-git does not confirm a repository there (200, false), where the local dashboard sees it.
		for _, via := range []bool{false, true} {
			var r struct {
				IsGit bool `json:"is_git"`
			}
			code, body := e.do("GET", "/api/filesystem/is-git", url.Values{"path": {filepath.Join(e.home, filepath.FromSlash(d))}}, "", via)
			require.Equal(t, 200, code, "is-git "+d)
			require.NoError(t, json.Unmarshal([]byte(body), &r))
			assert.Equal(t, !via, r.IsGit, "is-git "+d)
		}
	}
	// The home listing does not show the first-level entries either.
	code, body := e.do("GET", "/api/filesystem/list", url.Values{"path": {"~"}}, "", true)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "projects")
	for _, d := range []string{".gnupg", ".kube", ".oci", ".1password", ".vault-token", ".databrickscfg", ".terraformrc", ".git-credentials"} {
		assert.NotContains(t, body, d)
	}

	// File reads inside the repository.
	_, missing := e.get(fc, true, "filepath", "no/such/file")
	refused := func(rel string) {
		fp := filepath.Join(e.repo, filepath.FromSlash(rel))
		// The refusal holds for the local dashboard too, and looks like a missing file (no existence oracle).
		for _, via := range []bool{false, true} {
			code, body := e.get(fc, via, "filepath", fp)
			assert.Equal(t, 404, code, rel)
			assert.Equal(t, missing, body, rel)
			assert.NotContains(t, body, "secret", rel)
		}
	}
	for _, f := range auditHomeFiles {
		require.NoError(t, os.WriteFile(filepath.Join(e.repo, f), []byte("secret"), 0o600))
		refused(f)
	}
	for _, d := range []string{".oci", ".ibmcloud", ".minikube", ".pulumi", ".azure-devops", ".1password", ".kaggle", ".bluemix"} {
		require.NoError(t, os.MkdirAll(filepath.Join(e.repo, d), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(e.repo, d, "c.txt"), []byte("secret"), 0o600))
		refused(d + "/c.txt")
	}
	require.NoError(t, os.WriteFile(filepath.Join(e.repo, "notes.txt"), []byte("fine"), 0o644))
	code, body = e.get(fc, true, "filepath", filepath.Join(e.repo, "notes.txt"))
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "fine")
	code, _ = e.do("GET", "/api/filesystem/list", url.Values{"path": {"~/projects"}}, "", true)
	assert.Equal(t, 200, code)
}

func TestSensitivePathCoralDataDir(t *testing.T) {
	e := newBoundsEnv(t)
	custom := filepath.Join(e.base, "cdata")
	assert.False(t, sensitivePath(filepath.Join(custom, "api_key")))
	ProtectDataDir(custom)
	assert.True(t, sensitivePath(filepath.Join(custom, "api_key")))
	assert.True(t, sensitivePath(strings.ToUpper(filepath.Join(custom, "sessions.db"))))
	// #5280: the update state (the relaunch's acknowledgement key and used tokens, the updater's record) is under it too.
	for _, f := range []string{"ack.key", "ack_used.json", "state.json"} {
		assert.True(t, sensitivePath(filepath.Join(custom, "updates", f)), f)
	}
	assert.False(t, sensitivePath(filepath.Join(e.base, "cdata-notes", "a.txt")))

	t.Setenv("CORAL_DATA_DIR", filepath.Join(e.base, "envdata"))
	assert.True(t, sensitivePath(filepath.Join(e.base, "envdata", "api_key")))
	assert.True(t, sensitivePath(filepath.Join(e.home, ".coral", "api_key")))
}

// A data folder that is (or holds) the home folder is not walled off as a whole, or the home would list nothing; the
// per-name lists still apply inside it.
func TestSensitivePathDataDirHoldingHome(t *testing.T) {
	e := newBoundsEnv(t)
	ProtectDataDir(e.home)
	assert.False(t, sensitivePath(filepath.Join(e.home, "projects", "app.go")))
	assert.True(t, sensitivePath(filepath.Join(e.home, ".ssh", "id_rsa")))
}

// Windows keeps the temp folder under the home as AppData\Local\Temp, and the suites, the hub e2e homes and auto-attach all
// work there: the AppData rules must name credential and profile folders, never all of AppData\Local or Roaming (#4531, the
// windows-tests failure of gate 5).
func TestSensitivePathLeavesTempAndProjectsUnderAppData(t *testing.T) {
	tmp := realPath(t.TempDir())
	assert.False(t, sensitivePath(filepath.Join(tmp, "work", "proj", "main.go")), "the test's own temp folder")
	assert.False(t, sensitivePath(os.TempDir()))
	home := filepath.Join(tmp, "Users", "u")
	for _, ok := range []string{`AppData\Local\Temp\coral-test\data\sessions.db`, `AppData\Local\Temp\x\proj`, `AppData\Local\Programs\Coral`,
		`AppData\Local\Projects\app\src\main.go`, `AppData\Local\Tools\go\bin\go.exe`} {
		assert.False(t, sensitivePath(filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(ok, `\`, "/")))), ok)
	}
	// The home itself being under AppData\Local (a data folder or home in a temp folder) refuses nothing ordinary.
	tempHome := filepath.Join(tmp, "AppData", "Local", "Temp", "home123")
	assert.False(t, sensitivePath(filepath.Join(tempHome, "work", "proj")))
	assert.True(t, sensitivePath(filepath.Join(tempHome, "AppData", "Local", "Google", "Chrome", "User Data", "Default", "Login Data")))
}
