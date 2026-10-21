# coral-agents-token

Turns the "coral-agents" GitHub App key into short-lived installation tokens, so an agent never holds the key.

```
coral-agents-token --perms push|dispatch [--repo coral|homebrew-coral] [--installation-id N] token
coral-agents-token --perms push|dispatch [--repo coral|homebrew-coral] [--installation-id N] credential get|store|erase
```

- `--perms push` is contents:write + pull_requests:write; `--perms dispatch` is actions:write. Nothing else can be asked for.
- The token is limited to one repository (`coral` by default, or `homebrew-coral`). GitHub's answer is checked: a token wider than asked for is refused, not printed, not cached.
- Without `--installation-id` the helper asks `GET /app/installations` and accepts exactly one installation.
- The key is read from `~/.config/coral-agents/private-key.pem` only (the home folder comes from the user database, not from `$HOME`). The folder must be 0700, the file 0600, both owned by the user and neither a symlink. No flag, environment variable or standard input can name a key.
- It signs an RS256 JWT for app 5405319, issued 60 seconds in the past and valid 9 minutes, and asks for the token.
- The token is cached for at most 50 minutes (and never within 2 minutes of the expiry GitHub states) in `~/.config/coral-agents/cache/`, a 0700 folder of 0600 files, one per installation, repository and permission set. A cache file that is not 0600, is a link, is unreadable or does not match the request is ignored and replaced.
- Standard output carries the token and nothing else carries it: errors name the HTTP status and never the key, the JWT or a token. A redirect is never followed.

Git (the helper answers only `https://github.com`; git appends `get`):

```
git config credential.https://github.com.helper ''
git config --add credential.https://github.com.helper '!coral-agents-token --perms push credential'
```

`gh`: `GH_TOKEN=$(coral-agents-token --perms push token) gh pr create ...`

## Where it lives, and why

`coral-go/tools/coral-agents-token/` is its **own Go module** (its own `go.mod`). It is not a `cmd/` of the Coral module: Coral's `go.mod`, `go.sum`, SBOM, the SQLCipher amalgamation check that runs when either changes, `go test ./...` and every installer build are untouched, and none of this ships. Run it with `cd coral-go/tools/coral-agents-token && go test ./... && go build -o /dev/null .`

## Licences (checked before use)

| module | licence |
|---|---|
| github.com/bradleyfalzon/ghinstallation/v2 v2.19.0 | **Apache-2.0** (the #5521 note says MIT: it is Apache-2.0) |
| github.com/golang-jwt/jwt/v4 v4.5.2 | MIT |
| github.com/google/go-github/v88 v88.0.0 | BSD-3-Clause |
| github.com/google/go-querystring v1.2.0 | BSD-3-Clause |

The texts are in `licenses/`. The tool is not distributed, so no notice ships with Coral.

## Tests

`go test` runs against an httptest fake of the GitHub API and a key the test generates; it never reads a real key and never calls api.github.com.
