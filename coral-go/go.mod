module github.com/cdknorow/coral

go 1.26.9

require (
	fyne.io/systray v1.12.0
	// SQLCipher 4.17.0 on SQLite 3.53.3: at or past 3.51.3, which fixed the WAL-reset corruption bug
	// (https://sqlite.org/wal.html, section 11; #3677).
	// SQLCipher for the encrypted build. Kept as a plain dependency, not mirrored or vendored (#3677,
	// #3679): go.sum pins these exact bytes, and tools/verify-sqlcipher-amalgamation.sh (CI:
	// sqlcipher-amalgamation.yml, whenever go.mod or go.sum change) proves its bundled amalgamation is
	// byte for byte the official SQLCipher release. On a bump, update its pin table and re-run that check.
	github.com/0xCarbon/go-sqlite3 v1.15.1
	github.com/UserExistsError/conpty v0.1.4
	github.com/betterleaks/betterleaks v1.9.0
	github.com/coder/websocket v1.8.15
	github.com/creack/pty v1.1.24
	github.com/fsnotify/fsnotify v1.10.1
	github.com/gen2brain/beeep v0.11.2
	github.com/go-chi/chi/v5 v5.5.0
	github.com/go-chi/cors v1.2.2
	github.com/google/uuid v1.6.0
	github.com/hashicorp/yamux v0.1.2
	github.com/jmoiron/sqlx v1.4.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/rs/zerolog v1.33.0
	github.com/shirou/gopsutil/v4 v4.26.9
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stretchr/testify v1.12.1
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	golang.org/x/crypto v0.56.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	golang.org/x/text v0.41.0
	gopkg.in/yaml.v3 v3.0.1
	// SQLite 3.53.4 (from 3.50.4 in #3678): carries the WAL-reset corruption fix (3.51.3 and later,
	// https://sqlite.org/wal.html section 11; #3677). It pins modernc.org/libc v1.77.1 below: keep the two in step.
	modernc.org/sqlite v1.60.1
)

require go.uber.org/goleak v1.3.0

require github.com/evanw/esbuild v0.25.0

require (
	dario.cat/mergo v1.0.2 // indirect
	git.sr.ht/~jackmordaunt/go-toast v1.1.2 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/STARRY-S/zip v0.2.3 // indirect
	github.com/andybalholm/brotli v1.2.1 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/sevenzip v1.6.2 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/charlievieth/fastwalk v1.0.14 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/esiqveland/notify v0.13.3 // indirect
	github.com/eskriett/confusables v0.0.0-20250910043846-220432c5bd73
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/fatih/semgroup v1.2.0 // indirect
	github.com/gitleaks/go-gitdiff v0.9.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-sprout/sprout v1.0.3 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/go-github/v72 v72.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/h2non/filetype v1.1.3 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackmordaunt/icns/v3 v3.0.1 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/klauspost/pgzip v1.2.6 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mholt/archives v0.1.6-0.20260429171216-ef71b7a32fae // indirect
	github.com/mikelolasagasti/xz v1.0.1 // indirect
	github.com/minio/minlz v1.1.1 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/nwaples/rardecode/v2 v2.2.2 // indirect
	github.com/pelletier/go-toml/v2 v2.3.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pkoukk/tiktoken-go v0.1.8 // indirect
	github.com/power-devops/perfstat v0.0.0-20260805114148-88456608a4f6 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rrethy/ahocorasick v1.0.0 // indirect
	github.com/sergeymakinen/go-bmp v1.0.0 // indirect
	github.com/sergeymakinen/go-ico v1.0.0-beta.0 // indirect
	github.com/shurcooL/githubv4 v0.0.0-20260209031235-2402fdf4a9ed // indirect
	github.com/shurcooL/graphql v0.0.0-20240915155400-7ee5256398cf // indirect
	github.com/sorairolake/lzip-go v0.3.8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/tadvi/systray v0.0.0-20190226123456-11a2b8fa57af // indirect
	github.com/tklauser/go-sysconf v0.4.0 // indirect
	github.com/tklauser/numcpus v0.12.0 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go4.org v0.0.0-20260112195520-a5071408f32f // indirect
	golang.org/x/exp v0.0.0-20250218142911-aa4b98e5adaa // indirect
	golang.org/x/sync v0.23.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
